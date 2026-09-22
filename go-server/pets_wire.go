// Pet companion wiring (thin root adapter over internal/entity).
//
// TS sources (all read-only recon, no new opcodes):
//   - packages/server/src/game/entity/character/pet/pet.ts — Pet extends
//     Character, spawns at the owner's (x, y); serialize() is PetData
//     (types/pet.d.ts: EntityData + `owner` + `movementSpeed`).
//   - packages/server/src/controllers/entities.ts spawnPet/removePet —
//     spawnPet(owner, key) + addPet; removePet dispatches remove + deletes
//     the pets dict entry.
//   - packages/server/src/game/entity/character/player/player.ts — setPet
//     (spawnPet + follow, ALREADY_HAVE_PET guard), removePet (needs inventory
//     space, returns a "<mobkey>pet" item, NO_SPACE_PET otherwise), hasPet.
//   - packages/server/src/game/entity/character/player/handler.ts —
//     handleMovement: pet follows when Manhattan distance > 2 (follow packet);
//     distance > 10 -> removePet + spawnPet at the player + follow.
//     handleDrop: item.isPetItem() -> setPet(item.pet) instead of a world item.
//   - packages/server/src/game/entity/character/character.ts — follow()
//     sends Movement Follow [11,5,{instance,target}] to regions; teleport()
//     repositions + sends Teleport [12,{instance,x,y}].
//   - packages/server/src/game/entity/character/player/incoming.ts —
//     handlePet: C->S Pet [58,{opcode}] Pickup(0) -> removePet (pickingUpPet
//     spam guard).
//   - packages/server/data/items.json — pet items carry a "pet" mob key
//     (ratpet->rat, rathatpet->rathat, ratballoonpet->ratballoon).
//   - packages/server/data/quests/royalpet.json — the final stage grants a
//     "catpet" inventory ITEM (not a direct pet); the pet itself comes from
//     dropping/using that item (handleDrop parity below), so m11 needs no hook.
//
// Frames used (all pre-existing shapes, verified against the TS tree):
//
//	S->C Spawn [5, PetData] with type 7 (EntityType.Pet; protocol has no
//	  EntityPet constant so the literal 7 is used here). NOTE: there is no
//	  S->C Pet [58] spawn — packet 58 is C->S only (Pickup); pets ride the
//	  generic Spawn like every other entity (client entities.ts createPet
//	  reads info.owner + info.movementSpeed, so the payload carries `owner`,
//	  not just EntityData.ownerInstance).
//	S->C Movement Move [11,4,{instance,x,y}] per follow step (m9 moveToLocked
//	  precedent) + Movement Follow [11,5,{instance,target}] (character.follow).
//	S->C Despawn [13,{instance}] + Spawn [5, PetData] at the owner on
//	  teleport (handler.ts removePet+spawnPet parity).
//	S->C Teleport is NOT used for pets (handler.ts respawns instead).
//	C->S Pet [58,{opcode}] Pickup(0) -> inventory return + Despawn.
//	C->S Container Remove (drop) of a pet item -> setPet parity.
//	S->C Container Add on pickup return; S->C notify misc:ALREADY_HAVE_PET /
//	  misc:NO_SPACE_PET.
//
// Divergences from TS (documented):
//   - Teleport threshold: TS respawns at distance > 10; internal/pets uses
//     > 12 per its spec (pets walk a little farther before teleport).
//   - Follow stepping is server-side one-tile FollowStep toward the owner
//     (TS pets move via client pathing on the Follow packet; no server grid
//     step exists there). Pets never consult blocked() and never register in
//     resourceEntities, so they can never block movement (server isColliding
//     only checks tiles + resource occupants).
//   - Teleport reuses the SAME pet instance (TS spawnPet mints a fresh
//     "<type>-<n>" instance on every respawn); stable identity keeps Who/List
//     tracking trivial for the harness.
//   - Attack-mirror is new (TS pets never attack): each owner swing at a
//     killable mob deals a small fixed pet hit through the existing combat
//     pipeline (Animation + Combat Hit + m9PlayerHit / applyBossHitLocked),
//     credited to the owner (retaliate/loot/quest flow unchanged).
//   - Hunger/expiry (internal/pets IsHungry/IsExpired) have no TS source and
//     are NOT enforced here (documented-skip); born/fed timestamps are kept
//     so the state probe can report the predicates.
//
// Layout: the stateful registry and follow/teleport + attack-mirror
// orchestration live in internal/entity (imported, not duplicated); this file
// keeps the World implementation over existing globals (setEntityPos,
// broadcast, Movement/Spawn frames, m5 state, combat pipeline) plus thin
// wrappers so main.go/m6.go call sites compile UNCHANGED.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"rpg-world-server/internal/entity"
	gnet "rpg-world-server/internal/net"
	"rpg-world-server/internal/pets"
	worldcore "rpg-world-server/internal/world"
)

// petEntityType is Modules.EntityType.Pet (7). internal/protocol defines no
// EntityPet constant and packet shapes are frozen, so the literal lives here.
const petEntityType = 7

// Pet opcodes (Opcodes.Pet in opcodes.ts): Pickup 0 (the only opcode).
const petPickup = 0

// petMirrorDamage is the fixed pet swing damage (new-in-Go; TS pets never
// attack). Alias of entity.MirrorDamage; the orchestration passes the value
// through the World seam so this file never hard-codes a second copy.
const petMirrorDamage = entity.MirrorDamage

// petRecord is one live companion (alias of the entity registry record).
type petRecord = entity.Record

// petRegistry is the live companion store (owned by internal/entity).
var petRegistry = entity.NewRegistry()

// petSpawnData mirrors PetData (common/types/pet.d.ts): EntityData plus the
// `owner` instance string the client createPet reads (EntityData only carries
// ownerInstance, used by projectiles). movementSpeed rides EntityData.
type petSpawnData struct {
	EntityData
	Owner string `json:"owner"`
}

// petPayload builds the Spawn payload for a live pet (owner's movement speed
// copied per pet.ts serialize; hero default 220).
func petPayload(r *petRecord) petSpawnData {
	return petSpawnData{
		EntityData: EntityData{
			Instance:      r.Instance,
			Type:          petEntityType,
			Key:           r.MobKey,
			Name:          r.MobKey,
			X:             r.X,
			Y:             r.Y,
			Orientation:   intp(OrientationDown),
			Level:         intp(1),
			HitPoints:     intp(100),
			MaxHitPoints:  intp(100),
			MovementSpeed: intp(220),
			AttackRange:   intp(1),
		},
		Owner: r.Owner,
	}
}

// petFollowFrame builds the S->C Movement Follow frame (character.follow:
// [11,5,{instance,target}]).
func petFollowFrame(instance, owner string) []any {
	return pktOp(PacketMovement, MovementFollow, map[string]any{
		"instance": instance, "target": owner,
	})
}

// petWorld implements entity.World over the existing root globals. All frame
// construction (Spawn/Movement/Despawn/Animation/Combat) and all m5/combat
// pipeline calls stay here; internal/entity only decides what happens.
type petWorld struct{}

func (petWorld) OwnerPos(owner string) (int, int, bool) {
	return worldcore.EntityPos(owner)
}

func (petWorld) SpawnPet(rec entity.Record) {
	worldcore.SetEntityPos(rec.Instance, rec.X, rec.Y)
	worldcore.Broadcast(pkt(PacketSpawn, petPayload(&rec)))
	worldcore.Broadcast(petFollowFrame(rec.Instance, rec.Owner))
}

func (petWorld) MovePet(rec entity.Record) {
	worldcore.SetEntityPos(rec.Instance, rec.X, rec.Y)
	worldcore.Broadcast(pktOp(PacketMovement, MovementMove, serverMovement{
		Instance: rec.Instance, X: intp(rec.X), Y: intp(rec.Y),
	}))
	worldcore.Broadcast(petFollowFrame(rec.Instance, rec.Owner))
}

func (petWorld) TeleportPet(rec entity.Record) {
	worldcore.SetEntityPos(rec.Instance, rec.X, rec.Y)
	worldcore.Broadcast(pkt(PacketDespawn, despawnData{Instance: rec.Instance}))
	worldcore.Broadcast(pkt(PacketSpawn, petPayload(&rec)))
	worldcore.Broadcast(petFollowFrame(rec.Instance, rec.Owner))
	log.Printf("pets: %s teleported to owner %s (%d,%d)", rec.Instance, rec.Owner, rec.X, rec.Y)
}

func (petWorld) DespawnPet(instance string) {
	worldcore.RemoveEntity(instance)
	worldcore.Broadcast(pkt(PacketDespawn, despawnData{Instance: instance}))
}

func (petWorld) IsMob(target string) bool {
	return m9MobFor(target) != nil
}

func (petWorld) HitMob(petInstance, ownerInstance, target string, dmg int) {
	m := m9MobFor(target)
	if m == nil {
		return
	}
	worldcore.Broadcast(pkt(PacketAnimation, animationData{Instance: petInstance, Action: ActionAttack}))
	worldcore.Broadcast(pktOp(PacketCombat, CombatHit, combatData{
		Instance: petInstance, Target: target,
		Hit: HitData{Type: HitsNormal, Damage: dmg},
	}))
	m9PlayerHit(m, petConnByInstance(ownerInstance), dmg)
	log.Printf("pets: %s mirrored %s -> %s dmg=%d", petInstance, ownerInstance, target, dmg)
}

func (petWorld) HitDummy(petInstance, ownerInstance string, dmg int) bool {
	combatMu.Lock()
	defer combatMu.Unlock()
	if combatDead {
		return false
	}
	applyBossHitLocked(petInstance, dmg, HitsNormal, nil, false, -1, true)
	log.Printf("pets: %s mirrored %s -> dummy dmg=%d", petInstance, ownerInstance, dmg)
	return true
}

func (petWorld) DummyTarget() string {
	return combatDummyInstance
}

// petConnByInstance resolves a live playerConn by instance for combat credit
// (retaliate/loot/quest flow). Nil when the owner is gone; m9PlayerHit is
// nil-safe (skips credit, still applies broadcast-side damage already sent).
func petConnByInstance(instance string) *playerConn {
	c, _ := worldcore.Find[*playerConn](instance)
	return c
}

// petGrant spawns a companion for the owner's connection (player.ts setPet:
// ALREADY_HAVE_PET guard, spawn at the owner's tile, immediate follow).
// Returns nil when the owner already has a pet.
func petGrant(c *playerConn, mobKey, itemKey string) *petRecord {
	if c == nil || mobKey == "" {
		return nil
	}
	now := time.Now().UnixMilli()
	rec, already := petRegistry.Grant(c.Instance, c.Sess.PlayerX, c.Sess.PlayerY, mobKey, itemKey, now)
	if already {
		m6Notify(c, "misc:ALREADY_HAVE_PET")
		return nil
	}
	if rec == nil {
		return nil
	}
	petWorld{}.SpawnPet(*rec)
	log.Printf("pets: %s granted %s (%s) at %d,%d", c.Instance, rec.Instance, mobKey, rec.X, rec.Y)
	return rec
}

// petHasOwner reports whether the player instance currently owns a pet.
func petHasOwner(owner string) bool {
	return petRegistry.Has(owner)
}

// petPayloadByInstance resolves a Who lookup for a live pet instance.
func petPayloadByInstance(instance string) (any, bool) {
	r, ok := petRegistry.ByInstance(instance)
	if !ok {
		return nil, false
	}
	return petPayload(&r), true
}

// petTick steps every owned pet toward its owner (called from the central
// 20Hz tick loop; empty registry = no frames). Orchestration lives in
// internal/entity; frames stay in the petWorld seam above.
func petTick() {
	petRegistry.Tick(petWorld{})
}

// petMirrorSwing ports the attack-mirror (new-in-Go; TS pets never attack):
// after an owner swing at a killable target the pet issues a same-target
// swing through the existing combat pipeline (Animation + Combat Hit +
// damage), credited to the owner so retaliate/loot/quest flow is unchanged.
func petMirrorSwing(c *playerConn, target string) {
	if c == nil || target == "" {
		return
	}
	petRegistry.Mirror(petWorld{}, c.Instance, target)
}

// petForgetPlayer despawns + drops pet state on disconnect (abForgetPlayer /
// handler.ts disconnect removePet precedent: silent, no inventory return).
func petForgetPlayer(c *playerConn) {
	if c == nil {
		return
	}
	r, ok := petRegistry.RemoveByOwner(c.Instance)
	if !ok {
		return
	}
	petWorld{}.DespawnPet(r.Instance)
	log.Printf("pets: %s forgotten on disconnect of %s", r.Instance, c.Instance)
}

// petHandlePacket routes C->S Pet frames [58,{opcode}] (incoming.ts handlePet):
// Pickup(0) returns the pet to the inventory (removePet: NO_SPACE_PET when
// full, else a "<mobkey>pet" Container Add + Despawn).
func petHandlePacket(c *playerConn, frame clientFrame) {
	if len(frame) < 2 || c == nil {
		return
	}
	var d struct {
		Opcode *int `json:"opcode"`
	}
	if err := json.Unmarshal(frame[1], &d); err != nil || d.Opcode == nil || *d.Opcode != petPickup {
		return
	}
	r, ok := petRegistry.ByOwner(c.Instance)
	if !ok {
		return
	}
	if len(m5StateFor(c.Username).Inv) >= ModulesInventorySize {
		m6Notify(c, "misc:NO_SPACE_PET")
		return
	}
	idx := m5AddItem(c.Username, r.ItemKey, 1)
	_ = gnet.Send(c.Conn, pktOp(PacketContainer, ContainerAdd, containerData{
		Type: ContainerTypeInventory,
		Slot: &slotData{Index: idx, Key: r.ItemKey, Count: 1, Enchantments: map[string]any{}},
	}))
	markDirty(c.Username)
	_, _ = petRegistry.RemoveByOwner(c.Instance)
	petWorld{}.DespawnPet(r.Instance)
	log.Printf("pets: %s picked up by %s (+%s)", r.Instance, c.Instance, r.ItemKey)
}

// petDropKey peeks the inventory slot for a pet item (handler.ts
// item.isPetItem() parity via the items.json table in internal/entity).
func petDropKey(c *playerConn, index int) (mob, item string, ok bool) {
	st := m5StateFor(c.Username)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	if index < 0 || index >= len(st.Inv) {
		return "", "", false
	}
	key := st.Inv[index].Key
	mob, ok = entity.LookupItem(key)
	if !ok {
		return "", "", false
	}
	return mob, key, true
}

// petResolveKey accepts a pet-item key ("ratpet") or a mob key ("rat",
// "cat") for the debug grant; unknown/empty defaults to rat/ratpet.
func petResolveKey(key string) (mob, item string) {
	return entity.ResolveKey(key)
}

// petTestHandler is the TESTMAP-only debug dispatcher (m9test/m11test/abtest
// precedent, rides [46 {pettest}]): grant spawns a companion, state echoes
// the live record + hunger/expiry predicates, remove despawns it.
func petTestHandler(c *playerConn, data []byte) {
	if !testMode || c == nil {
		return
	}
	var d struct {
		PetTest string `json:"pettest"`
		Key     string `json:"key"`
	}
	if err := json.Unmarshal(data, &d); err != nil || d.PetTest == "" {
		return
	}
	switch d.PetTest {
	case "grant":
		mob, item := petResolveKey(d.Key)
		if r := petGrant(c, mob, item); r != nil {
			m6Notify(c, fmt.Sprintf("pet:grant %s mob=%s at=%d,%d", r.Instance, mob, r.X, r.Y))
		}
	case "state":
		r, ok := petRegistry.ByOwner(c.Instance)
		if !ok {
			m6Notify(c, "pet:state none")
			return
		}
		now := time.Now().UnixMilli()
		ox, oy, ok := worldcore.EntityPos(r.Owner)
		dist := -1
		if ok {
			dist = pets.Distance(ox, oy, r.X, r.Y)
		}
		m6Notify(c, fmt.Sprintf("pet:state %s mob=%s x=%d y=%d dist=%d hungry=%v expired=%v",
			r.Instance, r.MobKey, r.X, r.Y, dist,
			// Hunger/expiry are report-only (no TS source, not enforced):
			// hunger uses the package threshold, expiry a nominal
			// never-elapsing lifespan.
			pets.IsHungry(now, r.FedMs), pets.IsExpired(now, r.BornMs, 1<<62)))
	case "remove":
		r, ok := petRegistry.RemoveByOwner(c.Instance)
		if !ok {
			m6Notify(c, "pet:state none")
			return
		}
		petWorld{}.DespawnPet(r.Instance)
		m6Notify(c, "pet:removed "+r.Instance)
	}
}
