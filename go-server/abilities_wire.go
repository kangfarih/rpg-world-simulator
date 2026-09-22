// Ability + status wiring (behavior-additive root glue over
// internal/abilities and internal/status).
//
// TS sources: abilities.ts (add/has/setLevel/use + Add/Update/Batch/Toggle
// packets), ability.ts (activate gating: passive rejection, mana, cooldown,
// toggleCallback on activate + deactivate), quest.ts givePlayerAbility
// (stageData.ability + abilityLevel||1), achievement.ts finishCallback
// (rewardAbility/rewardAbilityLevel), character.ts handlePoisonDamage
// (poisonous attacker -> setPoison Venom) + effects() (freezing/burning
// EFFECT_RATE damage).
//
// Frames used (all pre-existing shapes, verified against the TS tree):
//
//	S->C Ability Batch=0 {abilities:[{key,level,quickSlot,type?}]}
//	S->C Ability Add=1 {key,level,quickSlot,type} (unlock)
//	S->C Ability Update=2 {key,level,quickSlot} (re-grant = setLevel)
//	S->C Ability Toggle=5 {key,level:-1} (activate + deactivate)
//	C->S Ability [22,{opcode,key,index?}] Use=3 QuickSlot=4 (menu.ts
//	  handleAbility -> socket.send(Packets.Ability,{opcode,key,index})).
package main

import (
	"encoding/json"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"rpg-world-server/internal/abilities"
	gnet "rpg-world-server/internal/net"
	"rpg-world-server/internal/status"
	worldcore "rpg-world-server/internal/world"
)

// Ability opcodes (Opcodes.Ability in opcodes.ts): Batch0 Add1 Update2
// Use3 QuickSlot4 Toggle5.
const (
	AbilityBatch     = 0
	AbilityAdd       = 1
	AbilityUpdate    = 2
	AbilityUse       = 3
	AbilityQuickSlot = 4
	AbilityToggle    = 5
)

// Modules.AbilityType (modules.ts): Active0 Passive1.
const (
	AbilityTypeActive  = 0
	AbilityTypePassive = 1
)

const (
	abMaxMana      = 50 // welcomePlayer mana/maxMana (m5 persists no mana)
	abFreezeSuffix = "|freeze"
)

// abilityEntry mirrors AbilityData (impl/ability.ts): serialize(true)
// carries key/level/quickSlot/type, Update omits type.
type abilityEntry struct {
	Key       string `json:"key"`
	Level     int    `json:"level"`
	QuickSlot int    `json:"quickSlot"`
	Type      *int   `json:"type,omitempty"`
}

type abilityToggle struct {
	Key   string `json:"key"`
	Level int    `json:"level"`
}

var (
	abOnce     sync.Once
	abRegistry *abilities.Registry

	abMu        sync.Mutex
	abLevels    = map[string]map[string]int{} // username -> ability -> level
	abQuick     = map[string]map[string]int{} // username -> ability -> quickSlot
	abLastCast  = map[string]int64{}          // username+"\x00"+key -> unix ms
	abMana      = map[string]int{}            // instance -> current mana
	abTarget    = map[string]string{}         // instance -> last attack target
	abFxMu      sync.Mutex
	abFx        = map[string]map[int]bool{} // instance -> effectID (ability casts)
	abFreezeSet = map[string]bool{}         // instances holding a freeze key

	abStatus = status.NewTracker()
)

// abLoadRegistry loads abilities.json once (resourceDataPath so RES_abilities
// overrides like every other data table).
func abLoadRegistry() *abilities.Registry {
	abOnce.Do(func() {
		r, err := abilities.Load(resourceDataPath("abilities"))
		if err != nil {
			log.Printf("abilities: %v (ability engine disabled)", err)
			return
		}
		abRegistry = r
		log.Printf("abilities: registry=%d", r.Count())
	})
	return abRegistry
}

// abEnsureTables creates the abilities table (m13 flags-table precedent;
// synchronous INSERT OR REPLACE on grant, SELECT into memory on login).
func abEnsureTables() {
	if dbConn == nil {
		return
	}
	if _, err := dbConn.Exec(`CREATE TABLE IF NOT EXISTS abilities(player TEXT, ability TEXT, level INT, PRIMARY KEY(player, ability))`); err != nil {
		log.Printf("abilities: ddl: %v", err)
	}
}

// abGrantAbility ports abilities.add + the quest/achievement reward calls
// (quest.ts givePlayerAbility, achievement.ts finishCallback): unknown keys
// are refused with a log (TS: `Ability <key> does not exist.`), re-grants
// raise the level via an Update frame (TS: has() -> setLevel), first grants
// send Add. Level mirrors `abilityLevel || 1` (0/negative -> 1) with the
// registry 1-4 clamp applied by ForKey.
func abGrantAbility(c *playerConn, username, key string, level int) bool {
	if username == "" || key == "" {
		return false
	}
	r := abLoadRegistry()
	if r == nil {
		return false
	}
	if level < 1 {
		level = 1
	}
	a, ok := r.ForKey(key, level)
	if !ok {
		log.Printf("abilities: Ability %s does not exist.", key)
		return false
	}
	abMu.Lock()
	m := abLevels[username]
	if m == nil {
		m = map[string]int{}
		abLevels[username] = m
	}
	_, had := m[key]
	m[key] = a.Level
	qm := abQuick[username]
	if qm == nil {
		qm = map[string]int{}
		abQuick[username] = qm
	}
	if _, ok := qm[key]; !ok {
		qm[key] = -1
	}
	quick := qm[key]
	abMu.Unlock()
	if dbConn != nil {
		if _, err := dbConn.Exec(`INSERT OR REPLACE INTO abilities(player,ability,level) VALUES(?,?,?)`,
			username, key, a.Level); err != nil {
			log.Printf("abilities: save %s/%s: %v", username, key, err)
		}
	}
	markDirty(username)
	if c == nil {
		return true
	}
	typ := AbilityTypeActive
	if !r.IsActive(key) {
		typ = AbilityTypePassive
	}
	if had {
		_ = gnet.Send(c.Conn, pktOp(PacketAbility, AbilityUpdate, abilityEntry{
			Key: key, Level: a.Level, QuickSlot: quick,
		}))
	} else {
		_ = gnet.Send(c.Conn, pktOp(PacketAbility, AbilityAdd, abilityEntry{
			Key: key, Level: a.Level, QuickSlot: quick, Type: intp(typ),
		}))
	}
	m6Notify(c, "You have unlocked the "+key+" ability.")
	log.Printf("abilities: %s granted %s lv%d", username, key, a.Level)
	return true
}

// abHas reports whether username unlocked key.
func abHas(username, key string) bool {
	abMu.Lock()
	defer abMu.Unlock()
	return abLevels[username][key] > 0
}

// abLoadAbilities restores persisted unlocks into memory (m11LoadQuests
// precedent — called before the login batches are built).
func abLoadAbilities(username string) {
	if dbConn == nil || username == "" {
		return
	}
	abLoadRegistry()
	rows, err := dbConn.Query(`SELECT ability,level FROM abilities WHERE player=?`, username)
	if err != nil {
		return
	}
	defer rows.Close()
	abMu.Lock()
	defer abMu.Unlock()
	for rows.Next() {
		var key string
		var level int
		if rows.Scan(&key, &level) == nil {
			if abLevels[username] == nil {
				abLevels[username] = map[string]int{}
			}
			abLevels[username][key] = level
		}
	}
}

// abLoginBatch builds the Ability Batch frame queued after the Welcome
// extras (handler.ts onLoaded ability serialize — empty list when nothing
// unlocked; previously the server never sent packet 22).
func abLoginBatch(username string) []any {
	abMu.Lock()
	m := abLevels[username]
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	list := make([]abilityEntry, 0, len(keys))
	r := abRegistry
	for _, k := range keys {
		e := abilityEntry{Key: k, Level: m[k], QuickSlot: abQuick[username][k]}
		if r != nil {
			typ := AbilityTypeActive
			if !r.IsActive(k) {
				typ = AbilityTypePassive
			}
			e.Type = intp(typ)
		}
		list = append(list, e)
	}
	abMu.Unlock()
	if list == nil {
		list = []abilityEntry{}
	}
	return pktOp(PacketAbility, AbilityBatch, map[string]any{"abilities": list})
}

// abManaFor returns the current mana for an instance (welcome default).
func abManaFor(instance string) int {
	abMu.Lock()
	defer abMu.Unlock()
	if m, ok := abMana[instance]; ok {
		return m
	}
	return abMaxMana
}

// abSetTarget records the hero's last attack target (RequiresTarget gate).
func abSetTarget(instance, target string) {
	if instance == "" {
		return
	}
	abMu.Lock()
	if target == "" {
		delete(abTarget, instance)
	} else {
		abTarget[instance] = target
	}
	abMu.Unlock()
}

// abLiveTarget resolves the recorded target when it is still alive.
func abLiveTarget(instance string) string {
	abMu.Lock()
	t := abTarget[instance]
	abMu.Unlock()
	if t == "" {
		return ""
	}
	if t == combatDummyInstance {
		combatMu.Lock()
		dead := combatDead
		combatMu.Unlock()
		if !dead {
			return t
		}
	} else if m := m9MobFor(t); m != nil {
		m.mu.Lock()
		dead := m.dead
		m.mu.Unlock()
		if !dead {
			return t
		}
	} else {
		return t // unknown entity kinds (players/NPCs): no liveness data
	}
	abSetTarget(instance, "")
	return ""
}

// abHandleAbility routes C->S Ability frames [22,{opcode,key,index?}]
// (incoming.ts handleAbility): Use activates, QuickSlot stores the slot.
func abHandleAbility(c *playerConn, data []byte) {
	var d struct {
		Opcode int    `json:"opcode"`
		Key    string `json:"key"`
		Index  *int   `json:"index"`
	}
	if err := json.Unmarshal(data, &d); err != nil || d.Key == "" {
		return
	}
	switch d.Opcode {
	case AbilityUse:
		abUse(c, d.Key)
	case AbilityQuickSlot:
		if d.Index == nil {
			return
		}
		abMu.Lock()
		if abLevels[c.Username][d.Key] > 0 {
			if abQuick[c.Username] == nil {
				abQuick[c.Username] = map[string]int{}
			}
			abQuick[c.Username][d.Key] = *d.Index
		}
		abMu.Unlock()
	}
}

// abUse ports Ability.activate gating (ability.ts): passive rejection,
// RequiresTarget combat gate (misc:NEED_COMBAT), mana (misc:NOT_ENOUGH_MANA)
// + cooldown via abilities.CanCast (misc:NEED_WAIT_ABILITY). On success the
// mana decrements (Points mana emit), a Toggle frame goes out, server-side
// effects ride the status tracker + Effect Add, and the TS setTimeout
// deactivate becomes a time.AfterFunc Toggle (+ EffectRemove).
func abUse(c *playerConn, key string) {
	if c == nil {
		return
	}
	r := abLoadRegistry()
	if r == nil {
		return
	}
	abMu.Lock()
	level := abLevels[c.Username][key]
	abMu.Unlock()
	if level < 1 {
		return // TS: abilities[key]?.activate — unknown/unowned is a no-op
	}
	if !r.IsActive(key) {
		return // TS: passive type check rejects before mana/cooldown
	}
	a, ok := r.ForKey(key, level)
	if !ok {
		return
	}
	if r.RequiresTarget(key) && abLiveTarget(c.Instance) == "" {
		m6Notify(c, "misc:NEED_COMBAT")
		return
	}
	nowMs := time.Now().UnixMilli()
	mana := abManaFor(c.Instance)
	abMu.Lock()
	last := abLastCast[c.Username+"\x00"+key]
	abMu.Unlock()
	if mana < a.ManaCost {
		m6Notify(c, "misc:NOT_ENOUGH_MANA")
		return
	}
	if !a.CanCast(mana, nowMs, last) {
		wait := (int64(a.CooldownMs) - (nowMs - last)) / 1000
		if wait < 1 {
			wait = 1
		}
		m6Notify(c, "misc:NEED_WAIT_ABILITY;duration="+itoa(wait))
		return
	}
	mana -= a.ManaCost
	abMu.Lock()
	abMana[c.Instance] = mana
	abLastCast[c.Username+"\x00"+key] = nowMs
	abMu.Unlock()
	_ = gnet.Send(c.Conn, pkt(PacketPoints, pointsData{
		Instance: c.Instance, Mana: intp(mana), MaxMana: intp(abMaxMana),
	}))
	_ = gnet.Send(c.Conn, pktOp(PacketAbility, AbilityToggle, abilityToggle{Key: key, Level: -1}))
	fx := a.Effect()
	if fx.Kind == abilities.EffectNone {
		// Client-visual window only (intimidate/hotshot/secretcalling):
		// untoggle when the duration lapses, no server status.
		if fx.DurationMs > 0 {
			time.AfterFunc(time.Duration(fx.DurationMs)*time.Millisecond, func() {
				_ = gnet.Send(c.Conn, pktOp(PacketAbility, AbilityToggle, abilityToggle{Key: key, Level: -1}))
			})
		}
		log.Printf("abilities: %s cast %s (window %dms)", c.Username, key, fx.DurationMs)
		return
	}
	effectID := int(fx.Kind)
	abStatus.Apply(status.Instance(c.Instance), status.Kind(effectID), 0, int64(fx.DurationMs), nowMs)
	abFxMu.Lock()
	m := abFx[c.Instance]
	if m == nil {
		m = map[int]bool{}
		abFx[c.Instance] = m
	}
	m[effectID] = true
	abFxMu.Unlock()
	worldcore.Broadcast(pktOp(PacketEffect, EffectAdd, effectData{Instance: c.Instance, Effect: effectID}))
	log.Printf("abilities: %s cast %s effect=%d dur=%dms", c.Username, key, effectID, fx.DurationMs)
	if fx.DurationMs > 0 {
		time.AfterFunc(time.Duration(fx.DurationMs)*time.Millisecond, func() {
			_ = gnet.Send(c.Conn, pktOp(PacketAbility, AbilityToggle, abilityToggle{Key: key, Level: -1}))
			abFxMu.Lock()
			if abFx[c.Instance][effectID] {
				delete(abFx[c.Instance], effectID)
				abFxMu.Unlock()
				worldcore.Broadcast(pktOp(PacketEffect, EffectRemove, effectData{Instance: c.Instance, Effect: effectID}))
				return
			}
			abFxMu.Unlock()
		})
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// abApplyPoison records Venom on an instance (character.ts setPoison default
// in handlePoisonDamage — replace, no stacking). No frames here: damage
// surfaces as Points ticks, expiry is silent (TS clears with no final hit).
func abApplyPoison(instance string) {
	if instance == "" {
		return
	}
	abStatus.Apply(status.Instance(instance), status.KindPoison, 0, 0, time.Now().UnixMilli())
}

// abHeroWeaponPoisonous reports whether the hero's equipped weapon carries
// the items.json `poisonous` flag (combat.ts poison-on-hit parity).
func abHeroWeaponPoisonous(username string) bool {
	st := m5StateFor(username)
	if len(st.Equip) <= EquipmentWeapon {
		return false
	}
	key := st.Equip[EquipmentWeapon].Key
	if key == "" {
		return false
	}
	it := m6ItemInfoFor(key)
	return it != nil && it.Poisonous
}

// abFreezeApply/Clear bridge the m10 freezing area (area.ts addPlayer/
// removePlayer -> status Freezing) into the tracker so EFFECT_RATE damage
// flows through the Points pipeline. The visual Effect frames stay owned by
// m10SetFreezing; the tracker entry rides a derived key so leaving the area
// clears exactly the freezing entry (Clear is per-instance).
func abFreezeApply(instance string) {
	if instance == "" {
		return
	}
	abStatus.Apply(status.Instance(instance+abFreezeSuffix), status.KindFreezing, 0, -1, time.Now().UnixMilli())
	abFxMu.Lock()
	abFreezeSet[instance] = true
	abFxMu.Unlock()
}

func abFreezeClear(instance string) {
	if instance == "" {
		return
	}
	abStatus.Clear(status.Instance(instance + abFreezeSuffix))
	abFxMu.Lock()
	delete(abFreezeSet, instance)
	abFxMu.Unlock()
}

// abStatusTick drains due DoT ticks into the existing Points pipeline and
// reaps expired ability effects into EffectRemove frames. Called from the
// central 20Hz tick loop (additive: empty tracker = Tick only).
func abStatusTick() {
	nowMs := time.Now().UnixMilli()
	for _, ex := range abStatus.Tick(nowMs) {
		inst := string(ex.Instance)
		base := inst
		if strings.HasSuffix(inst, abFreezeSuffix) {
			base = strings.TrimSuffix(inst, abFreezeSuffix)
		}
		if c, _ := worldcore.Find[*playerConn](base); c != nil {
			m9DamagePlayer(c, ex.Damage, nil)
			log.Printf("abilities: dot %s kind=%d dmg=%d hp=%d", base, int(ex.Kind), ex.Damage, m9PlayerHP(c))
			continue
		}
		if m := m9MobFor(base); m != nil {
			m9PlayerHit(m, nil, ex.Damage)
			log.Printf("abilities: dot mob %s kind=%d dmg=%d", base, int(ex.Kind), ex.Damage)
		}
	}
	// Expiry sweep: tracker reaps silently on Tick, so translate a vanished
	// ability entry into the EffectRemove the client expects. /toggle and m10
	// freezing effects live outside abFx and are untouched here.
	abFxMu.Lock()
	for inst, effs := range abFx {
		for effectID := range effs {
			if !abStatus.Has(status.Instance(inst), status.Kind(effectID)) {
				delete(effs, effectID)
				worldcore.Broadcast(pktOp(PacketEffect, EffectRemove, effectData{Instance: inst, Effect: effectID}))
			}
		}
		if len(effs) == 0 {
			delete(abFx, inst)
		}
	}
	abFxMu.Unlock()
}

// abForgetPlayer drops per-conn ability state on disconnect (m13ForgetPlayer
// precedent) including any leaked freeze-tracker keys.
func abForgetPlayer(c *playerConn) {
	if c == nil {
		return
	}
	abStatus.Clear(status.Instance(c.Instance + abFreezeSuffix))
	abMu.Lock()
	delete(abMana, c.Instance)
	delete(abTarget, c.Instance)
	abMu.Unlock()
	abFxMu.Lock()
	delete(abFx, c.Instance)
	delete(abFreezeSet, c.Instance)
	abFxMu.Unlock()
}

// ---------------------------------------------------------------------------
// TESTMAP debug dispatcher (m9test/m11test precedent, rides [46 {abtest}]).
// ---------------------------------------------------------------------------

// abTestHandler processes TESTMAP debug ops: grant/echo for the unlock leg,
// apply for deterministic DoT legs (kind poison|burning|freezing,
// power/durationMs optional, kind defaults apply), mana to set the session
// mana directly.
func abTestHandler(c *playerConn, data []byte) {
	if !testMode || c == nil {
		return
	}
	var d struct {
		AbTest     string `json:"abtest"`
		Key        string `json:"key"`
		Level      int    `json:"level"`
		Kind       string `json:"kind"`
		Power      int    `json:"power"`
		DurationMs int64  `json:"durationMs"`
		Value      int    `json:"value"`
	}
	if err := json.Unmarshal(data, &d); err != nil || d.AbTest == "" {
		return
	}
	switch d.AbTest {
	case "grant":
		level := d.Level
		if level < 1 {
			level = 1
		}
		if abGrantAbility(c, c.Username, d.Key, level) {
			m6Notify(c, "ab:grant "+d.Key+"="+itoa(int64(abLevels[c.Username][d.Key])))
		}
	case "apply":
		var kind status.Kind
		switch strings.ToLower(d.Kind) {
		case "poison":
			kind = status.KindPoison
		case "burning":
			kind = status.KindBurning
		case "freezing":
			kind = status.KindFreezing
		default:
			return
		}
		abStatus.Apply(status.Instance(c.Instance), kind, d.Power, d.DurationMs, time.Now().UnixMilli())
		m6Notify(c, "ab:applied "+strings.ToLower(d.Kind))
	case "mana":
		abMu.Lock()
		abMana[c.Instance] = d.Value
		abMu.Unlock()
		m6Notify(c, "ab:mana="+itoa(int64(d.Value)))
	case "echo":
		abMu.Lock()
		keys := make([]string, 0, len(abLevels[c.Username]))
		for k, lv := range abLevels[c.Username] {
			keys = append(keys, k+"="+itoa(int64(lv)))
		}
		sort.Strings(keys)
		mana, ok := abMana[c.Instance]
		abMu.Unlock()
		if !ok {
			mana = abMaxMana
		}
		m6Notify(c, "ab:abilities ["+strings.Join(keys, ",")+"] mana="+itoa(int64(mana)))
	}
}
