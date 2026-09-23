// Package status is an additive, transport-free status/poison damage-over-time
// engine for the Go server stub. The caller owns ticking: Apply records an
// effect, Tick(nowMs) returns the damage ticks that are due, and the caller
// applies the damage to HP and emits packets itself. There are no timers,
// goroutines, or network sends in this package.
//
// TS sources mirrored here:
//   - packages/server/src/game/entity/character/effect/poison.ts — Poison
//     (damage/duration/rate per PoisonTypes, expired(), duration -1 persists).
//   - packages/server/src/game/entity/character/effect/status.ts — Status
//     (add/addWithTimeout/remove/clear/has, permanent vs timed freezing).
//   - packages/server/src/game/entity/character/character.ts — handlePoison
//     (flat poison.damage per tick via Hits.Poison + hit()), setPoison
//     (replace, no stacking), effects() (Freezing/Burning damage every
//     EFFECT_RATE), handleColdDamage/handleBurningDamage (flat COLD/BURNING
//     constants, blocked by SnowPotion/FirePotion), addStatusEffect (hit type
//     -> effect + duration mapping), heal() (blocked while poisoned or while
//     Freezing/Burning/Terror is present).
//   - packages/common/network/modules.ts — Effects enum order, PoisonInfo
//     table, Constants (COLD/BURNING damage, durations, EFFECT_RATE).
//
// Effect packet conventions (for the caller, not emitted here): S->C
// [47, opcode, {instance, effect}] (m10.go m10SetFreezing, m13.go
// m13AddEffect/m13RemoveEffect, internal/protocol effectData) with opcode
// EffectAdd=0 / EffectRemove=1. Map Apply to an Add frame and expiry/Clear
// to Remove frames.
package status

import (
	"sort"
	"sync"
)

// Instance identifies the affected character (player/mob instance string).
type Instance string

// Kind is a status-effect key. Values match Modules.Effects IDs
// (modules.ts) except KindPoison: TS poison is not a Modules.Effects value —
// it travels on the separate Poison packet with Hits.Poison — so poison uses
// a sentinel outside the Effects range.
type Kind int

const (
	// KindPoison is venom/plague-style poison (PoisonInfo parity). Sentinel:
	// TS has no Modules.Effects ID for poison.
	KindPoison Kind = -1
	// KindTerror is Modules.Effects.Terror (combat path: Hits.Terror ->
	// Effects.Terror for TERROR_DURATION in addStatusEffect).
	KindTerror Kind = 2
	// KindTerrorStatus is Modules.Effects.TerrorStatus (the /toggle debug
	// variant in commands.ts / m13ToggleCommand). Usable as a distinct key.
	KindTerrorStatus Kind = 3
	// KindStun is Modules.Effects.Stun (STUN_DURATION in addStatusEffect).
	KindStun Kind = 4
	// KindBurning is Modules.Effects.Burning (BURNING_DURATION).
	KindBurning Kind = 16
	// KindFreezing is Modules.Effects.Freezing (FREEZING_DURATION; m10.go
	// effectFreezing and m13.go EffectFreezingM13 carry the same value).
	KindFreezing Kind = 17
	// KindBleed is Modules.Effects.Bleed.
	KindBleed Kind = 29
)

// Default damage/duration/cadence values, mirroring PoisonInfo and
// Modules.Constants (modules.ts). Apply uses these when power or durationMs
// is 0.
const (
	// Venom (the TS default: handlePoisonDamage always sets Venom).
	PoisonDamageDefault     = 5
	PoisonDurationDefaultMs = int64(30_000)
	PoisonTickDefaultMs     = int64(2_000)

	BurningDamageDefault     = 20 // Constants.BURNING_EFFECT_DAMAGE
	BurningDurationDefaultMs = int64(60_000)

	FreezingDamageDefault     = 10 // Constants.COLD_EFFECT_DAMAGE
	FreezingDurationDefaultMs = int64(60_000)

	TerrorDurationDefaultMs = int64(60_000)
	StunDurationDefaultMs   = int64(10_000)

	// EffectTickDefaultMs is Constants.EFFECT_RATE: character.effects()
	// applies freezing/burning damage on this cadence.
	EffectTickDefaultMs = int64(10_000)

	// BleedDurationDefaultMs has no TS source (TS Bleed is a client-sided
	// visual with no periodic damage path); it mirrors the Venom duration so
	// the key is usable as a timed status. See divergences below.
	BleedDurationDefaultMs = int64(30_000)
)

// Expired is one damage tick due for an instance. Tick returns these in
// deterministic order (instance, then kind). Damage is never applied to HP
// here — the caller does that (TS: character.hit / hitPoints.decrement) and
// emits the Combat Hit + Points frames itself.
type Expired struct {
	Instance Instance
	Kind     Kind
	Damage   int
}

// entry is one active effect on one instance.
type entry struct {
	kind       Kind
	power      int   // flat damage per tick for DoT kinds
	startMs    int64 // Apply time (TS Poison.start / status startTime basis)
	durationMs int64 // <0 persists until cleared (Persistent poison parity)
	tickMs     int64 // cadence between damage ticks (DoT kinds only)
	nextTickMs int64 // when the next damage tick is due
	dot        bool  // whether this kind emits damage ticks
}

// Tracker keeps per-instance status effects. It is mutex-guarded and safe
// for concurrent use. All time is explicit milliseconds; the caller drives
// Tick on its own loop.
type Tracker struct {
	mu sync.Mutex
	fx map[Instance]map[Kind]*entry
}

// NewTracker returns an empty Tracker.
func NewTracker() *Tracker {
	return &Tracker{fx: map[Instance]map[Kind]*entry{}}
}

// Apply records kind on inst, replacing any existing entry of the same kind
// (TS setPoison replaces the Poison object; status.addWithTimeout clears the
// old timeout and restarts it). power <= 0 selects the kind's default damage
// (Venom 5 / burning 20 / freezing 10); durationMs == 0 selects the kind's
// default duration; durationMs < 0 persists until Clear (Persistent poison).
// Unknown kinds default to non-DoT with a 60s duration.
func (t *Tracker) Apply(inst Instance, kind Kind, power int, durationMs int64, nowMs int64) {
	e := &entry{kind: kind, startMs: nowMs, durationMs: durationMs}
	switch kind {
	case KindPoison:
		e.dot = true
		e.tickMs = PoisonTickDefaultMs
		e.power = PoisonDamageDefault
		if e.durationMs == 0 {
			e.durationMs = PoisonDurationDefaultMs
		}
	case KindBurning:
		e.dot = true
		e.tickMs = EffectTickDefaultMs
		e.power = BurningDamageDefault
		if e.durationMs == 0 {
			e.durationMs = BurningDurationDefaultMs
		}
	case KindFreezing:
		e.dot = true
		e.tickMs = EffectTickDefaultMs
		e.power = FreezingDamageDefault
		if e.durationMs == 0 {
			e.durationMs = FreezingDurationDefaultMs
		}
	case KindTerror, KindTerrorStatus:
		if e.durationMs == 0 {
			e.durationMs = TerrorDurationDefaultMs
		}
	case KindStun:
		if e.durationMs == 0 {
			e.durationMs = StunDurationDefaultMs
		}
	case KindBleed:
		if e.durationMs == 0 {
			e.durationMs = BleedDurationDefaultMs
		}
	default:
		if e.durationMs == 0 {
			e.durationMs = TerrorDurationDefaultMs
		}
	}
	if power > 0 {
		e.power = power
	}
	e.nextTickMs = nowMs + e.tickMs

	t.mu.Lock()
	defer t.mu.Unlock()
	m := t.fx[inst]
	if m == nil {
		m = map[Kind]*entry{}
		t.fx[inst] = m
	}
	m[kind] = e
}

// Tick returns the damage ticks due at nowMs and reaps expired effects.
// Expiry is silent (TS handlePoison clears an expired poison with no final
// hit; status timeouts just remove the effect): once nowMs-startMs reaches
// durationMs the entry is dropped and no further ticks are emitted. Each DoT
// entry emits at most one tick per Tick call; if the caller stalls, the
// cadence resynchronises instead of bursting. Expiry is applied here, so
// call Tick (on the caller's loop) before relying on Has for freshness.
func (t *Tracker) Tick(nowMs int64) []Expired {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []Expired
	for inst, m := range t.fx {
		for kind, e := range m {
			if e.durationMs >= 0 && nowMs-e.startMs >= e.durationMs {
				delete(m, kind)
				continue
			}
			if !e.dot || e.power <= 0 || nowMs < e.nextTickMs {
				continue
			}
			out = append(out, Expired{Instance: inst, Kind: kind, Damage: e.power})
			e.nextTickMs += e.tickMs
			if e.nextTickMs <= nowMs {
				e.nextTickMs = nowMs + e.tickMs
			}
		}
		if len(m) == 0 {
			delete(t.fx, inst)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Instance != out[j].Instance {
			return out[i].Instance < out[j].Instance
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// Clear removes every effect on inst (TS status.clear()). After Clear, Tick
// emits nothing for inst and Has reports false for all its kinds.
func (t *Tracker) Clear(inst Instance) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.fx, inst)
}

// Remove drops one kind on inst, leaving the other effects untouched (TS
// status.remove / character.setPoison() cure parity: curing poison must not
// clear ability-cast Running/ThickSkin windows sharing the instance). After
// Remove, Tick emits nothing for that kind and Has reports false for it.
func (t *Tracker) Remove(inst Instance, kind Kind) {
	t.mu.Lock()
	defer t.mu.Unlock()
	m := t.fx[inst]
	if m == nil {
		return
	}
	delete(m, kind)
	if len(m) == 0 {
		delete(t.fx, inst)
	}
}

// Has reports whether inst currently holds kind (TS status.has). Presence
// covers persistent (duration < 0) entries. Note: expiry is reaped on Tick,
// so an entry past its duration still reports true until the next Tick.
func (t *Tracker) Has(inst Instance, kind Kind) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.fx[inst][kind] != nil
}

// Divergences from TS (documented, all favouring determinism in a
// transport-free engine):
//   - Re-poison cadence: TS setPoison replaces the Poison object but keeps
//     the OLD poisonInterval when already poisoned, so re-poisoning keeps the
//     old tick rate. Apply restarts the cadence from nowMs instead.
//   - Poison damage is flat per tick (PoisonInfo.damage). TS Plague vs Venom
//     differ only via the power/duration/rate the caller passes to Apply.
//   - status.ts startTime uses a Date.now()-1000 fudge and setTimeout
//     semantics; here expiry is a pure nowMs-startMs >= durationMs
//     comparison applied on Tick — no timers.
//   - SnowPotion/FirePotion immunity (addStatusEffect, handleColdDamage,
//     handleBurningDamage) and heal() blocking are caller-side Has checks,
//     not engine behaviour.
//   - Permanent freezing (status without timeout while standing in a
//     freezing area) is modelled as durationMs < 0 plus caller re-Apply on
//     area exit, matching m10SetFreezing's explicit add/remove.
//   - Bleed has no periodic damage path in TS; it is a timed non-DoT key.
//     Explicit power on a re-Applied DoT kind is honoured, but bleed/terror/
//     stun never emit damage ticks.
//   - TerrorStatus (3, the /toggle variant) is accepted as a key distinct
//     from combat Terror (2).
