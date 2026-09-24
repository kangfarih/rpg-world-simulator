// ---------------------------------------------------------------------------
// M9 — generic mob AI engine (worlds setup slice 1): thin root adapter.
//
// The engine lives in internal/entity (mob.go); this file keeps the live
// state + the world wiring with UNCHANGED public signatures so main.go,
// m5.go, m11.go, m13.go, pets_wire.go, ops_wire.go and abilities_wire.go
// compile untouched.
//
// STAYED here (m13.go/m5.go/abilities_wire.go touch these directly):
//   - m9Mob struct (all fields), m9Mu/m9Mobs registry, m9Prof/m9Spawn
//     tables, m9PlayerHPs hero HP store.
//   - The shared entity.World implementation (gameWorld): broadcast/send,
//     entities/players maps, setEntityPos, blocked, m5 loot/XP, m11 kill,
//     combatMu-adjacent lookups — packet shapes frozen here.
//   - m9Engine/m9AdoptExisting (mode globals), m9TestHandler (TESTMAP
//     debug frames), m9RatEntity/m.data (EntityData payload shape).
//
// MOVED to entity: profiles/load/merge/defaults, distances, damage roll,
// roam pick, chase step, aggro/leash/attack tick, retaliate, kill credit,
// respawn timers, hero HP/death hooks. The old m9<->m10 call cycle
// (m9SpawnMob->m10ChestAreaAt/m10AddChestMob; m10KillHooks->m9 state) is
// gone: both engines live in ONE package (internal/entity) behind the
// GameWorld seam.
// ---------------------------------------------------------------------------

package server

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"rpg-world-server/internal/entity"
	gnet "rpg-world-server/internal/net"
	worldcore "rpg-world-server/internal/world"
)

// m9MobProfile/m9SpawnOverride/m9Overrides are entity-owned now (same
// JSON/shape, type aliases — all existing references keep compiling).
type (
	m9MobProfile    = entity.MobProfile
	m9SpawnOverride = entity.SpawnOverride
	m9Overrides     = entity.MobOverrides
)

// m9Mob is one live AI mob instance (Node Mob + handler state).
type m9Mob struct {
	instance string
	key      string
	prof     m9MobProfile // mobs.json merged with spawns.json overrides

	spawnX, spawnY int
	x, y           int
	hp, maxHP      int
	dead           bool

	// plateau is the bound plateau level from the spawn tile
	// (mob.ts:148 parity; roam steps + incoming swings gate on it).
	plateau int

	mu        sync.Mutex
	target    string // player instance (combat.target)
	lastAtk   time.Time
	lastMove  time.Time
	lastRoam  time.Time
	lastTgt   time.Time
	attackers map[string]time.Time // instance -> last hit (addAttacker)

	over m9Overrides
}

// Engine registry. Lock order: m9Mu -> m.mu (never reversed).
var (
	m9Mu    sync.Mutex
	m9Mobs  = map[string]*m9Mob{}
	m9Prof  = map[string]*m9MobProfile{}
	m9Spawn = map[string]*m9SpawnOverride{}
)

// ---------------------------------------------------------------------------
// entity.Mob implementation (locked-context accessors; callers hold m.mu
// exactly where the old code held it).
// ---------------------------------------------------------------------------

func (m *m9Mob) Lock()   { m.mu.Lock() }
func (m *m9Mob) Unlock() { m.mu.Unlock() }

func (m *m9Mob) Instance() string               { return m.instance }
func (m *m9Mob) MobKey() string                 { return m.key }
func (m *m9Mob) Profile() entity.MobProfile     { return m.prof }
func (m *m9Mob) Overrides() entity.MobOverrides { return m.over }

func (m *m9Mob) SpawnPos() (int, int) { return m.spawnX, m.spawnY }
func (m *m9Mob) Pos() (int, int)      { return m.x, m.y }
func (m *m9Mob) SetPos(x, y int)      { m.x, m.y = x, y }

func (m *m9Mob) HP() int           { return m.hp }
func (m *m9Mob) MaxHP() int        { return m.maxHP }
func (m *m9Mob) SetHP(hp int)      { m.hp = hp }
func (m *m9Mob) Dead() bool        { return m.dead }
func (m *m9Mob) SetDead(dead bool) { m.dead = dead }

func (m *m9Mob) Target() string     { return m.target }
func (m *m9Mob) SetTarget(t string) { m.target = t }

// Plateau reports the bound spawn plateau level (mob.ts:148 parity; the
// caller must hold m.mu like every other accessor).
func (m *m9Mob) Plateau() int { return m.plateau }

func (m *m9Mob) LastAtk() time.Time      { return m.lastAtk }
func (m *m9Mob) SetLastAtk(t time.Time)  { m.lastAtk = t }
func (m *m9Mob) LastMove() time.Time     { return m.lastMove }
func (m *m9Mob) SetLastMove(t time.Time) { m.lastMove = t }
func (m *m9Mob) LastRoam() time.Time     { return m.lastRoam }
func (m *m9Mob) SetLastRoam(t time.Time) { m.lastRoam = t }
func (m *m9Mob) LastTgt() time.Time      { return m.lastTgt }
func (m *m9Mob) SetLastTgt(t time.Time)  { m.lastTgt = t }

func (m *m9Mob) TouchAttacker(inst string, now time.Time) { m.attackers[inst] = now }
func (m *m9Mob) DropAttacker(inst string)                 { delete(m.attackers, inst) }

func (m *m9Mob) Attackers() map[string]time.Time {
	out := make(map[string]time.Time, len(m.attackers))
	for k, v := range m.attackers {
		out[k] = v
	}
	return out
}

func (m *m9Mob) ClearAttackers() { m.attackers = map[string]time.Time{} }

// ---------------------------------------------------------------------------
// Shared entity.GameWorld implementation (M9+M10 world seam).
// ---------------------------------------------------------------------------

// gameWorldAdapter implements entity.GameWorld with the root helpers.
// Packet shapes are frozen here (identical frame builders to the old
// m9.go/m10.go bodies).
type gameWorldAdapter struct{}

var gameWorld = gameWorldAdapter{}

func (gameWorldAdapter) Players() []entity.PlayerView {
	out := []entity.PlayerView{}
	for _, c := range worldcore.AllOf[*playerConn]() {
		// Corpses never aggro (TS: dead players drop out of combat;
		// cleanCombat + the hit() dead-guard). Without this the next
		// tick re-acquires the corpse and re-strikes it, rebroadcasting
		// Points-0 and re-firing HeroDied. Fresh conns read full HP
		// (GetHeroHP default) and are unaffected.
		if gameWorld.GetHeroHP(c.Instance) <= 0 {
			continue
		}
		lvl, def := 0, 0
		if st := m5StateFor(c.Username); st != nil {
			lvl = st.Level
			if sk := st.Skills[SkillDefense]; sk != nil {
				def = sk.Level
			}
		}
		out = append(out, entity.PlayerView{
			Instance: c.Instance, Username: c.Username,
			X: c.Sess.PlayerX, Y: c.Sess.PlayerY,
			Level: lvl, Defense: def,
			Plateau: plateauGet(c.Instance),
		})
	}
	return out
}

func (gameWorldAdapter) PlayerPos(instance string) (int, int, bool) {
	if c, _ := worldcore.Find[*playerConn](instance); c != nil {
		return c.Sess.PlayerX, c.Sess.PlayerY, true
	}
	return 0, 0, false
}

func (gameWorldAdapter) Blocked(x, y int) bool { return blocked(x, y) }

func (gameWorldAdapter) PlateauLevel(x, y int) int { return plateauLevelOf(x, y) }

func (gameWorldAdapter) SetEntityPos(instance string, x, y int) {
	worldcore.SetEntityPos(instance, x, y)
}

func (gameWorldAdapter) Despawn(instance string) {
	worldcore.Broadcast(pkt(PacketDespawn, despawnData{Instance: instance}))
}

func (gameWorldAdapter) MoveMob(instance string, x, y int) {
	worldcore.SetEntityPos(instance, x, y)
	worldcore.Broadcast(pktOp(PacketMovement, MovementMove, serverMovement{
		Instance: instance, X: intp(x), Y: intp(y),
	}))
}

func (gameWorldAdapter) SpawnMobFrame(s entity.MobSpawn) {
	worldcore.SetEntityPos(s.Instance, s.X, s.Y)
	worldcore.Broadcast(pkt(PacketSpawn, EntityData{
		Instance: s.Instance, Type: EntityMob, Key: s.Key,
		Name: s.Name, X: s.X, Y: s.Y,
		Orientation: intp(OrientationDown),
		Level:       intp(s.Level),
		HitPoints:   intp(s.HP), MaxHitPoints: intp(s.MaxHP),
		MovementSpeed: intp(s.MoveSpeed),
		AttackRange:   intp(s.AttackRange),
	}))
}

func (gameWorldAdapter) MobPoints(instance string, hp, maxHP int) {
	worldcore.Broadcast(pkt(PacketPoints, pointsData{
		Instance: instance, HitPoints: intp(hp), MaxHitPoints: intp(maxHP),
	}))
}

func (gameWorldAdapter) StrikeMob(attacker, target string, dmg int) {
	worldcore.Broadcast(pktOp(PacketCombat, CombatHit, combatData{
		Instance: attacker, Target: target,
		Hit: HitData{Type: HitsNormal, Damage: dmg},
	}))
}

func (gameWorldAdapter) GetHeroHP(instance string) int {
	if v, ok := m9PlayerHPs.Load(instance); ok {
		return v.(int)
	}
	return entity.HeroMaxHP
}

func (gameWorldAdapter) SetHeroHP(instance string, hp int) {
	m9PlayerHPs.Store(instance, hp)
}

func (gameWorldAdapter) ForgetHeroHP(instance string) {
	m9PlayerHPs.Delete(instance)
}

func (gameWorldAdapter) HeroPoints(instance string, hp, maxHP int) {
	worldcore.Broadcast(pkt(PacketPoints, pointsData{
		Instance: instance, HitPoints: intp(hp), MaxHitPoints: intp(maxHP),
	}))
}

// m9DeathFired marks instances whose HeroDied funnel already ran, so the
// funnel is exactly-once per life (TS character.hit dead-guard parity:
// hits on a corpse are silent — no Points-0 rebroadcasts, no duplicate
// Death/Despawn/save). Lock-free sync.Map: HeroDied runs on the engine
// tick (holding m9Mu + the killer's m.mu), the StatusTick loop and admin
// intake, so it can take no subsystem mutexes of its own. Cleared on
// respawn (m9HandleRespawn) and disconnect (m9PlayerLeave) so the next
// life dies loudly again.
var m9DeathFired sync.Map // instance -> true

func (gameWorldAdapter) HeroDied(playerInstance, username, mobInstance string) {
	// Player handleDeath parity (player/handler.ts handleDeath): status
	// clear, Despawn broadcast, pet despawn, persist flush, Death unicast
	// to self. Attacker release: the killer's target already cleared in
	// DamageHero; every other mob targeting the victim releases it on its
	// next tick through the existing gone-target path (the corpse leaves
	// the Players scan set below) — the world.ts cleanCombat outcome with
	// no new locks. PvP accounting (pvpDeaths/killCallback), the
	// damageTable reset and the skills/combat stops have no Go counterparts
	// (no damage table, no hero combat loop — single-swing dispatch — and
	// gathering is per-swing with no continuous action to stop) and stay
	// omitted.
	//
	// LOCK DISCIPLINE: the strike path calls this holding m9Mu (m9Tick)
	// and the killer's m.mu — take neither here (self-deadlock). Every
	// seam below is lock-free or leaf-ordered (tracker/registry/pstateMu
	// follow the pre-existing m9Mu-outer order; m5 paths never take m9Mu).
	_ = mobInstance
	if _, dup := m9DeathFired.LoadOrStore(playerInstance, true); dup {
		return
	}
	abClearStatus(playerInstance) // status.clear() + setPoison() cure; unlocks kept
	c, _ := worldcore.Find[*playerConn](playerInstance)
	worldcore.Broadcast(pkt(PacketDespawn, despawnData{Instance: playerInstance}))
	if c != nil {
		petForgetPlayer(c) // disconnect removePet parity; no-op without a pet
	}
	m5SaveSync(username) // disconnect persist path reused, not duplicated
	if c != nil {
		// Death goes to the victim only (TS sends Death to self);
		// observers learn of the death via the Despawn above.
		_ = gnet.Send(c.Conn, pkt(PacketDeath, playerInstance))
	}
}

func (gameWorldAdapter) TeleportHero(instance string, x, y int) {
	worldcore.Broadcast(pkt(PacketTeleport, teleportData{Instance: instance, X: x, Y: y}))
}

func (gameWorldAdapter) SpawnHero(instance string) {
	worldcore.Broadcast(pkt(PacketSpawn, welcomePlayer(instance)))
}

func (gameWorldAdapter) HeroRespawned(instance string, x, y int) {
	if c, _ := worldcore.Find[*playerConn](instance); c != nil {
		_ = gnet.Send(c.Conn, pkt(PacketRespawn, respawnData{X: x, Y: y}))
	}
}

func (gameWorldAdapter) AfterDelay(d time.Duration, fn func()) {
	time.AfterFunc(d, fn)
}

func (gameWorldAdapter) ApplyPoison(instance string) {
	abApplyPoison(instance)
}

func (gameWorldAdapter) SpawnLoot(mobKey string, x, y int, owner string) {
	m5SpawnLoot(mobKey, x, y, owner)
}

func (gameWorldAdapter) NearWalkable(x, y int) (int, int) {
	return m5NearWalkable(x, y)
}

func (gameWorldAdapter) RegisterLoot(inst, key string, count, x, y int, owner string) {
	m5RegisterLoot(inst, key, count, x, y, owner)
}

func (gameWorldAdapter) SpawnLootItem(i entity.LootItem) {
	worldcore.Broadcast(pkt(PacketSpawn, EntityData{
		Instance: i.Instance, Type: EntityItem, Key: i.Key, Name: i.Key,
		X: i.X, Y: i.Y, Count: intp(i.Count),
	}))
}

func (gameWorldAdapter) QuestKill(killerInstance, mobKey string) {
	killer, _ := worldcore.Find[*playerConn](killerInstance)
	m11Kill(killer, mobKey) // nil-safe (m11Kill guards nil)
	// Statistics kill counter rides the same death signal (handler.ts:765
	// addMobKill parity — counter only, no achievement).
	statsRecordKill(killer, mobKey) // nil-safe
}

func (gameWorldAdapter) Notify(instance, msg string) {
	if c, _ := worldcore.Find[*playerConn](instance); c != nil {
		m6Notify(c, msg)
	}
}

func (gameWorldAdapter) SendPVP(instance string, state bool) {
	if c, _ := worldcore.Find[*playerConn](instance); c != nil {
		_ = gnet.Send(c.Conn, []any{PacketPVP, nil, map[string]any{"state": state}})
	}
}

func (gameWorldAdapter) SendOverlaySet(instance, image, colour string) {
	if c, _ := worldcore.Find[*playerConn](instance); c != nil {
		_ = gnet.Send(c.Conn, pktOp(PacketOverlay, entity.OverlaySet, map[string]any{
			"image":  image,
			"colour": colour,
		}))
	}
}

func (gameWorldAdapter) SendOverlayRemove(instance string) {
	if c, _ := worldcore.Find[*playerConn](instance); c != nil {
		_ = gnet.Send(c.Conn, pktOp(PacketOverlay, entity.OverlayRemove, nil))
	}
}

func (gameWorldAdapter) SendCamera(instance string, opcode int) {
	if c, _ := worldcore.Find[*playerConn](instance); c != nil {
		_ = gnet.Send(c.Conn, pktOp(PacketCamera, opcode, nil))
	}
}

func (gameWorldAdapter) SendMusic(instance, song string) {
	if c, _ := worldcore.Find[*playerConn](instance); c != nil {
		_ = gnet.Send(c.Conn, []any{PacketMusic, nil, song})
	}
}

func (gameWorldAdapter) SendEffect(instance string, add bool, effect int) {
	op := EffectRemove
	if add {
		op = EffectAdd
	}
	if c, _ := worldcore.Find[*playerConn](instance); c != nil {
		_ = gnet.Send(c.Conn, pktOp(PacketEffect, op, effectData{Instance: instance, Effect: effect}))
	}
}

func (gameWorldAdapter) FreezeApply(instance string) {
	abFreezeApply(instance)
}

func (gameWorldAdapter) FreezeClear(instance string) {
	abFreezeClear(instance)
}

func (gameWorldAdapter) SpawnChestFrame(c entity.ChestSpawn) {
	worldcore.SetEntityPos(c.Instance, c.X, c.Y)
	worldcore.Broadcast(pkt(PacketSpawn, EntityData{
		Instance: c.Instance, Type: EntityChest, Key: "chest", Name: "Chest", X: c.X, Y: c.Y,
	}))
}

func (gameWorldAdapter) FinishAchievement(instance, key string) {
	c, _ := worldcore.Find[*playerConn](instance)
	m11FinishAchievement(c, key) // nil-safe (unknown instance/achievement ignored)
}

// playerViewFor builds one PlayerView for a live conn (m9OnPlayerMoved).
func playerViewFor(c *playerConn) entity.PlayerView {
	lvl, def := 0, 0
	if st := m5StateFor(c.Username); st != nil {
		lvl = st.Level
		if sk := st.Skills[SkillDefense]; sk != nil {
			def = sk.Level
		}
	}
	return entity.PlayerView{
		Instance: c.Instance, Username: c.Username,
		X: c.Sess.PlayerX, Y: c.Sess.PlayerY,
		Level: lvl, Defense: def,
		Plateau: plateauGet(c.Instance),
	}
}

// mobAlive reports whether m is still the registered mob (respawn guard).
func mobAlive(m *m9Mob) bool {
	return m9MobFor(m.instance) == m
}

// killerView maps a killer conn to a PlayerView (nil-safe).
func killerView(killer *playerConn) *entity.PlayerView {
	if killer == nil {
		return nil
	}
	v := entity.PlayerView{Instance: killer.Instance, Username: killer.Username}
	return &v
}

// ---------------------------------------------------------------------------
// Tables / spawn / registry.
// ---------------------------------------------------------------------------

func m9LoadTables() {
	m9Mu.Lock()
	defer m9Mu.Unlock()
	if len(m9Prof) > 0 {
		return
	}
	for name, dst := range map[string]any{"mobs": &m9Prof, "spawns": &m9Spawn} {
		raw, err := os.ReadFile(resourceDataPath(name))
		if err != nil {
			log.Printf("m9: read %s.json: %v (engine disabled)", name, err)
			return
		}
		if err := json.Unmarshal(raw, dst); err != nil {
			log.Printf("m9: parse %s.json: %v (engine disabled)", name, err)
			return
		}
	}
	log.Printf("m9: mobs.json=%d profiles, spawns.json=%d overrides", len(m9Prof), len(m9Spawn))
}

// m9SpawnMob registers + broadcasts a mob (entities.ts spawnMob shape).
// World boot adoption and the m9test dispatcher both land here.
func m9SpawnMob(instance, key string, x, y int, over m9Overrides) bool {
	m9LoadTables()
	m9Mu.Lock()
	prof := entity.ProfileFor(m9Prof, m9Spawn, key, x, y)
	if prof == nil {
		m9Mu.Unlock()
		log.Printf("m9: unknown mob key %s (spawn skipped)", key)
		return false
	}
	entity.ApplyDefaults(prof)
	m := &m9Mob{
		instance: instance, key: key, prof: *prof,
		spawnX: x, spawnY: y, x: x, y: y,
		maxHP: prof.HitPoints, hp: prof.HitPoints,
		plateau:   plateauLevelOf(x, y), // mob.ts:148 spawn plateau bind
		lastMove:  time.Now(),
		lastRoam:  time.Now(),
		attackers: map[string]time.Time{},
		over:      over,
	}
	if over.Aggro > 0 {
		m.prof.AggroRange = over.Aggro
	}
	if over.Leash > 0 {
		m.prof.RoamDistance = over.Leash
	}
	m9Mobs[instance] = m
	payload := m.data()
	m9Mu.Unlock()

	worldcore.SetEntityPos(instance, x, y)
	worldcore.Broadcast(pkt(PacketSpawn, payload))
	// M10: Mob.addToChestArea parity — a mob spawning inside a chest area
	// registers with it (addEntity; removes any unlooted reward chest).
	if area := m10ChestAreaAt(x, y); area != nil {
		m10AddChestMob(area, instance, m.respawnDelay())
	}
	return true
}

// m9Remove drops a mob from the registry (no despawn frame; callers that
// need one broadcast it themselves — Node despawn/destroy split).
func m9Remove(instance string) {
	m9Mu.Lock()
	delete(m9Mobs, instance)
	m9Mu.Unlock()
}

func m9MobFor(instance string) *m9Mob {
	m9Mu.Lock()
	defer m9Mu.Unlock()
	return m9Mobs[instance]
}

// data mirrors Mob.serialize: hitPoints/maxHitPoints/attackRange/level.
func (m *m9Mob) data() EntityData {
	return EntityData{
		Instance: m.instance, Type: EntityMob, Key: m.key,
		Name: m.prof.Name, X: m.x, Y: m.y,
		Orientation: intp(OrientationDown),
		Level:       intp(m.prof.Level),
		HitPoints:   intp(m.hp), MaxHitPoints: intp(m.maxHP),
		MovementSpeed: intp(m.prof.MovementSpeed),
		AttackRange:   intp(m.prof.AttackRange),
	}
}

// respawnDelay mirrors Mob.respawn: override > profile > MobDefaults.
func (m *m9Mob) respawnDelay() time.Duration {
	return entity.RespawnDelayFor(m.over, m.prof)
}

// ---------------------------------------------------------------------------
// Engine tick / hit intake (orchestration lives in entity).
// ---------------------------------------------------------------------------

// m9Tick is the 500ms AI pass over every live mob.
func m9Tick() {
	m9Mu.Lock()
	defer m9Mu.Unlock()
	now := time.Now()
	for _, m := range m9Mobs {
		if m.dead {
			continue
		}
		entity.StepMob(m, gameWorld, now)
	}
}

// m9PlayerHit applies hero damage to a mob: Points, retaliate, death.
func m9PlayerHit(m *m9Mob, attacker *playerConn, dmg int) {
	entity.HitMob(m, killerView(attacker), dmg, gameWorld, time.Now(), func() bool {
		return mobAlive(m)
	})
}

// m9KillMob ports handler.handleDeath: despawn + kill credit (M5 loot) +
// destroy (respawn timer restores full HP at spawn).
func m9KillMob(m *m9Mob, killer *playerConn) {
	entity.KillMob(m, killerView(killer), gameWorld, func() bool {
		return mobAlive(m)
	})
}

// m9Respawn ports handler.handleRespawn: full HP, back at spawn, Spawn frame.
func m9Respawn(m *m9Mob) {
	if m9MobFor(m.instance) != m {
		return // removed while dead
	}
	entity.RespawnMob(m, gameWorld)
	log.Printf("m9: %s respawned full HP=%d", m.instance, m.maxHP)
}

// ---------------------------------------------------------------------------
// Player HP / death / respawn (character.hitPoints + player.respawn).
// ---------------------------------------------------------------------------

var m9PlayerHPs sync.Map // instance -> remaining HP

func m9PlayerMaxHP() int { return entity.HeroMaxHP } // stub hero max (divergence note)

func m9PlayerHP(c *playerConn) int {
	return gameWorld.GetHeroHP(c.Instance)
}

// m9DamagePlayer applies mob damage: Points frame, Death on empty.
func m9DamagePlayer(c *playerConn, dmg int, from *m9Mob) {
	var f entity.Mob
	if from != nil {
		f = from
	}
	entity.DamageHero(gameWorld, c.Instance, c.Username, dmg, f)
}

// m9HandleRespawn ports incoming.handleRespawn -> player.respawn: only when
// dead; teleport to spawn + Spawn broadcast + Respawn{x,y} + Points sync.
// The respawn tile is tracked via m5TrackPos (persist parity: a disconnect
// right after respawn must relogin at spawn, not at the death tile).
func m9HandleRespawn(c *playerConn) {
	if m9PlayerHP(c) > 0 {
		log.Printf("m9: invalid respawn request from %s", c.Username)
		return
	}
	x, y := entity.HeroSpawnX, entity.HeroSpawnY
	c.Sess.PlayerX, c.Sess.PlayerY = x, y
	if !entity.RespawnHero(gameWorld, c.Instance) {
		log.Printf("m9: invalid respawn request from %s", c.Username)
		return
	}
	m9DeathFired.Delete(c.Instance) // next life dies loudly again
	m5TrackPos(c)                   // tracks the respawn tile (plateauTrack rides along)
	plateauTrack(c)
	m8OnPositionUpdate(c)  // respawn position can cross an area boundary
	m10OnPositionUpdate(c) // M10: area callbacks on the respawn tile too
	log.Printf("m9: %s respawned at %d,%d", c.Username, x, y)
}

// m9PlayerLeave drops per-player state on disconnect.
func m9PlayerLeave(c *playerConn) {
	m9PlayerHPs.Delete(c.Instance)
	m9DeathFired.Delete(c.Instance)
	m9Mu.Lock()
	for _, m := range m9Mobs {
		m.mu.Lock()
		if m.target == c.Instance {
			m.target = ""
		}
		delete(m.attackers, c.Instance)
		m.mu.Unlock()
	}
	m9Mu.Unlock()
}

// m9Engine boots the AI loop + adopts the demo mobs (main()).
func m9Engine() {
	m9LoadTables()
	m9AdoptExisting()
	go func() {
		t := time.NewTicker(entity.RoamTick)
		defer t.Stop()
		for range t.C {
			m9Tick()
		}
	}()
}

// m9AdoptExisting re-registers the demo mobs under the engine. m-rat-1 keeps
// its M3 slice-1 demo semantics (forced chase, no strikes, aggro 6, leash
// 10, respawn 10s). m1 (plain-mode rat) runs the full profile.
func m9AdoptExisting() {
	switch {
	case combatMode:
		m9SpawnMob(combatRatInstance, "rat", combatRatX, combatRatY, m9Overrides{
			NoAttack: true, Chase: true, Aggro: combatRatAggro, Leash: combatRatLeash,
			Respawn: combatRatRespawnDelay,
		})
	case !testMode && !cleanMode:
		m9SpawnMob("m1", "rat", 104, 104, m9Overrides{})
	}
}

// m9RatEntity returns the combat leash-demo rat's Spawn payload, or nil
// when it is dead/absent (combatSpawns() parity with the old ratData gate).
func m9RatEntity() *EntityData {
	m := m9MobFor(combatRatInstance)
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dead {
		return nil
	}
	d := m.data()
	return &d
}

// m9OnPlayerMoved is the position-update hook from the movement handler:
// Node runs detectAggro on every position change; the engine scans on the
// next tick, so this only fast-forwards the aggro scan for responsiveness.
func m9OnPlayerMoved(c *playerConn) {
	v := playerViewFor(c)
	m9Mu.Lock()
	defer m9Mu.Unlock()
	for _, m := range m9Mobs {
		if m.dead {
			continue
		}
		m.mu.Lock()
		if m.target == "" && entity.CanAggro(m.prof, m.over, m.x, m.y, m.target, v) {
			m.target = c.Instance
			m.lastTgt = time.Now()
		}
		m.mu.Unlock()
	}
}

// m9TestHandler is the TESTMAP-only debug dispatcher (m8test precedent):
// spawn/remove engine mobs and reposition the hero for deterministic e2e.
func m9TestHandler(c *playerConn, data []byte) {
	if !testMode {
		return
	}
	var d struct {
		M9Test   string `json:"m9test"`
		Instance string `json:"instance"`
		Key      string `json:"key"`
		X        int    `json:"x"`
		Y        int    `json:"y"`
		Aggro    int    `json:"aggro"`
		Leash    int    `json:"leash"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return
	}
	switch d.M9Test {
	case "spawn":
		m9SpawnMob(d.Instance, d.Key, d.X, d.Y, m9Overrides{Aggro: d.Aggro, Leash: d.Leash})
	case "remove":
		if m := m9MobFor(d.Instance); m != nil {
			worldcore.Broadcast(pkt(PacketDespawn, despawnData{Instance: d.Instance}))
			m9Remove(d.Instance)
		}
	case "tp": // reposition the hero server-side (seedPos precedent)
		if c != nil {
			c.Sess.PlayerX, c.Sess.PlayerY = d.X, d.Y
			worldcore.SetEntityPos(c.Instance, d.X, d.Y)
			worldcore.Broadcast(pkt(PacketTeleport, teleportData{Instance: c.Instance, X: d.X, Y: d.Y}))
			m5TrackPos(c) // persist parity: the test tile must survive a save
			plateauTrack(c)
			m8OnPositionUpdate(c)
			m9OnPlayerMoved(c)     // position updates run the aggro scan
			m10OnPositionUpdate(c) // M10: camera/music/pvp/overlay area callbacks
		}
	case "mobhp": // debug echo: m9:mob=... hp=.../... x=... y=... tgt=...
		m := m9MobFor(d.Instance)
		if m == nil || c == nil {
			return
		}
		m.mu.Lock()
		echo := fmt.Sprintf("m9:mob=%s hp=%d/%d x=%d y=%d tgt=%s", d.Instance, m.hp, m.maxHP, m.x, m.y, m.target)
		m.mu.Unlock()
		m6Notify(c, echo)
	}
}
