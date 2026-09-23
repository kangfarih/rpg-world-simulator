// M13 slice — the full commands.ts port (finishes the M7 command half).
//
// Thin adapter over internal/controller (behavior-frozen move, task E8):
// all guild/player/mod/admin command tables, the mute/ban/jail/noclip
// flags model, effect toggle, mob admin helpers, quest/achievement admin
// and the TESTMAP dispatcher body live in the controller package operating
// on the CommandConn/CommandBus/CommandPeers/Flags/GuildAdmin/AdminWorld/
// MobAdmin/QuestAdmin/InventoryAdmin/LootAdmin seams below. This file only
// wires those seams to the root globals (players map, entities, pstates,
// dbConn, m5/m6/m7/m9/m10/m11 subsystems) and keeps the entry points
// main.go/m7.go/world_wire.go call — with UNCHANGED signatures — delegating
// to the controller. Command behavior, notify strings and the flags schema
// are identical.
//
// Stayed (shared state the controller must not own): the m13flags SQLite
// table DDL exec + load/save over dbConn/dbMu, and the loot-spawn helpers
// (m5SpawnLootAt/m5SpawnLootBag) over the shared loot registry.
package server

import (
	"encoding/json"
	"log"
	"net"
	"sync"
	"time"

	"rpg-world-server/internal/controller"
	"rpg-world-server/internal/entity"
	"rpg-world-server/internal/meta"
	gnet "rpg-world-server/internal/net"
	"rpg-world-server/internal/social"
	worldcore "rpg-world-server/internal/world"
)

// TS parity constants (aliases to the moved commands.ts values).
const (
	EffectTerrorStatus = controller.CmdEffectTerror
	EffectStun         = controller.CmdEffectStun
	EffectFreezingM13  = controller.CmdEffectFreezing
	EffectInvincibleM  = controller.CmdEffectInvincible
	EffectBurningM13   = controller.CmdEffectBurning
)

// Modules.Ranks Landlord value (modules.ts:327).
const (
	RankLandlordM13 = controller.CmdRankLandlord
)

// Mod caps from commands.ts (hours).
const (
	m13ModMuteCap = controller.ModMuteCapHours
	m13ModBanCap  = controller.ModBanCapHours
	m13ModJailCap = controller.ModJailCapHours
)

// m13TPSpots mirrors the /tp [key] switch (authoritative table lives in the
// controller; shared map, never mutated).
var m13TPSpots = controller.TPSpots

// m13Flags is the persisted mod/admin state (authoritative type lives in
// the controller).
type m13Flags = controller.CommandFlags

// ---------------------------------------------------------------------------
// controller.CommandConn seam (*playerConn satisfies it, so identity is
// preserved). InstanceID/PlayerName/TileX/TileY/GrantContainerAccess come
// from the m12 seam; rank + movement speed extend it here.
// ---------------------------------------------------------------------------

// Rank is the Modules.Ranks value (chatStateFor rank parity).
func (c *playerConn) Rank() int { return chatStateFor(c).rank }

// MovementSpeed is the session ms-per-tile override.
func (c *playerConn) MovementSpeed() int { return c.Sess.MovementSpeed }

// SetMovementSpeed applies the /ms override live (checkSpeed reads it).
func (c *playerConn) SetMovementSpeed(v int) { c.Sess.MovementSpeed = v }

// m13conn unwraps a controller.CommandConn back to the root conn (subsystem
// calls need it); falls back to an instance lookup for foreign impls.
func m13conn(c controller.CommandConn) *playerConn {
	if pc, ok := c.(*playerConn); ok {
		return pc
	}
	if c == nil {
		return nil
	}
	pc, _ := worldcore.Find[*playerConn](c.InstanceID())
	return pc
}

// ---------------------------------------------------------------------------
// controller.Flags seam (m13flags table over dbConn/dbMu; bodies verbatim).
// ---------------------------------------------------------------------------

type m13flagsStore struct{}

func (m13flagsStore) Load(username string) controller.CommandFlags { return m13FlagsFor(username) }
func (m13flagsStore) Save(username string, f controller.CommandFlags) {
	m13SaveFlags(username, f)
}

// m13EnsureTables creates the flags table up-front (called from main()).
func m13EnsureTables() {
	if dbConn == nil {
		return
	}
	dbMu.Lock()
	defer dbMu.Unlock()
	if _, err := dbConn.Exec(controller.FlagsDDL); err != nil {
		log.Fatalf("m13: ddl: %v", err)
	}
}

// m13FlagsFor loads the persisted flags for a username. Missing row = zero
// flags (mute/ban/jail 0, noclip false, mspeed 0 = server default).
func m13FlagsFor(username string) m13Flags {
	f := m13Flags{}
	if dbConn == nil || username == "" {
		return f
	}
	dbMu.Lock()
	defer dbMu.Unlock()
	var noclip int
	if err := dbConn.QueryRow(
		`SELECT mute,ban,jail,noclip,mspeed FROM m13flags WHERE player=?`, username,
	).Scan(&f.Mute, &f.Ban, &f.Jail, &noclip, &f.MSpeed); err != nil {
		return m13Flags{}
	}
	f.Noclip = noclip != 0
	return f
}

// m13SaveFlags upserts the flags row.
func m13SaveFlags(username string, f m13Flags) {
	if dbConn == nil || username == "" {
		return
	}
	dbMu.Lock()
	defer dbMu.Unlock()
	noclip := 0
	if f.Noclip {
		noclip = 1
	}
	if _, err := dbConn.Exec(
		`INSERT INTO m13flags(player,mute,ban,jail,noclip,mspeed) VALUES(?,?,?,?,?,?) `+
			`ON CONFLICT(player) DO UPDATE SET mute=?,ban=?,jail=?,noclip=?,mspeed=?`,
		username, f.Mute, f.Ban, f.Jail, noclip, f.MSpeed,
		f.Mute, f.Ban, f.Jail, noclip, f.MSpeed); err != nil {
		log.Printf("m13: save flags %s: %v", username, err)
	}
}

// m13IsMuted reports whether the user's mute deadline is in the future.
func m13IsMuted(username string) bool {
	return m13FlagsFor(username).Muted(time.Now().UnixMilli())
}

// m13IsBanned reports whether the user's ban deadline is in the future.
func m13IsBanned(username string) bool {
	return m13FlagsFor(username).Banned(time.Now().UnixMilli())
}

// m13IsJailed reports whether the user's jail deadline is in the future.
func m13IsJailed(username string) bool {
	return m13FlagsFor(username).Jailed(time.Now().UnixMilli())
}

// m13NoclipAllowed reports whether the user may pass blocked tiles
// (player.noclip). Called from the movement anti-cheat path.
func m13NoclipAllowed(username string) bool {
	return m13FlagsFor(username).Noclip
}

// m13MovementSpeed returns the user's overrideMovementSpeed (0 = default).
func m13MovementSpeed(username string) int {
	return m13FlagsFor(username).MSpeed
}

// m13CheckBan is the login ban gate: returns true when the conn must be
// rejected (user.ban future deadline). Node sends 'ban' as a UTF8 text
// frame then closes.
func m13CheckBan(username string) bool {
	return m13IsBanned(username)
}

// ---------------------------------------------------------------------------
// controller.GuildAdmin seam (social_wire delegation).
// ---------------------------------------------------------------------------

type m13guilds struct{}

func (m13guilds) InGuild(username string) bool {
	_, err := social.GuildOf(username)
	return err == nil
}
func (m13guilds) Invite(c controller.CommandConn, target string) {
	socGuildInvite(m13conn(c), target)
}
func (m13guilds) Kick(c controller.CommandConn, username string, viaCommand bool) {
	socGuildKick(m13conn(c), username, viaCommand)
}
func (m13guilds) RankCommand(c controller.CommandConn, rankStr, username string) {
	socGuildRankCommand(m13conn(c), rankStr, username)
}

// ---------------------------------------------------------------------------
// controller.AdminWorld seam (teleports, combat, region/collision, spawns).
// ---------------------------------------------------------------------------

type m13world struct{}

func (m13world) Teleport(c controller.CommandConn, x, y int) { m7Teleport(m13conn(c), x, y) }
func (m13world) DamagePlayer(c controller.CommandConn, dmg int) {
	m9DamagePlayer(m13conn(c), dmg, nil)
}
func (m13world) PlayerHP(c controller.CommandConn) int { return m9PlayerHP(m13conn(c)) }
func (m13world) SetPVP(c controller.CommandConn) {
	if p := m13conn(c); p != nil {
		m10UpdatePVP(p, true)
	}
}
func (m13world) Countdown(c controller.CommandConn, t int) {
	worldcore.Broadcast(pkt(PacketCountdown, map[string]any{"instance": c.InstanceID(), "time": t}))
}
func (m13world) SameRegion(ax, ay, bx, by int) bool {
	return worldcore.TileRegion(ax, ay) == worldcore.TileRegion(bx, by)
}
func (m13world) RegionOf(x, y int) int                  { return worldcore.TileRegion(x, y) }
func (m13world) TileBlocked(x, y int) bool              { return tileBlocked(x, y) }
func (m13world) WorldWidth() int                        { loadWorld(); return world.Width }
func (m13world) SetEntityPos(instance string, x, y int) { worldcore.SetEntityPos(instance, x, y) }
func (m13world) SpawnFrame(instance string) []any {
	return pkt(PacketSpawn, welcomePlayer(instance))
}
func (m13world) LiveNPCPos(npcKey string) (int, int, bool) {
	for _, e := range worldcore.EntitySnapshot() {
		if key := m6ResolveNPCKey(nil, e.Instance); key == npcKey {
			return e.X, e.Y, true
		}
	}
	return 0, 0, false
}

// ---------------------------------------------------------------------------
// controller.MobAdmin seam (m9 registry + combat targeting).
// ---------------------------------------------------------------------------

type m13mobs struct{}

func mobOf(h controller.MobHandle) *m9Mob {
	m, _ := h.(*m9Mob)
	return m
}

func (m13mobs) MobFor(instance string) (controller.MobHandle, bool) {
	m := m9MobFor(instance)
	if m == nil {
		return nil, false
	}
	return m, true
}
func (m13mobs) MobInstance(h controller.MobHandle) string { return mobOf(h).instance }
func (m13mobs) MobKey(h controller.MobHandle) string      { return mobOf(h).key }
func (m13mobs) MobHP(h controller.MobHandle) int {
	m := mobOf(h)
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hp
}
func (m13mobs) MobPos(h controller.MobHandle) (int, int) {
	m := mobOf(h)
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.x, m.y
}
func (m13mobs) SetMobPos(h controller.MobHandle, x, y int) {
	m := mobOf(h)
	m.mu.Lock()
	m.x, m.y = x, y
	m.spawnX, m.spawnY = x, y
	m.mu.Unlock()
}
func (m13mobs) Attack(a, b controller.MobHandle) {
	ma, mb := mobOf(a), mobOf(b)
	ma.mu.Lock()
	ma.target = mb.instance
	ma.lastTgt = time.Now()
	ma.mu.Unlock()
}
func (m13mobs) AttackTarget(h controller.MobHandle, target string) {
	m := mobOf(h)
	m.mu.Lock()
	m.target = target
	m.lastTgt = time.Now()
	m.mu.Unlock()
}
func (m13mobs) ClearTarget(h controller.MobHandle) {
	m := mobOf(h)
	m.mu.Lock()
	m.target = ""
	m.mu.Unlock()
}
func (m13mobs) HitMob(h controller.MobHandle, dmg int) { m9PlayerHit(mobOf(h), nil, dmg) }
func (m13mobs) Instances() []string {
	m9Mu.Lock()
	defer m9Mu.Unlock()
	out := make([]string, 0, len(m9Mobs))
	for inst := range m9Mobs {
		out = append(out, inst)
	}
	return out
}
func (m13mobs) SpawnMob(instance, key string, x, y int) bool {
	return m9SpawnMob(instance, key, x, y, m9Overrides{Chase: true})
}

// ---------------------------------------------------------------------------
// controller.QuestAdmin seam (m11 state bridging).
// ---------------------------------------------------------------------------

type m13quests struct{}

func (m13quests) MarkDirty(username string) { markDirty(username) }
func (m13quests) QuestDef(key string) (int, bool) {
	def := m11Q[key]
	if def == nil {
		return 0, false
	}
	return def.StageCount, true
}
func (m13quests) QuestStage(username, key string) (int, int) {
	q := m11StateFor(username).quest(key)
	return q.Stage, q.SubStage
}
func (m13quests) SetQuestStage(c controller.CommandConn, username, key string, stage, subStage int) {
	m11SetStage(m13conn(c), m11StateFor(username), key, stage, subStage, true)
}
func (m13quests) QuestKeys() []string {
	out := make([]string, 0, len(m11Q))
	for k := range m11Q {
		out = append(out, k)
	}
	return out
}
func (m13quests) AchDef(key string) (int, bool) {
	def := m11A[key]
	if def == nil {
		return 0, false
	}
	return def.StageCount, true
}
func (m13quests) AchStage(username, key string) int { return m11StateFor(username).Achs[key] }
func (m13quests) SetAchStage(username, key string, stage int) {
	m11StateFor(username).Achs[key] = stage
}
func (m13quests) AchProgress(c controller.CommandConn, username, key string) {
	m11AchProgress(m13conn(c), m11StateFor(username), key)
}
func (m13quests) AchKeys(username string) []string {
	st := m11StateFor(username)
	out := make([]string, 0, len(st.Achs))
	for k := range st.Achs {
		out = append(out, k)
	}
	return out
}
func (m13quests) AchDefs() map[string]int {
	out := make(map[string]int, len(m11A))
	for k, def := range m11A {
		out[k] = def.StageCount
	}
	return out
}
func (m13quests) SendAchProgress(c controller.CommandConn, key string, stage int) {
	m11SendAchievementProgress(m13conn(c), key, stage)
}
func (m13quests) SendPopup(c controller.CommandConn, title, message, colour string) {
	m11SendPopup(m13conn(c), title, message, colour)
}

// ---------------------------------------------------------------------------
// controller.InventoryAdmin seam (m5 container state).
// ---------------------------------------------------------------------------

type m13inv struct{}

func (m13inv) MarkDirty(username string)                   { markDirty(username) }
func (m13inv) ItemExists(key string) bool                  { return m6ItemInfoFor(key) != nil }
func (m13inv) AddItem(username, key string, count int) int { return m5AddItem(username, key, count) }
func (m13inv) SlotAt(username, container string, index int) (controller.CommandSlot, bool) {
	st := m5StateFor(username)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	var slots []m5Slot
	if container == "inventory" {
		slots = st.Inv
	} else {
		slots = st.Bank
	}
	if index < 0 || index >= len(slots) {
		return controller.CommandSlot{}, false
	}
	return controller.CommandSlot{Key: slots[index].Key, Count: slots[index].Count}, true
}
func (m13inv) RemoveAt(username, container string, index, count int) (string, int, bool) {
	st := m5StateFor(username)
	pstateMu.Lock()
	var slots *[]m5Slot
	if container == "inventory" {
		slots = &st.Inv
	} else {
		slots = &st.Bank
	}
	if index < 0 || index >= len(*slots) {
		pstateMu.Unlock()
		return "", 0, false
	}
	s := (*slots)[index]
	s.Count -= count
	key, left := s.Key, s.Count
	if s.Count > 0 {
		(*slots)[index] = s
	} else {
		*slots = append((*slots)[:index], (*slots)[index+1:]...)
		key, left = "", 0
	}
	pstateMu.Unlock()
	markDirty(username)
	return key, left, true
}
func (m13inv) RemoveKey(username, container, key string, count int) int {
	remaining := count
	st := m5StateFor(username)
	pstateMu.Lock()
	var slots *[]m5Slot
	if container == "inventory" {
		slots = &st.Inv
	} else {
		slots = &st.Bank
	}
	for i := len(*slots) - 1; i >= 0 && remaining > 0; i-- {
		if (*slots)[i].Key != key {
			continue
		}
		take := (*slots)[i].Count
		if take > remaining {
			take = remaining
		}
		(*slots)[i].Count -= take
		remaining -= take
		if (*slots)[i].Count <= 0 {
			*slots = append((*slots)[:i], (*slots)[i+1:]...)
		}
	}
	pstateMu.Unlock()
	if remaining < count {
		markDirty(username)
	}
	return count - remaining
}
func (m13inv) EmptyContainer(username, container string) {
	st := m5StateFor(username)
	pstateMu.Lock()
	if container == "inventory" {
		st.Inv = nil
	} else {
		st.Bank = nil
	}
	pstateMu.Unlock()
	markDirty(username)
}
func (m13inv) CopyContainer(src, dst string, bank bool) {
	stSrc := m5StateFor(src)
	stDst := m5StateFor(dst)
	pstateMu.Lock()
	if bank {
		clone := append([]m5Slot(nil), stSrc.Bank...)
		stDst.Bank = clone
	} else {
		clone := append([]m5Slot(nil), stSrc.Inv...)
		stDst.Inv = clone
	}
	pstateMu.Unlock()
	markDirty(dst)
}
func (m13inv) BankCount(username, itemKey string) int {
	st := m5StateFor(username)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	n := 0
	for _, s := range st.Bank {
		if s.Key == itemKey {
			n += s.Count
		}
	}
	return n
}
func (m13inv) InvCount(username, itemKey string) int { return m6InvCount(username, itemKey) }
func (m13inv) AppendBank(username, key string, count int) {
	st := m5StateFor(username)
	pstateMu.Lock()
	st.Bank = append(st.Bank, m5Slot{Key: key, Count: count})
	pstateMu.Unlock()
}
func (m13inv) BankSlots(username string) []controller.CommandSlot {
	st := m5StateFor(username)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	out := make([]controller.CommandSlot, 0, len(st.Bank))
	for _, s := range st.Bank {
		out = append(out, controller.CommandSlot{Key: s.Key, Count: s.Count})
	}
	return out
}

// ---------------------------------------------------------------------------
// controller.LootAdmin seam (shared loot registry; bodies verbatim).
// ---------------------------------------------------------------------------

type m13loot struct{}

func (m13loot) SpawnLootAt(owner, key string, count, x, y int) {
	m5SpawnLootAt(owner, key, count, x, y)
}
func (m13loot) SpawnLootBag(owner string, x, y int, items []controller.Drop) {
	drops := make([]m5Drop, len(items))
	for i, d := range items {
		drops[i] = m5Drop{Key: d.Key, Count: d.Count}
	}
	m5SpawnLootBag(owner, x, y, drops)
}

// m5SpawnLootAt wraps m5SpawnLoot with an explicit tile (the /drop path
// spawns without a killer gate — no drop-table roll, the exact key;
// canonical owner: internal/entity SpawnLootAt).
func m5SpawnLootAt(owner, key string, count, x, y int) {
	entity.SpawnLootAt(owner, key, count, x, y)
}

// m5SpawnLootBag creates a loot entity with the given exact items (used by
// /drop and /lootbag; TS spawns Item/LootBag entities directly; canonical
// owner: internal/entity SpawnLootBag).
func m5SpawnLootBag(owner string, cx, cy int, items []m5Drop) {
	entity.SpawnLootBag(owner, cx, cy, items)
}

// ---------------------------------------------------------------------------
// controller.CommandBus / CommandPeers seams (send/broadcast + registry).
// ---------------------------------------------------------------------------

type m13bus struct{}

func (m13bus) SendTo(instance string, frames ...[]any) {
	c, _ := worldcore.Find[*playerConn](instance)
	if c == nil {
		return
	}
	_ = gnet.Send(c.Conn, frames...)
}
func (m13bus) Notify(instance string, message string) {
	if c, _ := worldcore.Find[*playerConn](instance); c != nil {
		m6Notify(c, message)
	}
}
func (m13bus) Broadcast(frames ...[]any) { worldcore.Broadcast(frames...) }
func (m13bus) SendBan(instance string) {
	c, _ := worldcore.Find[*playerConn](instance)
	if c == nil {
		return
	}
	_ = gnet.WriteText(c.Conn.WS, []byte("ban"), 2*time.Second)
}
func (m13bus) Close(instance string) {
	c, _ := worldcore.Find[*playerConn](instance)
	if c == nil {
		return
	}
	worldcore.RemoveClient(c.Conn.WS)
}
func (m13bus) NotifySource(instance, message, colour, source string) {
	if c, _ := worldcore.Find[*playerConn](instance); c != nil {
		m6NotifyWithSource(c, message, colour, source)
	}
}

type m13peers struct{}

func (m13peers) ByInstance(instance string) (controller.CommandConn, bool) {
	c, _ := worldcore.Find[*playerConn](instance)
	if c == nil {
		return nil, false
	}
	return c, true
}
func (m13peers) ByUsername(username string) (controller.CommandConn, bool) {
	c := m7PlayerByName(username)
	if c == nil {
		return nil, false
	}
	return c, true
}
func (m13peers) Usernames() []string { return m7PlayerUsernames() }

// m13deps wires the controller seams to the root globals.
func m13deps() controller.CommandDeps {
	return controller.CommandDeps{
		Flags: m13flagsStore{}, Guilds: m13guilds{}, World: m13world{},
		Mobs: m13mobs{}, Quests: m13quests{}, Inv: m13inv{},
		Loot: m13loot{}, Bus: m13bus{}, Peers: m13peers{},
		Skills: m13skills{}, Abilities: m13abilities{}, Ranks: m13ranks{},
		Pets: m13pets{}, Poison: m13poisonStore{}, Misc: m13misc{},
	}
}

// ---------------------------------------------------------------------------
// Entry points (signatures UNCHANGED; m7.go/main.go/world_wire.go call sites
// compile as-is). All logic lives in the controller.
// ---------------------------------------------------------------------------

// m13ParseCommand runs the M13 command tables after the m7/m12 ones. The
// m7 player table handles players/coords/ping/g/pm; m13 adds guild.
func m13ParseCommand(c *playerConn, command string, blocks []string) {
	controller.ParseCommand(c, command, blocks, m13deps())
}

// m13GuildCommand ports the 'guild' case over the real guild registry.
func m13GuildCommand(c *playerConn, command string, blocks []string) {
	controller.GuildCommand(c, command, blocks, m13deps())
}

// m13ModeratorCommands ports handleModeratorCommands. Gate: rank >= Moderator.
func m13ModeratorCommands(c *playerConn, command string, blocks []string) {
	controller.ModeratorCommands(c, command, blocks, m13deps())
}

// m13AdminCommands ports handleAdminCommands. Gate: rank >= Admin.
func m13AdminCommands(c *playerConn, command string, blocks []string) {
	controller.AdminCommands(c, command, blocks, m13deps())
}

// m13ContainerSlot reads one slot by the TS 0-based array index.
func m13ContainerSlot(username, container string, index int) (m5Slot, bool) {
	s, ok := controller.ContainerSlot(m13deps(), username, container, index)
	if !ok {
		return m5Slot{}, false
	}
	return m5Slot{Key: s.Key, Count: s.Count}, true
}

// m13ContainerRemove removes count from the slot at the TS 0-based index
// and syncs the client if online.
func m13ContainerRemove(username, container string, index, count int) {
	controller.ContainerRemove(m13deps(), username, container, index, count)
}

// m13ContainerRemoveKey removes count of key from the container.
func m13ContainerRemoveKey(username, container, key string, count int) {
	controller.ContainerRemoveKey(m13deps(), username, container, key, count)
}

// m13BankCount sums the bank stacks of one key (bank echo helper).
func m13BankCount(username, itemKey string) int {
	return controller.BankCount(m13deps(), username, itemKey)
}

// m13EmptyContainer drops every slot (container.empty()).
func m13EmptyContainer(username, container string) {
	controller.EmptyContainer(m13deps(), username, container)
}

// m13CopyContainer clones src's container into dst (copybank/copyinventory).
func m13CopyContainer(src, dst string, bank bool) {
	controller.CopyContainer(m13deps(), src, dst, bank)
}

// m13SetMobPos ports mob.setPosition: registry + roam origin move.
func m13SetMobPos(m *m9Mob, x, y int) {
	controller.MoveMob(m13deps(), m, x, y)
}

// m13MobAttack makes mob a attack mob b (combat.attack over Character refs).
func m13MobAttack(a, b *m9Mob) {
	m13deps().Mobs.Attack(a, b)
}

// m13MobAttackTarget points a mob at an arbitrary instance (player or mob).
func m13MobAttackTarget(m *m9Mob, target string) {
	m13deps().Mobs.AttackTarget(m, target)
}

// m13MobRoam clears the target so the tick loop resumes roaming
// (entity.roamingCallback).
func m13MobRoam(m *m9Mob) {
	m13deps().Mobs.ClearTarget(m)
}

// m13FindNPC scans the npcs.json registry + the live entity tiles.
func m13FindNPC(c *playerConn, npcKey string) {
	controller.FindNPC(c, npcKey, m13deps())
}

// m13ToggleEffect flips a status effect with the TS notify strings.
func m13ToggleEffect(c *playerConn, effect int) {
	controller.ToggleEffect(c, effect, m13deps())
}

// m13ToggleCommand ports the /toggle switch.
func m13ToggleCommand(c *playerConn, blocks []string) {
	controller.ToggleCommand(c, blocks, m13deps())
}

// m13HasEffect reports whether the conn carries the effect.
func m13HasEffect(c *playerConn, effect int) bool {
	return controller.HasEffect(c, effect)
}

// m13AddEffect applies the effect + broadcasts Effect Add.
func m13AddEffect(c *playerConn, effect int) {
	controller.AddEffect(c, effect, m13deps())
}

// m13RemoveEffect clears the effect + broadcasts Effect Remove.
func m13RemoveEffect(c *playerConn, effect int) {
	controller.RemoveEffect(c, effect, m13deps())
}

// m13ClearEffects removes every effect on the conn.
func m13ClearEffects(c *playerConn) {
	controller.ClearEffects(c, m13deps())
}

// m13ForgetPlayer drops the per-conn effect state on disconnect.
func m13ForgetPlayer(instance string) {
	controller.ForgetCommandPlayer(instance)
}

// m13ToggleHide ports the 'hide' command: flip player.visible and
// Despawn/Spawn to the surrounding regions.
func m13ToggleHide(c *playerConn) {
	controller.ToggleHide(c, m13deps())
}

// m13IsHidden reports whether the instance is currently hidden.
func m13IsHidden(instance string) bool {
	return controller.IsHidden(instance)
}

// m13QuestStageShift shifts the quest stage by delta (undostage).
func m13QuestStageShift(c *playerConn, key string, delta int) {
	controller.QuestStageShift(c, key, delta, m13deps())
}

// m13QuestReset sets the quest to stage 0 (resetquest/resetquests).
func m13QuestReset(c *playerConn, key string) {
	controller.QuestReset(c, key, m13deps())
}

// m13QuestResetAll resets every quest.
func m13QuestResetAll(c *playerConn) {
	controller.QuestResetAll(c, m13deps())
}

// m13QuestFinish completes the quest (finishquest).
func m13QuestFinish(c *playerConn, key string) {
	controller.QuestFinish(c, key, m13deps())
}

// m13AchievementsReset zeroes every achievement stage.
func m13AchievementsReset(c *playerConn) {
	controller.AchievementsReset(c, m13deps())
}

// m13AchievementFinish completes one achievement.
func m13AchievementFinish(c *playerConn, key string) {
	controller.AchievementFinish(c, key, m13deps())
}

// m13AchievementsFinishAll completes every achievement.
func m13AchievementsFinishAll(c *playerConn) {
	controller.AchievementsFinishAll(c, m13deps())
}

// m13TestItems empties the bank and adds 100x of every items.json key.
func m13TestItems(c *playerConn) {
	controller.TestItems(c, m13deps())
}

// m13TestHandler ports the m9test/m11test dispatcher pattern: TESTMAP-only
// seeding + echo. The testMode gate stays here; the body lives in the
// controller.
func m13TestHandler(c *playerConn, data []byte) {
	if !testMode {
		return
	}
	controller.HandleCommandTest(c, data, m13deps())
}

// ---------------------------------------------------------------------------
// controller.SkillAdmin seam (m5 skill state + XP frame paths).
// ---------------------------------------------------------------------------

type m13skills struct{}

func (m13skills) MarkDirty(username string) { markDirty(username) }

func (m13skills) SkillOf(c controller.CommandConn, skill int) (int, int, bool) {
	pc := m13conn(c)
	if pc == nil {
		return 0, 1, false
	}
	st := m5StateFor(pc.Username)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	s, ok := st.Skills[skill]
	if !ok || s == nil {
		return 0, 1, false
	}
	return s.XP, s.Level, true
}

func (m13skills) AddSkillXP(c controller.CommandConn, skill, amount int) {
	pc := m13conn(c)
	if pc == nil {
		return
	}
	m5AddXP(pc, pc.Username, skill, amount)
}

// m13SkillFrames builds the handleExperience withInfo pair: Experience
// Skill + Skill Update with the full serialize (level/percentage/
// nextExperience/combat).
func m13SkillFrames(pc *playerConn, skill int, xp, level int) []gnet.Frame {
	return []gnet.Frame{
		pktOp(PacketExperience, ExperienceSkill, experienceData{
			Instance: pc.Instance, Amount: intp(0), Skill: intp(skill),
		}),
		pktOp(PacketSkill, SkillUpdate, skillData{
			Type: skill, Experience: xp, Level: intp(level),
			Percentage: floatp(m5Percentage(xp)), NextExperience: intp(nextExp(xp)),
			Combat: boolp(m5CombatSkill(skill)),
		}),
	}
}

// m13SkillBatch builds the Skill Batch over every earned skill (login
// m5LoginWelcome shape parity) plus the Experience Sync carrying the
// combat level (skills.sync parity).
func m13SkillBatch(pc *playerConn) []gnet.Frame {
	st := m5StateFor(pc.Username)
	pstateMu.Lock()
	skills := make([]any, 0, len(st.Skills))
	for id, s := range st.Skills {
		skills = append(skills, map[string]any{
			"type": id, "experience": s.XP, "level": s.Level,
			"percentage": m5Percentage(s.XP), "nextExperience": nextExp(s.XP),
			"combat": m5CombatSkill(id),
		})
	}
	combatLevel := st.Level
	pstateMu.Unlock()
	return []gnet.Frame{
		pktOp(PacketSkill, SkillBatch, map[string]any{"skills": skills, "cheater": false}),
		pktOp(PacketExperience, ExperienceSync, experienceData{Instance: pc.Instance, Level: intp(combatLevel)}),
	}
}

func (m13skills) SetSkillXP(c controller.CommandConn, skill, xp int) {
	pc := m13conn(c)
	if pc == nil {
		return
	}
	st := m5StateFor(pc.Username)
	pstateMu.Lock()
	s, ok := st.Skills[skill]
	if !ok || s == nil {
		s = &m5Skill{Level: 1}
		st.Skills[skill] = s
	}
	s.XP = xp
	s.Level = expToLevel(xp)
	if s.Level < 1 {
		s.Level = 1
	}
	if m5CombatSkill(skill) {
		st.Level = m5CombatLevelLocked(st)
	}
	level := s.Level
	pstateMu.Unlock()
	markDirty(pc.Username)
	_ = gnet.Send(pc.Conn, m13SkillFrames(pc, skill, xp, level)...)
}

func (m13skills) ResetSkills(c controller.CommandConn) {
	pc := m13conn(c)
	if pc == nil {
		return
	}
	st := m5StateFor(pc.Username)
	pstateMu.Lock()
	for _, id := range controller.ProgressionSkillIDs {
		s, ok := st.Skills[id]
		if !ok || s == nil {
			s = &m5Skill{Level: 1}
			st.Skills[id] = s
		}
		s.XP, s.Level = 0, 1 // setExperience(0) parity (silent part)
	}
	st.Level = m5CombatLevelLocked(st)
	pstateMu.Unlock()
	markDirty(pc.Username)
	// addExperience(0) per skill is silent at level 1 (no level-up), then
	// skills.sync() — the Batch + Experience Sync pair.
	_ = gnet.Send(pc.Conn, m13SkillBatch(pc)...)
}

func (m13skills) MaxSkills(c controller.CommandConn) {
	pc := m13conn(c)
	if pc == nil {
		return
	}
	st := m5StateFor(pc.Username)
	pstateMu.Lock()
	for _, id := range controller.ProgressionSkillIDs {
		s, ok := st.Skills[id]
		if !ok || s == nil {
			s = &m5Skill{Level: 1}
			st.Skills[id] = s
		}
		s.XP, s.Level = 0, 1 // setExperience(0) first, like TS
	}
	pstateMu.Unlock()
	for _, id := range controller.ProgressionSkillIDs {
		m5AddXP(pc, pc.Username, id, controller.MaxAwardXP)
	}
}

func (m13skills) SyncSkills(c controller.CommandConn) {
	if pc := m13conn(c); pc != nil {
		_ = gnet.Send(pc.Conn, m13SkillBatch(pc)...)
	}
}

func (m13skills) LevelsToExperience(fromLevel, toLevel int) int {
	tbl := meta.BuildLevelExp(meta.MaxLevel)
	if len(tbl) == 0 {
		return 0
	}
	if fromLevel < 0 {
		fromLevel = 0
	}
	if toLevel < 0 {
		toLevel = 0
	}
	if fromLevel >= len(tbl) {
		fromLevel = len(tbl) - 1
	}
	if toLevel >= len(tbl) {
		toLevel = len(tbl) - 1
	}
	return tbl[toLevel] - tbl[fromLevel] // Formulas.levelsToExperience
}

// ---------------------------------------------------------------------------
// controller.AbilityAdmin seam (abilities registry grant paths).
// ---------------------------------------------------------------------------

type m13abilities struct{}

func (m13abilities) MarkDirty(username string) { markDirty(username) }
func (m13abilities) HasAbility(username, key string) bool {
	return abHas(username, key)
}
func (m13abilities) GrantAbility(c controller.CommandConn, username, key string, level int) bool {
	return abGrantAbility(m13conn(c), username, key, level)
}
func (m13abilities) QuickSlotAbility(c controller.CommandConn, username, key string, slot int) {
	pc := m13conn(c)
	if pc == nil {
		return
	}
	// Same store the C->S Ability QuickSlot opcode writes (HandleAbility
	// QuickSlot branch, owned-ability gate included).
	raw, _ := json.Marshal(map[string]any{"opcode": AbilityQuickSlot, "key": key, "index": slot})
	abHandleAbility(pc, raw)
	markDirty(username)
}
func (m13abilities) ResetAbilities(c controller.CommandConn) {
	pc := m13conn(c)
	if pc == nil {
		return
	}
	// abilities.reset() parity: clear the server-side unlock map, then the
	// client reload signal (loadCallback -> Ability Batch, empty now).
	abResetAbilities(pc.Username)
	_ = gnet.Send(pc.Conn, abLoginBatch(pc.Username))
	markDirty(pc.Username)
	log.Printf("m13: %s reset abilities", pc.Username)
}

// ---------------------------------------------------------------------------
// controller.RankAdmin seam (player rank sets).
// ---------------------------------------------------------------------------

type m13ranks struct{}

func (m13ranks) MarkDirty(username string) { markDirty(username) }
func (m13ranks) SetRank(target controller.CommandConn, rank int) {
	pc := m13conn(target)
	if pc == nil {
		return
	}
	pc.rank = rank
	chatStateFor(pc).rank = rank
	_ = gnet.Send(pc.Conn, pkt(PacketRank, rank)) // RankPacket(rank)
	// player.sync() region fanout (SyncPacket serialize parity).
	st := m5StateFor(pc.Username)
	pstateMu.Lock()
	x, y, level := st.X, st.Y, st.Level
	st.Rank = rank // durable across relogin via the persist rank column
	pstateMu.Unlock()
	ph := welcomePlayer(pc.Instance)
	ph.X, ph.Y = x, y
	ph.Level = intp(level)
	worldcore.Broadcast(pkt(PacketSync, ph))
	markDirty(pc.Username)
}
func (m13ranks) SetRankOffline(username string, rank int) {
	// database.setRank parity: persist the rank for an offline player so the
	// login path restores it. Missing row = warn like TS (`No player found
	// with the username ...`), no stub insert.
	if dbConn == nil || username == "" {
		return
	}
	dbMu.Lock()
	defer dbMu.Unlock()
	res, err := dbConn.Exec(`UPDATE players SET rank=? WHERE instance=?`, rank, username)
	if err != nil {
		log.Printf("m13: setrank offline %s: %v", username, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		log.Printf("m13: No player found with the username %s.", username)
		return
	}
	log.Printf("m13: setrank offline %s rank=%d", username, rank)
}

// ---------------------------------------------------------------------------
// controller.PetAdmin seam (pet grant path).
// ---------------------------------------------------------------------------

type m13pets struct{}

func (m13pets) GrantPet(c controller.CommandConn, key string) {
	pc := m13conn(c)
	if pc == nil {
		return
	}
	mob, item := petResolveKey(key)
	petGrant(pc, mob, item) // duplicate-pet notify lives in the grant
}

// ---------------------------------------------------------------------------
// controller.PoisonAdmin seam (status Tracker pipeline + region scan).
// Poison Apply/Clear ride the status engine (abApplyPoison/abRemovePoison,
// the same calls the poisonous-weapon hook rides via
// gameWorldAdapter.ApplyPoison); the toggle bookkeeping below mirrors the
// controller-side expiry map.
// ---------------------------------------------------------------------------

type m13poisonStore struct{}

var (
	m13poisonMu  sync.Mutex
	m13poisoned  = map[string]int64{}
	m13poisonGen int64
)

func (m13poisonStore) PoisonHas(instance string) bool {
	m13poisonMu.Lock()
	defer m13poisonMu.Unlock()
	return m13poisoned[instance] != 0
}
func (m13poisonStore) PoisonApply(instance string) {
	if instance == "" {
		return
	}
	abApplyPoison(instance)
	m13poisonMu.Lock()
	m13poisonGen++
	gen := m13poisonGen
	m13poisoned[instance] = gen
	m13poisonMu.Unlock()
	// Natural Venom expiry clears the toggle state (30s default).
	time.AfterFunc(controller.PoisonExpiry, func() {
		m13poisonMu.Lock()
		defer m13poisonMu.Unlock()
		if m13poisoned[instance] == gen {
			delete(m13poisoned, instance)
		}
	})
}
func (m13poisonStore) PoisonClear(instance string) {
	// Early cure (character.ts setPoison() with no argument): drop the Venom
	// DoT in the status engine, not just the toggle bookkeeping below —
	// otherwise the 30s ticks keep hitting after the cure notify.
	abRemovePoison(instance)
	m13poisonMu.Lock()
	delete(m13poisoned, instance)
	m13poisonMu.Unlock()
}
func (m13poisonStore) EntityKind(instance string) string {
	if m9MobFor(instance) != nil {
		return "mob"
	}
	if c, _ := worldcore.Find[*playerConn](instance); c != nil {
		return "player"
	}
	if _, _, ok := worldcore.EntityPos(instance); ok {
		return "other"
	}
	return ""
}
func (m13poisonStore) RegionCharInstances(c controller.CommandConn) []string {
	adminRegion := worldcore.TileRegion(c.TileX(), c.TileY())
	var out []string
	for _, e := range worldcore.EntitySnapshot() {
		if worldcore.TileRegion(e.X, e.Y) != adminRegion {
			continue
		}
		if m9MobFor(e.Instance) != nil {
			out = append(out, e.Instance)
			continue
		}
		if p, _ := worldcore.Find[*playerConn](e.Instance); p != nil {
			out = append(out, e.Instance)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// controller.MiscAdmin seam (attack range, debug frame, region resend,
// IP bans shared with the stdin console form).
// ---------------------------------------------------------------------------

type m13misc struct{}

// m13AttackRange is the stub's canonical attack range (welcomePlayer
// AttackRange intp(1) everywhere; player.sync recomputes it from the
// weapon in TS, which the stub does not model).
func (m13misc) AttackRange(c controller.CommandConn) int { return 1 }

func (m13misc) SendDebug(c controller.CommandConn) {
	pc := m13conn(c)
	if pc == nil {
		return
	}
	// CommandPacket {command:'debug'}: [Packets.Command, data] (packet.ts
	// serialize with no opcode; Packets.Command = 20).
	_ = gnet.Send(pc.Conn, pkt(PacketCommand, map[string]any{"command": "debug"}))
}

func (m13misc) ResendRegions(c controller.CommandConn) {
	pc := m13conn(c)
	if pc == nil {
		return
	}
	// regionsLoaded = [] + updateRegion() parity: recompute interest,
	// then the login region-load burst scoped to the admin (List Spawns +
	// Positions, then one Spawn per surrounding-region entity).
	worldcore.UpdateRegion(pc, pc.Sess.PlayerX, pc.Sess.PlayerY)
	handleList(pc)
	regions := pc.Conn.Regions()
	regionSet := make(map[int]bool, len(regions))
	for _, r := range regions {
		regionSet[r] = true
	}
	n := 0
	for _, e := range worldcore.EntitySnapshot() {
		if !regionSet[worldcore.TileRegion(e.X, e.Y)] {
			continue
		}
		p, ok := spawnPayload(e.Instance)
		if !ok {
			continue
		}
		_ = gnet.Send(pc.Conn, pkt(PacketSpawn, p))
		n++
	}
	log.Printf("m13: resetregions resent %d spawns to %s", n, pc.Instance)
}

func (m13misc) PlayerIP(username string) (string, bool) {
	target := m7PlayerByName(username)
	if target == nil {
		return "", false
	}
	return m13ConnIP(target), true
}

// m13ConnIP resolves the remote host of a conn (ops_wire BanIP parity).
func m13ConnIP(target *playerConn) string {
	host, _, err := net.SplitHostPort(gnet.AddrID(target.Conn.WS))
	if err != nil {
		host = gnet.AddrID(target.Conn.WS)
	}
	return host
}

func (m13misc) BanIP(ip string) { m13BanIP(ip) }

// m13BanIP records the IP ban + drops matching conns — the same list and
// drop the stdin console /ipban drives (ops_wire BanIP closure parity;
// kept as one helper so both forms share the implementation).
func m13BanIP(ip string) {
	gnet.BanIP(ip)
	for _, k := range worldcore.AllWS() {
		host, _, err := net.SplitHostPort(gnet.AddrID(k))
		if err != nil {
			host = gnet.AddrID(k)
		}
		if host == ip {
			worldcore.RemoveClient(k)
		}
	}
}
