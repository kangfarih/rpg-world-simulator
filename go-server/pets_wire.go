// Pet companion wiring (behavior-additive root glue over internal/pets).
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
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"rpg-world-server/internal/pets"
)

// petEntityType is Modules.EntityType.Pet (7). internal/protocol defines no
// EntityPet constant and packet shapes are frozen, so the literal lives here.
const petEntityType = 7

// Pet opcodes (Opcodes.Pet in opcodes.ts): Pickup 0 (the only opcode).
const petPickup = 0

// petMirrorDamage is the fixed pet swing damage (new-in-Go; TS pets never
// attack). Small enough to never skew combat-harness DPS (pets only exist
// when explicitly granted).
const petMirrorDamage = 3

// petItemMob maps items.json pet-item keys to their "pet" mob keys
// (verified: ratpet->rat, rathatpet->rathat, ratballoonpet->ratballoon;
// royalpet quest reward "catpet"->cat by the same "<mob>pet" convention).
var petItemMob = map[string]string{
	"ratpet":        "rat",
	"rathatpet":     "rathat",
	"ratballoonpet": "ratballoon",
	"catpet":        "cat",
}

// petRecord is one live companion. Owner is the owner's player instance.
type petRecord struct {
	Instance string
	MobKey   string
	ItemKey  string
	Owner    string
	X, Y     int
	BornMs   int64
	FedMs    int64
}

var (
	petMu         sync.Mutex
	petByOwner    = map[string]*petRecord{}
	petByInstance = map[string]*petRecord{}
	petSeq        int64
)

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

// petGrant spawns a companion for the owner's connection (player.ts setPet:
// ALREADY_HAVE_PET guard, spawn at the owner's tile, immediate follow).
// Returns nil when the owner already has a pet.
func petGrant(c *playerConn, mobKey, itemKey string) *petRecord {
	if c == nil || mobKey == "" {
		return nil
	}
	if itemKey == "" {
		itemKey = mobKey + "pet"
	}
	petMu.Lock()
	if _, has := petByOwner[c.instance]; has {
		petMu.Unlock()
		m6Notify(c, "misc:ALREADY_HAVE_PET")
		return nil
	}
	petSeq++
	now := time.Now().UnixMilli()
	r := &petRecord{
		Instance: fmt.Sprintf("pet-%d", petSeq),
		MobKey:   mobKey, ItemKey: itemKey, Owner: c.instance,
		X: c.sess.playerX, Y: c.sess.playerY,
		BornMs: now, FedMs: now,
	}
	petByOwner[c.instance] = r
	petByInstance[r.Instance] = r
	petMu.Unlock()
	setEntityPos(r.Instance, r.X, r.Y)
	broadcast(pkt(PacketSpawn, petPayload(r)))
	broadcast(petFollowFrame(r.Instance, r.Owner))
	log.Printf("pets: %s granted %s (%s) at %d,%d", c.instance, r.Instance, mobKey, r.X, r.Y)
	return r
}

// petHasOwner reports whether the player instance currently owns a pet.
func petHasOwner(owner string) bool {
	petMu.Lock()
	defer petMu.Unlock()
	return petByOwner[owner] != nil
}

// petPayloadByInstance resolves a Who lookup for a live pet instance.
func petPayloadByInstance(instance string) (any, bool) {
	petMu.Lock()
	r := petByInstance[instance]
	petMu.Unlock()
	if r == nil {
		return nil, false
	}
	return petPayload(r), true
}

// petTick steps every owned pet toward its owner (called from the central
// 20Hz tick loop; empty registry = no frames). Teleport (despawn + respawn
// at the owner) beyond pets.TeleportDistance, else one FollowStep + Move and
// Follow frames. Pets never block movement: no blocked() consult, no
// resourceEntities registration (collision checks only see tiles + resources).
func petTick() {
	petMu.Lock()
	recs := make([]*petRecord, 0, len(petByOwner))
	for _, r := range petByOwner {
		recs = append(recs, r)
	}
	petMu.Unlock()
	for _, r := range recs {
		ox, oy, ok := entityPos(r.Owner)
		if !ok {
			continue // owner gone (disconnect cleanup removes the pet)
		}
		petMu.Lock()
		px, py := r.X, r.Y
		petMu.Unlock()
		switch {
		case pets.ShouldTeleport(ox, oy, px, py):
			petMu.Lock()
			r.X, r.Y = ox, oy
			petMu.Unlock()
			setEntityPos(r.Instance, ox, oy)
			broadcast(pkt(PacketDespawn, despawnData{Instance: r.Instance}))
			broadcast(pkt(PacketSpawn, petPayload(r)))
			broadcast(petFollowFrame(r.Instance, r.Owner))
			log.Printf("pets: %s teleported to owner %s (%d,%d)", r.Instance, r.Owner, ox, oy)
		case pets.ShouldFollow(ox, oy, px, py):
			nx, ny := pets.FollowStep(ox, oy, px, py)
			if nx == px && ny == py {
				continue
			}
			petMu.Lock()
			r.X, r.Y = nx, ny
			petMu.Unlock()
			setEntityPos(r.Instance, nx, ny)
			broadcast(pktOp(PacketMovement, MovementMove, serverMovement{
				Instance: r.Instance, X: intp(nx), Y: intp(ny),
			}))
			broadcast(petFollowFrame(r.Instance, r.Owner))
		}
	}
}

// petMirrorSwing ports the attack-mirror (new-in-Go; TS pets never attack):
// after an owner swing at a killable target the pet issues a same-target
// swing through the existing combat pipeline (Animation + Combat Hit +
// damage), credited to the owner so retaliate/loot/quest flow is unchanged.
func petMirrorSwing(c *playerConn, target string) {
	if c == nil || target == "" {
		return
	}
	petMu.Lock()
	r := petByOwner[c.instance]
	petMu.Unlock()
	if r == nil {
		return
	}
	if m := m9MobFor(target); m != nil {
		broadcast(pkt(PacketAnimation, animationData{Instance: r.Instance, Action: ActionAttack}))
		broadcast(pktOp(PacketCombat, CombatHit, combatData{
			Instance: r.Instance, Target: target,
			Hit: HitData{Type: HitsNormal, Damage: petMirrorDamage},
		}))
		m9PlayerHit(m, c, petMirrorDamage)
		log.Printf("pets: %s mirrored %s -> %s dmg=%d", r.Instance, c.instance, target, petMirrorDamage)
		return
	}
	if target == combatDummyInstance {
		combatMu.Lock()
		defer combatMu.Unlock()
		if combatDead {
			return
		}
		applyBossHitLocked(r.Instance, petMirrorDamage, HitsNormal, nil, false, -1, true)
		log.Printf("pets: %s mirrored %s -> dummy dmg=%d", r.Instance, c.instance, petMirrorDamage)
	}
}

// petForgetPlayer despawns + drops pet state on disconnect (abForgetPlayer /
// handler.ts disconnect removePet precedent: silent, no inventory return).
func petForgetPlayer(c *playerConn) {
	if c == nil {
		return
	}
	petMu.Lock()
	r := petByOwner[c.instance]
	if r != nil {
		delete(petByOwner, c.instance)
		delete(petByInstance, r.Instance)
	}
	petMu.Unlock()
	if r == nil {
		return
	}
	entitiesMu.Lock()
	delete(entities, r.Instance)
	entitiesMu.Unlock()
	broadcast(pkt(PacketDespawn, despawnData{Instance: r.Instance}))
	log.Printf("pets: %s forgotten on disconnect of %s", r.Instance, c.instance)
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
	petMu.Lock()
	r := petByOwner[c.instance]
	petMu.Unlock()
	if r == nil {
		return
	}
	if len(m5StateFor(c.username).Inv) >= ModulesInventorySize {
		m6Notify(c, "misc:NO_SPACE_PET")
		return
	}
	idx := m5AddItem(c.username, r.ItemKey, 1)
	_ = send(c.conn, pktOp(PacketContainer, ContainerAdd, containerData{
		Type: ContainerTypeInventory,
		Slot: &slotData{Index: idx, Key: r.ItemKey, Count: 1, Enchantments: map[string]any{}},
	}))
	markDirty(c.username)
	petMu.Lock()
	delete(petByOwner, c.instance)
	delete(petByInstance, r.Instance)
	petMu.Unlock()
	entitiesMu.Lock()
	delete(entities, r.Instance)
	entitiesMu.Unlock()
	broadcast(pkt(PacketDespawn, despawnData{Instance: r.Instance}))
	log.Printf("pets: %s picked up by %s (+%s)", r.Instance, c.instance, r.ItemKey)
}

// petDropKey peeks the inventory slot for a pet item (handler.ts
// item.isPetItem() parity via the items.json table above).
func petDropKey(c *playerConn, index int) (mob, item string, ok bool) {
	st := m5StateFor(c.username)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	if index < 0 || index >= len(st.Inv) {
		return "", "", false
	}
	key := st.Inv[index].Key
	mob, ok = petItemMob[key]
	if !ok {
		return "", "", false
	}
	return mob, key, true
}

// petResolveKey accepts a pet-item key ("ratpet") or a mob key ("rat",
// "cat") for the debug grant; unknown/empty defaults to rat/ratpet.
func petResolveKey(key string) (mob, item string) {
	if m, ok := petItemMob[key]; ok {
		return m, key
	}
	for _, m := range petItemMob {
		if key == m {
			return m, m + "pet"
		}
	}
	return "rat", "ratpet"
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
		petMu.Lock()
		r := petByOwner[c.instance]
		petMu.Unlock()
		if r == nil {
			m6Notify(c, "pet:state none")
			return
		}
		now := time.Now().UnixMilli()
		ox, oy, ok := entityPos(r.Owner)
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
		petMu.Lock()
		r := petByOwner[c.instance]
		if r != nil {
			delete(petByOwner, c.instance)
			delete(petByInstance, r.Instance)
		}
		petMu.Unlock()
		if r == nil {
			m6Notify(c, "pet:state none")
			return
		}
		entitiesMu.Lock()
		delete(entities, r.Instance)
		entitiesMu.Unlock()
		broadcast(pkt(PacketDespawn, despawnData{Instance: r.Instance}))
		m6Notify(c, "pet:removed "+r.Instance)
	}
}
