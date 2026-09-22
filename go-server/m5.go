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
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"rpg-world-server/internal/meta"
	gnet "rpg-world-server/internal/net"
	"rpg-world-server/internal/persist"
	worldcore "rpg-world-server/internal/world"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// XP formula (formulas.ts LevelExp + loader.ts loadLevels, RuneScape curve).
// ---------------------------------------------------------------------------

var (
	levelExpOnce sync.Once
	levelExpTbl  []int
)

func initLevelExp() {
	levelExpOnce.Do(func() {
		// Mirror loader.ts exactly: indices 0..MAX_LEVEL-1 (loop i < MAX_LEVEL).
		levelExpTbl = meta.BuildLevelExp(ModulesMaxLevel)
	})
}

func expToLevel(xp int) int {
	initLevelExp()
	return meta.ExpToLevel(levelExpTbl, ModulesMaxLevel, xp)
}

func nextExp(xp int) int {
	initLevelExp()
	return meta.NextExp(levelExpTbl, xp)
}

// ---------------------------------------------------------------------------
// Drop tables (packages/server/data/tables.json + mobs.json).
// ---------------------------------------------------------------------------

type m5DropEntry struct {
	Key     string `json:"key"`
	Chance  int    `json:"chance"`
	Count   int    `json:"count"`
	Quest   string `json:"quest"`
	Backend string `json:"-"`
}

type m5MobProfile struct {
	Name       string       `json:"name"`
	Level      int          `json:"level"`
	HitPoints  int          `json:"hitPoints"`
	Drops      []m5DropJSON `json:"drops"`
	DropTables []string     `json:"dropTables"`
}

type m5DropJSON struct {
	Key         string `json:"key"`
	Chance      int    `json:"chance"`
	Count       int    `json:"count"`
	Quest       string `json:"quest"`
	Status      string `json:"status"`
	Achievement string `json:"achievement"`
}

const m5DropProbability = 100000 // Modules.Constants.DROP_PROBABILITY.

var (
	m5TablesOnce sync.Once
	m5Tables     = map[string][]m5DropJSON{}
	m5Mobs       = map[string]*m5MobProfile{}
	m5Stackable  = map[string]bool{}
)

func m5DataPath(name string) string {
	return resourceDataPath(name)
}

func loadM5Tables() {
	m5TablesOnce.Do(func() {
		raw, err := os.ReadFile(m5DataPath("tables"))
		if err != nil {
			log.Fatalf("read tables.json: %v", err)
		}
		var tbl map[string]struct {
			Drops []m5DropJSON `json:"drops"`
		}
		if err := json.Unmarshal(raw, &tbl); err != nil {
			log.Fatalf("parse tables.json: %v", err)
		}
		for k, v := range tbl {
			m5Tables[k] = v.Drops
		}
		raw, err = os.ReadFile(m5DataPath("mobs"))
		if err != nil {
			log.Fatalf("read mobs.json: %v", err)
		}
		if err := json.Unmarshal(raw, &m5Mobs); err != nil {
			log.Fatalf("parse mobs.json: %v", err)
		}
		raw, err = os.ReadFile(m5DataPath("items"))
		if err != nil {
			log.Fatalf("read items.json: %v", err)
		}
		var items map[string]struct {
			Stackable *bool `json:"stackable"`
		}
		if err := json.Unmarshal(raw, &items); err != nil {
			log.Fatalf("parse items.json: %v", err)
		}
		for k, v := range items {
			if v.Stackable != nil && *v.Stackable {
				m5Stackable[k] = true
			}
		}
		log.Printf("m5: tables=%d mobs=%d stackable=%d", len(m5Tables), len(m5Mobs), len(m5Stackable))
	})
}

// m5RollEntry ports mob.getRandomItem: pick one entry uniformly, fix counts
// for gold/flask/arrow/feather, then roll chance vs DROP_PROBABILITY.
// Quest/achievement-gated entries roll only when the gate passes (M11:
// m11DropGated activates the codersglitch skeleton talisman etc.); gated
// entries never selected uniformly — Node filters them from the pool first
// (fullfillsQuest), so the gate check happens before the uniform pick.
func m5RollEntry(entries []m5DropJSON, level int) (key string, count int, ok bool) {
	// Gate filtering moved to m5RollEntryGated (M11: quest/achievement gates
	// evaluate against the killer's progression, mob.getRandomItem receives
	// the player). This function rolls uniformly over the entries given.
	avail := entries
	if len(avail) == 0 {
		return "", 0, false
	}
	drop := avail[rand.Intn(len(avail))]
	count = drop.Count
	if count <= 0 {
		count = 1
	}
	switch drop.Key {
	case "gold":
		count = rand.Intn(level*10-level+1) + level
	case "flask":
		count = rand.Intn(3) + 1
	case "arrow":
		count = rand.Intn(level) + 1
	case "firearrow":
		count = rand.Intn((level+1)/2) + 1
	case "feather":
		count = rand.Intn(level) + 1
	}
	chance := drop.Chance
	if chance > m5DropProbability {
		chance = m5DropProbability
	}
	if rand.Intn(m5DropProbability+1) < chance {
		return drop.Key, count, true
	}
	return "", 0, false
}

type m5Drop struct {
	Key   string
	Count int
}

// m5GetDrops ports mob.getDrops for a mob key: personal roll + one roll per
// drop table. Empty result falls back to coins + log so every kill in the
// slice is observable (spec-authorized fallback).
func m5GetDrops(mobKey string) []m5Drop {
	return m5GetDropsFor(mobKey, "")
}

// m5GetDropsFor ports mob.getDrops with a killer context: M11 quest gates
// evaluate against the killer's progression (mob.getDrops takes the player).
// Empty result falls back to coins + log so every kill in the slice is
// observable (spec-authorized fallback).
func m5GetDropsFor(mobKey, username string) []m5Drop {
	loadM5Tables()
	prof := m5Mobs[mobKey]
	level := 1
	var out []m5Drop
	if prof != nil {
		level = prof.Level
		if level < 1 {
			level = 1
		}
		if k, c, ok := m5RollEntryGated(username, prof.Drops, level); ok {
			out = append(out, m5Drop{Key: k, Count: c})
		}
		for _, t := range prof.DropTables {
			entries, ok := m5Tables[t]
			if !ok {
				log.Printf("m5: mob %s has invalid drop table %s", mobKey, t)
				continue
			}
			if k, c, ok := m5RollEntryGated(username, entries, level); ok {
				out = append(out, m5Drop{Key: k, Count: c})
			}
		}
	}
	if len(out) == 0 {
		out = []m5Drop{
			{Key: "gold", Count: rand.Intn(level) + 1},
			{Key: "logs", Count: 1},
		}
		log.Printf("m5: %s rolls empty -> fallback coins+log", mobKey)
	}
	return out
}

// m5RollEntryGated filters quest/achievement-gated entries by the killer's
// progression (mob.getRandomItem receives the player), then rolls uniformly
// over the survivors (m5RollEntry body). Empty username = gates closed.
func m5RollEntryGated(username string, entries []m5DropJSON, level int) (string, int, bool) {
	if username != "" {
		filtered := entries[:0:0]
		for _, e := range entries {
			if m11DropGated(username, e.Quest, e.Achievement, e.Status) {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}
	return m5RollEntry(entries, level)
}

// ---------------------------------------------------------------------------
// Loot entities (Item type 2 / LootBag type 8).
// ---------------------------------------------------------------------------

var (
	lootDespawnDelay = func() time.Duration {
		if v := os.Getenv("LOOT_DESPAWN_MS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				return time.Duration(n) * time.Millisecond
			}
		}
		return 30 * time.Second
	}()
	lootBlinkDelay = func() time.Duration {
		if v := os.Getenv("LOOT_BLINK_MS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				return time.Duration(n) * time.Millisecond
			}
		}
		if d := lootDespawnDelay - 10*time.Second; d > 0 {
			return d
		}
		return lootDespawnDelay / 2
	}()
)

type m5Loot struct {
	Instance string
	Bag      bool
	Items    []m5Drop
	X, Y     int
	Owner    string
	blinkT   *time.Timer
	destroyT *time.Timer
}

var (
	lootMu  sync.Mutex
	loots   = map[string]*m5Loot{}
	lootSeq int
)

func m5NearWalkable(x, y int) (int, int) {
	if !blocked(x, y) {
		return x, y
	}
	for r := 1; r <= 3; r++ {
		for dy := -r; dy <= r; dy++ {
			for dx := -r; dx <= r; dx++ {
				if nx, ny := x+dx, y+dy; !blocked(nx, ny) {
					return nx, ny
				}
			}
		}
	}
	return x, y
}

// m5SpawnLoot drops the roll at the corpse: single -> Item, multi -> LootBag
// (take-all on Target/Step; lootbag menu Open flow deferred, logged).
func m5SpawnLoot(mobKey string, cx, cy int, owner string) {
	drops := m5GetDropsFor(mobKey, owner)
	drops = worldDoubleDrops(drops) // world: double-drops event duplicates the roll
	lx, ly := m5NearWalkable(cx, cy)
	lootMu.Lock()
	lootSeq++
	inst := fmt.Sprintf("loot-%d", lootSeq)
	bag := len(drops) > 1
	l := &m5Loot{Instance: inst, Bag: bag, Items: drops, X: lx, Y: ly, Owner: owner}
	loots[inst] = l
	lootMu.Unlock()
	worldcore.SetEntityPos(inst, lx, ly)
	var payload EntityData
	if bag {
		payload = EntityData{Instance: inst, Type: EntityLootBag, Key: "lootbag", Name: "Loot Bag", X: lx, Y: ly}
	} else {
		payload = EntityData{Instance: inst, Type: EntityItem, Key: drops[0].Key, Name: drops[0].Key, X: lx, Y: ly, Count: intp(drops[0].Count)}
	}
	worldcore.Broadcast(pkt(PacketSpawn, payload))
	// M11: multi-drop bags log their contents (Node logs the roll set; the
	// bag itself only carries the keys server-side, so the log is the only
	// observable record — e2e asserts on this line).
	keys := make([]string, 0, len(drops))
	for _, d := range drops {
		keys = append(keys, d.Key)
	}
	log.Printf("m5: loot %s spawned (%s x%d) at %d,%d owner=%s bag=%v items=%v",
		inst, drops[0].Key, drops[0].Count, lx, ly, owner, bag, keys)
	l.blinkT = time.AfterFunc(lootBlinkDelay, func() { m5BlinkLoot(inst) })
	l.destroyT = time.AfterFunc(lootDespawnDelay, func() { m5DestroyLoot(inst, "expired") })
}

func m5BlinkLoot(inst string) {
	lootMu.Lock()
	l, ok := loots[inst]
	lootMu.Unlock()
	if !ok {
		return
	}
	l.Owner = ""
	worldcore.Broadcast(pkt(PacketBlink, inst))
	log.Printf("m5: loot %s blinking (free for all, destroy in %v)", inst, lootDespawnDelay-lootBlinkDelay)
}

func m5DestroyLoot(inst, why string) {
	lootMu.Lock()
	l, ok := loots[inst]
	if ok {
		delete(loots, inst)
		if l.blinkT != nil {
			l.blinkT.Stop()
		}
		if l.destroyT != nil {
			l.destroyT.Stop()
		}
	}
	lootMu.Unlock()
	if !ok {
		return
	}
	worldcore.RemoveEntity(inst)
	worldcore.Broadcast(pkt(PacketDespawn, despawnData{Instance: inst}))
	log.Printf("m5: loot %s destroyed (%s)", inst, why)
}

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
	Inv    []m5Slot
	Bank   []m5Slot
	Equip  []m5Slot // length ModulesEquipmentCount; Count 0 = empty slot
	Skills map[int]*m5Skill
}

var (
	pstateMu sync.Mutex
	pstates  = map[string]*m5State{}
)

// Skill ids mirror Modules.Skills order.
const (
	SkillLumberjacking = 0
	SkillAccuracy      = 1
	SkillArchery       = 2
	SkillHealth        = 3
	SkillMagic         = 4
	SkillMining        = 5
	SkillStrength      = 6
	SkillDefense       = 7
	SkillFishing       = 8
	SkillForaging      = 15
)

func m5SkillName(id int) string {
	switch id {
	case SkillLumberjacking:
		return "Lumberjacking"
	case SkillAccuracy:
		return "Accuracy"
	case SkillArchery:
		return "Archery"
	case SkillHealth:
		return "Health"
	case SkillMagic:
		return "Magic"
	case SkillMining:
		return "Mining"
	case SkillStrength:
		return "Strength"
	case SkillDefense:
		return "Defense"
	case SkillFishing:
		return "Fishing"
	case SkillForaging:
		return "Foraging"
	}
	return fmt.Sprintf("Skill%d", id)
}

func m5CombatSkill(id int) bool {
	switch id {
	case SkillAccuracy, SkillArchery, SkillHealth, SkillMagic, SkillStrength, SkillDefense:
		return true
	}
	return false
}

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
	level := 1
	for id, s := range st.Skills {
		if m5CombatSkill(id) && s.Level > 1 {
			level += s.Level - 1
		}
	}
	if level < 1 {
		level = 1
	}
	return level
}

// connByInstance moved to internal/world (D2a): use
// worldcore.Find[*playerConn](inst) at the former call sites.

// m5AddXP awards skill XP, emitting Experience Skill + Skill Update, and on
// level-up a Sync broadcast + Healing FX heal anim. Returns new level.
func m5AddXP(c *playerConn, key string, skill, amount int) int {
	if amount < 1 {
		return 1
	}
	st := m5StateFor(key)
	pstateMu.Lock()
	s, ok := st.Skills[skill]
	if !ok {
		s = &m5Skill{Level: 1}
		st.Skills[skill] = s
	}
	prev := s.Level
	s.XP += amount
	s.Level = expToLevel(s.XP)
	if s.Level < 1 {
		s.Level = 1
	}
	level, xp := s.Level, s.XP
	if m5CombatSkill(skill) {
		st.Level = m5CombatLevelLocked(st)
	}
	combatLevel := st.Level
	pstateMu.Unlock()

	if c != nil {
		_ = gnet.Send(c.Conn, pktOp(PacketExperience, ExperienceSkill, experienceData{
			Instance: c.Instance, Amount: intp(amount), Skill: intp(skill),
		}))
		_ = gnet.Send(c.Conn, pktOp(PacketSkill, SkillUpdate, skillData{
			Type: skill, Experience: xp, Level: intp(level),
			Percentage: floatp(m5Percentage(xp)), NextExperience: intp(nextExp(xp)),
			Combat: boolp(m5CombatSkill(skill)),
		}))
	}
	if level != prev {
		log.Printf("m5: %s %s leveled %d -> %d (xp=%d)", key, m5SkillName(skill), prev, level, xp)
		if c != nil {
			ph := welcomePlayer(c.Instance)
			ph.X, ph.Y = st.X, st.Y
			ph.Level = intp(combatLevel)
			worldcore.Broadcast(pkt(PacketSync, ph))
			worldcore.Broadcast(pktOp(PacketEffect, EffectAdd, effectData{Instance: c.Instance, Effect: EffectHealing}))
			log.Printf("m5: %s level-up heal anim (Healing FX only, no Heal packet)", key)
		}
		markDirty(key)
	}
	return level
}

func m5Percentage(xp int) float64 {
	nx := nextExp(xp)
	if nx < 0 {
		return 1
	}
	px := 0
	initLevelExp()
	for i := ModulesMaxLevel - 1; i > 0; i-- {
		if i < len(levelExpTbl) && xp >= levelExpTbl[i] {
			px = levelExpTbl[i]
			break
		}
	}
	if nx <= px {
		return 1
	}
	p := float64(xp-px) / float64(nx-px)
	if p < 0 {
		return 0
	}
	return p
}

// m5AwardCombatXP ports player.handleExperience (slash default: Strength +
// Health; archers/mages route by class flag).
func m5AwardCombatXP(c *playerConn, key string, damage int, archer, mage bool) {
	if damage < 1 {
		return
	}
	xp := damage * 2 // Modules.Constants.EXPERIENCE_PER_HIT.
	if worldXPBoost() {
		xp = xp * 3 / 2 // world: 1.5x experience event (experiencePerHit parity)
	}
	m5AddXP(c, key, SkillHealth, (xp+3)/4)
	switch {
	case archer:
		m5AddXP(c, key, SkillArchery, (xp*3+3)/4)
	case mage:
		m5AddXP(c, key, SkillMagic, (xp*3+3)/4)
	default:
		m5AddXP(c, key, SkillStrength, (xp*3+3)/4)
	}
}

// m5AwardGatherXP is the M4-hook successor: table experience on exhaust.
func m5GatherXP(attackerInstance, skill string, xp int) {
	c, _ := worldcore.Find[*playerConn](attackerInstance)
	if c == nil {
		return
	}
	id := map[string]int{
		"lumberjacking": SkillLumberjacking, "mining": SkillMining,
		"fishing": SkillFishing, "foraging": SkillForaging,
	}[skill]
	m5AddXP(c, c.Username, id, xp)
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
	if ench == nil && m5Stackable[itemKey] {
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
	lootMu.Lock()
	l, ok := loots[inst]
	lootMu.Unlock()
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
		idx := m5AddItem(c.Username, it.Key, it.Count)
		_ = gnet.Send(c.Conn, pktOp(PacketContainer, ContainerAdd, containerData{
			Type: ContainerTypeInventory,
			Slot: &slotData{Index: idx, Key: it.Key, Count: it.Count, Enchantments: map[string]any{}},
		}))
	}
	markDirty(c.Username)
	m5DestroyLoot(inst, "picked up by "+c.Instance)
	return true
}

// m5PickupAt steps onto loot: any loot on the player's tile is taken.
func m5PickupAt(c *playerConn) {
	m5PickupAtTile(c, c.Sess.PlayerX, c.Sess.PlayerY)
}

// m5PickupAtTile takes loot lying on (x,y) (Step destination path).
func m5PickupAtTile(c *playerConn, x, y int) {
	lootMu.Lock()
	var inst string
	for id, l := range loots {
		if l.X == x && l.Y == y {
			inst = id
			break
		}
	}
	lootMu.Unlock()
	if inst != "" {
		m5Pickup(c, inst)
	}
}

// m5TrackPos records the authoritative tile and marks the row dirty.
func m5TrackPos(c *playerConn) {
	if c.Username == "" {
		return
	}
	st := m5StateFor(c.Username)
	pstateMu.Lock()
	st.X, st.Y = c.Sess.PlayerX, c.Sess.PlayerY
	pstateMu.Unlock()
	markDirty(c.Username)
}

// m5RegisterLoot adds a pre-built loot entry to the registry without any
// timers (M10 chest drops: persistent items with no blink/expiry — Node
// chest items never expire on their own). Pickup routing is shared.
func m5RegisterLoot(inst, key string, count, x, y int, owner string) {
	lootMu.Lock()
	loots[inst] = &m5Loot{Instance: inst, Bag: false, Items: []m5Drop{{Key: key, Count: count}}, X: x, Y: y, Owner: owner}
	lootMu.Unlock()
	worldcore.SetEntityPos(inst, x, y)
}

// m5IsLoot reports whether id is a live loot entity.
func m5IsLoot(id string) bool {
	lootMu.Lock()
	defer lootMu.Unlock()
	_, ok := loots[id]
	return ok
}

// m5LootPayload rebuilds the Spawn payload for a loot instance (Who path).
func m5LootPayload(inst string) (any, bool) {
	lootMu.Lock()
	l, ok := loots[inst]
	lootMu.Unlock()
	if !ok {
		return nil, false
	}
	x, y, found := worldcore.EntityPos(inst)
	if !found {
		x, y = l.X, l.Y
	}
	if l.Bag {
		return EntityData{Instance: inst, Type: EntityLootBag, Key: "lootbag", Name: "Loot Bag", X: x, Y: y}, true
	}
	return EntityData{Instance: inst, Type: EntityItem, Key: l.Items[0].Key, Name: l.Items[0].Key, X: x, Y: y, Count: intp(l.Items[0].Count)}, true
}

// ---------------------------------------------------------------------------
// Player combat (C Combat {instance,target} vs killable mobs).
// ---------------------------------------------------------------------------

var (
	mobHPMu sync.Mutex
	mobHP   = map[string]int{} // non-combat-mode mob instances (m1).
)

func m5MobMaxHP(instance, mobKey string) int {
	loadM5Tables()
	if instance == "m1" {
		return 30 // spawnFrames flat default.
	}
	if p := m5Mobs[mobKey]; p != nil && p.HitPoints > 0 {
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
	dmg := 8 + rand.Intn(5)
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
		abSetTarget(c.Instance, target)
		worldcore.Broadcast(pkt(PacketAnimation, animationData{Instance: c.Instance, Action: ActionAttack}))
		worldcore.Broadcast(pktOp(PacketCombat, CombatHit, combatData{
			Instance: c.Instance, Target: target,
			Hit: HitData{Type: HitsNormal, Damage: dmg},
		}))
		m9PlayerHit(m, c, dmg)
		// TS combat.ts poison-on-hit: a poisonous weapon poisons the victim.
		if abHeroWeaponPoisonous(c.Username) {
			abApplyPoison(target)
		}
		m5AwardCombatXP(c, c.Username, dmg, false, false)
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
		abSetTarget(c.Instance, target)
		applyBossHitLocked(c.Instance, dmg, HitsNormal, nil, false, -1, true)
		died := combatDead
		combatMu.Unlock()
		m5AwardCombatXP(c, c.Username, dmg, false, false)
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
	initLevelExp()
	loadM5Tables()
	st, err := persist.Open(dbPath())
	if err != nil {
		log.Fatalf("m5: %v", err)
	}
	persistStore = st
	dbConn = st.DB()
	log.Printf("m5: sqlite open %s (WAL+NORMAL)", dbPath())
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for range t.C {
			flushDirty()
		}
	}()
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
		<-ch
		log.Printf("m5: shutdown signal -> final flush")
		flushDirty()
		os.Exit(0)
	}()
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
	cp := &m5State{X: st.X, Y: st.Y, Level: st.Level, HP: st.HP, Skills: map[int]*m5Skill{}}
	cp.Inv = append(cp.Inv, st.Inv...)
	cp.Bank = append(cp.Bank, st.Bank...)
	cp.Equip = append(cp.Equip, st.Equip...)
	for id, s := range st.Skills {
		cp.Skills[id] = &m5Skill{Level: s.Level, XP: s.XP}
	}
	return cp
}

// m5ToPersist converts an in-memory player state to the persist snapshot
// (inventory enchantments serialized to the DB column format; bank rows
// carry no enchantments, matching the bank table).
func m5ToPersist(st *m5State) persist.State {
	ps := persist.State{
		X: st.X, Y: st.Y, Level: st.Level, HP: st.HP,
		Skills: make(map[int]persist.Skill, len(st.Skills)),
	}
	for _, s := range st.Inv {
		ps.Inv = append(ps.Inv, persist.Slot{Key: s.Key, Count: s.Count, Ench: m5SlotEnchJSON(s)})
	}
	for _, s := range st.Bank {
		ps.Bank = append(ps.Bank, persist.Slot{Key: s.Key, Count: s.Count})
	}
	for _, e := range st.Equip {
		ps.Equip = append(ps.Equip, persist.Slot{Key: e.Key, Count: e.Count})
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
	st := &m5State{X: ps.X, Y: ps.Y, Level: ps.Level, HP: ps.HP, Skills: map[int]*m5Skill{}}
	for _, s := range ps.Inv {
		st.Inv = append(st.Inv, m5Slot{Key: s.Key, Count: s.Count, Ench: m5SlotEnchParse(s.Ench)})
	}
	for _, s := range ps.Bank {
		st.Bank = append(st.Bank, m5Slot{Key: s.Key, Count: s.Count})
	}
	eslots := make([]m5Slot, 0, len(ps.Equip))
	for _, s := range ps.Equip {
		eslots = append(eslots, m5Slot{Key: s.Key, Count: s.Count})
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
	_ = persistStore.WritePlayer(key, m5ToPersist(st))
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
		eqs = append(eqs, m6EquipmentData(t, e.Key, e.Count, true))
	}
	if len(eqs) > 0 {
		extra = append(extra, pktOp(PacketEquipment, EquipmentBatch, equipBatchData{Equipments: eqs}))
	}
	return ph, extra
}
