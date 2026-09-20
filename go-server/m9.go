// ---------------------------------------------------------------------------
// M9 — generic mob AI engine (worlds setup slice 1).
//
// Faithful port of the Node mob stack:
//   - mob.ts: data load (mobs.json + spawns.json per-instance overrides via
//     the "x-y" key), canAggro (isNear Chebyshev + level*3 gate),
//     outsideRoaming (utils.getDistance = Manhattan vs roamDistance),
//     sendToSpawn, respawn (MobDefaults.RESPAWN_DELAY 60s), canChangeTarget
//     (13s).
//   - handler.ts: leash (no-target outsideRoaming -> sendToSpawn; target
//     outside roamDistance*2 -> drop + return), handleHit retaliate
//     (addAttacker + combat.attack), handleCombatLoop attacker pruning
//     (roamDistance*2 or Constants.ATTACKER_TIMEOUT 20s), handleDeath
//     (kill credit -> M5 loot), handleRespawn (full HP at spawn).
//   - combat.ts: attack loop (attackRate cadence, follow throttle 500ms,
//     shouldTeleportNearby stall >5s — nudge omitted, chase covers it).
//   - character.ts/player.ts/incoming.ts: player HP + Death frame on empty,
//     C→S Respawn -> player.respawn (teleport to spawn + Spawn broadcast +
//     Respawn{x,y} to self + Points sync), "Invalid respawn request." guard.
//   - formulas.ts: mob->player getDamage/getMaxDamage (bonuses + skills vs
//     player defense level/stats; weighted floor(rand^accuracy * (max+1))).
//
// Data: mobs.json profiles merged with spawns.json "<x>-<y>" overrides
// (mob.ts loadSpawns keying) through resourceDataPath resolution.
//
// Documented divergences (stub scope):
//   - One AI tick every 500ms steps one tile (no 20Hz pathing; the M3 rat
//     demo's established shape). Roam interval honors ROAM_FREQUENCY 17s.
//   - Plateau checks skipped (TESTMAP is flat; no plateaus wired).
//   - Player max HP stays the stub's 100 (Node getMaxHitPoints 39+30*level
//     would reshape every persisted hero; M5 owns the HP shape).
//   - Ranged mobs damage immediately (projectile flight visual stays M3's).
//   - Aggro scan runs on the tick instead of per-position-update.
//   - m-rat-1 (COMBAT leash demo) keeps M3 slice-1 semantics via overrides:
//     forced chase (the old demo chased regardless of `aggressive`), no
//     strikes, aggro 6, leash 10, respawn 10s.
//   - M9_MOBDMG env multiplies mob damage (debug, default 1).
// ---------------------------------------------------------------------------

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"strconv"
	"sync"
	"time"
)

// Modules.MobDefaults (modules.ts:690).
const (
	m9AggroRange    = 2                // AGGRO_RANGE
	m9RespawnDelay  = 60 * time.Second // RESPAWN_DELAY
	m9RoamDistance  = 7                // ROAM_DISTANCE
	m9RoamFrequency = 17 * time.Second // ROAM_FREQUENCY
)

// Modules.Constants / Defaults.
const (
	m9AttackerTimeout   = 20 * time.Second       // Constants.ATTACKER_TIMEOUT
	m9TargetChangeCD    = 13 * time.Second       // Mob.canChangeTarget
	m9FollowThrottle    = 500 * time.Millisecond // combat.ts follow spam guard
	m9RoamTick          = 500 * time.Millisecond
	m9DefaultAttackRate = 1000 // Defaults.ATTACK_RATE
	m9DefaultMoveSpeed  = 220  // Defaults.MOVEMENT_SPEED
)

// m9MobProfile mirrors the mobs.json entry shape Node Mob.loadData consumes.
type m9MobProfile struct {
	Name          string `json:"name"`
	Level         int    `json:"level"`
	HitPoints     int    `json:"hitPoints"`
	AggroRange    int    `json:"aggroRange"`
	AttackRange   int    `json:"attackRange"`
	AttackRate    int    `json:"attackRate"`
	MovementSpeed int    `json:"movementSpeed"`
	RespawnDelay  int    `json:"respawnDelay"`
	RoamDistance  int    `json:"roamDistance"`
	Roaming       *bool  `json:"roaming"`
	Aggressive    bool   `json:"aggressive"`
	AlwaysAggro   bool   `json:"alwaysAggressive"`
	Boss          bool   `json:"boss"`
	Miniboss      bool   `json:"miniboss"`
	AttackStats   struct {
		Crush   int `json:"crush"`
		Slash   int `json:"slash"`
		Stab    int `json:"stab"`
		Archery int `json:"archery"`
		Magic   int `json:"magic"`
	} `json:"attackStats"`
	DefenseStats struct {
		Crush   int `json:"crush"`
		Slash   int `json:"slash"`
		Stab    int `json:"stab"`
		Archery int `json:"archery"`
		Magic   int `json:"magic"`
	} `json:"defenseStats"`
	Bonuses struct {
		Accuracy int `json:"accuracy"`
		Strength int `json:"strength"`
		Archery  int `json:"archery"`
		Magic    int `json:"magic"`
	} `json:"bonuses"`
	Skills struct {
		Accuracy int `json:"accuracy"`
		Strength int `json:"strength"`
		Defense  int `json:"defense"`
		Archery  int `json:"archery"`
		Magic    int `json:"magic"`
	} `json:"skills"`
}

// m9SpawnOverride mirrors a spawns.json value (per-instance MobData).
type m9SpawnOverride m9MobProfile

// m9Overrides carries per-instance demo/test knobs layered on the profile.
type m9Overrides struct {
	NoAttack bool // M3 rat demo: chase+leash but never strike
	Chase    bool // M3 demo: chase regardless of `aggressive`
	Aggro    int  // override aggroRange (0 = profile)
	Leash    int  // override roamDistance (0 = profile)
	Respawn  time.Duration
}

// m9Mob is one live AI mob instance (Node Mob + handler state).
type m9Mob struct {
	instance string
	key      string
	prof     m9MobProfile // mobs.json merged with spawns.json overrides

	spawnX, spawnY int
	x, y           int
	hp, maxHP      int
	dead           bool

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

// m9RoamInterval honors M9_ROAMMS (debug override for fast e2e; default is
// MobDefaults.ROAM_FREQUENCY 17s).
func m9RoamInterval() time.Duration {
	if v := os.Getenv("M9_ROAMMS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return m9RoamFrequency
}

// m9MobDamageMult multiplies mob damage when M9_MOBDMG is set (debug).
func m9MobDamageMult() float64 {
	if v := os.Getenv("M9_MOBDMG"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return f
		}
	}
	return 1
}

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

// m9ProfileFor merges mobs.json[key] with spawns.json["x-y"] overrides
// (mob.ts loadSpawns keying). Returns nil when the key is unknown/invalid.
func m9ProfileFor(key string, x, y int) *m9MobProfile {
	base, ok := m9Prof[key]
	if !ok {
		return nil
	}
	p := *base
	if ov, ok := m9Spawn[fmt.Sprintf("%d-%d", x, y)]; ok {
		apply := func(dst *int, v int) {
			if v != 0 {
				*dst = v
			}
		}
		apply(&p.Level, ov.Level)
		apply(&p.HitPoints, ov.HitPoints)
		apply(&p.AggroRange, ov.AggroRange)
		apply(&p.AttackRange, ov.AttackRange)
		apply(&p.AttackRate, ov.AttackRate)
		apply(&p.MovementSpeed, ov.MovementSpeed)
		apply(&p.RespawnDelay, ov.RespawnDelay)
		apply(&p.RoamDistance, ov.RoamDistance)
		p.Aggressive = p.Aggressive || ov.Aggressive
		p.AlwaysAggro = p.AlwaysAggro || ov.AlwaysAggro
		p.Boss = p.Boss || ov.Boss
		p.Miniboss = p.Miniboss || ov.Miniboss
		if ov.Roaming != nil { // Node: absent key keeps the base value
			p.Roaming = ov.Roaming
		}
	}
	if p.HitPoints <= 0 {
		return nil
	}
	return &p
}

// m9DefaultsLocked fills Node defaults for zero-valued profile fields
// (mob.ts field defaults + MobDefaults).
func m9DefaultsLocked(p *m9MobProfile) {
	if p.Level <= 0 {
		p.Level = 1
	}
	if p.AggroRange <= 0 {
		p.AggroRange = m9AggroRange
	}
	if p.AttackRange <= 0 {
		p.AttackRange = 1
	}
	if p.AttackRate <= 0 {
		p.AttackRate = m9DefaultAttackRate
	}
	if p.MovementSpeed <= 0 {
		p.MovementSpeed = m9DefaultMoveSpeed
	}
	if p.RespawnDelay <= 0 {
		p.RespawnDelay = int(m9RespawnDelay / time.Millisecond)
	}
	if p.RoamDistance <= 0 {
		p.RoamDistance = m9RoamDistance
	}
	if p.Roaming == nil {
		t := true // mob.ts: roaming true by default
		p.Roaming = &t
	}
	if p.Skills.Accuracy <= 0 {
		p.Skills.Accuracy = 1
	}
	if p.Skills.Strength <= 0 {
		p.Skills.Strength = 1
	}
	if p.Skills.Defense <= 0 {
		p.Skills.Defense = 1
	}
	if p.Skills.Archery <= 0 {
		p.Skills.Archery = 1
	}
	if p.Skills.Magic <= 0 {
		p.Skills.Magic = 1
	}
}

// m9SpawnMob registers + broadcasts a mob (entities.ts spawnMob shape).
// World boot adoption and the m9test dispatcher both land here.
func m9SpawnMob(instance, key string, x, y int, over m9Overrides) bool {
	m9LoadTables()
	m9Mu.Lock()
	prof := m9ProfileFor(key, x, y)
	if prof == nil {
		m9Mu.Unlock()
		log.Printf("m9: unknown mob key %s (spawn skipped)", key)
		return false
	}
	m9DefaultsLocked(prof)
	m := &m9Mob{
		instance: instance, key: key, prof: *prof,
		spawnX: x, spawnY: y, x: x, y: y,
		maxHP: prof.HitPoints, hp: prof.HitPoints,
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

	setEntityPos(instance, x, y)
	broadcast(pkt(PacketSpawn, payload))
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

func (m *m9Mob) pos() (int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.x, m.y
}

// respawnDelay mirrors Mob.respawn: override > profile > MobDefaults.
func (m *m9Mob) respawnDelay() time.Duration {
	if m.over.Respawn > 0 {
		return m.over.Respawn
	}
	return time.Duration(m.prof.RespawnDelay) * time.Millisecond
}

// canAggro ports Mob.canAggro: existing-target gate, aggressive flags
// (+ M3 demo Chase override), level*3 gate (unless alwaysAggressive),
// isNear (Chebyshev) range check.
func (m *m9Mob) canAggro(p *playerConn) bool {
	if m.target != "" {
		return false
	}
	if !m.prof.Aggressive && !m.prof.AlwaysAggro && !m.over.Chase {
		return false
	}
	lvl := 1
	if st := m5StateFor(p.username); st != nil && st.Level > 1 {
		lvl = st.Level
	}
	if m.prof.Level*3 < lvl && !m.prof.AlwaysAggro {
		return false
	}
	return m9Cheb(m.x, m.y, p.sess.playerX, p.sess.playerY) <= m.prof.AggroRange
}

// isNearTarget: melee = adjacent (isAdjacent), ranged = Manhattan <=
// attackRange (character.ts isNearTarget; utils.getDistance is Manhattan).
func (m *m9Mob) isNearTarget(px, py int) bool {
	if m.prof.AttackRange > 1 {
		return m9Manhattan(m.x, m.y, px, py) <= m.prof.AttackRange
	}
	return m9Cheb(m.x, m.y, px, py) <= 1
}

// outsideRoaming: Manhattan distance vs roamDistance (mob.ts outsideRoaming;
// dist<=0 means the mob's own roamDistance).
func (m *m9Mob) outsideRoaming(x, y, dist int) bool {
	if dist <= 0 {
		dist = m.prof.RoamDistance
	}
	return m9Manhattan(x, y, m.spawnX, m.spawnY) > dist
}

func m9Cheb(ax, ay, bx, by int) int {
	dx, dy := ax-bx, ay-by
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	if dx > dy {
		return dx
	}
	return dy
}

func m9Manhattan(ax, ay, bx, by int) int {
	dx, dy := ax-bx, ay-by
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	return dx + dy
}

// m9Tick is the 500ms AI pass over every live mob.
func m9Tick() {
	m9Mu.Lock()
	defer m9Mu.Unlock()
	for _, m := range m9Mobs {
		if m.dead {
			continue
		}
		m.step()
	}
}

// step folds Node's movement-callback leash, combat loop, aggro detection
// and roam interval into one per-tick decision.
func (m *m9Mob) step() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dead {
		return
	}
	now := time.Now()

	// 1. Gone target releases the mob.
	if m.target != "" && connByInstance(m.target) == nil {
		m.clearTargetLocked()
	}

	// 2. Leash (handler.handleMovement).
	if m.target == "" {
		if m.outsideRoaming(m.x, m.y, 0) {
			m.sendToSpawnLocked()
			return
		}
	} else {
		c := connByInstance(m.target)
		if c == nil {
			m.clearTargetLocked()
			return
		}
		if m.outsideRoaming(c.sess.playerX, c.sess.playerY, m.prof.RoamDistance*2) {
			// Multi-attacker retarget omitted (stub scope: sendToSpawn).
			m.clearTargetLocked()
			m.sendToSpawnLocked()
			return
		}
		// Attacker pruning (handler.handleCombatLoop).
		for inst, last := range m.attackers {
			ax, ay, ok := ccPos(inst)
			far := !ok || m9Manhattan(m.x, m.y, ax, ay) > m.prof.RoamDistance*2
			if far || (!ok && now.Sub(last) > m9AttackerTimeout) {
				delete(m.attackers, inst)
			}
		}

		// 3. Combat loop (combat.ts handleLoop): strike in range, else chase.
		px, py := c.sess.playerX, c.sess.playerY
		if m.isNearTarget(px, py) {
			if now.Sub(m.lastAtk) >= time.Duration(m.prof.AttackRate)*time.Millisecond {
				m.lastAtk = now
				m.attackLocked(c)
			}
		} else if now.Sub(m.lastMove) >= m9FollowThrottle {
			m.lastMove = now
			m.chaseLocked(px, py)
		}
		return
	}

	// 4. Aggro scan (handler.detectAggro on position updates; tick-scoped).
	if tgt, ok := m9FindAggro(m); ok && now.Sub(m.lastTgt) >= m9TargetChangeCD {
		m.target = tgt
		m.lastTgt = now
	}

	// 5. Roam (roamingCallback on the ROAM_FREQUENCY interval).
	if now.Sub(m.lastRoam) >= m9RoamInterval() {
		m.lastRoam = now
		m.roamLocked()
	}
}

// roamLocked ports handler.handleRoaming: random point around spawn within
// roamDistance, collision guard, plateau skipped (flat TESTMAP).
func (m *m9Mob) roamLocked() {
	if !*m.prof.Roaming || m.dead {
		return
	}
	newX := m.spawnX + rand.Intn(2*m.prof.RoamDistance+1) - m.prof.RoamDistance
	newY := m.spawnY + rand.Intn(2*m.prof.RoamDistance+1) - m.prof.RoamDistance
	if m9Manhattan(m.spawnX, m.spawnY, newX, newY) > m.prof.RoamDistance {
		return
	}
	if newX == m.x && newY == m.y {
		return
	}
	if blocked(newX, newY) {
		return
	}
	m.moveToLocked(newX, newY)
}

// chaseLocked steps one tile toward the target with axis fallback.
func (m *m9Mob) chaseLocked(px, py int) {
	if m9Cheb(m.x, m.y, px, py) <= 1 && m.prof.AttackRange <= 1 {
		return // adjacent melee: hold position
	}
	dx, dy := 0, 0
	if px > m.x {
		dx = 1
	} else if px < m.x {
		dx = -1
	}
	if py > m.y {
		dy = 1
	} else if py < m.y {
		dy = -1
	}
	nx, ny := m.x+dx, m.y+dy
	if blocked(nx, ny) {
		switch {
		case dx != 0 && !blocked(m.x+dx, m.y):
			nx, ny = m.x+dx, m.y
		case dy != 0 && !blocked(m.x, m.y+dy):
			nx, ny = m.x, m.y+dy
		default:
			return
		}
	}
	m.moveToLocked(nx, ny)
}

// moveToLocked applies + broadcasts a Movement Move.
func (m *m9Mob) moveToLocked(nx, ny int) {
	m.x, m.y = nx, ny
	setEntityPos(m.instance, nx, ny)
	broadcast(pktOp(PacketMovement, MovementMove, serverMovement{
		Instance: m.instance, X: intp(nx), Y: intp(ny),
	}))
}

// sendToSpawnLocked ports mob.sendToSpawn (combat.stop + setPosition).
func (m *m9Mob) sendToSpawnLocked() {
	m.clearTargetLocked()
	m.attackers = map[string]time.Time{}
	if m.x == m.spawnX && m.y == m.spawnY {
		return
	}
	m.moveToLocked(m.spawnX, m.spawnY)
	log.Printf("m9: %s leashed to spawn %d,%d", m.instance, m.spawnX, m.spawnY)
}

func (m *m9Mob) clearTargetLocked() {
	m.target = ""
}

// attackLocked mirrors combat.sendAttack (melee path): Combat Hit broadcast
// + player damage. Damage 0 hits still emit the Hit frame (Node does too).
func (m *m9Mob) attackLocked(c *playerConn) {
	if m.over.NoAttack {
		return // M3 rat demo semantics
	}
	dmg := m.rollDamage(c)
	m9DamagePlayer(c, dmg, m)
	broadcast(pktOp(PacketCombat, CombatHit, combatData{
		Instance: m.instance, Target: c.instance,
		Hit: HitData{Type: HitsNormal, Damage: dmg},
	}))
	log.Printf("m9: %s hits %s dmg=%d hp=%d/%d", m.instance, c.username, dmg, m9PlayerHP(c), m9PlayerMaxHP())
}

// rollDamage ports formulas.getDamage/getMaxDamage for a mob attacker.
func (m *m9Mob) rollDamage(c *playerConn) int {
	p := &m.prof
	ranged := p.AttackRange > 1
	bonus, dmgLevel := p.Bonuses.Strength, p.Skills.Strength
	if ranged {
		bonus, dmgLevel = p.Bonuses.Archery, p.Skills.Archery
	}
	maxDmg := float64(bonus+dmgLevel) * 1.25
	maxDmg *= m9MobDamageMult()

	acc := ModulesMaxAccuracy
	if b := float64(p.Bonuses.Accuracy); b <= 70 {
		acc += 1 - b/70
	}
	acc += float64(ModulesMaxLevel-p.Skills.Accuracy+1) * 0.01

	// Target defense level: the player's defense skill (Node default 1).
	defLvl := 1
	if st := m5StateFor(c.username); st != nil {
		if sk := st.Skills[SkillDefense]; sk != nil && sk.Level > defLvl {
			defLvl = sk.Level
		}
	}
	acc += float64(defLvl) * 0.0175

	// getAccuracyWeight: per-school (attack - defense)/3, positives summed;
	// archers/mages use only their school. Player defense stats are all 0.
	w := 0.0
	if ranged && p.AttackStats.Magic > 0 {
		w = float64(p.AttackStats.Magic) / 3 // isMagic: own weight
	} else if ranged {
		w = float64(p.AttackStats.Archery) / 3
	} else {
		for _, v := range []int{p.AttackStats.Crush, p.AttackStats.Slash, p.AttackStats.Stab, p.AttackStats.Archery, p.AttackStats.Magic} {
			if v > 0 {
				w += float64(v) / 3
			}
		}
	}
	if w < 0 {
		acc += 1.5 // Node: negative weight appends the 1.5 accuracy penalty
	} else {
		acc += -(math.Sqrt(w) / 22.36) + 1
	}
	if acc < 0.7 {
		acc = 0.7 // M3 clamp, kept for playable stub damage
	}
	if acc > 2.0 {
		acc = 2.0
	}

	dmg := int(math.Floor(math.Pow(rand.Float64(), acc) * (maxDmg + 1)))
	if hp := m9PlayerHP(c); dmg > hp {
		dmg = hp
	}
	if dmg < 0 {
		dmg = 0
	}
	return dmg
}

// ---------------------------------------------------------------------------
// Mob hit intake (Node handler.handleHit + handleDeath).
// ---------------------------------------------------------------------------

// m9PlayerHit applies hero damage to a mob: Points, retaliate, death.
func m9PlayerHit(m *m9Mob, attacker *playerConn, dmg int) {
	m.mu.Lock()
	if m.dead {
		m.mu.Unlock()
		return
	}
	m.hp -= dmg
	if m.hp < 0 {
		m.hp = 0
	}
	hp := m.hp
	if attacker != nil {
		m.attackers[attacker.instance] = time.Now()
		// Retaliate (handler.handleHit): idle mobs swing back; busy mobs
		// keep their current target.
		if m.target == "" && !m.over.NoAttack {
			m.target = attacker.instance
			m.lastTgt = time.Now()
		}
	}
	m.mu.Unlock()

	broadcast(pkt(PacketPoints, pointsData{
		Instance: m.instance, HitPoints: intp(hp), MaxHitPoints: intp(m.maxHP),
	}))
	if hp <= 0 {
		m9KillMob(m, attacker)
	}
}

// m9KillMob ports handler.handleDeath: despawn + kill credit (M5 loot) +
// destroy (respawn timer restores full HP at spawn).
func m9KillMob(m *m9Mob, killer *playerConn) {
	m.mu.Lock()
	if m.dead {
		m.mu.Unlock()
		return
	}
	m.dead = true
	m.hp = 0
	m.clearTargetLocked()
	m.mu.Unlock()

	x, y := m.x, m.y
	broadcast(pkt(PacketDespawn, despawnData{Instance: m.instance}))
	log.Printf("m9: %s (%s) died -> respawn in %v", m.instance, m.key, m.respawnDelay())

	if killer != nil {
		m5SpawnLoot(m.key, x, y, killer.username)
	}
	m10KillHooks(m, killer) // M10: chest-area onEmpty (reward chest spawn)
	m11Kill(killer, m.key)  // M11: quest kill stages + achievement progress
	time.AfterFunc(m.respawnDelay(), func() { m9Respawn(m) })
}

// m9Respawn ports handler.handleRespawn: full HP, back at spawn, Spawn frame.
func m9Respawn(m *m9Mob) {
	if m9MobFor(m.instance) != m {
		return // removed while dead
	}
	m.mu.Lock()
	m.dead = false
	m.hp = m.maxHP
	m.x, m.y = m.spawnX, m.spawnY
	m.attackers = map[string]time.Time{}
	payload := m.data()
	delay := m.respawnDelay()
	m.mu.Unlock()
	setEntityPos(m.instance, m.spawnX, m.spawnY)
	broadcast(pkt(PacketSpawn, payload))
	// M10: addMob -> addToChestArea parity on respawn (handler.handleRespawn
	// re-registers the mob with its chest area).
	if area := m10ChestAreaAt(m.spawnX, m.spawnY); area != nil {
		m10AddChestMob(area, m.instance, delay)
	}
	log.Printf("m9: %s respawned full HP=%d", m.instance, m.maxHP)
}

// m9FindAggro scans connected players for the first aggroable one.
func m9FindAggro(m *m9Mob) (string, bool) {
	playersMu.Lock()
	defer playersMu.Unlock()
	for _, c := range players {
		if m.canAggro(c) {
			return c.instance, true
		}
	}
	return "", false
}

// ccPos locates a tracked attacker (ok=false when gone; isNearTarget then
// fails and the prune drops the attacker).
func ccPos(inst string) (int, int, bool) {
	if c := connByInstance(inst); c != nil {
		return c.sess.playerX, c.sess.playerY, true
	}
	return 0, 0, false
}

// ---------------------------------------------------------------------------
// Player HP / death / respawn (character.hitPoints + player.respawn).
// ---------------------------------------------------------------------------

var m9PlayerHPs sync.Map // instance -> remaining HP

func m9PlayerMaxHP() int { return 100 } // stub hero max (divergence note)

func m9PlayerHP(c *playerConn) int {
	if v, ok := m9PlayerHPs.Load(c.instance); ok {
		return v.(int)
	}
	return m9PlayerMaxHP()
}

// m9DamagePlayer applies mob damage: Points frame, Death on empty.
func m9DamagePlayer(c *playerConn, dmg int, from *m9Mob) {
	hp := m9PlayerHP(c) - dmg
	if hp < 0 {
		hp = 0
	}
	m9PlayerHPs.Store(c.instance, hp)
	broadcast(pkt(PacketPoints, pointsData{
		Instance: c.instance, HitPoints: intp(hp), MaxHitPoints: intp(m9PlayerMaxHP()),
	}))
	if hp <= 0 && from != nil {
		// Caller holds from.mu (step -> attackLocked is the only path here);
		// Go mutexes are not reentrant, so re-locking would self-deadlock on
		// every killing blow.
		from.clearTargetLocked()
		broadcast(pkt(PacketDeath, c.instance))
		log.Printf("m9: %s (%s) died to %s", c.instance, c.username, from.instance)
	}
}

// m9HandleRespawn ports incoming.handleRespawn -> player.respawn: only when
// dead; teleport to spawn + Spawn broadcast + Respawn{x,y} + Points sync.
func m9HandleRespawn(c *playerConn) {
	if m9PlayerHP(c) > 0 {
		log.Printf("m9: invalid respawn request from %s", c.username)
		return
	}
	x, y := 100, 96 // getSpawn(): the stub's fixed spawn point
	m9PlayerHPs.Store(c.instance, m9PlayerMaxHP())
	c.sess.playerX, c.sess.playerY = x, y
	setEntityPos(c.instance, x, y)

	broadcast(pkt(PacketTeleport, teleportData{Instance: c.instance, X: x, Y: y}))
	broadcast(pkt(PacketSpawn, welcomePlayer(c.instance)))
	_ = send(c.conn, pkt(PacketRespawn, respawnData{X: x, Y: y}))
	broadcast(pkt(PacketPoints, pointsData{
		Instance: c.instance, HitPoints: intp(m9PlayerMaxHP()), MaxHitPoints: intp(m9PlayerMaxHP()),
	}))
	m8OnPositionUpdate(c)  // respawn position can cross an area boundary
	m10OnPositionUpdate(c) // M10: area callbacks on the respawn tile too
	log.Printf("m9: %s respawned at %d,%d", c.username, x, y)
}

// m9PlayerLeave drops per-player state on disconnect.
func m9PlayerLeave(c *playerConn) {
	m9PlayerHPs.Delete(c.instance)
	m9Mu.Lock()
	for _, m := range m9Mobs {
		m.mu.Lock()
		if m.target == c.instance {
			m.clearTargetLocked()
		}
		delete(m.attackers, c.instance)
		m.mu.Unlock()
	}
	m9Mu.Unlock()
}

// m9Engine boots the AI loop + adopts the demo mobs (main()).
func m9Engine() {
	m9LoadTables()
	m9AdoptExisting()
	go func() {
		t := time.NewTicker(m9RoamTick)
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
	m9Mu.Lock()
	defer m9Mu.Unlock()
	for _, m := range m9Mobs {
		if m.dead {
			continue
		}
		m.mu.Lock()
		if m.target == "" && m.canAggro(c) {
			m.target = c.instance
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
			broadcast(pkt(PacketDespawn, despawnData{Instance: d.Instance}))
			m9Remove(d.Instance)
		}
	case "tp": // reposition the hero server-side (seedPos precedent)
		if c != nil {
			c.sess.playerX, c.sess.playerY = d.X, d.Y
			setEntityPos(c.instance, d.X, d.Y)
			broadcast(pkt(PacketTeleport, teleportData{Instance: c.instance, X: d.X, Y: d.Y}))
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
