package main

// M10 — the area system (game/map/areas/*): camera, music, pvp, overlay,
// chest and dynamic areas loaded from world.json's `areas` groups, with
// enter/exit callbacks fired from the movement path (player handler
// detectAreas) — the generalization of M8's minigame-area plumbing.
//
// Per-type behavior ports (areas/impl/* + player.ts updates):
//   - camera:      Camera frame [42, opcode] on enter (lockX/lockY/player)
//                  and FreeFlow on exit; change-detected per player.
//   - music:       Music frame [30, null, song] on song change (null stays
//                  null — [30,null] is the valid "stop music" frame).
//   - pvp:         PVP frame [37, null, {state}] + IN/NOT_IN_PVP_ZONE
//                  notify on state flip; player.pvp feeds Spawn PlayerData.
//   - overlay:     Overlay [41, Set, {image,colour}] on enter, [41, Remove]
//                  on exit; colour = rgba(rgb…) or rgba(0,0,0,darkness).
//                  type "freezing" areas apply the Freezing status effect
//                  (Effect [47, Add, {instance, effect:17}]).
//   - chest:       chest-AREA flow: each mob spawn adopts its containing
//                  chest area; clearing the area of mobs spawns a reward
//                  chest (Spawn frame, type Chest4 key "chest") at spawnX/Y
//                  guarded by the mob respawn delay (Utils.timePassed);
//                  the next mob spawn removes the unlooted chest. Opening
//                  rolls the item list (key:count:probability, Node
//                  Utils.randomInt inclusive) and spawns the drop as an M5
//                  loot entity so pickup lands in the inventory.
//   - dynamic:     pairs linked by the `mapping` id (mappedArea /
//                  mappedAnimation by id) — the tile-remap endpoint for
//                  M12's full-world streaming; parsed + linked here, no
//                  per-tile work in the stub (documented divergence).
//
// Chest entity OPEN flow (entities.ts spawnChest onOpen): open despawns the
// chest, rolls one item, spawns it at the chest tile (persistent — no blink
// expiry, matching the isStatic=false spawnItem in Node's chest flow), and
// rewards the area achievement when achievements land (logged TODO).

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strconv"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Opcodes (common/network/opcodes.ts) + frame constants.
// ---------------------------------------------------------------------------

// Opcodes.Overlay: Set0 Remove1 Lamp2 RemoveLamps3 Darkness4.
const (
	OverlaySet    = 0
	OverlayRemove = 1
)

// Opcodes.Camera: LockX0 LockY1 FreeFlow2 Player3.
const (
	CameraLockX    = 0
	CameraLockY    = 1
	CameraFreeFlow = 2
	CameraPlayer   = 3
)

// Modules.Effects (modules.ts enum order — None0..Burning17 Freezing18? no:
// None0 Critical1 Terror2 TerrorStatus3 Stun4 Healing5 Fireball6 Iceball7
// Poisonball8 Boulder9 Running10 HotSauce11 DualistsMark12 ThickSkin13
// SnowPotion14 FirePotion15 Burning16 Freezing17 Invincible18 …). The
// freezing status area applies Effects.Freezing (area.ts addPlayer).
const effectFreezing = 17

// Modules.Constants.CHEST_RESPAWN (modules.ts): 50s chest respawn for
// statically spawned chests. Chest AREAS use the mob respawnDelay as the
// spawn guard (area.ts spawnDelay from the first added mob).
const chestRespawnStatic = 50 * time.Second

// ---------------------------------------------------------------------------
// Area model (areas/area.ts + areas/areas.ts base + ProcessedArea fields).
// ---------------------------------------------------------------------------

// m10Area is one world.json area entry. A single struct carries the union of
// the per-type impl fields exactly like Node's Area base class does.
type m10Area struct {
	ID     int    `json:"id"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Type   string `json:"type"` // camera mode / overlay kind
	Song   string `json:"song"` // music areas

	Darkness float64 `json:"darkness"` // overlay
	Fog      string  `json:"fog"`      // overlay fog image
	RGB      string  `json:"rgb"`      // overlay "r,g,b"

	Entities int    `json:"entities"` // chest area: mob count to clear
	Items    string `json:"items"`    // chest area: comma list (key:count:prob)
	SpawnX   int    `json:"spawnX"`   // chest spawn tile
	SpawnY   int    `json:"spawnY"`

	Mapping   int `json:"mapping"`   // dynamic: mapped counterpart area id
	Animation int `json:"animation"` // dynamic: mapped animation area id

	Polygon []struct {
		X int `json:"x"`
		Y int `json:"y"`
	} `json:"polygon"`
	Ignore bool `json:"ignore"` // omitted from walk callbacks (area.ts)

	// ---- runtime (not serialized) ----
	mobs       []string      // chest area: instances of mobs inside (area.entities)
	mobMu      sync.Mutex    // guards mobs + chest + lastSpawn
	chest      *m10Chest     // live chest entity (nil = none)
	lastSpawn  int64         // last chest spawn ms (Utils.timePassed guard)
	spawnDelay time.Duration // taken from the first mob's respawnDelay

	// linked dynamic counterparts (dynamic.ts link())
	mappedArea      *m10Area
	mappedAnimation *m10Area
}

// inside ports Area.contains/inRectangularArea (x <= px < x+width). The
// polygon branch exists in Node for music areas; none of the loaded groups
// in the current world.json use polygons with width 0 except music — the
// ray-cast port covers them (inPolygon).
func (a *m10Area) inside(x, y int) bool {
	if a == nil || a.Ignore {
		return false // Area.contains: ignorable areas are skipped
	}
	if len(a.Polygon) > 0 {
		return a.inPolygon(x, y)
	}
	return x >= a.X && x < a.X+a.Width && y >= a.Y && y < a.Y+a.Height
}

// inPolygon ports Area.inPolygon (ray cast, same winding).
func (a *m10Area) inPolygon(x, y int) bool {
	inside := false
	j := len(a.Polygon) - 1
	for i := 0; i < len(a.Polygon); i++ {
		xi, yi := a.Polygon[i].X, a.Polygon[i].Y
		xj, yj := a.Polygon[j].X, a.Polygon[j].Y
		if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
			inside = !inside
		}
		j = i
	}
	return inside
}

// isStatusArea ports Area.isStatusArea: overlay areas of type "freezing".
func (a *m10Area) isStatusArea() bool {
	return a.Type == "freezing"
}

// rgbList parses the "r,g,b" string (overlay.ts: split(',').map(Number)).
func (a *m10Area) rgbList() []int {
	if a.RGB == "" {
		return nil
	}
	var out []int
	for _, p := range splitComma(a.RGB) {
		n, err := strconv.Atoi(p)
		if err == nil {
			out = append(out, n)
		}
	}
	return out
}

// itemsList splits the chest items CSV (chest.ts: rawData.items.split(',')).
func (a *m10Area) itemsList() []string {
	if a.Items == "" {
		return nil
	}
	return splitComma(a.Items)
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	out = append(out, cur)
	return out
}

// ---------------------------------------------------------------------------
// Registries (areas.ts subclass instances -> one registry per group).
// ---------------------------------------------------------------------------

var (
	m10Mu          sync.Mutex
	m10Camera      []*m10Area
	m10Music       []*m10Area
	m10PVP         []*m10Area
	m10Overlay     []*m10Area
	m10Chests      []*m10Area
	m10Dynamic     []*m10Area
	m10DynamicByID = map[int]*m10Area{}
	m10Loaded      bool

	// m10PlayerAreas mirrors player.overlayArea/cameraArea/currentSong/pvp —
	// keyed by connection instance (player.ts change-detection fields).
	m10StateMu     sync.Mutex
	m10OverlayArea = map[string]*m10Area{}
	m10CameraArea  = map[string]*m10Area{}
	m10Song        = map[string]string{}
	m10PvpState    = map[string]bool{}
	m10Frozen      = map[string]bool{} // active Freezing effect
)

// m10Chest is the chest entity spawned by a cleared chest area
// (entities.ts spawnChest -> Chest entity).
type m10Chest struct {
	instance string
	x, y     int
	items    []string
	area     *m10Area
}

// m10LoadAreas parses world.json `areas` groups at boot (Node world.ts
// constructor builds one Areas subclass per group from map.areas).
func m10LoadAreas() {
	m10Mu.Lock()
	defer m10Mu.Unlock()
	if m10Loaded {
		return
	}
	m10Loaded = true
	loadWorld()
	if world == nil {
		return
	}
	raw, err := os.ReadFile(worldPath())
	if err != nil {
		return
	}
	var doc struct {
		Areas struct {
			Camera  []m10Area `json:"camera"`
			Music   []m10Area `json:"music"`
			PVP     []m10Area `json:"pvp"`
			Overlay []m10Area `json:"overlay"`
			Chests  []m10Area `json:"chests"`
			Dynamic []m10Area `json:"dynamic"`
		} `json:"areas"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		log.Printf("m10: parse areas: %v", err)
		return
	}
	cp := func(src []m10Area) []*m10Area {
		out := make([]*m10Area, 0, len(src))
		for i := range src {
			out = append(out, &src[i])
		}
		return out
	}
	m10Camera = cp(doc.Areas.Camera)
	m10Music = cp(doc.Areas.Music)
	m10PVP = cp(doc.Areas.PVP)
	m10Overlay = cp(doc.Areas.Overlay)
	m10Chests = cp(doc.Areas.Chests)
	m10Dynamic = cp(doc.Areas.Dynamic)

	// dynamic.ts link(): map `mapping`/`animation` ids to their areas.
	for _, a := range m10Dynamic {
		m10DynamicByID[a.ID] = a
	}
	for _, a := range m10Dynamic {
		if a.Mapping != 0 {
			a.mappedArea = m10DynamicByID[a.Mapping]
		}
		if a.Animation != 0 {
			a.mappedAnimation = m10DynamicByID[a.Animation]
		}
	}

	log.Printf("m10: areas loaded camera=%d music=%d pvp=%d overlay=%d chests=%d dynamic=%d",
		len(m10Camera), len(m10Music), len(m10PVP), len(m10Overlay), len(m10Chests), len(m10Dynamic))
}

// m10InArea ports Areas.inArea over one group: first containing area.
func m10InArea(group []*m10Area, x, y int) *m10Area {
	for _, a := range group {
		if a.inside(x, y) {
			return a
		}
	}
	return nil
}

// m10ChestAreaAt ports Mob.addToChestArea (mob.ts: chestAreas.inArea).
func m10ChestAreaAt(x, y int) *m10Area {
	m10Mu.Lock()
	defer m10Mu.Unlock()
	return m10InArea(m10Chests, x, y)
}

// m10AddChestMob ports Area.addEntity (mob.ts addToChestArea). Records the
// mob in the area and (first mob only) adopts its respawn delay as the
// chest spawn guard; a live unlooted chest is removed (chest.ts onSpawn ->
// removeChest).
func m10AddChestMob(area *m10Area, instance string, respawnDelay time.Duration) {
	area.mobMu.Lock()
	defer area.mobMu.Unlock()
	for _, m := range area.mobs {
		if m == instance {
			return
		}
	}
	area.mobs = append(area.mobs, instance)
	if area.spawnDelay == 0 {
		area.spawnDelay = respawnDelay
	}
	if area.chest != nil {
		// chest.ts removeChest path for area chests (non-static): despawn.
		c := area.chest
		area.chest = nil
		broadcast(pkt(PacketDespawn, despawnData{Instance: c.instance}))
		log.Printf("m10: chest %s removed (area %d repopulated)", c.instance, area.ID)
	}
}

// m10RemoveChestMob ports Area.removeEntity + onEmpty (mob handler death
// path): drop the mob from the area; when the last one leaves, spawn the
// reward chest guarded by the respawn delay (chest.ts spawnChest +
// Utils.timePassed).
func m10RemoveChestMob(area *m10Area, instance string) {
	area.mobMu.Lock()
	idx := -1
	for i, m := range area.mobs {
		if m == instance {
			idx = i
			break
		}
	}
	if idx >= 0 {
		area.mobs = append(area.mobs[:idx], area.mobs[idx+1:]...)
	}
	empty := len(area.mobs) == 0
	delay := area.spawnDelay
	if delay <= 0 {
		delay = m9RespawnDelay // Node fallback: MobDefaults.RESPAWN_DELAY
	}
	canSpawn := empty && time.Now().UnixMilli()-area.lastSpawn >= delay.Milliseconds()
	if canSpawn {
		area.lastSpawn = time.Now().UnixMilli()
	}
	area.mobMu.Unlock()

	if !canSpawn {
		return
	}
	m10SpawnChestEntity(area)
}

// m10KillHooks fires the M10 chest-area death path for a killed mob
// (handler.ts: mob.area?.removeEntity(mob, attacker) -> onEmpty chest spawn).
// Called from m9KillMob; killer is unused until achievements land.
func m10KillHooks(m *m9Mob, killer *playerConn) {
	area := m10ChestAreaAt(m.x, m.y)
	if area == nil {
		return
	}
	m10RemoveChestMob(area, m.instance)
}

// m10SpawnChestEntity ports entities.ts spawnChest for the chest-AREA flow:
// Spawn frame (type Chest4, key "chest") + registry entry.
func m10SpawnChestEntity(area *m10Area) {
	inst := fmt.Sprintf("chest-%d-%d", area.ID, time.Now().UnixMilli()%1_000_000)
	c := &m10Chest{instance: inst, x: area.SpawnX, y: area.SpawnY, items: area.itemsList(), area: area}

	area.mobMu.Lock()
	area.chest = c
	area.mobMu.Unlock()

	setEntityPos(inst, c.x, c.y)
	broadcast(pkt(PacketSpawn, EntityData{
		Instance: inst, Type: EntityChest, Key: "chest", Name: "Chest", X: c.x, Y: c.y,
	}))
	log.Printf("m10: chest %s spawned at %d,%d (area %d cleared, items=%v)",
		inst, c.x, c.y, area.ID, c.items)
}

// m10ChestFor finds a live chest entity by instance.
func m10ChestFor(instance string) *m10Chest {
	m10Mu.Lock()
	defer m10Mu.Unlock()
	for _, area := range m10Chests {
		area.mobMu.Lock()
		c := area.chest
		area.mobMu.Unlock()
		if c != nil && c.instance == instance {
			return c
		}
	}
	return nil
}

// m10ChestItemsAt reports the chest occupying a tile (movement-block check).
func m10ChestItemsAt(x, y int) bool {
	m10Mu.Lock()
	defer m10Mu.Unlock()
	for _, area := range m10Chests {
		area.mobMu.Lock()
		c := area.chest
		area.mobMu.Unlock()
		if c != nil && c.x == x && c.y == y {
			return true
		}
	}
	return false
}

// m10OpenChest ports Chest.getItem roll + entities.ts spawnChest onOpen:
// despawn the chest, roll one entry (key:count:probability, Utils.randomInt
// inclusive), spawn the item at the chest tile as a persistent M5 loot
// entity, and log the (future) achievement reward.
func m10OpenChest(c *playerConn, chest *m10Chest) {
	// Remove the chest first (onOpen -> this.remove(chest)).
	chest.area.mobMu.Lock()
	if chest.area.chest == chest {
		chest.area.chest = nil
	}
	// Node's onEmpty fired once already; opening does NOT re-clear mobs —
	// lastSpawn is NOT reset here (matches chest.ts removeChest).
	chest.area.mobMu.Unlock()

	broadcast(pkt(PacketDespawn, despawnData{Instance: chest.instance}))
	log.Printf("m10: %s opened chest %s at %d,%d", c.username, chest.instance, chest.x, chest.y)

	item := m10RollChestItem(chest.items)
	if item == nil {
		return
	}
	// Chest item spawns are persistent (isStatic=false, no blink expiry in
	// the Node chest flow) — spawn via the M5 loot path with no timers.
	m10SendPersistentLoot(item.key, item.count, chest.x, chest.y, c.username)
	// TODO(m-achievements): chest.achievement finish() lands with the
	// achievements slice.
}

// m10SendPersistentLoot spawns a chest item as a persistent world item:
// EntityData Type Item2, full Count, no owner/blink (Node spawnItem key,x,y,
// dropped=true,count -> Item entity; pickup via the M5 loot path). The M5
// loot registry entry routes Target/Step pickup through m5Pickup.
func m10SendPersistentLoot(key string, count, x, y int, owner string) {
	lx, ly := m5NearWalkable(x, y)
	inst := fmt.Sprintf("chestitem-%d", time.Now().UnixNano()%1_000_000)
	m5RegisterLoot(inst, key, count, lx, ly, owner)
	broadcast(pkt(PacketSpawn, EntityData{
		Instance: inst, Type: EntityItem, Key: key, Name: key,
		X: lx, Y: ly, Count: intp(count),
	}))
	log.Printf("m10: chest item %s (x%d) spawned at %d,%d", key, count, lx, ly)
}

// m10ChestItem is one rolled drop (chest.ts getItem return).
type m10ChestItem struct {
	key   string
	count int
}

// m10RollChestItem ports Chest.getItem: random entry, "key:count:probability"
// suffix parsing, probability roll with inclusive randomInt(0,100).
func m10RollChestItem(items []string) *m10ChestItem {
	if len(items) == 0 {
		return nil
	}
	item := items[rand.Intn(len(items))] // randomInt(0, len-1) inclusive
	count, probability := 1, 100
	// Entries look like "bronzesword" or "gold:25:50" — parse ':' segments.
	if idx := indexOfByte(item, ':'); idx >= 0 {
		segs := splitColon(item)
		item = segs[0]
		if len(segs) > 1 {
			if n, err := strconv.Atoi(segs[1]); err == nil {
				count = n
			}
		}
		if len(segs) > 2 {
			if n, err := strconv.Atoi(segs[2]); err == nil {
				probability = n
			}
		}
	}
	if item == "" {
		return nil
	}
	// Roll: randomInt(0,100) > probability -> no drop.
	if rand.Intn(101) > probability {
		return nil
	}
	return &m10ChestItem{key: item, count: count}
}

func indexOfByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func splitColon(s string) []string {
	var out []string
	cur := ""
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(s[i])
	}
	out = append(out, cur)
	return out
}

// ---------------------------------------------------------------------------
// Per-player area detection (player.ts updatePVP/Overlay/Camera/Music +
// handler.detectAreas). Fired from the movement path + teleports.
// ---------------------------------------------------------------------------

// m10OnPositionUpdate is the M10 hook on the movement path (Node
// handleMovement -> detectAreas). Change-detection is per player per group.
func m10OnPositionUpdate(c *playerConn) {
	x, y := c.sess.playerX, c.sess.playerY

	m10Mu.Lock()
	pvpArea := m10InArea(m10PVP, x, y)
	overlayArea := m10InArea(m10Overlay, x, y)
	cameraArea := m10InArea(m10Camera, x, y)
	musicArea := m10InArea(m10Music, x, y)
	m10Mu.Unlock()

	m10UpdatePVP(c, pvpArea != nil)
	m10UpdateOverlay(c, overlayArea)
	m10UpdateCamera(c, cameraArea)
	m10UpdateMusic(c, musicArea)
}

// m10UpdatePVP ports player.updatePVP: notify + PVP packet on state flip.
func m10UpdatePVP(c *playerConn, inPVP bool) {
	m10StateMu.Lock()
	if m10PvpState[c.instance] == inPVP {
		m10StateMu.Unlock()
		return
	}
	m10PvpState[c.instance] = inPVP
	m10StateMu.Unlock()

	if !inPVP {
		m6Notify(c, "misc:NOT_IN_PVP_ZONE")
	} else {
		m6Notify(c, "misc:IN_PVP_ZONE")
	}
	// PVPPacket serializes [37, undefined, data] -> [37, null, {state}]
	// (opcode element present as null — packet.ts serialize).
	_ = send(c.conn, []any{PacketPVP, nil, map[string]any{"state": inPVP}})
	log.Printf("m10: %s pvp=%v", c.username, inPVP)
}

// m10UpdateOverlay ports player.updateOverlay: Overlay Set/Remove on area
// change; freezing status areas apply/remove the Freezing effect.
func m10UpdateOverlay(c *playerConn, area *m10Area) {
	m10StateMu.Lock()
	if m10OverlayArea[c.instance] == area {
		m10StateMu.Unlock()
		return
	}
	prev := m10OverlayArea[c.instance]
	m10OverlayArea[c.instance] = area
	m10StateMu.Unlock()

	if area == nil {
		if prev != nil && prev.isStatusArea() {
			m10SetFreezing(c, false)
		}
		_ = send(c.conn, pktOp(PacketOverlay, OverlayRemove, nil))
		return
	}

	rgb := area.rgbList()
	colour := fmt.Sprintf("rgba(0, 0, 0, %v)", area.Darkness)
	if len(rgb) > 1 {
		colour = fmt.Sprintf("rgba(%d, %d, %d, %v)", rgb[0], rgb[1], rgb[2], area.Darkness)
	}
	image := area.Fog
	if image == "" {
		image = "blank" // updateOverlay: fog || 'blank'
	}
	_ = send(c.conn, pktOp(PacketOverlay, OverlaySet, map[string]any{
		"image":  image,
		"colour": colour,
	}))
	if area.isStatusArea() {
		m10SetFreezing(c, true)
	}
	log.Printf("m10: %s overlay area %d (%s)", c.username, area.ID, area.Type)
}

// m10SetFreezing applies/removes the Freezing status effect
// (Area.addPlayer/removePlayer -> player.status Effects.Freezing).
func m10SetFreezing(c *playerConn, on bool) {
	m10StateMu.Lock()
	if m10Frozen[c.instance] == on {
		m10StateMu.Unlock()
		return
	}
	m10Frozen[c.instance] = on
	m10StateMu.Unlock()

	op := EffectRemove
	if on {
		op = EffectAdd
	}
	_ = send(c.conn, pktOp(PacketEffect, op, effectData{Instance: c.instance, Effect: effectFreezing}))
}

// m10UpdateCamera ports player.updateCamera: mode opcode on enter, FreeFlow
// on exit (change-detected).
func m10UpdateCamera(c *playerConn, area *m10Area) {
	m10StateMu.Lock()
	if m10CameraArea[c.instance] == area {
		m10StateMu.Unlock()
		return
	}
	m10CameraArea[c.instance] = area
	m10StateMu.Unlock()

	if area == nil {
		_ = send(c.conn, pktOp(PacketCamera, CameraFreeFlow, nil))
		return
	}
	switch area.Type {
	case "lockX":
		_ = send(c.conn, pktOp(PacketCamera, CameraLockX, nil))
	case "lockY":
		_ = send(c.conn, pktOp(PacketCamera, CameraLockY, nil))
	case "player":
		_ = send(c.conn, pktOp(PacketCamera, CameraPlayer, nil))
	}
	log.Printf("m10: %s camera %s (area %d)", c.username, area.Type, area.ID)
}

// m10UpdateMusic ports player.updateMusic: Music frame on song change; an
// empty song sends [30,null] explicitly (Node sends newSong=undefined —
// serialized as a null element).
func m10UpdateMusic(c *playerConn, area *m10Area) {
	song := ""
	if area != nil {
		song = area.Song
	}
	m10StateMu.Lock()
	if m10Song[c.instance] == song {
		m10StateMu.Unlock()
		return
	}
	m10Song[c.instance] = song
	m10StateMu.Unlock()

	// MusicPacket serializes [30, undefined, song] -> [30, null, song]
	// (opcode element present as null).
	_ = send(c.conn, []any{PacketMusic, nil, song})
	log.Printf("m10: %s music %q", c.username, song)
}

// m10PVPState reports the player's current pvp flag (Spawn PlayerData.pvp).
func m10PVPState(instance string) bool {
	m10StateMu.Lock()
	defer m10StateMu.Unlock()
	return m10PvpState[instance]
}

// m10ForgetPlayer drops per-player area state on disconnect.
func m10ForgetPlayer(instance string) {
	m10StateMu.Lock()
	delete(m10OverlayArea, instance)
	delete(m10CameraArea, instance)
	delete(m10Song, instance)
	delete(m10PvpState, instance)
	delete(m10Frozen, instance)
	m10StateMu.Unlock()
}

// ---------------------------------------------------------------------------
// TESTMAP synthetic areas (the stub world has no real camera/pvp/overlay
// tiles). Injected once when running the default TESTMAP mode so the e2e can
// drive every area type on the synthetic map (mirrors the showcase grid
// convention). Layout (TESTMAP pond at 112..120 x 93..99):
//   music   x98..107  y100..103   song "beach"
//   overlay x110..120 y100..103   dark 0.75
//   pvp     x98..108  y105..108
//   chests  x110..120 y105..107   chest spawn (114,106)
//   chest-mob band x110..120 y100..103 (mobs adopted via m9test spawn)
//   camera  x98..107  y105..108   lockX (world.json has 0 camera areas; the
//                                  type value is exercised for parity)
// ---------------------------------------------------------------------------

func m10InjectTestAreas() {
	if !testMode || cleanMode || combatMode {
		return
	}
	m10Mu.Lock()
	defer m10Mu.Unlock()
	// Per-group injection: only groups the loaded world does NOT provide
	// (the real world.json carries music/pvp/overlay/chests — injecting over
	// them would shadow the e2e's real-area legs; camera is the group it
	// lacks, so that band always lands under TESTMAP).
	rect := func(id, x, y, w, h int) *m10Area {
		return &m10Area{ID: id, X: x, Y: y, Width: w, Height: h}
	}
	if len(m10Music) == 0 {
		m10Music = append(m10Music, &m10Area{ID: 9001, X: 98, Y: 100, Width: 10, Height: 4, Song: "beach"})
	}
	if len(m10Overlay) == 0 {
		m10Overlay = append(m10Overlay, &m10Area{ID: 9002, X: 110, Y: 100, Width: 11, Height: 4, Type: "dark", Darkness: 0.75})
	}
	if len(m10PVP) == 0 {
		m10PVP = append(m10PVP, rect(9003, 98, 105, 11, 4))
	}
	if len(m10Camera) == 0 {
		m10Camera = append(m10Camera, &m10Area{ID: 9004, X: 98, Y: 105, Width: 10, Height: 4, Type: "lockX"})
	}
	if len(m10Chests) == 0 {
		chest := rect(9005, 110, 105, 11, 3)
		chest.Items = "bronzesword"
		chest.SpawnX, chest.SpawnY = 114, 106
		m10Chests = append(m10Chests, chest)
	}
	log.Printf("m10: TESTMAP synthetic area bands ensured (camera=%d music=%d pvp=%d overlay=%d chests=%d)",
		len(m10Camera), len(m10Music), len(m10PVP), len(m10Overlay), len(m10Chests))
}

// ---------------------------------------------------------------------------
// M10TEST debug frame (TESTMAP-only): chest-mob adoption + area echo for the
// e2e. Shape: C->S [46, {"m10test":"..."}] (rides the Minigame dispatcher).
// ---------------------------------------------------------------------------

type m10ChestDrop struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

func m10HandleTest(c *playerConn, frame clientFrame) {
	if !testMode || cleanMode || combatMode || len(frame) < 2 {
		return
	}
	var data struct {
		M10Test  string `json:"m10test"`
		Instance string `json:"instance"`
		Key      string `json:"key"`
		Delay    int    `json:"delay"`
		X        int    `json:"x"`
		Y        int    `json:"y"`
	}
	if err := json.Unmarshal(frame[1], &data); err != nil {
		return
	}
	// Default key preserved for the M9-era rat spawn; the M10 chest harness
	// picks a passive low-HP mob (crab) so the kill lands inside the area
	// before the engine's roam pass can move it out.
	mobKey := data.Key
	if mobKey == "" {
		mobKey = "rat"
	}
	switch data.M10Test {
	case "chestmob":
		// Spawn a mob inside the chest area and adopt it (Mob.addToChestArea
		// parity — the harness spawns at coordinates inside the area). The
		// Respawn override rides the spawn call (no post-spawn mutation).
		m10Mu.Lock()
		var area *m10Area
		if len(m10Chests) > 0 {
			area = m10Chests[0]
		}
		m10Mu.Unlock()
		if area == nil {
			return
		}
		over := m9Overrides{}
		if data.Delay > 0 {
			over.Respawn = time.Duration(data.Delay) * time.Millisecond
		}
		if !m9SpawnMob(data.Instance, mobKey, data.X, data.Y, over) {
			return
		}
		if m := m9MobFor(data.Instance); m != nil {
			m10AddChestMob(area, data.Instance, m.respawnDelay())
			m6Notify(c, fmt.Sprintf("m10:chestmob=%s", data.Instance))
		}
	case "mobhp":
		// Echo mob state (mirrors m9's mobhp for the kill leg).
		m := m9MobFor(data.Instance)
		if m == nil || c == nil {
			return
		}
		m.mu.Lock()
		echo := fmt.Sprintf("m10:mob=%s hp=%d/%d", data.Instance, m.hp, m.maxHP)
		m.mu.Unlock()
		m6Notify(c, echo)
	case "chest":
		// Echo live chest state for the chest leg (introspection).
		m10Mu.Lock()
		var echo string
		for _, a := range m10Chests {
			a.mobMu.Lock()
			if a.chest != nil {
				echo = fmt.Sprintf("m10:chest=%s x=%d y=%d", a.chest.instance, a.chest.x, a.chest.y)
			}
			a.mobMu.Unlock()
		}
		m10Mu.Unlock()
		if echo == "" {
			echo = "m10:chest=none"
		}
		m6Notify(c, echo)
	}
}
