// Package main implements a minimal Kaetram-compatible WebSocket stub.
//
// Packet structs mirror packages/common/types/entity.d.ts (EntityData),
// packages/common/network/impl/player.ts (PlayerData) and
// packages/common/network/impl/handshake.ts (client handshake).
// Every field carries its own `json` tag; all optionals use `omitempty`.
// EntityType.Object (6) is never spawned: entities.ts has no case for it.
package main

import "encoding/json"

// Packet IDs (packages/common/network/packets.ts, 0-based).
const (
	PacketConnected    = 0
	PacketHandshake    = 1
	PacketLogin        = 2
	PacketWelcome      = 3
	PacketMap          = 4
	PacketSpawn        = 5
	PacketList         = 6
	PacketWho          = 7
	PacketEquipment    = 8
	PacketReady        = 9
	PacketSync         = 10
	PacketMovement     = 11
	PacketTeleport     = 12
	PacketDespawn      = 13
	PacketTarget       = 14
	PacketCombat       = 15
	PacketAnimation    = 16
	PacketPoints       = 17
	PacketNetwork      = 18
	PacketChat         = 19
	PacketCommand      = 20
	PacketContainer    = 21
	PacketAbility      = 22
	PacketQuest        = 23
	PacketAchievement  = 24
	PacketNotification = 25
	PacketBlink        = 26
	PacketHeal         = 27
	PacketExperience   = 28
	PacketDeath        = 29
	PacketMusic        = 30
	PacketNPC          = 31
	PacketRespawn      = 32
	PacketTrade        = 33
	PacketEnchant      = 34
	PacketGuild        = 35
	PacketPointer      = 36
	PacketPVP          = 37
	PacketPoison       = 38
	PacketWarp         = 39
	PacketStore        = 40
	PacketOverlay      = 41
	PacketCamera       = 42
	PacketBubble       = 43
	PacketSkill        = 44
	PacketUpdate       = 45
	PacketMinigame     = 46
	PacketEffect       = 47
	PacketFriends      = 48
	PacketFocus        = 49
	PacketRank         = 50
	PacketExamine      = 51
	PacketPlayer       = 52
	PacketRelay        = 53
	PacketCrafting     = 54
	PacketInterface    = 55
	PacketLootBag      = 56
	PacketCountdown    = 57
	PacketPet          = 58
	PacketResource     = 59
	PacketAdminSync    = 60
)

// Entity types (packages/common/network/modules.ts EntityType: Player0 NPC1
// Item2 Mob3 Chest4 Projectile5 Object6 Pet7 LootBag8 Effect9 Tree10 Rock11
// Foraging12 FishSpot13). Object6 omitted on purpose.
const (
	EntityPlayer     = 0
	EntityNPC        = 1
	EntityItem       = 2
	EntityMob        = 3
	EntityChest      = 4
	EntityProjectile = 5
	EntityLootBag    = 8
	EntityTree       = 10
	EntityRock       = 11
	EntityForaging   = 12
	EntityFishSpot   = 13
)

// Orientation (modules.ts): Up0 Down1 Left2 Right3.
const OrientationDown = 1

// Movement opcodes share Opcodes.Movement (packages/common/network/opcodes.ts):
// Request0 Started1 Step2 Stop3 Move4 Follow5 Entity6 Speed7.
const (
	MovementRequest = 0
	MovementStarted = 1
	MovementStep    = 2
	MovementStop    = 3
)

// clientMovement mirrors a C->S movement frame: [11, data] with the opcode
// INSIDE data (client player/handler.ts:59-67,101-109,126-134,202-217).
// Only the fields the stub reads are modelled.
type clientMovement struct {
	Opcode         *int   `json:"opcode,omitempty"`
	RequestX       *int   `json:"requestX,omitempty"`
	RequestY       *int   `json:"requestY,omitempty"`
	PlayerX        *int   `json:"playerX,omitempty"`
	PlayerY        *int   `json:"playerY,omitempty"`
	NextGridX      *int   `json:"nextGridX,omitempty"`
	NextGridY      *int   `json:"nextGridY,omitempty"`
	TargetInstance string `json:"targetInstance,omitempty"`
	Orientation    *int   `json:"orientation,omitempty"`
}

// serverMovement mirrors MovementPacketData (common/network/impl/movement.ts).
// S->C frames carry the opcode as its own element: [11, opcode, data]
// (packet.ts serialize; client connection.ts:409-470).
type serverMovement struct {
	Instance string `json:"instance"`
	X        *int   `json:"x,omitempty"`
	Y        *int   `json:"y,omitempty"`
}

// teleportData mirrors TeleportPacketData (common/network/impl/teleport.ts):
// [12, data], no opcode element.
type teleportData struct {
	Instance string `json:"instance"`
	X        int    `json:"x"`
	Y        int    `json:"y"`
}

// Enchantment mirrors Enchantment in types/item.d.ts:1-3.
type Enchantment struct {
	Level int `json:"level"`
}

// Enchantments mirrors Enchantments in types/item.d.ts:5-7 ([id: number]).
type Enchantments map[int]Enchantment

// HitData mirrors HitData in types/info.d.ts:1-9.
type HitData struct {
	Type   int      `json:"type"`
	Damage int      `json:"damage"`
	Ranged *bool    `json:"ranged,omitempty"`
	Aoe    *int     `json:"aoe,omitempty"`
	Terror *bool    `json:"terror,omitempty"`
	Poison *bool    `json:"poison,omitempty"`
	Skills []string `json:"skills,omitempty"`
}

// EntityDisplayInfo mirrors EntityDisplayInfo in types/entity.d.ts.
type EntityDisplayInfo struct {
	Instance    string   `json:"instance"`
	Colour      string   `json:"colour,omitempty"`
	Scale       *float64 `json:"scale,omitempty"`
	Exclamation string   `json:"exclamation,omitempty"`
}

// EntityData mirrors EntityData in types/entity.d.ts:25-57 exactly.
// Required fields have plain tags; every optional has its own tag + omitempty.
type EntityData struct {
	Instance       string             `json:"instance"`
	Type           int                `json:"type"`
	Key            string             `json:"key"`
	Name           string             `json:"name"`
	X              int                `json:"x"`
	Y              int                `json:"y"`
	Colour         string             `json:"colour,omitempty"`
	Scale          *float64           `json:"scale,omitempty"`
	MovementSpeed  *int               `json:"movementSpeed,omitempty"`
	HitPoints      *int               `json:"hitPoints,omitempty"`
	MaxHitPoints   *int               `json:"maxHitPoints,omitempty"`
	AttackRange    *int               `json:"attackRange,omitempty"`
	Level          *int               `json:"level,omitempty"`
	HiddenName     *bool              `json:"hiddenName,omitempty"`
	Orientation    *int               `json:"orientation,omitempty"`
	Count          *int               `json:"count,omitempty"`
	Enchantments   Enchantments       `json:"enchantments,omitempty"`
	OwnerInstance  string             `json:"ownerInstance,omitempty"`
	TargetInstance string             `json:"targetInstance,omitempty"`
	Hit            *HitData           `json:"hit,omitempty"`
	DisplayInfo    *EntityDisplayInfo `json:"displayInfo,omitempty"`
}

// ResourceEntityData extends EntityData with the resource state field
// (tree/rock/etc, see entities.ts:380-432). Kept separate so EntityData
// stays a 1:1 match with entity.d.ts.
type ResourceEntityData struct {
	EntityData
	State *int `json:"state,omitempty"`
}

// PlayerData mirrors PlayerData in network/impl/player.ts:15-28.
// Orientation is required (always emitted); the outer field shadows the
// embedded EntityData.Orientation so "orientation" appears exactly once.
type PlayerData struct {
	EntityData
	Rank           int   `json:"rank"`
	Pvp            bool  `json:"pvp"`
	Orientation    int   `json:"orientation"`
	Experience     *int  `json:"experience,omitempty"`
	NextExperience *int  `json:"nextExperience,omitempty"`
	PrevExperience *int  `json:"prevExperience,omitempty"`
	Mana           *int  `json:"mana,omitempty"`
	MaxMana        *int  `json:"maxMana,omitempty"`
	Equipments     []any `json:"equipments"`
}

// HandshakeData mirrors ClientHandshakePacketData in network/impl/handshake.ts.
type HandshakeData struct {
	Type       string `json:"type"`
	Instance   string `json:"instance,omitempty"`
	ServerID   int    `json:"serverId,omitempty"`
	ServerTime int64  `json:"serverTime,omitempty"`
}

// Server animation action IDs (Modules.Actions: Idle0 Attack1 Walk2 Orientate3).
const (
	ActionIdle   = 0
	ActionAttack = 1
)

// Equipment slot IDs (Modules.Equipment: Helmet0 Pendant1 Arrows2
// Chestplate3 Weapon4 Shield5 Ring6 ArmourSkin7 WeaponSkin8 Legplates9
// Cape10 Boots11 (modules.ts:132-145)). Used for the paperdoll demos and the
// CLEAN-mode adventurer showcase.
const (
	EquipmentHelmet     = 0
	EquipmentPendant    = 1
	EquipmentArrows     = 2
	EquipmentChestplate = 3
	EquipmentWeapon     = 4
	EquipmentShield     = 5
	EquipmentRing       = 6
	EquipmentArmourSkin = 7
	EquipmentWeaponSkin = 8
	EquipmentLegplates  = 9
	EquipmentCape       = 10
	EquipmentBoots      = 11
)

// Equipment opcodes (Opcodes.Equipment in opcodes.ts): Batch0 Equip1
// Unequip2 Style3. Batch carries {data:{equipments:[...]}} (SerializedEquipment,
// impl/equipment.ts) and the client applies each entry via player.equip
// (connection.ts handleEquipment); Spawn PlayerData carries the same entries
// inline (player.ts load -> equip -> `player/<slot>/<key>` sprite, boots fall
// back to `items/<key>` since getType returns ” for boots).
const (
	EquipmentBatch   = 0
	EquipmentEquip   = 1
	EquipmentUnequip = 2
	EquipmentStyle   = 3
)

// Resource states (Modules.ResourceState: Default0 Depleted1).
const (
	ResourceStateDefault  = 0
	ResourceStateDepleted = 1
)

// Target opcodes (Opcodes.Target: Talk0 Attack1 None2 Object3). Resources use
// Object: client player/handler.ts getTargetType returns Object for resources.
const (
	TargetAttack = 1
	TargetObject = 3
)

// Combat opcodes (Opcodes.Combat in opcodes.ts): Initiate0 Hit1 Finish2 Sync3.
// CombatInitiate is defined above; Hit drives the whole damage pipeline.
const (
	CombatHit    = 1
	CombatFinish = 2
	CombatSync   = 3
)

// Hit types (Modules.Hits in modules.ts): Normal0 Poison1 Heal2 Mana3
// Experience4 LevelUp5 Critical6 Stun7 Profession8 Freezing9 Burning10
// Terror11 Explosive12. Autos use Normal, Sunder uses Critical.
const (
	HitsNormal   = 0
	HitsCritical = 6
	HitsHeal     = 2
)

// Client-sided effects (Modules.Effects in modules.ts): None0 Critical1
// Terror2 TerrorStatus3 Stun4 Healing5 Fireball6 Iceball7 Poisonball8
// Boulder9 ... AccuracyBuff19 StrengthBuff20 DefenseBuff21 MagicBuff22
// ArcheryBuff23 AccuracySuperBuff24 StrengthSuperBuff25 DefenseSuperBuff26
// MagicSuperBuff27 ArcherySuperBuff28 Bleed29. Sunder renders Boulder;
// Storm renders Fireball; party buff alternates DefenseBuff/StrengthSuperBuff.
const (
	EffectBoulder          = 9
	EffectHealing          = 5
	EffectFireball         = 6
	EffectDefenseBuff      = 21
	EffectStrengthSuperBuf = 25
)

// Effect opcodes (Opcodes.Effect in opcodes.ts): Add0 Remove1.
const (
	EffectAdd    = 0
	EffectRemove = 1
)

// Orientation (modules.ts: Up0 Down1 Left2 Right3; OrientationDown above).
const (
	OrientationLeft  = 2
	OrientationRight = 3
)

// combatData mirrors CombatPacketData (common/network/impl/combat.ts):
// S->C [15, Hit=1, {instance, target, hit}]. Client handleCombat renders the
// damage splat (info.create -> Splat float), plays hit sounds, flashes the
// target and triggers both health bars. hit.skills carries skill keys
// (e.g. ["sunder"]) rendered as skill icons on the splat.
type combatData struct {
	Instance string  `json:"instance"`
	Target   string  `json:"target"`
	Hit      HitData `json:"hit"`
}

// pointsData mirrors PointsPacketData (common/network/impl/points.ts):
// S->C [17, {instance, hitPoints, maxHitPoints}]. Client handlePoints applies
// setHitPoints (HP bar + HUD); the server sends this from
// character.handleHitPoints to nearby regions on every HP change.
type pointsData struct {
	Instance     string `json:"instance"`
	HitPoints    *int   `json:"hitPoints,omitempty"`
	MaxHitPoints *int   `json:"maxHitPoints,omitempty"`
	Mana         *int   `json:"mana,omitempty"`
	MaxMana      *int   `json:"maxMana,omitempty"`
}

// effectData mirrors EffectPacketData (common/network/impl/effect.ts):
// S->C [47, opcode, {instance, effect}]. Add renders the client-sided
// effect sprite on the target (Boulder impact for Sunder).
type effectData struct {
	Instance string `json:"instance"`
	Effect   int    `json:"effect"`
}

// healData mirrors HealPacketData (common/network/impl/heal.ts):
// S->C [27, {instance, type, amount}]. type 'hitpoints' renders a green +HP
// splat (info.create Hits.Heal) + the Healing effect + heal sound when the
// target is game.player (client connection.ts handleHeal).
type healData struct {
	Instance string `json:"instance"`
	Type     string `json:"type"`
	Amount   int    `json:"amount"`
}

// despawnData mirrors DespawnPacketData (common/network/impl/despawn.ts):
// S->C [13, {instance}]. Removes a mob from the client (mob death path;
// player death instead uses the Death packet [29] + death scroll UI).
type despawnData struct {
	Instance string `json:"instance"`
}

// Movement opcodes that carry targetInstance (Opcodes.Movement: Follow5 Entity6).
// MovementMove(4) is the S->C walk order: [11,4,{instance,x,y}] (SPEC section 4B).
const (
	MovementMove   = 4
	MovementFollow = 5
	MovementEntity = 6
)

// animationData mirrors AnimationPacketData (common/network/impl/animation.ts):
// [16, {instance, action, resourceInstance?}]. S->C with resourceInstance set
// makes the client shake the resource + play the chop/mine sound
// (client connection.ts handleAnimation).
type animationData struct {
	Instance         string `json:"instance"`
	Action           int    `json:"action"`
	ResourceInstance string `json:"resourceInstance,omitempty"`
}

// resourceData mirrors ResourcePacketData (common/network/impl/resource.ts):
// [59, {instance, state}]. state 1 (Depleted) shows the stump frame via
// setExhausted(true); state 0 restores the full tree.
type resourceData struct {
	Instance string `json:"instance"`
	State    int    `json:"state"`
}

// RegionTile mirrors RegionTileData in types/map.d.ts:17-25.
// Data is Tile (number | number[]) straight from world.json. C is always
// emitted (false = walkable grass, true = collision); o/cur only when set.
type RegionTile struct {
	X    int    `json:"x"`
	Y    int    `json:"y"`
	Data any    `json:"data"`
	C    bool   `json:"c"`
	O    bool   `json:"o,omitempty"`
	Cur  string `json:"cur,omitempty"`
}

// Container opcodes (Opcodes.Container: Batch0 Add1 Remove2 ...) + the
// Inventory container type (Modules.ContainerType: Bank0 Inventory1 ...).
const (
	ContainerBatch  = 0
	ContainerAdd    = 1
	ContainerRemove = 2

	ContainerTypeInventory = 1
)

// Experience opcodes (Opcodes.Experience: Sync0 Skill1).
const (
	ExperienceSync  = 0
	ExperienceSkill = 1
)

// Skill opcodes (Opcodes.Skill: Batch0 Update1).
const (
	SkillBatch  = 0
	SkillUpdate = 1
)

// slotData mirrors SlotData (common/types/slot.d.ts) as sent in Container
// Add/Batch frames; containerBatch mirrors SerializedContainer.
type slotData struct {
	Index        int            `json:"index"`
	Key          string         `json:"key"`
	Count        int            `json:"count"`
	Enchantments map[string]any `json:"enchantments"`
}

type containerBatch struct {
	Slots []any `json:"slots"`
}

type containerData struct {
	Type int             `json:"type"`
	Data *containerBatch `json:"data,omitempty"`
	Slot *slotData       `json:"slot,omitempty"`
}

// experienceData mirrors ExperiencePacketData (impl/experience.ts).
type experienceData struct {
	Instance string `json:"instance"`
	Amount   *int   `json:"amount,omitempty"`
	Level    *int   `json:"level,omitempty"`
	Skill    *int   `json:"skill,omitempty"`
}

// skillData mirrors SkillData (impl/skill.ts).
type skillData struct {
	Type           int      `json:"type"`
	Experience     int      `json:"experience"`
	Level          *int     `json:"level,omitempty"`
	Percentage     *float64 `json:"percentage,omitempty"`
	NextExperience *int     `json:"nextExperience,omitempty"`
	Combat         *bool    `json:"combat,omitempty"`
}

func floatp(v float64) *float64 { return &v }

// pkt builds a no-opcode packet frame: [id, data].
// The Map packet is the only 3-element frame: [4, base64, bufSize]
// (packet.ts:25-33 appends bufferSize; impl/map.ts:9-15).
func pkt(id int, data any) []any {
	return []any{id, data}
}

// pktOp builds an opcode packet frame: [id, opcode, data]
// (packet.ts serialize; e.g. S->C Movement impl/movement.ts).
func pktOp(id, opcode int, data any) []any {
	return []any{id, opcode, data}
}

// mapPkt builds the Map frame: [4, base64(deflate(JSON regions)), bufSize].
func mapPkt(base64 string, bufSize int) []any {
	return []any{PacketMap, base64, bufSize}
}

// bulk serializes several packet frames into one bulk WS text message:
// [[id,data],[id,opcode,data],...] (messages.ts handleBulkData).
func bulk(frames ...[]any) []byte {
	raw, _ := json.Marshal(frames)
	return raw
}
