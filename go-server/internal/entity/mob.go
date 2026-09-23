// Package entity (mob engine) holds the M9 generic mob AI engine state
// machine, extracted behavior-frozen from the root m9.go adapter.
//
// Faithful port of the Node mob stack (see the root adapter for the full
// TS-source tour): mob.ts data load + canAggro + outsideRoaming +
// sendToSpawn + respawn, handler.ts leash + retaliate + attacker pruning +
// death + respawn, combat.ts attack loop, character/player/incoming player
// HP + Death + Respawn, formulas.ts mob damage.
//
// Everything transport/world related stays with the root adapter and is
// reached only through the World seam below (root helpers in parentheses):
//
//	Players/PlayerPos -> players map + connByInstance (aggro scan + leash)
//	Blocked           -> blocked (roam/chase collision guard)
//	SetEntityPos      -> setEntityPos (position registry)
//	Despawn           -> S->C Despawn [13] broadcast
//	MoveMob           -> setEntityPos + S->C Movement Move [11,4] broadcast
//	SpawnMobFrame     -> setEntityPos + S->C Spawn [5] broadcast (Mob.serialize)
//	MobPoints         -> S->C Points [8] broadcast for a mob
//	StrikeMob         -> S->C Combat Hit [7,1] broadcast (mob swing)
//	Get/Set/ForgetHeroHP -> m9PlayerHPs hero HP store (stays in root)
//	HeroPoints        -> S->C Points [8] broadcast for a hero
//	HeroDied          -> status clear + S->C Despawn [13] broadcast + pet
//	                      despawn + persist flush + S->C Death [29] unicast
//	                      to self, exactly-once per life (mob killing blow;
//	                      player handleDeath parity — the root adapter owns
//	                      the transport; other mobs release the corpse via
//	                      the gone-target path, cleanCombat outcome)
//	TeleportHero      -> S->C Teleport broadcast (hero respawn)
//	SpawnHero         -> S->C Spawn broadcast (welcomePlayer, hero respawn)
//	HeroRespawned     -> S->C Respawn frame to self (hero respawn)
//	AfterDelay        -> time.AfterFunc (respawn timers)
//	ApplyPoison       -> abApplyPoison (poisonous attackers)
//	SpawnLoot         -> m5SpawnLoot (kill credit, via ChestLoot)
//	QuestKill         -> m11Kill (quest/achievement kill credit, via QuestSink)
//
// Mob state itself stays in the root adapter (m13.go, m5.go and
// abilities_wire.go touch m9Mob fields directly and must compile
// untouched): the engine operates on the Mob interface below, which the
// root m9Mob implements. Locking mirrors the original: every orchestration
// entry takes Mob.Lock (the old m.mu) and the accessors assume it is held,
// exactly like the old step()/attackLocked() paths (Go mutexes are not
// reentrant, so DamageHero must NOT relock a non-nil `from` — the killing
// blow path calls it with the attacker's lock held).
//
// Packet shapes and cadences are frozen: this package never builds frames
// (no pkt/pktOp) and never sends. Tick cadence (500ms RoamTick), respawn
// delays, respawn timers and the M9_ROAMMS / M9_MOBDMG debug env knobs are
// unchanged.
package entity

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"strconv"
	"time"
)

// Modules.MobDefaults (modules.ts:690).
const (
	// AggroRange is the default aggroRange (AGGRO_RANGE).
	AggroRange = 2
	// RespawnDelay is the default respawn delay (RESPAWN_DELAY 60s).
	RespawnDelay = 60 * time.Second
	// RoamDistance is the default roamDistance (ROAM_DISTANCE).
	RoamDistance = 7
	// RoamFrequency is the default roam interval (ROAM_FREQUENCY 17s).
	RoamFrequency = 17 * time.Second
)

// Modules.Constants / Defaults.
const (
	// AttackerTimeout is Constants.ATTACKER_TIMEOUT (20s).
	AttackerTimeout = 20 * time.Second
	// TargetChangeCD is Mob.canChangeTarget (13s).
	TargetChangeCD = 13 * time.Second
	// FollowThrottle is the combat.ts follow spam guard (500ms).
	FollowThrottle = 500 * time.Millisecond
	// RoamTick is the AI tick cadence (500ms).
	RoamTick = 500 * time.Millisecond
	// DefaultAttackRate is Defaults.ATTACK_RATE.
	DefaultAttackRate = 1000
	// DefaultMoveSpeed is Defaults.MOVEMENT_SPEED.
	DefaultMoveSpeed = 220
)

// HeroMaxHP is the stub hero max HP (the Node getMaxHitPoints shape stays
// M5-owned; see the root adapter divergence note).
const HeroMaxHP = 100

// HeroSpawnX/HeroSpawnY is getSpawn(): the stub's fixed spawn point.
const (
	HeroSpawnX = 100
	HeroSpawnY = 96
)

// Modules mirrors for the damage roll (main.go:930-931).
const (
	// MaxAccuracy mirrors ModulesMaxAccuracy (0.45).
	MaxAccuracy = 0.45
	// MaxLevel mirrors ModulesMaxLevel (120).
	MaxLevel = 120
)

// DescText is a mobs.json description: a plain string, or (eye only) a
// string[] from which Node picks one at random (mob.getDescription).
type DescText []string

// UnmarshalJSON accepts either a string or an array of strings.
func (d *DescText) UnmarshalJSON(raw []byte) error {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		*d = DescText{s}
		return nil
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err != nil {
		return err
	}
	*d = DescText(arr)
	return nil
}

// Pick returns one description (uniform, mob.getDescription parity).
// ok=false when the profile carries no description.
func (d DescText) Pick() (string, bool) {
	if len(d) == 0 {
		return "", false
	}
	return d[rand.Intn(len(d))], true
}

// MobProfile mirrors the mobs.json entry shape Node Mob.loadData consumes.
// JSON tags are identical to the old root m9MobProfile (file shape frozen).
type MobProfile struct {
	Name          string     `json:"name"`
	Description   DescText   `json:"description"`
	Level         int        `json:"level"`
	HitPoints     int        `json:"hitPoints"`
	AggroRange    int        `json:"aggroRange"`
	AttackRange   int        `json:"attackRange"`
	AttackRate    int        `json:"attackRate"`
	MovementSpeed int        `json:"movementSpeed"`
	RespawnDelay  int        `json:"respawnDelay"`
	RoamDistance  int        `json:"roamDistance"`
	Roaming       *bool      `json:"roaming"`
	Aggressive    bool       `json:"aggressive"`
	AlwaysAggro   bool       `json:"alwaysAggressive"`
	Poisonous     bool       `json:"poisonous"`
	Boss          bool       `json:"boss"`
	Miniboss      bool       `json:"miniboss"`
	Drops         []DropJSON `json:"drops"`
	DropTables    []string   `json:"dropTables"`
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

// SpawnOverride mirrors a spawns.json value (per-instance MobData).
type SpawnOverride MobProfile

// MobOverrides carries per-instance demo/test knobs layered on the profile.
type MobOverrides struct {
	NoAttack bool // M3 rat demo: chase+leash but never strike
	Chase    bool // M3 demo: chase regardless of `aggressive`
	Aggro    int  // override aggroRange (0 = profile)
	Leash    int  // override roamDistance (0 = profile)
	Respawn  time.Duration
}

// LoadProfiles unmarshals mobs.json + spawns.json payloads (the file reads
// via resourceDataPath stay in the root adapter).
func LoadProfiles(mobsJSON, spawnsJSON []byte) (map[string]*MobProfile, map[string]*SpawnOverride, error) {
	var profs map[string]*MobProfile
	if err := json.Unmarshal(mobsJSON, &profs); err != nil {
		return nil, nil, fmt.Errorf("parse mobs.json: %w", err)
	}
	var spawns map[string]*SpawnOverride
	if err := json.Unmarshal(spawnsJSON, &spawns); err != nil {
		return nil, nil, fmt.Errorf("parse spawns.json: %w", err)
	}
	return profs, spawns, nil
}

// ProfileFor merges profs[key] with spawns["x-y"] overrides (mob.ts
// loadSpawns keying). Returns nil when the key is unknown/invalid.
func ProfileFor(profs map[string]*MobProfile, spawns map[string]*SpawnOverride, key string, x, y int) *MobProfile {
	base, ok := profs[key]
	if !ok {
		return nil
	}
	p := *base
	if ov, ok := spawns[fmt.Sprintf("%d-%d", x, y)]; ok {
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

// ApplyDefaults fills Node defaults for zero-valued profile fields
// (mob.ts field defaults + MobDefaults).
func ApplyDefaults(p *MobProfile) {
	if p.Level <= 0 {
		p.Level = 1
	}
	if p.AggroRange <= 0 {
		p.AggroRange = AggroRange
	}
	if p.AttackRange <= 0 {
		p.AttackRange = 1
	}
	if p.AttackRate <= 0 {
		p.AttackRate = DefaultAttackRate
	}
	if p.MovementSpeed <= 0 {
		p.MovementSpeed = DefaultMoveSpeed
	}
	if p.RespawnDelay <= 0 {
		p.RespawnDelay = int(RespawnDelay / time.Millisecond)
	}
	if p.RoamDistance <= 0 {
		p.RoamDistance = RoamDistance
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

// RoamInterval honors M9_ROAMMS (debug override for fast e2e; default is
// MobDefaults.ROAM_FREQUENCY 17s).
func RoamInterval() time.Duration {
	if v := os.Getenv("M9_ROAMMS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return RoamFrequency
}

// MobDamageMult multiplies mob damage when M9_MOBDMG is set (debug).
func MobDamageMult() float64 {
	if v := os.Getenv("M9_MOBDMG"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return f
		}
	}
	return 1
}

// RespawnDelayFor mirrors Mob.respawn: override > profile > MobDefaults.
func RespawnDelayFor(o MobOverrides, p MobProfile) time.Duration {
	if o.Respawn > 0 {
		return o.Respawn
	}
	return time.Duration(p.RespawnDelay) * time.Millisecond
}

// Cheb is the Chebyshev distance (Node isNear).
func Cheb(ax, ay, bx, by int) int {
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

// Manhattan is utils.getDistance (Manhattan).
func Manhattan(ax, ay, bx, by int) int {
	dx, dy := ax-bx, ay-by
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	return dx + dy
}

// PlayerView is the per-player snapshot the engine needs (aggro scan,
// leash, damage). The root adapter builds it from the players map +
// m5 state; Level/Defense are the raw m5 values (0 when unknown) and the
// Node fallback gates (>1) live in the predicates below. Plateau is the
// hero's tracked plateauLevel (handler.ts:333 parity) for the cross-plateau
// combat gate.
type PlayerView struct {
	Instance string
	Username string
	X, Y     int
	Level    int // raw m5 level (0 = unknown)
	Defense  int // raw m5 defense skill (0 = unknown)
	Plateau  int // tracked plateauLevel (0 = default/unset)
}

// Mob is the live mob state, implemented by the root m9Mob (the single
// live state — m13.go, m5.go and abilities_wire.go touch its fields
// directly). All accessors assume Mob.Lock is held unless noted.
type Mob interface {
	Lock()
	Unlock()
	Instance() string
	MobKey() string
	Profile() MobProfile
	Overrides() MobOverrides
	SpawnPos() (x, y int)
	Pos() (x, y int)
	SetPos(x, y int)
	HP() int
	MaxHP() int
	SetHP(hp int)
	Dead() bool
	SetDead(dead bool)
	Target() string
	SetTarget(t string)
	LastAtk() time.Time
	SetLastAtk(t time.Time)
	LastMove() time.Time
	SetLastMove(t time.Time)
	LastRoam() time.Time
	SetLastRoam(t time.Time)
	LastTgt() time.Time
	SetLastTgt(t time.Time)
	TouchAttacker(inst string, now time.Time)
	DropAttacker(inst string)
	Attackers() map[string]time.Time // copy
	ClearAttackers()
	// Plateau is the mob's bound plateau level, set at spawn from the
	// spawn tile (mob.ts:148) and never changed afterwards (respawns
	// return to spawn, so the level is stable).
	Plateau() int
}

// MobSpawn is the Spawn-frame descriptor for a mob (Mob.serialize:
// hitPoints/maxHitPoints/attackRange/level). The root adapter renders the
// EntityData frame from it (packet shape frozen in root).
type MobSpawn struct {
	Instance               string
	Key, Name              string
	X, Y                   int
	Level, HP, MaxHP       int
	MoveSpeed, AttackRange int
}

// MobWorld is the mob-engine half of the World seam (root helpers in
// parentheses): players (players map), PlayerPos (connByInstance),
// Blocked (blocked), SetEntityPos (setEntityPos), Despawn/MoveMob/
// SpawnMobFrame/MobPoints/StrikeMob/HeroPoints/HeroDied/TeleportHero/
// SpawnHero/HeroRespawned (broadcast/send frame builders),
// Get/Set/ForgetHeroHP (m9PlayerHPs store), AfterDelay (time.AfterFunc),
// ApplyPoison (abApplyPoison), PlateauLevel (map.getPlateauLevel).
type MobWorld interface {
	Players() []PlayerView
	PlayerPos(instance string) (x, y int, ok bool)
	Blocked(x, y int) bool
	// PlateauLevel is map.getPlateauLevel (map.ts): the plateau level at
	// (x,y), 0 when the tile carries none. Serves the roam-step gate.
	PlateauLevel(x, y int) int
	SetEntityPos(instance string, x, y int)
	Despawn(instance string)
	MoveMob(instance string, x, y int)
	SpawnMobFrame(s MobSpawn)
	MobPoints(instance string, hp, maxHP int)
	StrikeMob(attacker, target string, dmg int)
	GetHeroHP(instance string) int
	SetHeroHP(instance string, hp int)
	ForgetHeroHP(instance string)
	HeroPoints(instance string, hp, maxHP int)
	// HeroDied is the player-death funnel (player handleDeath parity),
	// exactly-once per life (TS character.hit dead-guard parity): the root
	// adapter clears the victim's live status entries, broadcasts the
	// Despawn frame, despawns the owner's pet (no-op without one), flushes
	// the victim's persist row synchronously, and unicasts Death to the
	// victim only. Attacker release: the killer's target already cleared
	// in DamageHero; other mobs release the corpse on their next tick via
	// the existing gone-target path (corpses leave the Players scan set) —
	// the world.ts cleanCombat outcome with no new locks (HeroDied runs on
	// the tick holding m9Mu + the killer's lock, so it takes neither).
	// PvP accounting and damageTable/skills/combat stops have no Go
	// counterparts and stay omitted.
	HeroDied(playerInstance, username, mobInstance string)
	TeleportHero(instance string, x, y int)
	SpawnHero(instance string)
	HeroRespawned(instance string, x, y int)
	AfterDelay(d time.Duration, fn func())
	ApplyPoison(instance string)
}

// ChestLoot is the loot half of the World seam (m5SpawnLoot for kill
// credit; m5NearWalkable + m5RegisterLoot + the persistent Item Spawn
// frame for chest-area drops).
type ChestLoot interface {
	SpawnLoot(mobKey string, x, y int, owner string)
	NearWalkable(x, y int) (lx, ly int)
	RegisterLoot(inst, key string, count, x, y int, owner string)
	SpawnLootItem(i LootItem)
}

// LootItem is the Spawn-frame descriptor for a persistent chest drop
// (EntityData Type Item2, full Count, no owner/blink).
type LootItem struct {
	Instance string
	Key      string
	Count    int
	X, Y     int
}

// QuestSink is the quest half of the World seam (m11Kill; the root
// adapter resolves the killer conn, nil-safe like the original).
type QuestSink interface {
	QuestKill(killerInstance, mobKey string)
}

// GameWorld is the full World seam for the mob + area engines (both live
// in this one package, so the old m9<->m10 call cycle cannot recur).
type GameWorld interface {
	MobWorld
	ChestLoot
	QuestSink
	AreaWorld
}

// FindPlayer locates a player view by instance.
func FindPlayer(players []PlayerView, instance string) (PlayerView, bool) {
	for _, v := range players {
		if v.Instance == instance {
			return v, true
		}
	}
	return PlayerView{}, false
}

// CanAggro ports Mob.canAggro: existing-target gate, aggressive flags
// (+ the M3 demo Chase override), level*3 gate (unless alwaysAggressive),
// isNear (Chebyshev) range check.
func CanAggro(p MobProfile, o MobOverrides, mx, my int, curTarget string, v PlayerView) bool {
	if curTarget != "" {
		return false
	}
	if !p.Aggressive && !p.AlwaysAggro && !o.Chase {
		return false
	}
	lvl := 1
	if v.Level > 1 {
		lvl = v.Level
	}
	if p.Level*3 < lvl && !p.AlwaysAggro {
		return false
	}
	return Cheb(mx, my, v.X, v.Y) <= p.AggroRange
}

// FindAggro scans players for the first aggroable one. Call with the mob
// lock held (reads profile/pos/target).
func FindAggro(m Mob, players []PlayerView) (string, bool) {
	p := m.Profile()
	o := m.Overrides()
	mx, my := m.Pos()
	cur := m.Target()
	for _, v := range players {
		if CanAggro(p, o, mx, my, cur, v) {
			return v.Instance, true
		}
	}
	return "", false
}

// NearTarget ports the melee/ranged proximity check: melee = adjacent
// (isAdjacent), ranged = Manhattan <= attackRange (character.ts
// isNearTarget; utils.getDistance is Manhattan).
func NearTarget(p MobProfile, mx, my, px, py int) bool {
	if p.AttackRange > 1 {
		return Manhattan(mx, my, px, py) <= p.AttackRange
	}
	return Cheb(mx, my, px, py) <= 1
}

// OutsideRoaming ports mob.ts outsideRoaming: Manhattan distance vs
// roamDistance (dist<=0 means the mob's own roamDistance).
func OutsideRoaming(sx, sy, roamDist, x, y, dist int) bool {
	if dist <= 0 {
		dist = roamDist
	}
	return Manhattan(x, y, sx, sy) > dist
}

// PickRoam draws one roam candidate around the spawn within roamDistance.
// ok=false means the draw fell outside the radius (no retry — the tick
// tries again on the next roam interval, like the original).
func PickRoam(sx, sy, roamDist int) (nx, ny int, ok bool) {
	nx = sx + rand.Intn(2*roamDist+1) - roamDist
	ny = sy + rand.Intn(2*roamDist+1) - roamDist
	if Manhattan(sx, sy, nx, ny) > roamDist {
		return 0, 0, false
	}
	return nx, ny, true
}

// ChaseStep steps one tile toward the target with axis fallback.
// ok=false means hold position (adjacent melee or fully blocked).
// blocked is w.Blocked in production and a stub in tests.
func ChaseStep(mx, my, px, py, attackRange int, blocked func(x, y int) bool) (nx, ny int, ok bool) {
	if Cheb(mx, my, px, py) <= 1 && attackRange <= 1 {
		return 0, 0, false // adjacent melee: hold position
	}
	dx, dy := 0, 0
	if px > mx {
		dx = 1
	} else if px < mx {
		dx = -1
	}
	if py > my {
		dy = 1
	} else if py < my {
		dy = -1
	}
	nx, ny = mx+dx, my+dy
	if blocked(nx, ny) {
		switch {
		case dx != 0 && !blocked(mx+dx, my):
			nx, ny = mx+dx, my
		case dy != 0 && !blocked(mx, my+dy):
			nx, ny = mx, my+dy
		default:
			return 0, 0, false
		}
	}
	return nx, ny, true
}

// RollMobDamage ports formulas.getDamage/getMaxDamage for a mob attacker.
// defenseLevel is the victim's defense skill (Node default 1); mult is the
// M9_MOBDMG debug multiplier. The result is NOT clamped to remaining HP
// (callers clamp, like the original).
func RollMobDamage(p MobProfile, defenseLevel int, mult float64) int {
	ranged := p.AttackRange > 1
	bonus, dmgLevel := p.Bonuses.Strength, p.Skills.Strength
	if ranged {
		bonus, dmgLevel = p.Bonuses.Archery, p.Skills.Archery
	}
	maxDmg := float64(bonus+dmgLevel) * 1.25
	maxDmg *= mult

	acc := MaxAccuracy
	if b := float64(p.Bonuses.Accuracy); b <= 70 {
		acc += 1 - b/70
	}
	acc += float64(MaxLevel-p.Skills.Accuracy+1) * 0.01

	defLvl := 1
	if defenseLevel > defLvl {
		defLvl = defenseLevel
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

	return int(math.Floor(math.Pow(rand.Float64(), acc) * (maxDmg + 1)))
}

// StepMob is the per-mob 500ms AI pass. It folds Node's movement-callback
// leash, combat loop, aggro detection and roam interval into one per-tick
// decision (same order and gates as the old m9Mob.step).
func StepMob(m Mob, w GameWorld, now time.Time) {
	m.Lock()
	defer m.Unlock()
	if m.Dead() {
		return
	}

	// 1. Gone target releases the mob.
	if m.Target() != "" {
		if _, _, ok := w.PlayerPos(m.Target()); !ok {
			m.SetTarget("")
		}
	}

	// 2. Leash (handler.handleMovement).
	if m.Target() == "" {
		mx, my := m.Pos()
		sx, sy := m.SpawnPos()
		if OutsideRoaming(sx, sy, m.Profile().RoamDistance, mx, my, 0) {
			sendToSpawn(m, w)
			return
		}
	} else {
		viewer, ok := FindPlayer(w.Players(), m.Target())
		if !ok {
			m.SetTarget("")
			return
		}
		p := m.Profile()
		sx, sy := m.SpawnPos()
		if OutsideRoaming(sx, sy, p.RoamDistance, viewer.X, viewer.Y, p.RoamDistance*2) {
			// Multi-attacker retarget omitted (stub scope: sendToSpawn).
			m.SetTarget("")
			sendToSpawn(m, w)
			return
		}
		mx, my := m.Pos()
		// Attacker pruning (handler.handleCombatLoop).
		for inst, last := range m.Attackers() {
			ax, ay, ok := w.PlayerPos(inst)
			far := !ok || Manhattan(mx, my, ax, ay) > p.RoamDistance*2
			if far || (!ok && now.Sub(last) > AttackerTimeout) {
				m.DropAttacker(inst)
			}
		}

		// 3. Combat loop (combat.ts handleLoop): strike in range, else chase.
		if NearTarget(p, mx, my, viewer.X, viewer.Y) {
			if now.Sub(m.LastAtk()) >= time.Duration(p.AttackRate)*time.Millisecond {
				m.SetLastAtk(now)
				strikeMob(m, p, viewer, w)
			}
		} else if now.Sub(m.LastMove()) >= FollowThrottle {
			m.SetLastMove(now)
			if nx, ny, ok := ChaseStep(mx, my, viewer.X, viewer.Y, p.AttackRange, w.Blocked); ok {
				m.SetPos(nx, ny)
				w.MoveMob(m.Instance(), nx, ny)
			}
		}
		return
	}

	// 4. Aggro scan (handler.detectAggro on position updates; tick-scoped).
	if tgt, ok := FindAggro(m, w.Players()); ok && now.Sub(m.LastTgt()) >= TargetChangeCD {
		m.SetTarget(tgt)
		m.SetLastTgt(now)
	}

	// 5. Roam (roamingCallback on the ROAM_FREQUENCY interval).
	if now.Sub(m.LastRoam()) >= RoamInterval() {
		m.SetLastRoam(now)
		roamMob(m, w)
	}
}

// sendToSpawn ports mob.sendToSpawn (combat.stop + setPosition).
// Call with the mob lock held.
func sendToSpawn(m Mob, w GameWorld) {
	m.SetTarget("")
	m.ClearAttackers()
	mx, my := m.Pos()
	sx, sy := m.SpawnPos()
	if mx == sx && my == sy {
		return
	}
	m.SetPos(sx, sy)
	w.MoveMob(m.Instance(), sx, sy)
	log.Printf("m9: %s leashed to spawn %d,%d", m.Instance(), sx, sy)
}

// roamMob ports handler.handleRoaming: random point around spawn within
// roamDistance, plateau gate, collision guard. A mob is bound to its spawn
// plateau level and cannot roam onto a different one (mob/handler.ts:184);
// only roam STEPS are gated, never spawn positions (showcase/chase legs are
// unaffected — a refused draw simply retries on the next roam interval).
// Call with the mob lock held.
func roamMob(m Mob, w GameWorld) {
	p := m.Profile()
	if !*p.Roaming || m.Dead() {
		return
	}
	sx, sy := m.SpawnPos()
	mx, my := m.Pos()
	nx, ny, ok := PickRoam(sx, sy, p.RoamDistance)
	if !ok {
		return
	}
	if nx == mx && ny == my {
		return
	}
	if m.Plateau() != w.PlateauLevel(nx, ny) {
		return
	}
	if w.Blocked(nx, ny) {
		return
	}
	m.SetPos(nx, ny)
	w.MoveMob(m.Instance(), nx, ny)
}

// PlateauCombatBlocked ports the cross-plateau combat refusal: combat never
// crosses plateau levels (silent no-swing, TS combat-loop parity).
//
// TS-parity note: the exact Node rule is narrower — character.ts
// isNearTarget gates only RANGED attacks (attacker.plateauLevel >=
// target.plateauLevel, so higher-or-equal may snipe down) while melee
// adjacency is ungated, and combat.ts:292 is the shouldTeleportNearby
// (stuck-mob teleport) guard, not a combat-start gate. The Go engine has no
// hero range model and no combat loop (single-swing dispatch both ways), so
// both swings take the conservative symmetric gate: any plateau difference
// refuses the swing. No-op on flat maps (all e2e legs run on plateau 0).
func PlateauCombatBlocked(attackerPlateau, targetPlateau int) bool {
	return attackerPlateau != targetPlateau
}

// strikeMob mirrors combat.sendAttack (melee path): player damage + Combat
// Hit broadcast. Damage 0 hits still emit the Hit frame (Node does too).
// Cross-plateau swings are refused silently (PlateauCombatBlocked parity).
// Call with the mob lock held.
func strikeMob(m Mob, p MobProfile, viewer PlayerView, w GameWorld) {
	if m.Overrides().NoAttack {
		return // M3 rat demo semantics
	}
	if PlateauCombatBlocked(m.Plateau(), viewer.Plateau) {
		return
	}
	defLvl := 1
	if viewer.Defense > defLvl {
		defLvl = viewer.Defense
	}
	dmg := RollMobDamage(p, defLvl, MobDamageMult())
	if hp := w.GetHeroHP(viewer.Instance); dmg > hp {
		dmg = hp
	}
	if dmg < 0 {
		dmg = 0
	}
	DamageHero(w, viewer.Instance, viewer.Username, dmg, m)
	// TS character.ts handlePoisonDamage: a poisonous attacker poisons the
	// victim (setPoison Venom default).
	if p.Poisonous {
		w.ApplyPoison(viewer.Instance)
	}
	w.StrikeMob(m.Instance(), viewer.Instance, dmg)
	log.Printf("m9: %s hits %s dmg=%d hp=%d/%d", m.Instance(), viewer.Username, dmg, w.GetHeroHP(viewer.Instance), HeroMaxHP)
}

// HitMob applies hero damage to a mob: Points, retaliate, death
// (Node handler.handleHit + handleDeath). attacker is nil for
// non-player damage (DoTs, admin commands). alive reports whether the
// mob is still registered (the old m9MobFor check); the respawn timer
// only fires when it does.
func HitMob(m Mob, attacker *PlayerView, dmg int, w GameWorld, now time.Time, alive func() bool) {
	m.Lock()
	if m.Dead() {
		m.Unlock()
		return
	}
	hp := m.HP() - dmg
	if hp < 0 {
		hp = 0
	}
	m.SetHP(hp)
	if attacker != nil {
		m.TouchAttacker(attacker.Instance, now)
		// Retaliate (handler.handleHit): idle mobs swing back; busy mobs
		// keep their current target.
		if m.Target() == "" && !m.Overrides().NoAttack {
			m.SetTarget(attacker.Instance)
			m.SetLastTgt(now)
		}
	}
	maxHP := m.MaxHP()
	inst := m.Instance()
	m.Unlock()

	w.MobPoints(inst, hp, maxHP)
	if hp <= 0 {
		KillMob(m, attacker, w, alive)
	}
}

// KillMob ports handler.handleDeath: despawn + kill credit (M5 loot) +
// chest-area onEmpty + M11 quest credit + destroy (respawn timer restores
// full HP at spawn). killer is nil for non-player kills.
func KillMob(m Mob, killer *PlayerView, w GameWorld, alive func() bool) {
	m.Lock()
	if m.Dead() {
		m.Unlock()
		return
	}
	m.SetDead(true)
	m.SetHP(0)
	m.SetTarget("")
	mx, my := m.Pos()
	inst := m.Instance()
	key := m.MobKey()
	p := m.Profile()
	o := m.Overrides()
	m.Unlock()

	w.Despawn(inst)
	delay := RespawnDelayFor(o, p)
	log.Printf("m9: %s (%s) died -> respawn in %v", inst, key, delay)

	hasKiller := killer != nil
	if hasKiller {
		w.SpawnLoot(key, mx, my, killer.Username)
	}
	killerInstance := ""
	if hasKiller {
		killerInstance = killer.Instance
	}
	KillHookForMob(mx, my, inst, killerInstance, w) // chest-area onEmpty (reward chest spawn)
	if hasKiller {
		w.QuestKill(killer.Instance, key) // quest kill stages + achievements
	}
	w.AfterDelay(delay, func() {
		if alive == nil || alive() {
			RespawnMob(m, w)
		}
	})
}

// RespawnMob ports handler.handleRespawn: full HP, back at spawn, Spawn
// frame, plus the chest-area re-registration (the next mob spawn removes
// any unlooted chest). The alive (still registered) check is the caller's.
func RespawnMob(m Mob, w GameWorld) {
	m.Lock()
	m.SetDead(false)
	m.SetHP(m.MaxHP())
	sx, sy := m.SpawnPos()
	m.SetPos(sx, sy)
	m.ClearAttackers()
	inst := m.Instance()
	p := m.Profile()
	s := MobSpawn{
		Instance: inst, Key: m.MobKey(), Name: p.Name, X: sx, Y: sy,
		Level: p.Level, HP: m.HP(), MaxHP: m.MaxHP(),
		MoveSpeed: p.MovementSpeed, AttackRange: p.AttackRange,
	}
	delay := RespawnDelayFor(m.Overrides(), p)
	m.Unlock()
	w.SetEntityPos(inst, sx, sy)
	w.SpawnMobFrame(s)
	// addMob -> addToChestArea parity on respawn (handler.handleRespawn
	// re-registers the mob with its chest area).
	if area := ChestAreaAt(sx, sy); area != nil {
		AddChestMob(area, inst, delay, w)
	}
}

// DamageHero applies mob damage: Points frame, then the HeroDied funnel on
// empty (character.hitPoints). from is nil for non-mob damage (admin/DoT —
// no Death frame, like the original); a non-nil from must be lock-held by
// the caller (the step -> strike path is the only such caller; Go
// mutexes are not reentrant, so relocking would self-deadlock on every
// killing blow). The killer's target is released here; everything else
// (status clear, Despawn, pet despawn, save, Death unicast) is HeroDied's,
// owned by the root adapter.
func DamageHero(w GameWorld, instance, username string, dmg int, from Mob) {
	hp := w.GetHeroHP(instance) - dmg
	if hp < 0 {
		hp = 0
	}
	w.SetHeroHP(instance, hp)
	w.HeroPoints(instance, hp, HeroMaxHP)
	if hp <= 0 && from != nil {
		// Caller holds from's lock (see above); clear without relocking.
		from.SetTarget("")
		w.HeroDied(instance, username, from.Instance())
		log.Printf("m9: %s (%s) died to %s", instance, username, from.Instance())
	}
}

// RespawnHero ports incoming.handleRespawn -> player.respawn: only when
// dead; the root adapter sets the session position, then this resets HP
// and emits Teleport + Spawn + Respawn + Points. Reports false when the
// hero is not dead ("Invalid respawn request." guard).
func RespawnHero(w GameWorld, instance string) bool {
	if w.GetHeroHP(instance) > 0 {
		return false
	}
	w.SetHeroHP(instance, HeroMaxHP)
	w.SetEntityPos(instance, HeroSpawnX, HeroSpawnY)
	w.TeleportHero(instance, HeroSpawnX, HeroSpawnY)
	w.SpawnHero(instance)
	w.HeroRespawned(instance, HeroSpawnX, HeroSpawnY)
	w.HeroPoints(instance, HeroMaxHP, HeroMaxHP)
	return true
}
