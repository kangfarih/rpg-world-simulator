// Package abilities is a pure-logic port of the Kaetram player ability
// system. It carries no transport and no timers: callers load the
// registry, gate casts with CanCast, and apply the returned Effect
// values (status add + timed remove) themselves.
//
// TS sources (faithful to their numbers):
//   - packages/server/src/game/entity/character/player/ability/ability.ts
//     (activate gating: passive rejection, mana cost, cooldown window,
//     mana decrement, duration-based deactivate callback via setTimeout)
//   - packages/server/src/game/entity/character/player/ability/impl/*.ts
//     (awareness, dualistsmark, hotshot, intimidate, precognition, run,
//     secretcalling, thickskin, plus impl/index.ts)
//   - packages/server/src/game/entity/character/player/abilities.ts
//     (add/has/setLevel/use keyed by ability key)
//   - packages/server/data/abilities.json (per-level cooldown/duration/mana)
//
// Effect-ID conventions follow Modules.Effects
// (packages/common/network/modules.ts:271) and agree with the m13.go
// /toggle constants (TerrorStatus=3, Stun=4, Burning=16, Freezing=17,
// Invincible=18): None=0, Running=10, DualistsMark=12, ThickSkin=13.
//
// Divergences from TS (all deliberate, no timers/no transport here):
//   - TS Ability.activate performs mana decrement, Date.now() bookkeeping,
//     toggleCallback packets, and a setTimeout deactivate. Here CanCast is
//     the pure mana+cooldown predicate and Effect is the pure descriptor;
//     the caller owns mana mutation, last-cast timestamps, toggle
//     notification, and deactivate scheduling.
//   - abilities.json carries no display names, so Ability.Name defaults to
//     the ability key.
//   - TS counts 8 ability keys (impl/index.ts); there is no 9th ability in
//     the JSON or the index. "9 files" in the TS tree is the base
//     ability.ts plus the 8 impl classes.
//   - intimidate/hotshot/secretcalling/precognition apply no server-side
//     status effect in TS (only the client toggle window + hasTarget gate),
//     so their Effect.Kind is EffectNone with DurationMs set to the active
//     window; the hasTarget gate is exposed via RequiresTarget.
//   - awareness/precognition are passive (super.activate always returns
//     false), so their Effect is the zero Effect and IsActive is false.
//     Note precognition.ts still contains a hasTarget gate, but it is
//     unreachable in TS because the passive type check runs first in
//     super.activate; RequiresTarget still reports it for fidelity.
package abilities

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// EffectKind identifies the server-side status effect an ability applies.
// Values are Modules.Effects IDs (packages/common/network/modules.ts:271).
type EffectKind int

const (
	// EffectNone means the ability applies no server-side status effect
	// (passive abilities and combat-gated actives whose window is
	// client-visual only).
	EffectNone EffectKind = 0
	// EffectRunning is Modules.Effects.Running (run ability).
	EffectRunning EffectKind = 10
	// EffectDualistsMark is Modules.Effects.DualistsMark.
	EffectDualistsMark EffectKind = 12
	// EffectThickSkin is Modules.Effects.ThickSkin.
	EffectThickSkin EffectKind = 13
)

// Ability is one resolved ability level: the key, display name, level,
// and the mana/cooldown/duration numbers from abilities.json.
type Ability struct {
	Key        string
	Name       string
	Level      int
	ManaCost   int
	CooldownMs int
	// DurationMs is the active window in milliseconds from the JSON level
	// row (0 for passive abilities, which have no level rows). It is an
	// extension beyond the {Key, Name, Level, ManaCost, CooldownMs} shape
	// so Effect descriptors can carry the TS duration without timers.
	DurationMs int
}

// Effect is the pure outcome of activating an ability: which status
// effect to apply, with what magnitude, for how long. The caller applies
// it (status add + timed remove) and owns all transport.
type Effect struct {
	Kind EffectKind
	// Power is the TS magnitude: run 0.9 (getMovementSpeed multiplies step
	// time by 0.9, player.ts), dualistsmark -200 (getAttackRate subtracts
	// 200ms, player.ts), thickskin -0.2 (getDamageReduction subtracts 0.2,
	// player.ts). Zero when the ability carries no server-side magnitude.
	Power float64
	// DurationMs is the active window from abilities.json (0 for passives).
	DurationMs int
}

// rawLevel mirrors RawAbilityLevelData (network/impl/ability.ts): all
// fields optional in TS, absent for passive abilities.
type rawLevel struct {
	Cooldown int `json:"cooldown"`
	Duration int `json:"duration"`
	Mana     int `json:"mana"`
}

// rawAbility mirrors RawAbilityData: "active" abilities carry per-level
// rows keyed "1".."4"; passive abilities carry no levels.
type rawAbility struct {
	Type   string              `json:"type"`
	Levels map[string]rawLevel `json:"levels"`
}

// combatGated lists the impls whose activate() first requires a combat
// target (player.hasTarget(), else notify misc:NEED_COMBAT and return
// false): hotshot.ts, intimidate.ts, precognition.ts, secretcalling.ts.
var combatGated = map[string]bool{
	"hotshot":       true,
	"intimidate":    true,
	"precognition":  true,
	"secretcalling": true,
}

// Registry is the loaded abilities.json: raw per-key definitions.
// Resolve one level with ForKey; query metadata with IsActive and
// RequiresTarget; describe outcomes with Effect.
type Registry struct {
	defs map[string]rawAbility
}

// Load reads and parses an abilities.json file (canonical location:
// packages/server/data/abilities.json).
func Load(path string) (*Registry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("abilities: read %s: %w", path, err)
	}
	var defs map[string]rawAbility
	if err := json.Unmarshal(raw, &defs); err != nil {
		return nil, fmt.Errorf("abilities: parse %s: %w", path, err)
	}
	if len(defs) == 0 {
		return nil, fmt.Errorf("abilities: parse %s: no abilities defined", path)
	}
	return &Registry{defs: defs}, nil
}

// clampLevel mirrors Ability.setLevel (ability.ts): levels range 1-4.
func clampLevel(level int) int {
	if level < 1 {
		return 1
	}
	if level > 4 {
		return 4
	}
	return level
}

// levelRow returns the JSON row for key at level ("1".."4"). The second
// return is false for unknown keys or keys without that level row
// (notably passive abilities, which define no levels).
func (r *Registry) levelRow(key string, level int) (rawLevel, bool) {
	def, ok := r.defs[key]
	if !ok || def.Levels == nil {
		return rawLevel{}, false
	}
	row, ok := def.Levels[fmt.Sprint(clampLevel(level))]
	return row, ok
}

// ForKey resolves the ability numbers for key at level (clamped 1-4).
// Passive abilities resolve with zero ManaCost/CooldownMs/DurationMs since
// they define no level rows; use IsActive to distinguish them. Unknown
// keys return false.
func (r *Registry) ForKey(key string, level int) (Ability, bool) {
	if _, ok := r.defs[key]; !ok {
		return Ability{}, false
	}
	a := Ability{Key: key, Name: key, Level: clampLevel(level)}
	if row, ok := r.levelRow(key, level); ok {
		a.ManaCost = row.Mana
		a.CooldownMs = row.Cooldown
		a.DurationMs = row.Duration
	}
	return a, true
}

// IsActive reports whether key is an "active" ability (activatable).
// Unknown keys report false.
func (r *Registry) IsActive(key string) bool {
	def, ok := r.defs[key]
	return ok && def.Type == "active"
}

// RequiresTarget reports whether the TS impl gates activation on
// player.hasTarget() (misc:NEED_COMBAT). See combatGated.
func (r *Registry) RequiresTarget(key string) bool {
	return combatGated[key]
}

// Keys returns the sorted registry keys (mirrors impl/index.ts membership).
func (r *Registry) Keys() []string {
	keys := make([]string, 0, len(r.defs))
	for k := range r.defs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Count returns the number of registered abilities.
func (r *Registry) Count() int {
	return len(r.defs)
}

// CanCast is the pure mana+cooldown predicate from Ability.activate
// (ability.ts): enough mana remaining and the cooldown window since
// lastCastMs has elapsed. Times are Unix milliseconds, mirroring
// Date.now() usage. A zero lastCastMs (never cast) always passes the
// cooldown check, matching TS lastActivated = 0.
//
// It covers only the mana+cooldown portion: callers must check IsActive
// first (TS rejects passive types before the mana/cooldown checks) and
// RequiresTarget where applicable.
func (a Ability) CanCast(mana int, nowMs, lastCastMs int64) bool {
	if mana < a.ManaCost {
		return false
	}
	return nowMs-lastCastMs >= int64(a.CooldownMs)
}

// Effect returns the pure outcome descriptor for the resolved ability,
// per TS impl class:
//   - run.ts: setRunning(true) adds Running; deactivate setRunning(false).
//     Power 0.9 (10% speed boost, getMovementSpeed).
//   - dualistsmark.ts: setDualistsMark(true) adds DualistsMark; deactivate
//     clears it. Power -200 (attack-rate delta ms, getAttackRate).
//   - thickskin.ts: status.add(ThickSkin); deactivate status.remove.
//     Power -0.2 (damage-reduction delta, getDamageReduction).
//   - intimidate/hotshot/secretcalling: hasTarget gate, then only the
//     mana/cooldown/duration bookkeeping + client toggle; no server status
//     effect, so Kind is EffectNone with the active window preserved.
//   - awareness/precognition: passive, never activate; zero Effect.
func (a Ability) Effect() Effect {
	switch a.Key {
	case "run":
		return Effect{Kind: EffectRunning, Power: 0.9, DurationMs: a.DurationMs}
	case "dualistsmark":
		return Effect{Kind: EffectDualistsMark, Power: -200, DurationMs: a.DurationMs}
	case "thickskin":
		return Effect{Kind: EffectThickSkin, Power: -0.2, DurationMs: a.DurationMs}
	case "intimidate", "hotshot", "secretcalling":
		return Effect{Kind: EffectNone, DurationMs: a.DurationMs}
	default:
		return Effect{}
	}
}

// Effect resolves key at level and returns its descriptor. Unknown keys
// return false.
func (r *Registry) Effect(key string, level int) (Effect, bool) {
	a, ok := r.ForKey(key, level)
	if !ok {
		return Effect{}, false
	}
	return a.Effect(), true
}
