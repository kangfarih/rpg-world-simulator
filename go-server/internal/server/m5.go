// M5 slice 1: drops/loot + XP/skills + SQLite persist slice.
//
// Drops mirror packages/server mob.getDrops: one roll on the mob's personal
// `drops` list plus one roll per `dropTables` entry (tables.json), chance vs
// DROP_PROBABILITY 100000 (modules.ts:625). Single drop -> Item entity
// (type 2); multiple -> LootBag entity (type 8, take-all on Target/Step).
// Blink [26] fires LOOT_BLINK_MS before destroy at LOOT_DESPAWN_MS
// (defaults 20s/30s; truth ItemDefaults are 30s/34s - shortened so loot
// never outlives the e2e windows).
//
// XP mirrors player.handleExperience (2 XP per damage, Health 1/4 + school
// share) and resourceskill (table experience on exhaust). Thresholds are the
// RuneScape LevelExp port (loader.ts loadLevels). Level-up broadcasts Sync
// + Experience Skill, plus the Healing FX as the heal anim (no Heal packet:
// connection.ts handleHeal would render a +HP splat we never earned).
//
// Persist is SQLite via modernc.org/sqlite (pure Go, no cgo), DB_PATH or
// data.db next to the binary (go-server/data.db, gitignored via
// go-server/*.db*). WAL + NORMAL, single writer (dbMu), dirty flush every
// 10s + synchronously on disconnect + on SIGTERM/SIGINT.
package server

import (
	"database/sql"
	"encoding/json"
	"log"
	"math"
	"math/rand"
	"os"
	"sync"
	"time"

	"rpg-world-server/internal/abilities"
	"rpg-world-server/internal/controller"
	"rpg-world-server/internal/entity"
	gnet "rpg-world-server/internal/net"
	"rpg-world-server/internal/persist"
	"rpg-world-server/internal/player"
	worldcore "rpg-world-server/internal/world"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// XP formula (formulas.ts LevelExp + loader.ts loadLevels, RuneScape curve).
// ---------------------------------------------------------------------------
//
// Canonical owner: internal/player (xp.go). The wrappers below keep the
// root names every other seam uses with identical values.
func expToLevel(xp int) int { return player.ExpToLevel(xp) }

func nextExp(xp int) int { return player.NextExp(xp) }

// ---------------------------------------------------------------------------
// Drop tables (packages/server/data/tables.json + mobs.json).
// ---------------------------------------------------------------------------
//
// Canonical owner: internal/entity (loot.go). The aliases + wrappers below
// keep the root names every other seam uses with identical values.
type m5DropEntry struct {
	Key     string `json:"key"`
	Chance  int    `json:"chance"`
	Count   int    `json:"count"`
	Quest   string `json:"quest"`
	Backend string `json:"-"`
}

type (
	m5MobProfile = entity.MobProfile
	m5DropJSON   = entity.DropJSON
	m5Drop       = entity.Drop
	m5Loot       = entity.Loot
)

const m5DropProbability = entity.DropProbability

func m5DataPath(name string) string {
	return resourceDataPath(name)
}

func loadM5Tables() { entity.LoadLootTables() }

// m5RollEntry ports mob.getRandomItem (canonical owner: internal/entity
// RollEntry). Gate filtering lives in m5RollEntryGated (M11 quest gates
// evaluate against the killer's progression); this rolls uniformly over the
// entries given.
func m5RollEntry(entries []m5DropJSON, level int) (key string, count int, ok bool) {
	return entity.RollEntry(entries, level)
}

// m5GetDrops ports mob.getDrops for a mob key (canonical owner:
// internal/entity GetDrops).
func m5GetDrops(mobKey string) []m5Drop {
	return entity.GetDrops(mobKey)
}

// m5GetDropsFor ports mob.getDrops with a killer context (canonical owner:
// internal/entity GetDropsFor).
func m5GetDropsFor(mobKey, username string) []m5Drop {
	return entity.GetDropsFor(mobKey, username)
}

// m5RollEntryGated filters quest/achievement-gated entries by the killer's
// progression, then rolls uniformly (canonical owner: internal/entity
// RollEntryGated).
func m5RollEntryGated(username string, entries []m5DropJSON, level int) (string, int, bool) {
	return entity.RollEntryGated(username, entries, level)
}

// ---------------------------------------------------------------------------
// Loot entities (Item type 2 / LootBag type 8).
// ---------------------------------------------------------------------------
//
// Canonical owner: internal/entity (loot.go: registry, spawn paths,
// blink/destroy timers, Who payloads). The wrappers below keep the root
// names every other seam uses with identical frames/logs.
// lootWorld implements entity.LootWorld over the world Registry (position
// index + Spawn/Blink/Despawn fan-out). Frames keep their exact shapes;
// the package builds them with internal/protocol (same constructors).
type lootWorld struct{}

func (lootWorld) SetEntityPos(inst string, x, y int)     { worldcore.SetEntityPos(inst, x, y) }
func (lootWorld) EntityPos(inst string) (int, int, bool) { return worldcore.EntityPos(inst) }
func (lootWorld) RemoveEntity(inst string)               { worldcore.RemoveEntity(inst) }
func (lootWorld) Broadcast(frames ...[]any)              { worldcore.Broadcast(frames...) }

func m5NearWalkable(x, y int) (int, int) { return entity.NearWalkable(x, y) }

// m5SpawnLoot drops the roll at the corpse: single -> Item, multi -> LootBag
// (take-all on Target/Step; lootbag menu Open flow deferred, logged).
func m5SpawnLoot(mobKey string, cx, cy int, owner string) {
	entity.SpawnLoot(mobKey, cx, cy, owner)
}

func m5BlinkLoot(inst string) { entity.BlinkLoot(inst) }

func m5DestroyLoot(inst, why string) { entity.DestroyLoot(inst, why) }

// ---------------------------------------------------------------------------
// Player state: inventory + skills + XP.
// ---------------------------------------------------------------------------

type m5Slot struct {
	Key   string
	Count int
	Ench  Enchantments // item enchantments {enchantmentId: {level}} (M12)
}

// m5SlotEnchJSON serializes a slot's enchantments for the inventory DB
// column ({} when none).
func m5SlotEnchJSON(s m5Slot) string {
	if s.Ench == nil {
		return "{}"
	}
	raw, err := json.Marshal(s.Ench)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// m5SlotEnchParse restores a slot's enchantments from the DB column.
func m5SlotEnchParse(raw string) Enchantments {
	if raw == "" || raw == "{}" {
		return nil
	}
	ench := Enchantments{}
	if err := json.Unmarshal([]byte(raw), &ench); err != nil {
		return nil
	}
	if len(ench) == 0 {
		return nil
	}
	return ench
}

type m5Skill struct {
	Level int
	XP    int
}

type m5State struct {
	X, Y   int
	Level  int
	HP     int
	Rank   int // Modules.Ranks value (persist rank column parity)
	Inv    []m5Slot
	Bank   []m5Slot
	Equip  []m5Slot // length ModulesEquipmentCount; Count 0 = empty slot
	Skills map[int]*m5Skill
}

var (
	pstateMu sync.Mutex
	pstates  = map[string]*m5State{}
)

// Skill ids mirror Modules.Skills order (canonical owner: internal/player).
const (
	SkillLumberjacking = player.SkillLumberjacking
	SkillAccuracy      = player.SkillAccuracy
	SkillArchery       = player.SkillArchery
	SkillHealth        = player.SkillHealth
	SkillMagic         = player.SkillMagic
	SkillMining        = player.SkillMining
	SkillStrength      = player.SkillStrength
	SkillDefense       = player.SkillDefense
	SkillFishing       = player.SkillFishing
	SkillForaging      = player.SkillForaging
)

func m5SkillName(id int) string { return player.SkillName(id) }

func m5CombatSkill(id int) bool { return player.CombatSkill(id) }

func m5StateFor(key string) *m5State {
	pstateMu.Lock()
	defer pstateMu.Unlock()
	st, ok := pstates[key]
	if !ok {
		st = &m5State{X: 100, Y: 96, Level: 1, HP: 100, Skills: map[int]*m5Skill{}}
		pstates[key] = st
	}
	if st.Skills == nil {
		st.Skills = map[int]*m5Skill{}
	}
	// Equipment is a fixed 12-slot array (Modules.Equipment): nil or short
	// slices would panic on slot indexing, so normalize lazily here.
	if len(st.Equip) != ModulesEquipmentCount {
		eq := make([]m5Slot, ModulesEquipmentCount)
		copy(eq, st.Equip)
		st.Equip = eq
	}
	return st
}

func m5CombatLevelLocked(st *m5State) int {
	levels := make(map[int]int, len(st.Skills))
	for id, s := range st.Skills {
		levels[id] = s.Level
	}
	return player.CombatLevel(levels)
}

// connByInstance moved to internal/world (D2a): use
// worldcore.Find[*playerConn](inst) at the former call sites.

// m5AddXP awards skill XP (canonical owner: internal/player AddXP). The
// state transition runs under pstateMu here; frames, level-up fanout and
// logs live in the package with identical shapes/text.
func m5AddXP(c *playerConn, key string, skill, amount int) int {
	var pc *player.Conn
	if c != nil {
		cc := c
		pc = &player.Conn{
			Instance: c.Instance,
			Username: c.Username,
			Send: func(frames ...[]any) {
				_ = gnet.Send(cc.Conn, frames...)
			},
		}
	}
	return player.AddXP(xpDeps(), pc, key, skill, amount)
}

// xpDeps wires the player.XP seams to the root globals (m5StateFor/pstateMu
// state, gnet/worldcore transport, welcomePlayer Sync payload, persist
// dirty set, world XP event).
func xpDeps() player.Deps {
	return player.Deps{
		ApplyAward: func(key string, skill, amount int) player.AwardResult {
			st := m5StateFor(key)
			pstateMu.Lock()
			defer pstateMu.Unlock()
			s, ok := st.Skills[skill]
			if !ok {
				s = &m5Skill{Level: 1}
				st.Skills[skill] = s
			}
			prev := s.Level
			s.XP += amount
			if s.XP < 0 {
				s.XP = 0 // TS subtraction floors at 0 (no negative XP store)
			}
			s.Level = expToLevel(s.XP)
			if s.Level < 1 {
				s.Level = 1
			}
			if m5CombatSkill(skill) {
				st.Level = m5CombatLevelLocked(st)
			}
			return player.AwardResult{
				Prev: prev, Level: s.Level, XP: s.XP,
				CombatLevel: st.Level, X: st.X, Y: st.Y,
			}
		},
		Broadcast: func(frames ...[]any) { worldcore.Broadcast(frames...) },
		MarkDirty: markDirty,
		SyncFrame: func(instance string, x, y, combatLevel int) []any {
			ph := welcomePlayer(instance)
			ph.X, ph.Y = x, y
			ph.Level = intp(combatLevel)
			return pkt(PacketSync, ph)
		},
		Lookup: func(instance string) (player.Conn, bool) {
			c, _ := worldcore.Find[*playerConn](instance)
			if c == nil {
				return player.Conn{}, false
			}
			cc := c
			return player.Conn{
				Instance: c.Instance,
				Username: c.Username,
				Send: func(frames ...[]any) {
					_ = gnet.Send(cc.Conn, frames...)
				},
			}, true
		},
		XPBoost: worldXPBoost,
	}
}

func m5Percentage(xp int) float64 { return player.Percentage(xp) }

// m5AwardCombatXP ports player.handleExperience (canonical owner:
// internal/player AwardCombatXP). The class flags mirror
// weapon.isArcher/isMagic (heroIsArcher/heroIsMagic over the equipped
// weapon, with precedence over the style switch exactly as in TS);
// Style carries the attack-style store (controller.AttackStyleFor:
// last explicit switch, else the weapon's first style) and HasMana
// the hasManaForAttack gate (player.ts:1700 — current mana >= weapon
// manaCost via the same heroManaCost lookup the swing gate uses;
// 0 for non-magic weapons so melee never halves).
func m5AwardCombatXP(c *playerConn, key string, damage int, archer, mage bool) {
	var pc *player.Conn
	d := xpDeps()
	if c != nil {
		cc := c
		pc = &player.Conn{
			Instance: c.Instance,
			Username: c.Username,
			Send: func(frames ...[]any) {
				_ = gnet.Send(cc.Conn, frames...)
			},
		}
		d.Style = func() int { return controller.AttackStyleFor(m6deps(), cc.Username) }
		d.HasMana = func() bool { return abilities.ManaFor(cc.Instance) >= heroManaCost(cc.Username) }
	}
	player.AwardCombatXP(d, pc, key, damage, archer, mage)
}

// m5AwardGatherXP is the M4-hook successor: table experience on exhaust
// (canonical owner: internal/player GatherXP).
func m5GatherXP(attackerInstance, skill string, xp int) {
	player.GatherXP(xpDeps(), attackerInstance, skill, xp)
}

// m5AddItem stacks (items.json stackable) or appends; returns slot index.
func m5AddItem(key, itemKey string, count int) int {
	return m5AddItemEnch(key, itemKey, count, nil)
}

// m5AddItemEnch adds with enchantments (M12 crafting/enchant/trade paths);
// stacking only merges when both stacks have identical enchantment maps —
// a non-nil ench always takes a fresh slot.
func m5AddItemEnch(key, itemKey string, count int, ench Enchantments) int {
	loadM5Tables()
	st := m5StateFor(key)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	if ench == nil && entity.Stackable(itemKey) {
		for i, s := range st.Inv {
			if s.Key == itemKey {
				st.Inv[i].Count += count
				return i
			}
		}
	}
	st.Inv = append(st.Inv, m5Slot{Key: itemKey, Count: count, Ench: ench})
	return len(st.Inv) - 1
}

// m5Pickup takes one loot entity for the player: inventory + Container Add +
// Despawn. Step path calls with the on-tile instance; Target path with the
// clicked instance (range-lenient, logged - truth enforces adjacency via
// getDistance, slice 1 keeps pickup observable).
func m5Pickup(c *playerConn, inst string) bool {
	l, ok := entity.FindLoot(inst)
	if !ok {
		return false
	}
	dx := c.Sess.PlayerX - l.X
	if dx < 0 {
		dx = -dx
	}
	dy := c.Sess.PlayerY - l.Y
	if dy < 0 {
		dy = -dy
	}
	if dx+dy > 1 {
		log.Printf("m5: %s takes %s from %d tiles (lenient pickup)", c.Instance, inst, dx+dy)
	}
	for _, it := range l.Items {
		if it.Key == "" {
			continue // taken lootbag slot (hole — single-take path)
		}
		idx := m5AddItem(c.Username, it.Key, it.Count)
		_ = gnet.Send(c.Conn, pktOp(PacketContainer, ContainerAdd, containerData{
			Type: ContainerTypeInventory,
			Slot: &slotData{Index: idx, Key: it.Key, Count: it.Count, Enchantments: map[string]any{}},
		}))
		// Statistics: owner pickups count as drops (player.ts:1268 parity —
		// only when the loot owner matches the picker).
		if l.Owner != "" && l.Owner == c.Username {
			statsAddDrop(c, it.Key, it.Count)
		}
	}
	markDirty(c.Username)
	m5DestroyLoot(inst, "picked up by "+c.Instance)
	return true
}

// m5PickupAt steps onto loot: any loot on the player's tile is taken —
// single Items instantly, bags via the Open menu (lootbag.ts parity:
// handleMovementStop opens bags instead of taking them).
func m5PickupAt(c *playerConn) {
	m5PickupAtTile(c, c.Sess.PlayerX, c.Sess.PlayerY)
}

// m5PickupAtTile takes loot lying on (x,y) (Step destination path).
func m5PickupAtTile(c *playerConn, x, y int) {
	if inst, ok := entity.FindLootAt(x, y); ok {
		if entity.IsBag(inst) {
			openLootBagFor(c, inst)
			return
		}
		m5Pickup(c, inst)
	}
}

// m5TrackPos records the authoritative tile and marks the row dirty.
// The tracked plateauLevel refreshes on the same update (handler.ts:333
// player.plateauLevel parity — every authoritative position update).
func m5TrackPos(c *playerConn) {
	if c.Username == "" {
		return
	}
	st := m5StateFor(c.Username)
	pstateMu.Lock()
	st.X, st.Y = c.Sess.PlayerX, c.Sess.PlayerY
	pstateMu.Unlock()
	markDirty(c.Username)
	plateauTrack(c)
}

// m5RegisterLoot adds a pre-built loot entry to the registry without any
// timers (M10 chest drops: persistent items with no blink/expiry — Node
// chest items never expire on their own). Pickup routing is shared.
func m5RegisterLoot(inst, key string, count, x, y int, owner string) {
	entity.RegisterLoot(inst, key, count, x, y, owner)
}

// m5IsLoot reports whether id is a live loot entity.
func m5IsLoot(id string) bool { return entity.IsLoot(id) }

// m5LootPayload rebuilds the Spawn payload for a loot instance (Who path;
// canonical owner: internal/entity LootPayload).
func m5LootPayload(inst string) (any, bool) { return entity.LootPayload(inst) }

// ---------------------------------------------------------------------------
// Player combat (C Combat {instance,target} vs killable mobs).
// ---------------------------------------------------------------------------

var (
	mobHPMu sync.Mutex
	mobHP   = map[string]int{} // non-combat-mode mob instances (m1).
)

func m5MobMaxHP(instance, mobKey string) int {
	if instance == "m1" {
		return 30 // spawnFrames flat default.
	}
	if p, ok := entity.Profile(mobKey); ok && p != nil && p.HitPoints > 0 {
		return p.HitPoints
	}
	return 20
}

// handlePlayerAttack routes one hero swing at the combat rat, the boss dummy,
// or the plain-mode rat. Damage pipeline mirrors applyBossHitLocked
// (Animation + Combat Hit + Points); mob death uses the Despawn path.
// M9: engine-registered mobs (m-rat-1, m1, any m9test spawn) take the
// mob.ts/handler.ts path (Points + retaliate + engine respawn) instead of
// the legacy per-instance blocks.
func handlePlayerAttack(c *playerConn, target string) {
	if target == "" || c == nil {
		return
	}
	// Arrow gate (combat.ts:208-210 sendRangedAttack): a non-magic archer
	// with no arrows stops the loop — the per-swing Go equivalent is a
	// silent no-swing (no frames). Same heroIsArcher detection the
	// damage-type roll uses, same EquipmentArrows slot lookup the equip
	// code uses. No arrows are consumed per shot (TS has no consumption).
	if heroIsArcher(c.Username) && !heroIsMagic(c.Username) && !heroHasArrows(c.Username) {
		log.Printf("m5: %s bow swing refused (no arrows)", c.Instance)
		return
	}
	dmg := 8 + rand.Intn(5)
	// Attack-style damage bonus (formulas.getMaxDamage parity): the hero's
	// current style scales the swing (bots keep their own styles via
	// combatMaxDamageFloat). Round (not truncate) so slash/crush/shared
	// stay observable on the small 8-12 hero roll.
	if mult := controller.StyleDamageMult(controller.AttackStyleFor(m6deps(), c.Username)); mult != 1 {
		dmg = int(math.Round(float64(dmg) * mult))
	}
	// M9: engine mobs first — Points/retaliate/death/respawn/loot live in
	// the engine now (m9PlayerHit -> m9KillMob -> m5SpawnLoot).
	// M11_HERODMG debug accelerator (mirrors M9_MOBDMG): keeps the e2e's
	// 140-HP mobs in a few-swing kill range.
	dmg = int(float64(dmg) * m11HeroDamageMult())
	if m := m9MobFor(target); m != nil {
		if m.dead {
			log.Printf("m5: %s swings at dead %s (ignored)", c.Instance, target)
			return
		}
		// TS-exact plateau gate (character.ts isNearTarget): only RANGED
		// attackers (attackRange > 1) shooting UP a plateau are refused
		// (silent no-swing, see entity.RangedBlocked). The hero has no
		// range model (welcomePlayer AttackRange 1; player.sync weapon
		// recompute unmodeled), so the hero always swings as melee (1).
		m.mu.Lock()
		mobPlateau := m.plateau
		m.mu.Unlock()
		if entity.RangedBlocked(1, plateauGet(c.Instance), mobPlateau) {
			log.Printf("m5: %s swings at %s across plateaus (refused)", c.Instance, target)
			return
		}
		// Magic mana gate (player/handler.ts handleAttack): staff swings
		// need mana — LOW_MANA + no swing when short.
		if !heroMagicGate(c) {
			return
		}
		abSetTarget(c.Instance, target)
		worldcore.Broadcast(pkt(PacketAnimation, animationData{Instance: c.Instance, Action: ActionAttack}))
		// Hero damage-type roll (player.ts getDamageType): the TYPE (+ AoE
		// flag) changes, damage numbers stay in the 8-12 roll shape.
		hitType, aoe := heroDamageType(c.Username, combatRand)
		hit := HitData{Type: hitType, Damage: dmg}
		if aoe > 0 {
			hit.Aoe = intp(aoe)
		}
		worldcore.Broadcast(pktOp(PacketCombat, CombatHit, combatData{
			Instance: c.Instance, Target: target,
			Hit: hit,
		}))
		m9PlayerHit(m, c, dmg)
		// TS combat.ts sendAttack order: hit, then target.addStatusEffect.
		// Skip corpses (TS death clears status; the engine has no death
		// clear, so a tracker entry on a corpse would leak).
		m.mu.Lock()
		victimDead := m.dead
		m.mu.Unlock()
		if !victimDead {
			applyHitStatus(target, hitType)
		}
		// Explosive splash damages nearby mobs (character.ts handleAoE).
		if aoe > 0 {
			explosiveSplash(m, c, dmg)
		}
		// Bloodsucking proc on the attacker (character.ts handleBloodsucking
		// — inside hit(), before the death check, so it runs here too).
		if ok, level := heroBloodsucking(c.Username); ok && bloodsuckRoll(combatRand) {
			if heal := bloodsuckHeal(dmg, level); heal >= 1 {
				m6vitals{}.HealHero(c.Instance, heal, 0)
			}
		}
		// TS combat.ts poison-on-hit: a poisonous weapon poisons the victim.
		if abHeroWeaponPoisonous(c.Username) {
			abApplyPoison(target)
		}
		m5AwardCombatXP(c, c.Username, dmg, heroIsArcher(c.Username), heroIsMagic(c.Username))
		return
	}
	switch target {
	case combatDummyInstance:
		combatMu.Lock()
		if combatDead {
			combatMu.Unlock()
			log.Printf("m5: %s swings at dead boss (ignored)", c.Instance)
			return
		}
		// Magic mana gate + damage-type roll (same hero-swing rules as the
		// engine-mob path; no splash victim on the legacy dummy).
		if !heroMagicGate(c) {
			combatMu.Unlock()
			return
		}
		hitType, _ := heroDamageType(c.Username, combatRand)
		abSetTarget(c.Instance, target)
		applyBossHitLocked(c.Instance, dmg, hitType, nil, false, -1, true)
		died := combatDead
		combatMu.Unlock()
		if ok, level := heroBloodsucking(c.Username); ok && bloodsuckRoll(combatRand) {
			if heal := bloodsuckHeal(dmg, level); heal >= 1 {
				m6vitals{}.HealHero(c.Instance, heal, 0)
			}
		}
		m5AwardCombatXP(c, c.Username, dmg, heroIsArcher(c.Username), heroIsMagic(c.Username))
		if died {
			m5SpawnLoot("golem", combatDummyX, combatDummyY, c.Instance)
		}
	default:
		// M9: engine-registered mobs were handled above; anything else is
		// not killable (legacy note kept from slice 1).
		log.Printf("m5: %s attacks %s (not killable)", c.Instance, target)
	}
}

// ---------------------------------------------------------------------------
// SQLite persist.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// SQLite persist (single-writer Store in internal/persist).
// ---------------------------------------------------------------------------
//
// The SQL + dirty set live in internal/persist (persist.Store: Open,
// EnsureSchema, MarkDirty/MarkClean/DirtyList, WritePlayer/LoadPlayer,
// Snapshot, FlushDirty, Close — moved here verbatim, identical schema,
// WAL+NORMAL pragmas, identical log text). This section keeps the root
// names every other seam uses — dbConn/dbMu for the m11/m13/social/
// abilities/ops direct-table access, and m5Init/markDirty/flushDirty/
// m5SaveSync/m5Load/m5LoginWelcome with unchanged signatures — and
// delegates to the store (converting m5State <-> persist.State).
// dbConn aliases the store handle (single connection, SetMaxOpenConns(1)).
// Ticker/goroutine ownership stays in root: m5Init starts the 10s dirty
// flush and the SIGTERM/SIGINT final-flush handler exactly as before.
// Lock discipline: never hold dbMu and pstateMu at the same time
// (m5Snapshot needs pstateMu). flushDirty/m5SaveSync snapshot first, then
// write under dbMu; m5Load holds dbMu across the store read and takes
// pstateMu only to install the result.

var (
	dbConn       *sql.DB
	dbMu         sync.Mutex
	persistStore *persist.Store
)

func dbPath() string {
	if p := os.Getenv("DB_PATH"); p != "" {
		return p
	}
	return "data.db"
}

func m5Init() {
	loadM5Tables()
	entity.ConfigureLoot(entity.LootDeps{
		World:       lootWorld{},
		Walkable:    func(x, y int) bool { return !blocked(x, y) },
		DoubleDrops: worldDoubleDrops,
		Gate:        m11DropGated,
		DataPath:    resourceDataPath,
	})
	st, err := persist.Open(dbPath())
	if err != nil {
		log.Fatalf("m5: %v", err)
	}
	persistStore = st
	dbConn = st.DB()
	log.Printf("m5: sqlite open %s (WAL+NORMAL)", dbPath())
	checkWorldHashGate()   // D3: world.json sha256 vs meta.world_hash (warn; strict only)
	abConfigure()          // abilities: wire the session seams (table handle + helpers)
	petConfigure()         // entity: wire the companion seams (inventory + combat)
	socConfigure()         // social: wire friends/guilds/hub seams (tables + presence)
	worldConfigureWarps()  // controller: wire the warp-runner seams (gates + teleport)
	worldConfigureEvents() // controller: wire the event fan-out seam (global notices)
	opsConfigure()         // app: wire the ops seams (API providers + console world)
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for range t.C {
			flushDirty()
		}
	}()
	// R1 drain lifecycle (same boot step): SIGTERM/SIGINT -> DRAINING (no
	// new conns, sim continues) -> empty-or-timeout -> flush barrier ->
	// exit. With zero players this is the old final-flush-and-exit.
	startDrainDriver()
	// R1 shard role: hub Client registration (all-in-one starts none).
	startShardClient()
}

func markDirty(key string) {
	if key == "" || dbConn == nil || persistStore == nil {
		return
	}
	persistStore.MarkDirty(key)
}

func m5Snapshot(key string) *m5State {
	pstateMu.Lock()
	defer pstateMu.Unlock()
	st, ok := pstates[key]
	if !ok {
		return nil
	}
	cp := &m5State{X: st.X, Y: st.Y, Level: st.Level, HP: st.HP, Rank: st.Rank, Skills: map[int]*m5Skill{}}
	cp.Inv = append(cp.Inv, st.Inv...)
	cp.Bank = append(cp.Bank, st.Bank...)
	cp.Equip = append(cp.Equip, st.Equip...)
	for id, s := range st.Skills {
		cp.Skills[id] = &m5Skill{Level: s.Level, XP: s.XP}
	}
	return cp
}

// m5ToPersist converts an in-memory player state to the persist snapshot
// (inventory + equipment enchantments serialized to the DB column format;
// bank rows carry no enchantments, matching the bank table).
func m5ToPersist(st *m5State) persist.State {
	ps := persist.State{
		X: st.X, Y: st.Y, Level: st.Level, HP: st.HP, Rank: st.Rank,
		Skills: make(map[int]persist.Skill, len(st.Skills)),
	}
	for _, s := range st.Inv {
		ps.Inv = append(ps.Inv, persist.Slot{Key: s.Key, Count: s.Count, Ench: m5SlotEnchJSON(s)})
	}
	for _, s := range st.Bank {
		ps.Bank = append(ps.Bank, persist.Slot{Key: s.Key, Count: s.Count})
	}
	for _, e := range st.Equip {
		ps.Equip = append(ps.Equip, persist.Slot{Key: e.Key, Count: e.Count, Ench: m5SlotEnchJSON(e)})
	}
	for id, s := range st.Skills {
		ps.Skills[id] = persist.Skill{Level: s.Level, XP: s.XP}
	}
	return ps
}

// persistToM5 converts a persist snapshot back to the in-memory state,
// normalizing the fixed ModulesEquipmentCount slot array (slot indexing
// must never panic) exactly like the old m5Load tail.
func persistToM5(ps persist.State) *m5State {
	st := &m5State{X: ps.X, Y: ps.Y, Level: ps.Level, HP: ps.HP, Rank: ps.Rank, Skills: map[int]*m5Skill{}}
	for _, s := range ps.Inv {
		st.Inv = append(st.Inv, m5Slot{Key: s.Key, Count: s.Count, Ench: m5SlotEnchParse(s.Ench)})
	}
	for _, s := range ps.Bank {
		st.Bank = append(st.Bank, m5Slot{Key: s.Key, Count: s.Count})
	}
	eslots := make([]m5Slot, 0, len(ps.Equip))
	for _, s := range ps.Equip {
		eslots = append(eslots, m5Slot{Key: s.Key, Count: s.Count, Ench: m5SlotEnchParse(s.Ench)})
	}
	eq := make([]m5Slot, ModulesEquipmentCount)
	copy(eq, eslots)
	st.Equip = eq
	for id, s := range ps.Skills {
		st.Skills[id] = &m5Skill{Level: s.Level, XP: s.XP}
	}
	return st
}

func writePlayer(key string, st *m5State) {
	if persistStore == nil {
		return
	}
	// Statistics counters ride the same row write as a JSON blob in the
	// additive `statistics` table (key-aware call site: the converters stay
	// key-agnostic so handoff.go keeps compiling untouched).
	ps := m5ToPersist(st)
	snap := statsCopyOf(key)
	ps.Stats = persist.StatsBlob{
		MobKills: snap.MobKills, MobExamines: snap.MobExamines,
		Resources: snap.Resources, Drops: snap.Drops,
	}
	_ = persistStore.WritePlayer(key, ps)
}

func flushDirty() {
	if dbConn == nil || persistStore == nil {
		return
	}
	// Lock discipline: never hold dbMu and pstateMu at the same time
	// (m5Snapshot needs pstateMu). Snapshot first, then write under dbMu.
	keys := persistStore.DirtyList()
	for _, key := range keys {
		st := m5Snapshot(key)
		dbMu.Lock()
		if st != nil {
			writePlayer(key, st)
		}
		persistStore.MarkClean(key)
		dbMu.Unlock()
	}
}

// m5SaveSync flushes one player immediately (disconnect path).
// Lock discipline: never hold dbMu and pstateMu at the same time.
func m5SaveSync(key string) {
	if dbConn == nil || persistStore == nil || key == "" {
		return
	}
	st := m5Snapshot(key)
	dbMu.Lock()
	if st != nil {
		writePlayer(key, st)
	}
	persistStore.MarkClean(key)
	dbMu.Unlock()
}

// m5Load restores a player row (Welcome from DB when the instance is known).
func m5Load(key string) (*m5State, bool) {
	if dbConn == nil || persistStore == nil || key == "" {
		return nil, false
	}
	dbMu.Lock()
	defer dbMu.Unlock()
	ps, ok := persistStore.LoadPlayer(key)
	if !ok {
		return nil, false
	}
	st := persistToM5(ps)
	// Statistics counters restore into the stats registry (statistics.load
	// parity for the gameplay fields).
	statsInstall(key, ps.Stats)
	pstateMu.Lock()
	pstates[key] = st
	pstateMu.Unlock()
	log.Printf("m5: loaded %s (pos %d,%d level %d inv %d bank %d skills %d)", key, st.X, st.Y, st.Level, len(st.Inv), len(st.Bank), len(st.Skills))
	return st, true
}

// m5LoginWelcome builds the Welcome payload: DB row when the login username
// is known, else the fresh hero. Also queues Container + Skill batches so a
// reconnect visibly restores inventory/skills.
func m5LoginWelcome(c *playerConn, username string) (PlayerData, [][]any) {
	key := username
	if key == "" {
		key = c.Instance
	}
	c.Username = key
	var st *m5State
	if loaded, ok := m5Load(key); ok {
		st = loaded
	} else {
		st = m5StateFor(key)
		markDirty(key)
	}
	// Rank durability (database.setRank parity): a persisted offline /setrank
	// lands on the session at login. Fresh rows carry 0 (None), a no-op.
	c.rank = st.Rank
	chatStateFor(c).rank = st.Rank
	c.Sess.PlayerX, c.Sess.PlayerY = st.X, st.Y
	worldcore.SetEntityPos(c.Instance, st.X, st.Y)
	worldcore.UpdateRegion(c, st.X, st.Y)
	ph := welcomePlayer(c.Instance)
	ph.X, ph.Y = st.X, st.Y
	if st.Level > 0 {
		ph.Level = intp(st.Level)
	}
	if st.HP > 0 {
		ph.HitPoints = intp(st.HP)
	}
	var extra [][]any
	pstateMu.Lock()
	slots := make([]any, 0, len(st.Inv))
	for i, s := range st.Inv {
		slots = append(slots, map[string]any{
			"index": i, "key": s.Key, "count": s.Count, "enchantments": enchAny(s.Ench),
		})
	}
	skills := make([]any, 0, len(st.Skills))
	for id, s := range st.Skills {
		skills = append(skills, map[string]any{
			"type": id, "experience": s.XP, "level": s.Level,
			"percentage": m5Percentage(s.XP), "nextExperience": nextExp(s.XP),
			"combat": m5CombatSkill(id),
		})
	}
	pstateMu.Unlock()
	if len(slots) > 0 {
		extra = append(extra, pktOp(PacketContainer, ContainerBatch, containerData{
			Type: ContainerTypeInventory, Data: &containerBatch{Slots: slots},
		}))
	}
	if len(skills) > 0 {
		extra = append(extra, pktOp(PacketSkill, SkillBatch, map[string]any{
			"skills": skills, "cheater": false,
		}))
	}
	if len(st.Bank) > 0 {
		bslots := make([]any, 0, len(st.Bank))
		for i, s := range st.Bank {
			bslots = append(bslots, map[string]any{
				"index": i, "key": s.Key, "count": s.Count, "enchantments": enchAny(s.Ench),
			})
		}
		extra = append(extra, pktOp(PacketContainer, ContainerBatch, containerData{
			Type: ContainerTypeBank, Data: &containerBatch{Slots: bslots},
		}))
	}
	// Equipment restore: Batch frame with the equipped entries (client
	// player.equip each -> sprites + profile). Empty slots are omitted,
	// matching Node's DB loader (skips falsy keys).
	eqs := make([]any, 0, len(st.Equip))
	for t, e := range st.Equip {
		if e.Key == "" || e.Count < 1 {
			continue
		}
		data := m6EquipmentData(t, e.Key, e.Count, true)
		data["enchantments"] = enchAny(e.Ench)
		eqs = append(eqs, data)
	}
	if len(eqs) > 0 {
		extra = append(extra, pktOp(PacketEquipment, EquipmentBatch, equipBatchData{Equipments: eqs}))
	}
	return ph, extra
}
