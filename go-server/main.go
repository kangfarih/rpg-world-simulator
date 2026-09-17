// Minimal Kaetram-compatible WebSocket stub.
//
// Listens on ws://127.0.0.1:9001 (client server PORT, HUB disabled).
// Boot: S Connected -> C Handshake{gVer} -> S Handshake{type:client} ->
// C Login -> S Welcome + Map -> C Ready{regionsLoaded,userAgent} ->
// S Spawn* (SPEC.md section 2).
// Every RX/TX packet is logged to stdout for manual verification
// against the stock client at http://127.0.0.1:9000.
package main

import (
	"bytes"
	"compress/gzip"
	crand "crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const addr = "127.0.0.1:9001"

var upgrader = websocket.Upgrader{
	CheckOrigin: func(_ *http.Request) bool { return true },
}

func intp(v int) *int { return &v }

func boolp(v bool) *bool { return &v }

// bufferSize emulates Utils.getBufferSize (util/utils.ts:257-258):
// encodeURI(JSON.stringify(data)).split(/%..|./).length - 1. Every ASCII
// char counts 1 and every UTF-8 byte of non-ASCII encodes as one %XX, so
// the result equals the UTF-8 byte length of the JSON string.
func bufferSize(jsonBytes []byte) int {
	return len(jsonBytes)
}

// TESTMAP mode (default ON; TESTMAP=0/false/off/no or --testmap=false /
// --notestmap selects the 9 real regions pure): CLONE of the 9 real regions
// around spawn as the base map, plus overlays: center pond + 5-oak row +
// showcase grid stamped over the base (see getTestRegionData).
//
// Tile IDs (repo read-only recon):
//   - grass testGrassTile=3907: base layer of the layered [3907,9787] grass
//     under the hero spawn (100,100); neither id is in world.json
//     `collisions`/`objects`, so buildTile flags it c:false (walkable).
//   - water testWaterTile=29: in world.json `collisions` (c:true) AND in the
//     client map animations dict (map.json "29": 28->29->30->31 water ripple),
//     so it renders animated and blocks movement.
const (
	testGrassTile = 3907
	testWaterTile = 29

	// Center pond: ellipse centered (pondCX,pondCY) radii pondRX x pondRY.
	// Covers x100-108, y101-107; oak row y=98 and spawns y96 stay on grass.
	pondCX, pondCY = 104, 104
	pondRX, pondRY = 4, 3
)

// testMode defaults ON for now; env wins, then CLI flags.
var testMode = func() bool {
	if v := os.Getenv("TESTMAP"); v != "" {
		return v != "0" && v != "false" && v != "off" && v != "no"
	}
	for _, a := range os.Args[1:] {
		switch a {
		case "--testmap", "--testmap=true", "--testmap=1":
			return true
		case "--testmap=false", "--testmap=0", "--notestmap":
			return false
		}
	}
	return true
}()

// cleanMode serves pure original terrain with zero overlays plus ONE
// fully-equipped adventurer showcase. Select with CLEAN=1/true/on/yes or
// --clean (CLEAN=0/false/off/no or --clean=false/--noclean turns it off;
// default OFF). It reuses the TESTMAP=0 pure path for tiles (getRegionData,
// no pond/grass stamps) and additionally drops ALL test entities (oak row,
// showcase grid, demos, guest p2, rat) and the showcase anim ticker — only
// the Welcome hero + one adventurer Spawn + one Equipment Batch remain.
// Collision/movement/chop handlers stay wired but dormant (no trees).
var cleanMode = func() bool {
	if v := os.Getenv("CLEAN"); v != "" {
		switch v {
		case "0", "false", "off", "no":
			return false
		default:
			return true
		}
	}
	for _, a := range os.Args[1:] {
		switch a {
		case "--clean", "--clean=true", "--clean=1":
			return true
		case "--clean=false", "--clean=0", "--noclean":
			return false
		}
	}
	return false
}()

// combatMode serves pure original terrain (like CLEAN) plus a combat test
// scene: a boss dummy + a 4-bot party (warrior, archer, mage, support) that
// beats on it. Select with COMBAT=1/true/on/yes or --combat (COMBAT=
// 0/false/off/no or --combat=false/--nocombat turns it off; default OFF).
// It reuses the TESTMAP=0 pure path for tiles (getRegionData, no pond/grass
// stamps) and drops ALL test entities (oak row, showcase grid, demos,
// guest p2, rat); only the Welcome hero + 4 bot Spawns + Boss Spawn remain.
// The party brain (startCombat) ticks independently of clients; broadcasts
// are no-ops with no subscribers.
var combatMode = func() bool {
	if v := os.Getenv("COMBAT"); v != "" {
		switch v {
		case "0", "false", "off", "no":
			return false
		default:
			return true
		}
	}
	for _, a := range os.Args[1:] {
		switch a {
		case "--combat", "--combat=true", "--combat=1":
			return true
		case "--combat=false", "--combat=0", "--nocombat":
			return false
		}
	}
	return false
}()

// isTestWater reports whether (x,y) is pond water (ellipse test).
func isTestWater(x, y int) bool {
	dx := float64(x - pondCX)
	dy := float64(y - pondCY)
	return (dx*dx)/float64(pondRX*pondRX)+(dy*dy)/float64(pondRY*pondRY) <= 1
}

// isTestGrass reports whether (x,y) is a forced-walkable overlay tile in
// test mode: under each of the 5 oaks, under every showcase grid slot, and
// under the 2 paperdoll demos. getTestRegionData stamps grass 3907 c:false
// there (replacing any colliding base tile); tileBlocked mirrors it.
func isTestGrass(x, y int) bool {
	if y == 98 {
		for _, ox := range []int{98, 100, 102, 104, 106} {
			if x == ox {
				return true
			}
		}
	}
	if (x == 98 && y == 108) || (x == 100 && y == 108) {
		return true
	}
	if x >= showOX && y >= showOY && (x-showOX)%showStep == 0 && (y-showOY)%showStep == 0 {
		col := (x - showOX) / showStep
		row := (y - showOY) / showStep
		if col < showCols {
			if row*showCols+col < len(showMobs)+len(showNPCs) {
				return true
			}
		}
	}
	return false
}

// getTestRegionData clones the 9 real regions around spawn (100,100) as the
// base map, then stamps overlays over the base (replacing tiles, adding where
// the base was empty): center pond (water 29 c:true), grass 3907 c:false
// under each oak + every showcase grid slot + demo spots (so all 232 stand
// on walkable). RegionTile shape and [4,base64gzip,bufSize] framing are
// identical to the real path.
func getTestRegionData() map[int][]RegionTile {
	data := getRegionData(100, 100)
	posIdx := make(map[int]map[[2]int]int, len(data))
	for rid, tiles := range data {
		m := make(map[[2]int]int, len(tiles))
		for i, t := range tiles {
			m[[2]int{t.X, t.Y}] = i
		}
		posIdx[rid] = m
	}
	set := func(x, y int, tile RegionTile) {
		rid := (y/mapDivisionSize)*sideLen + (x / mapDivisionSize)
		m := posIdx[rid]
		if m == nil {
			m = make(map[[2]int]int)
			posIdx[rid] = m
		}
		if i, ok := m[[2]int{x, y}]; ok {
			tiles := data[rid]
			tiles[i].Data = tile.Data
			tiles[i].C = tile.C
			tiles[i].O = false
			tiles[i].Cur = ""
			data[rid] = tiles
			return
		}
		data[rid] = append(data[rid], tile)
		m[[2]int{x, y}] = len(data[rid]) - 1
	}
	// Grass overlays first so the pond always wins on any overlap.
	for _, ox := range []int{98, 100, 102, 104, 106} {
		set(ox, 98, RegionTile{X: ox, Y: 98, Data: testGrassTile})
	}
	for i := 0; i < len(showMobs)+len(showNPCs); i++ {
		gx, gy := showPos(i)
		set(gx, gy, RegionTile{X: gx, Y: gy, Data: testGrassTile})
	}
	for _, p := range [][2]int{{98, 108}, {100, 108}} {
		set(p[0], p[1], RegionTile{X: p[0], Y: p[1], Data: testGrassTile})
	}
	pondWater, grassOver := 0, 0
	for y := pondCY - pondRY; y <= pondCY+pondRY; y++ {
		for x := pondCX - pondRX; x <= pondCX+pondRX; x++ {
			if isTestWater(x, y) {
				set(x, y, RegionTile{X: x, Y: y, Data: testWaterTile, C: true})
				pondWater++
			}
		}
	}
	_ = grassOver
	base := 0
	for _, tiles := range data {
		base += len(tiles)
	}
	log.Printf("testmap: base+overlay tiles=%d regions=%d pondWater=%d", base, len(data), pondWater)
	return data
}

// buildMapFrame serves the 9 real regions around spawn (100,100): region 50
// (x96-143 y96-143) + neighbours 25,26,27,49,51,73,74,75 (regions.ts
// getSurroundingRegions; sideLength = 1152/48 = 24). Tiles are extracted
// from world.json (index = y*width+x, number|[n..] layered) with empty
// (0/[]) skipped and data>=1 kept, mirroring getRegionTileData
// (regions.ts:541-563). Framing stays [4,base64gzip,bufSize] with gzip
// (Utils.compress default) + encodeURI byte length. Built once and cached.
func buildMapFrame() []any {
	mapOnce.Do(func() {
		var data map[int][]RegionTile
		if cleanMode || combatMode {
			data = getRegionData(100, 100)
		} else if testMode {
			data = getTestRegionData()
		} else {
			data = getRegionData(100, 100)
		}
		regionsJSON, err := json.Marshal(data)
		if err != nil {
			log.Fatalf("marshal regions: %v", err)
		}

		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		if _, err := w.Write(regionsJSON); err != nil {
			log.Fatalf("gzip write: %v", err)
		}
		if err := w.Close(); err != nil {
			log.Fatalf("gzip close: %v", err)
		}

		mapFrame = mapPkt(base64.StdEncoding.EncodeToString(buf.Bytes()), bufferSize(regionsJSON))
		log.Printf("map frame: cleanMode=%v testMode=%v %d regions, %d bytes json", cleanMode, testMode, len(data), len(regionsJSON))
	})
	return mapFrame
}

var (
	mapOnce  sync.Once
	mapFrame []any
)

// Region plumbing (mirrors server map/regions.ts + map/map.ts).
const (
	mapDivisionSize = 48 // Modules.Constants.MAP_DIVISION_SIZE (modules.ts:630)

	diagonalFlag = 0x20000000 // Modules.MapFlags (modules.ts:706-708)
	flipMask     = 0x20000000 | 0x40000000 | 0x80000000
)

func worldPath() string {
	if p := os.Getenv("WORLD_JSON"); p != "" {
		return p
	}
	// Fresh-clone portable default: ../packages/server/data/map/world.json
	// relative to the server working directory (go-server/). Fall back to
	// the legacy absolute path only if the relative candidate is missing.
	rel := filepath.Join("..", "packages", "server", "data", "map", "world.json")
	if _, err := os.Stat(rel); err == nil {
		return rel
	}
	for _, alt := range []string{
		filepath.Join("..", "..", "packages", "server", "data", "map", "world.json"),
		filepath.Join("packages", "server", "data", "map", "world.json"),
	} {
		if _, err := os.Stat(alt); err == nil {
			return alt
		}
	}
	return "/Users/appfuxion/repo/rpg-world-sim/packages/server/data/map/world.json"
}

type worldFile struct {
	Width      int               `json:"width"`
	Height     int               `json:"height"`
	Data       []json.RawMessage `json:"data"`
	Collisions []int             `json:"collisions"`
	Objects    []int             `json:"objects"`
	Cursors    map[string]string `json:"cursors"`
}

var (
	worldOnce sync.Once
	world     *worldFile
	collSet   map[int]bool
	objSet    map[int]bool
	curSet    map[int]string
	sideLen   int
)

func loadWorld() {
	worldOnce.Do(func() {
		raw, err := os.ReadFile(worldPath())
		if err != nil {
			log.Fatalf("read world.json: %v", err)
		}
		var w worldFile
		if err := json.Unmarshal(raw, &w); err != nil {
			log.Fatalf("parse world.json: %v", err)
		}
		collSet = make(map[int]bool, len(w.Collisions))
		for _, id := range w.Collisions {
			collSet[id] = true
		}
		objSet = make(map[int]bool, len(w.Objects))
		for _, id := range w.Objects {
			objSet[id] = true
		}
		curSet = make(map[int]string, len(w.Cursors))
		for k, v := range w.Cursors {
			id, err := strconv.Atoi(k)
			if err != nil {
				continue
			}
			curSet[id] = v
		}
		sideLen = w.Width / mapDivisionSize
		world = &w
		log.Printf("world loaded: %dx%d sideLen=%d tiles=%d collisions=%d objects=%d",
			w.Width, w.Height, sideLen, len(w.Data), len(collSet), len(objSet))
	})
}

// unflipTile strips Tiled flip bitmasks (map.ts:353-361).
func unflipTile(id int) int {
	if id > diagonalFlag {
		return id &^ flipMask
	}
	return id
}

// buildTile mirrors regions.ts:610-646: skip empty, keep data>=1 (arrays are
// always kept), flag collisions/objects/cursors per layer. Walkable tiles
// keep C=false. Returns false for skipped tiles.
func buildTile(x, y int) (RegionTile, bool) {
	idx := y*world.Width + x
	if idx < 0 || idx >= len(world.Data) {
		return RegionTile{}, false
	}
	raw := world.Data[idx]
	if string(raw) == "0" || string(raw) == "[]" || string(raw) == "null" {
		return RegionTile{}, false
	}
	var layers []int
	var data any
	var num float64
	if err := json.Unmarshal(raw, &num); err == nil {
		if num < 1 {
			return RegionTile{}, false
		}
		layers = []int{int(num)}
		data = layers[0]
	} else {
		var arr []float64
		if err := json.Unmarshal(raw, &arr); err != nil || len(arr) == 0 {
			return RegionTile{}, false
		}
		layers = make([]int, len(arr))
		for i, v := range arr {
			layers[i] = int(v)
		}
		data = layers
	}

	tile := RegionTile{X: x, Y: y, Data: data}
	for _, id := range layers {
		u := unflipTile(id)
		if objSet[u] {
			tile.O = true
			tile.C = true
		} else if collSet[u] {
			tile.C = true
		}
		if cur, ok := curSet[u]; ok {
			tile.Cur = cur
		}
	}
	return tile, true
}

// surroundingRegions mirrors getSurroundingRegions (regions.ts:711-766),
// region first then neighbours (9 for interior regions like 50).
func surroundingRegions(region int) []int {
	total := (world.Width / mapDivisionSize) * (world.Height / mapDivisionSize)
	if region < 0 || region > total-1 {
		return nil
	}
	out := []int{region}
	left := region%sideLen == 0
	right := region%sideLen == sideLen-1
	top := region < sideLen
	bottom := region > total-sideLen-1

	switch {
	case left:
		out = append(out, region+1)
	case right:
		out = append(out, region-1)
	default:
		out = append(out, region-1, region+1)
	}

	switch {
	case top || bottom:
		rel := region + sideLen
		if !top {
			rel = region - sideLen
		}
		out = append(out, rel)
		switch {
		case rel%sideLen == 0:
			out = append(out, rel+1)
		case rel%sideLen == sideLen-1:
			out = append(out, rel-1)
		default:
			out = append(out, rel-1, rel+1)
		}
	case left:
		out = append(out, region-sideLen, region-sideLen+1, region+sideLen, region+sideLen+1)
	case right:
		out = append(out, region-sideLen, region-sideLen-1, region+sideLen, region+sideLen-1)
	default:
		out = append(out, region+sideLen, region-sideLen,
			region+sideLen-1, region+sideLen+1, region-sideLen-1, region-sideLen+1)
	}
	return out
}

// getRegionData mirrors getRegionData (regions.ts:501-529) for a static
// spawn: region of (px,py) plus all surrounding regions, empty ones dropped.
func getRegionData(px, py int) map[int][]RegionTile {
	loadWorld()
	region := (py/mapDivisionSize)*sideLen + (px / mapDivisionSize)
	data := make(map[int][]RegionTile)
	for _, rid := range surroundingRegions(region) {
		x0 := (rid % sideLen) * mapDivisionSize
		y0 := (rid / sideLen) * mapDivisionSize
		var tiles []RegionTile
		for y := y0; y < y0+mapDivisionSize; y++ {
			for x := x0; x < x0+mapDivisionSize; x++ {
				if t, ok := buildTile(x, y); ok {
					tiles = append(tiles, t)
				}
			}
		}
		if len(tiles) > 0 {
			data[rid] = tiles
		}
	}
	return data
}

// newPlayerInstance mints a random per-connection player id (p-<12 hex).
// Guest/scenario bots keep their stable scenario IDs (p2, p-warbot, ...);
// only the Welcome self player is random per connection.
func newPlayerInstance() string {
	var b [6]byte
	if _, err := crand.Read(b[:]); err != nil {
		return fmt.Sprintf("p-%d", time.Now().UnixNano()%0xffffff)
	}
	return "p-" + hex.EncodeToString(b[:])
}

// welcomePlayer is the self player delivered via Welcome (PlayerData,
// impl/player.ts:15-28). Key 'base' resolves to sprite player/base.
// Orientation is the required PlayerData-level field (always emitted).
// The instance is random per connection (M2 registry); scenario bots keep
// their stable IDs.
func welcomePlayer(instance string) PlayerData {
	return PlayerData{
		EntityData: EntityData{
			Instance:      instance,
			Type:          EntityPlayer,
			Key:           "base",
			Name:          "hero",
			X:             100,
			Y:             96,
			Level:         intp(1),
			HitPoints:     intp(100),
			MaxHitPoints:  intp(100),
			MovementSpeed: intp(220),
			AttackRange:   intp(1),
		},
		Orientation: OrientationDown,
		Rank:        0,
		Pvp:         false,
		Mana:        intp(50),
		MaxMana:     intp(50),
		Equipments:  []any{},
	}
}

// oakSpawns lists every cuttable Tree entity. TESTMAP mode: 5 oaks in a
// horizontal row at y=98, x=98,100,102,104,106 — all on walkable test grass
// (pond is y101-107, so no overlap), each blocking via the
// resource-occupant rule. Real mode: the legacy demo oak t1 plus one per real
// world.json `entities` oak marker nearest spawn (tileIndex->key,
// map.ts forEachEntity -> entities.ts spawnTree; key "oak" renders trees/oak
// via the entities.ts Type->prefix rule). All stand on walkable (c:false) tiles
// with walkable neighbours (verified against `collisions`/`objects`), and each
// blocks movement via the resource-occupant rule below (server map.isColliding
// hasEntityAt + isResource, map.ts:246-254).
var oakSpawns = func() []ResourceEntityData {
	if cleanMode || combatMode {
		return nil // CLEAN/COMBAT: pure terrain, no trees to chop (handlers dormant).
	}
	if testMode {
		xs := []int{98, 100, 102, 104, 106}
		out := make([]ResourceEntityData, 0, len(xs))
		for i, x := range xs {
			out = append(out, ResourceEntityData{
				EntityData: EntityData{
					Instance: fmt.Sprintf("t-test-%d", i+1),
					Type:     EntityTree, Key: "oak", Name: "Oak", X: x, Y: 98,
				},
				State: intp(0),
			})
		}
		return out
	}
	return []ResourceEntityData{
		{EntityData: EntityData{Instance: "t1", Type: EntityTree, Key: "oak", Name: "Oak", X: 105, Y: 100}, State: intp(0)},
		{EntityData: EntityData{Instance: "t-oak-1", Type: EntityTree, Key: "oak", Name: "Oak", X: 105, Y: 106}, State: intp(0)}, // entities idx 122217
		{EntityData: EntityData{Instance: "t-oak-2", Type: EntityTree, Key: "oak", Name: "Oak", X: 118, Y: 105}, State: intp(0)}, // idx 121078
		{EntityData: EntityData{Instance: "t-oak-3", Type: EntityTree, Key: "oak", Name: "Oak", X: 122, Y: 109}, State: intp(0)}, // idx 125690
		{EntityData: EntityData{Instance: "t-oak-4", Type: EntityTree, Key: "oak", Name: "Oak", X: 126, Y: 109}, State: intp(0)}, // idx 125694
		{EntityData: EntityData{Instance: "t-oak-5", Type: EntityTree, Key: "oak", Name: "Oak", X: 102, Y: 122}, State: intp(0)}, // idx 140646
		{EntityData: EntityData{Instance: "t-oak-6", Type: EntityTree, Key: "oak", Name: "Oak", X: 132, Y: 112}, State: intp(0)}, // idx 129156
		{EntityData: EntityData{Instance: "t-oak-7", Type: EntityTree, Key: "oak", Name: "Oak", X: 147, Y: 102}, State: intp(0)}, // idx 117651 (region 51)
	}
}()

// Showcase grid: EVERY mobs/* key from sprites.json that has a PNG (156;
// mobs/ghostrider has no PNG and is skipped, logged below) spawned as Mob
// entities + every npcs/* key (76 unique — sprites.json lists
// npcs/redbikinigirlnpc twice) as NPC entities. 232 total, all STATIC (no
// roam broadcasts) so the formation holds for screenshots. Alphabetical
// within each group (mobs first at indices 0-155, NPCs at 156-231), spacing
// 2 tiles from origin (showOX,showOY): extent x96-126, y110-138 — all inside
// test region 50 (x96-143 y96-143), clear of the pond (y101-107) and the oak
// row (y=98), every tile walkable grass (c:false in test mode).
const (
	showOX, showOY     = 96, 110
	showCols, showStep = 16, 2
)

var showMobs = []string{
	"adherer", "ancientwizard", "angel", "ant", "babyspider", "bat", "beetle",
	"blackpirateskeleton", "blackwizard", "blazespider", "bluepreta", "brownmouse",
	"cactus", "card", "card2", "cat", "clam", "cobra", "cowwarrior", "crab",
	"crystalscorpion", "cursedhahoemask", "cursedjangseung", "darkogre", "darkregion",
	"darkregionillusion", "darkscorpion", "darkskeleton", "darkwolf", "deathknight",
	"desertscorpion", "devilkazya", "earthworm", "eliminator", "enel", "eye",
	"firespider", "flaredeathknight", "fluffy", "forestdragon", "frog", "frostqueen",
	"ghost", "goblin", "goldgolem", "golem", "greencockroach", "greenfish",
	"greenpirateskeleton", "guardmace", "guardsword", "hellhound", "hellspider",
	"hermitcrab", "hobgoblin", "hongcheol", "icebat", "icecrab", "icegoblin",
	"icegolem", "iceknight", "icerat", "icevulture", "icewizard", "infectedguard",
	"infectedvillager", "ironogre", "jirisanmoonbear", "kaonashi", "lavaslime",
	"lightningguardian", "livingarmor", "mantis", "mermaid", "mimic", "minidragon",
	"miniemperor", "miniiceknight1", "miniiceknight2", "miniiceknight3",
	"miniiceknight4", "miniiceknight5", "miniknight1", "miniknight2", "miniknight3",
	"miniknight4", "miniknight5", "miniknight6", "miniseadragon", "moleking",
	"nightmareregion", "ogre", "ogrelord", "oldogre", "orc", "paladin", "penguin",
	"picklemob", "pierrot", "pinkelf", "piratecaptain", "pirateking", "pirateskeleton",
	"poisonspider", "preta", "purplepreta", "queenant", "queenspider", "rabbitman",
	"rat", "redcockroach", "redelf", "redfish", "redguard", "redmouse",
	"redpirateskeleton", "regionhenchman", "rhaphidophoridae", "rooster", "rudolf",
	"santa", "santaelf", "seadragon", "shadowregion", "skeleton", "skeleton2",
	"skeletonking", "skydinosaur", "skyelf", "slime", "smalldevil", "snek",
	"snowelf", "snowlady", "snowman", "snowman2", "snowrabbit", "snowwolf",
	"soldierant", "soybeanbug", "spectre", "spider", "squid", "suicideghost",
	"vulture", "whitebear", "whitemouse", "whitetiger", "windguardian", "wizard",
	"wolf", "yellowbat", "yellowfish", "yellowmouse", "yellowpreta", "zombie",
}

var showNPCs = []string{
	"agent", "ancientmanumentnpc", "angelnpc", "beachnpc", "blacksmith",
	"blacksmith2", "bluebikinigirlnpc", "bluestoremannpc", "boxingman", "coder",
	"desertnpc", "doctor", "elfnpc", "fairynpc", "fairynpc2", "firstsonangelnpc",
	"fisherman", "forestnpc", "greenstoremannpc", "guard", "guard2", "guildnpc",
	"herbalist", "iamverycoldnpc", "iceelfnpc", "king", "king2", "lavanpc",
	"madscientist", "mermaidnpc", "miner", "miner2", "mojojojonpc", "momangelnpc",
	"nyan", "octocat", "octopus", "oddeyecat", "oldlady", "oldlady2", "picklenpc",
	"pirategirlnpc", "priest", "prisoner", "purplestoremannpc", "ratnpc",
	"redbikinigirlnpc", "redstoremannpc", "rick", "rickgf", "royalguard",
	"royalguard2", "royalguard3", "santaelfnpc", "scientist", "secondsonangelnpc",
	"shepherdboy", "snowshepherdboy", "soldier", "sorcerer", "sponge",
	"superiorangelnpc", "vampire", "vendingmachine", "villagegirl", "villagegirl2",
	"villagegirl3", "villagegirl4", "villagegirl5", "villagegirl6", "villagegirl7",
	"villager", "villager2", "villager3", "villager4", "zombiegf",
}

// showPos returns the grid tile for showcase index i (mobs 0-155, NPCs
// continue at 156-231).
func showPos(i int) (int, int) {
	return showOX + (i%showCols)*showStep, showOY + (i/showCols)*showStep
}

// showAnimTick broadcasts the rotating showcase anims: Animation{action:Attack}
// for a window of 10 mobs + Idle for a window of 5 NPCs, so screenshots catch
// attack frames. The grid itself never moves (no Movement broadcasts).
var showTick uint64

func showAnimTick() {
	frames := make([][]any, 0, 15)
	start := int(showTick*10) % len(showMobs)
	for k := 0; k < 10; k++ {
		idx := (start + k) % len(showMobs)
		frames = append(frames, pkt(PacketAnimation, animationData{
			Instance: fmt.Sprintf("m-show-%d", idx+1), Action: ActionAttack,
		}))
	}
	ns := int(showTick*5) % len(showNPCs)
	for k := 0; k < 5; k++ {
		idx := (ns + k) % len(showNPCs)
		frames = append(frames, pkt(PacketAnimation, animationData{
			Instance: fmt.Sprintf("n-show-%d", idx+1), Action: ActionIdle,
		}))
	}
	showTick++
	broadcast(frames...)
	log.Printf("showcase anim tick %d: 10 atk (m-show-%d..) + 5 idle", showTick, start+1)
}

// startShowcase launches the 5s anim loop (once; broadcasts are no-ops
// with no subscribers).
var showOnce sync.Once

func startShowcase() {
	if cleanMode || combatMode {
		return // CLEAN/COMBAT: no grid, no anim ticker.
	}
	showOnce.Do(func() {
		go func() {
			t := time.NewTicker(5 * time.Second)
			defer t.Stop()
			for range t.C {
				showAnimTick()
			}
		}()
	})
}

// demoPlayers spawns 2 paperdoll demo players just north of the grid (grass,
// clear of pond/oak row). Weapon/helmet keys verified in server items.json
// (ironsword/ironhelmet, goldsword/goldhelmet) and sprites PNGs.
func demoPlayers() []PlayerData {
	mk := func(inst, name string, x, y int, weapon, helmet string) PlayerData {
		return PlayerData{
			EntityData: EntityData{
				Instance: inst, Type: EntityPlayer, Key: "base", Name: name,
				X: x, Y: y, Orientation: intp(OrientationDown),
				Level: intp(1), HitPoints: intp(100), MaxHitPoints: intp(100),
				MovementSpeed: intp(220), AttackRange: intp(1),
			},
			Orientation: OrientationDown, Rank: 0, Pvp: false,
			Mana: intp(50), MaxMana: intp(50),
			Equipments: []any{
				map[string]any{"type": EquipmentWeapon, "key": weapon, "count": 1, "enchantments": map[string]any{}},
				map[string]any{"type": EquipmentHelmet, "key": helmet, "count": 1, "enchantments": map[string]any{}},
			},
		}
	}
	return []PlayerData{
		mk("p-show-1", "ironknight", 98, 108, "ironsword", "ironhelmet"),
		mk("p-show-2", "goldknight", 100, 108, "goldsword", "goldhelmet"),
	}
}

// cleanAdventurerEquipments is the full paperdoll set for the CLEAN-mode
// adventurer showcase. Every key verified in packages/server/data/items.json
// with a client sprite at player/<slot>/<key>.png (boots have no player/
// folder — player.ts getType returns ” so the client falls back to
// items/<key>.png, which exists for ironboots). Slots per Modules.Equipment.
func cleanAdventurerEquipments() []any {
	mk := func(slot int, key string) any {
		return map[string]any{"type": slot, "key": key, "count": 1, "enchantments": map[string]any{}}
	}
	return []any{
		mk(EquipmentHelmet, "ironhelmet"),
		mk(EquipmentChestplate, "ironchestplate"),
		mk(EquipmentWeapon, "ironsword"),
		mk(EquipmentShield, "ironshield"),
		mk(EquipmentLegplates, "ironlegplates"),
		mk(EquipmentCape, "wings"),
		mk(EquipmentBoots, "ironboots"),
	}
}

// cleanAdventurer is the ONE fully-equipped Player near the hero at (102,96):
// walkable base grass 3907 (the suggested 102,100 is colliding tile 9975 in
// world.json). Type Player=0 so the client resolves the base sprite via the
// entities.ts prefix rule; paperdoll layers come from Equipments (player.ts
// load -> equip) plus the Batch below.
func cleanAdventurer() PlayerData {
	return PlayerData{
		EntityData: EntityData{
			Instance: "p-adv-1", Type: EntityPlayer, Key: "base", Name: "adventurer",
			X: 102, Y: 96, Orientation: intp(OrientationDown),
			Level: intp(1), HitPoints: intp(100), MaxHitPoints: intp(100),
			MovementSpeed: intp(220), AttackRange: intp(1),
		},
		Orientation: OrientationDown, Rank: 0, Pvp: false,
		Mana: intp(50), MaxMana: intp(50),
		Equipments: cleanAdventurerEquipments(),
	}
}

// cleanEquipmentBatch mirrors server handler.ts handleEquipment:
// [8, Batch=0, {data:{equipments:[...]}}] (EquipmentPacket serialize with
// opcode; impl/equipment.ts SerializedEquipment). Sent right after the
// adventurer Spawn so paperdoll layers render even for clients that only
// apply Batch frames.
func cleanEquipmentBatch() []any {
	return pktOp(PacketEquipment, EquipmentBatch, map[string]any{
		"data": map[string]any{"equipments": cleanAdventurerEquipments()},
	})
}

// Combat scene: boss dummy + 4-bot party (COMBAT mode only).
//
// BossDummy: Mob `golem` (sprite mobs/golem.png verified) at (106,96), tile
// 5094 walkable (verified vs world.json collisions/objects), name
// "BossDummy", 5000 HP, static (no roam ticker, no retaliate — the brain
// only ever broadcasts bot->boss frames).
//
// Party (all facing the boss, orientation Right=3; boss faces Left=2):
//   - WarBot  Lv20 warrior at (102,96) tile 3907 walkable, full gold paperdoll
//     inline in Spawn (goldhelmet 0, goldchestplate 3, goldsword 4, goldshield
//     5, goldlegplates 9, goldboots 11 — all sprites verified). Autos 1200ms
//     15-25, Sunder 40/5s (Critical + Boulder + skills).
//   - ArchBot archer at (100,98) tile 3969 walkable, leather set
//     (leatherhelmet 0, leatherchest 3, leatherleggings 9, leatherboots 11) +
//     woodenbow (weaponarcher 4, sprite player/weapon/woodenbow verified) +
//     arrow (arrows slot 2, items/arrow + projectiles/arrow verified).
//     Autos 1600ms 12-20 ranged, Volley 3x30 Critical/6s, Spawn
//     attackRange 8 (ARCHER_ATTACK_RANGE default).
//   - MageBot mage at (102,98) tile 3394 walkable, leather set + naturestaff
//     (weaponmagic 4, sprite player/weapon/naturestaff verified; level 17 in
//     items.json — the spec "naturestaff17" is this item). Autos 2000ms
//     18-28 ranged (purplebolt/greenbolt flavour), Storm 45/7s
//     (Critical + Fireball), Spawn attackRange 9 (items.json).
//   - SupBot support at (100,96) tile 3394 walkable, leather set, NO weapon
//     (no priest player sprites exist — only npcs/priest — so the unarmed
//     leather healer is the priest-look approximation). NEVER attacks: every
//     6s heals the lowest-HP bot if anyone is hurt (Idle anim + Heal packet
//   - Healing FX + Points, amount clamped to missing HP, skipped at full),
//     every 12s party buff (Effect Add DefenseBuff/StrengthSuperBuff on all
//     4 bots).
//
// Ranged flavour: real ranged attacks spawn Projectile entities (type 5) and
// apply damage on impact; the stub collapses flight into the instant Combat
// Hit pipeline (same splat + Points HP bar) with hit.ranged=true, matching
// Hit.serialize. No Equipment Batch is sent: Batch equips game.player (the
// hero), while inline Spawn equipments are applied per-entity via
// player.load -> equip.
const (
	combatDummyInstance  = "m-dummy"
	combatBotInstance    = "p-warbot"
	combatArcherInstance = "p-archer"
	combatMageInstance   = "p-mage"
	combatSupInstance    = "p-support"
	combatDummyX         = 106
	combatDummyY         = 96
	combatBotX           = 102
	combatBotY           = 96
	combatArcherX        = 100
	combatArcherY        = 98
	combatMageX          = 102
	combatMageY          = 98
	combatSupX           = 100
	combatSupY           = 96

	combatAutoMs      = 1200 // warrior player attack cycle (player attackAnimationSpeed=120)
	combatSkillMs     = 5000 // Sunder attempt cadence
	combatAutoMin     = 15
	combatAutoMax     = 25
	combatSkillDamage = 40

	combatArcherAutoMs  = 1600 // archer cycle
	combatArcherSkillMs = 6000 // Volley attempt cadence
	combatArcherAutoMin = 12
	combatArcherAutoMax = 20
	combatVolleyHits    = 3
	combatVolleyDamage  = 30

	combatMageAutoMs  = 2000 // mage cycle
	combatMageSkillMs = 7000 // Storm attempt cadence
	combatMageAutoMin = 18
	combatMageAutoMax = 28
	combatStormDamage = 45

	combatHealMs     = 6000 // support heal cadence (+40 HP)
	combatHealAmount = 40
	combatBuffMs     = 12000 // support party-buff cadence
)

const combatGlobalCD = time.Second // 1s GLOBAL CD shared across skills

// dummyMaxHP/dummyRespawnDelay are env-overridable for fast scripted checks
// (DUMMY_HP/DUMMY_RESPAWN seconds); defaults are the spec values (BossDummy
// 5000 HP, 15s respawn).
var dummyMaxHP = func() int {
	if v := os.Getenv("DUMMY_HP"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 5000
}()

var dummyRespawnDelay = func() time.Duration {
	if v := os.Getenv("DUMMY_RESPAWN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 15 * time.Second
}()

// Bot MaxHP (spawn full; support heals cap here). Boss deals no damage, so
// heals are observable as Heal + Healing-FX + Points packets.
var combatBotMaxHP = map[string]int{
	combatBotInstance:    400,
	combatArcherInstance: 300,
	combatMageInstance:   300,
	combatSupInstance:    300,
}

// combatBotOrder is the spawn/roster order and the heal tiebreak order.
var combatBotOrder = []string{
	combatBotInstance,
	combatArcherInstance,
	combatMageInstance,
	combatSupInstance,
}

var (
	combatOnce sync.Once
	combatMu   sync.Mutex
	combatHP   int
	combatDead bool
	// combatLastSkill is the per-bot 1s GLOBAL skill CD (skills only; autos
	// never consult it). combatBotHP tracks live bot HP for the healer.
	combatLastSkill map[string]time.Time
	combatBotHP     map[string]int
	combatBuffCycle int
)

// warbotEquipments is the full gold paperdoll (slots per Modules.Equipment).
func warbotEquipments() []any {
	mk := func(slot int, key string) any {
		return map[string]any{"type": slot, "key": key, "count": 1, "enchantments": map[string]any{}}
	}
	return []any{
		mk(EquipmentHelmet, "goldhelmet"),
		mk(EquipmentChestplate, "goldchestplate"),
		mk(EquipmentWeapon, "goldsword"),
		mk(EquipmentShield, "goldshield"),
		mk(EquipmentLegplates, "goldlegplates"),
		mk(EquipmentBoots, "goldboots"),
	}
}

func warbot() PlayerData {
	return PlayerData{
		EntityData: EntityData{
			Instance: combatBotInstance, Type: EntityPlayer, Key: "base", Name: "WarBot",
			X: combatBotX, Y: combatBotY, Orientation: intp(OrientationRight),
			Level: intp(20), HitPoints: intp(400), MaxHitPoints: intp(400),
			MovementSpeed: intp(220), AttackRange: intp(1),
		},
		Orientation: OrientationRight, Rank: 0, Pvp: false,
		Mana: intp(100), MaxMana: intp(100),
		Equipments: warbotEquipments(),
	}
}

// dummyData builds the dummy Spawn payload with live HP (omitted when dead;
// the respawn broadcast re-spawns it).
func dummyData() (EntityData, bool) {
	combatMu.Lock()
	defer combatMu.Unlock()
	if combatDead {
		return EntityData{}, false
	}
	return EntityData{
		Instance: combatDummyInstance, Type: EntityMob, Key: "golem", Name: "BossDummy",
		X: combatDummyX, Y: combatDummyY, Orientation: intp(OrientationLeft),
		Level: intp(1), HitPoints: intp(combatHP), MaxHitPoints: intp(dummyMaxHP),
		MovementSpeed: intp(150), AttackRange: intp(1),
	}, true
}

// leatherSet is the shared leather paperdoll base (helmet 0, chestplate 3,
// legplates 9, boots 11 — all keys in items.json with client sprites).
func leatherSet() []any {
	mk := func(slot int, key string) any {
		return map[string]any{"type": slot, "key": key, "count": 1, "enchantments": map[string]any{}}
	}
	return []any{
		mk(EquipmentHelmet, "leatherhelmet"),
		mk(EquipmentChestplate, "leatherchest"),
		mk(EquipmentLegplates, "leatherleggings"),
		mk(EquipmentBoots, "leatherboots"),
	}
}

func archerbotEquipments() []any {
	mk := func(slot int, key string) any {
		return map[string]any{"type": slot, "key": key, "count": 1, "enchantments": map[string]any{}}
	}
	return append(leatherSet(),
		mk(EquipmentWeapon, "woodenbow"),
		mk(EquipmentArrows, "arrow"),
	)
}

func magebotEquipments() []any {
	mk := func(slot int, key string) any {
		return map[string]any{"type": slot, "key": key, "count": 1, "enchantments": map[string]any{}}
	}
	return append(leatherSet(),
		mk(EquipmentWeapon, "naturestaff"),
	)
}

func supbotEquipments() []any { return leatherSet() }

// combatBotData builds one party bot Spawn. attackRange is per-class from the
// real server: bows default to ARCHER_ATTACK_RANGE=8 when items.json carries
// no attackRange (e.g. woodenbow; modules.ts:644 + item.ts
// getDefaultAttackRange), staves carry attackRange 9 in items.json (e.g.
// naturestaff), melee/unarmed stay 1 (goldsword has no attackRange, support
// is unarmed).
func combatBotData(instance, name string, x, y, level, hp, maxHP, attackRange int, equip []any) PlayerData {
	return PlayerData{
		EntityData: EntityData{
			Instance: instance, Type: EntityPlayer, Key: "base", Name: name,
			X: x, Y: y, Orientation: intp(OrientationRight),
			Level: intp(level), HitPoints: intp(hp), MaxHitPoints: intp(maxHP),
			MovementSpeed: intp(220), AttackRange: intp(attackRange),
		},
		Orientation: OrientationRight, Rank: 0, Pvp: false,
		Mana: intp(100), MaxMana: intp(100),
		Equipments: equip,
	}
}

func archerbot() PlayerData {
	return combatBotData(combatArcherInstance, "ArchBot",
		combatArcherX, combatArcherY, 20,
		combatBotMaxHP[combatArcherInstance], combatBotMaxHP[combatArcherInstance],
		8,
		archerbotEquipments())
}

func magebot() PlayerData {
	return combatBotData(combatMageInstance, "MageBot",
		combatMageX, combatMageY, 20,
		combatBotMaxHP[combatMageInstance], combatBotMaxHP[combatMageInstance],
		9,
		magebotEquipments())
}

func supbot() PlayerData {
	return combatBotData(combatSupInstance, "SupBot",
		combatSupX, combatSupY, 20,
		combatBotMaxHP[combatSupInstance], combatBotMaxHP[combatSupInstance],
		1,
		supbotEquipments())
}

func combatSpawns() [][]any {
	frames := [][]any{
		pkt(PacketSpawn, warbot()),
		pkt(PacketSpawn, archerbot()),
		pkt(PacketSpawn, magebot()),
		pkt(PacketSpawn, supbot()),
	}
	if d, ok := dummyData(); ok {
		frames = append(frames, pkt(PacketSpawn, d))
	}
	return frames
}

// strikeBossLocked applies dmg to the boss and broadcasts the full damage
// pipeline (server combat.ts sendAttack + character.handleHitPoints shape):
// Animation Attack (bot swing) + Combat Hit (damage splat via client
// handleCombat -> info.create -> Splat float + hurt flash + health bars) +
// Points (boss HP bar via client handlePoints -> setHitPoints). Ranged bots
// set hit.ranged (their real-server path spawns Projectile entities; flight
// is collapsed here into the instant pipeline). Skill strikes additionally
// carry skills (splat skill icon) + an Effect Add impact (Boulder for Sunder,
// Fireball for Storm). Caller holds combatMu; no-op when boss dead.
// At 0 HP the boss despawns (mob death path: Despawn, not the player Death
// scroll packet) and a respawn timer restores full HP + re-spawns it.
func strikeBossLocked(attacker string, dmg, typ int, skills []string, ranged bool, effect int) {
	if combatDead {
		return
	}
	combatHP -= dmg
	if combatHP < 0 {
		combatHP = 0
	}
	hit := HitData{Type: typ, Damage: dmg, Skills: skills}
	if ranged {
		hit.Ranged = boolp(true)
	}
	broadcast(pkt(PacketAnimation, animationData{
		Instance: attacker, Action: ActionAttack,
	}))
	broadcast(pktOp(PacketCombat, CombatHit, combatData{
		Instance: attacker, Target: combatDummyInstance, Hit: hit,
	}))
	if effect >= 0 {
		broadcast(pktOp(PacketEffect, EffectAdd, effectData{
			Instance: combatDummyInstance, Effect: effect,
		}))
	}
	broadcast(pkt(PacketPoints, pointsData{
		Instance:  combatDummyInstance,
		HitPoints: intp(combatHP), MaxHitPoints: intp(dummyMaxHP),
	}))
	log.Printf("combat: %s hit boss type=%d dmg=%d hp=%d/%d", attacker, typ, dmg, combatHP, dummyMaxHP)
	if combatHP <= 0 {
		combatDead = true
		broadcast(pkt(PacketDespawn, despawnData{Instance: combatDummyInstance}))
		log.Printf("combat: boss died -> despawned, respawn in %v", dummyRespawnDelay)
		time.AfterFunc(dummyRespawnDelay, combatRespawn)
	}
}

// trySkill consumes one bot's 1s GLOBAL skill CD and runs fn. Returns false
// when the GCD rejects it (shared per-bot across that bot's skills).
// Caller must hold combatMu (every skill path locks first; autos never
// consult it).
func trySkill(bot string, fn func()) bool {
	now := time.Now()
	if now.Sub(combatLastSkill[bot]) < combatGlobalCD {
		log.Printf("combat: %s skill rejected by GCD (Δ=%v since last skill)", bot, now.Sub(combatLastSkill[bot]))
		return false
	}
	combatLastSkill[bot] = now
	fn()
	return true
}

// Autos NEVER consult the skill GCD; each class ticks at its own speed.
func combatAuto() {
	combatMu.Lock()
	defer combatMu.Unlock()
	strikeBossLocked(combatBotInstance, combatAutoMin+rand.Intn(combatAutoMax-combatAutoMin+1), HitsNormal, nil, false, -1)
}

func archerAuto() {
	combatMu.Lock()
	defer combatMu.Unlock()
	strikeBossLocked(combatArcherInstance, combatArcherAutoMin+rand.Intn(combatArcherAutoMax-combatArcherAutoMin+1), HitsNormal, nil, true, -1)
}

func mageAuto() {
	combatMu.Lock()
	defer combatMu.Unlock()
	strikeBossLocked(combatMageInstance, combatMageAutoMin+rand.Intn(combatMageAutoMax-combatMageAutoMin+1), HitsNormal, nil, true, -1)
}

// combatSunder attempts one Sunder (Critical + Boulder, 40 dmg). Returns
// false when the 1s GLOBAL CD rejects it (shared across skills via
// combatLastSkill); true when cast (or fizzled on a dead boss — the GCD is
// still consumed).
func combatSunder() bool {
	combatMu.Lock()
	defer combatMu.Unlock()
	return trySkill(combatBotInstance, func() {
		if combatDead {
			log.Printf("combat: sunder fizzles (boss dead)")
			return
		}
		strikeBossLocked(combatBotInstance, combatSkillDamage, HitsCritical, []string{"sunder"}, false, EffectBoulder)
	})
}

// combatVolley attempts one Volley: 3x Critical 30 arrow hits (skills
// ["volley"]) under a single GCD consumption. Returns false on GCD reject.
func combatVolley() bool {
	combatMu.Lock()
	defer combatMu.Unlock()
	return trySkill(combatArcherInstance, func() {
		if combatDead {
			log.Printf("combat: volley fizzles (boss dead)")
			return
		}
		for i := 0; i < combatVolleyHits; i++ {
			strikeBossLocked(combatArcherInstance, combatVolleyDamage, HitsCritical, []string{"volley"}, true, -1)
		}
	})
}

// combatStorm attempts one Storm (Critical 45 + Fireball, skills ["storm"]).
// Returns false on GCD reject.
func combatStorm() bool {
	combatMu.Lock()
	defer combatMu.Unlock()
	return trySkill(combatMageInstance, func() {
		if combatDead {
			log.Printf("combat: storm fizzles (boss dead)")
			return
		}
		strikeBossLocked(combatMageInstance, combatStormDamage, HitsCritical, []string{"storm"}, true, EffectFireball)
	})
}

// combatHeal heals the lowest-HP bot, clamped to its missing HP (skipped
// while all bots are full — the BossDummy never retaliates, so the healer
// stays silent until something can actually wound the party), and broadcasts
// Heal (green +HP splat via client handleHeal) + Healing FX + Points (bot HP
// bar). The heal anim is Idle: no weapon swing. The support NEVER attacks
// the boss.
func combatHeal() {
	combatMu.Lock()
	defer combatMu.Unlock()
	target := combatBotOrder[0]
	lowest := combatBotHP[target] - combatBotMaxHP[target]
	for _, b := range combatBotOrder[1:] {
		if d := combatBotHP[b] - combatBotMaxHP[b]; d < lowest {
			lowest, target = d, b
		}
	}
	missing := combatBotMaxHP[target] - combatBotHP[target]
	if missing <= 0 {
		log.Printf("combat: support heal skipped (all bots full)")
		return
	}
	amount := combatHealAmount
	if amount > missing {
		amount = missing
	}
	hp := combatBotHP[target] + amount
	combatBotHP[target] = hp
	broadcast(pkt(PacketAnimation, animationData{
		Instance: combatSupInstance, Action: ActionIdle,
	}))
	broadcast(pkt(PacketHeal, healData{
		Instance: target, Type: "hitpoints", Amount: amount,
	}))
	broadcast(pktOp(PacketEffect, EffectAdd, effectData{
		Instance: target, Effect: EffectHealing,
	}))
	broadcast(pkt(PacketPoints, pointsData{
		Instance: target, HitPoints: intp(hp), MaxHitPoints: intp(combatBotMaxHP[target]),
	}))
	log.Printf("combat: support healed %s +%d hp=%d/%d", target, amount, hp, combatBotMaxHP[target])
}

// combatBuffTick attempts the buff under the support's GCD; when the 6s heal
// tick wins the race in the same second, retry once after the GCD clears so
// no buff cycle is ever silently dropped.
func combatBuffTick() {
	combatMu.Lock()
	ok := trySkill(combatSupInstance, func() { combatBuffNoLock() })
	combatMu.Unlock()
	if !ok {
		time.AfterFunc(combatGlobalCD+100*time.Millisecond, func() {
			combatMu.Lock()
			defer combatMu.Unlock()
			trySkill(combatSupInstance, func() { combatBuffNoLock() })
		})
	}
}

// combatBuffNoLock is the single party-buff body (Effect Add on all 4 bots,
// alternating DefenseBuff / StrengthSuperBuff each cycle). Caller must hold
// combatMu; the buff ticker (combatBuffTick) is its only caller.

func combatBuffNoLock() {
	effect := EffectDefenseBuff
	name := "DefenseBuff"
	if combatBuffCycle%2 == 1 {
		effect = EffectStrengthSuperBuf
		name = "StrengthSuperBuff"
	}
	combatBuffCycle++
	for _, b := range combatBotOrder {
		broadcast(pktOp(PacketEffect, EffectAdd, effectData{
			Instance: b, Effect: effect,
		}))
	}
	log.Printf("combat: support party buff %s on 4 bots", name)
}

// combatSkillTick fires every 5s: one real Sunder attempt plus an immediate
// probe that the GCD must reject (observable in logs as exactly one sunder
// emission per window).
func combatSkillTick() {
	combatSunder()
	combatSunder()
}

// volleySkillTick fires every 6s: one Volley plus a GCD-rejected probe.
func volleySkillTick() {
	combatVolley()
	combatVolley()
}

// stormSkillTick fires every 7s: one Storm plus a GCD-rejected probe.
func stormSkillTick() {
	combatStorm()
	combatStorm()
}

func combatRespawn() {
	combatMu.Lock()
	combatHP = dummyMaxHP
	combatDead = false
	combatMu.Unlock()
	if d, ok := dummyData(); ok {
		broadcast(pkt(PacketSpawn, d))
	}
	log.Printf("combat: boss respawned full HP=%d", dummyMaxHP)
}

// startCombat launches the party brain (once): warrior auto 1200ms + Sunder
// 5s, archer auto 1600ms + Volley 6s, mage auto 2000ms + Storm 7s, support
// heal 6s + buff 12s. Broadcasts are no-ops with no subscribers.
func startCombat() {
	combatOnce.Do(func() {
		combatMu.Lock()
		combatHP = dummyMaxHP
		combatLastSkill = make(map[string]time.Time)
		combatBotHP = make(map[string]int, len(combatBotOrder))
		for _, b := range combatBotOrder {
			combatBotHP[b] = combatBotMaxHP[b]
		}
		combatMu.Unlock()
		go func() {
			warAutoT := time.NewTicker(combatAutoMs * time.Millisecond)
			defer warAutoT.Stop()
			skillT := time.NewTicker(combatSkillMs * time.Millisecond)
			defer skillT.Stop()
			archAutoT := time.NewTicker(combatArcherAutoMs * time.Millisecond)
			defer archAutoT.Stop()
			volleyT := time.NewTicker(combatArcherSkillMs * time.Millisecond)
			defer volleyT.Stop()
			mageAutoT := time.NewTicker(combatMageAutoMs * time.Millisecond)
			defer mageAutoT.Stop()
			stormT := time.NewTicker(combatMageSkillMs * time.Millisecond)
			defer stormT.Stop()
			healT := time.NewTicker(combatHealMs * time.Millisecond)
			defer healT.Stop()
			buffT := time.NewTicker(combatBuffMs * time.Millisecond)
			defer buffT.Stop()
			for {
				select {
				case <-warAutoT.C:
					combatAuto()
				case <-skillT.C:
					combatSkillTick()
				case <-archAutoT.C:
					archerAuto()
				case <-volleyT.C:
					volleySkillTick()
				case <-mageAutoT.C:
					mageAuto()
				case <-stormT.C:
					stormSkillTick()
				case <-healT.C:
					combatHeal()
				case <-buffT.C:
					combatBuffTick()
				}
			}
		}()
	})
}

// spawnFrames returns Spawn frames: guest p2, every oak, the 232-entity
// showcase grid (156 m-show-* Mobs + 76 n-show-* NPCs, Type Mob=3 / NPC=1 so
// the client resolves sprites mobs/<key> / npcs/<key> via the entities.ts
// prefix rule), and 2 paperdoll demo players. Levels/HP are flat defaults
// (mobs + NPCs 1/50). NPCs idle statically; mobs never roam so the formation
// holds for screenshots. Resource State reflects the LIVE depleted state
// (reconnects during a depletion window see the stump, not a phantom tree).
func spawnFrames() [][]any {
	if combatMode {
		// COMBAT: hero arrives via Welcome; 4 bots + Boss Spawns only.
		return combatSpawns()
	}
	if cleanMode {
		// CLEAN: hero arrives via Welcome; exactly one adventurer Spawn
		// plus the Equipment Batch (total 2 players, zero overlays).
		return [][]any{
			pkt(PacketSpawn, cleanAdventurer()),
			cleanEquipmentBatch(),
		}
	}
	gx, gy := 102, 98
	if testMode {
		gx, gy = 101, 96
	}
	other := EntityData{
		Instance:      "p2",
		Type:          EntityPlayer,
		Key:           "base",
		Name:          "guest",
		X:             gx,
		Y:             gy,
		Orientation:   intp(OrientationDown),
		Level:         intp(1),
		HitPoints:     intp(100),
		MaxHitPoints:  intp(100),
		MovementSpeed: intp(220),
		AttackRange:   intp(1),
	}
	rat := EntityData{
		Instance:      "m1",
		Type:          EntityMob,
		Key:           "rat",
		Name:          "Rat",
		X:             104,
		Y:             104,
		Orientation:   intp(OrientationDown),
		Level:         intp(1),
		HitPoints:     intp(30),
		MaxHitPoints:  intp(30),
		MovementSpeed: intp(220),
		AttackRange:   intp(1),
	}
	frames := [][]any{
		pkt(PacketSpawn, other),
	}
	if !testMode {
		frames = append(frames, pkt(PacketSpawn, rat))
	} else {
		for i, key := range showMobs {
			x, y := showPos(i)
			frames = append(frames, pkt(PacketSpawn, EntityData{
				Instance:      fmt.Sprintf("m-show-%d", i+1),
				Type:          EntityMob,
				Key:           key,
				Name:          key,
				X:             x,
				Y:             y,
				Orientation:   intp(OrientationDown),
				Level:         intp(1),
				HitPoints:     intp(50),
				MaxHitPoints:  intp(50),
				MovementSpeed: intp(150),
				AttackRange:   intp(1),
			}))
		}
		for i, key := range showNPCs {
			x, y := showPos(len(showMobs) + i)
			frames = append(frames, pkt(PacketSpawn, EntityData{
				Instance:      fmt.Sprintf("n-show-%d", i+1),
				Type:          EntityNPC,
				Key:           key,
				Name:          key,
				X:             x,
				Y:             y,
				Orientation:   intp(OrientationDown),
				Level:         intp(1),
				HitPoints:     intp(50),
				MaxHitPoints:  intp(50),
				MovementSpeed: intp(150),
				AttackRange:   intp(1),
			}))
		}
		for _, p := range demoPlayers() {
			p := p
			frames = append(frames, pkt(PacketSpawn, p))
		}
	}
	treesMu.Lock()
	defer treesMu.Unlock()
	for _, oak := range oakSpawns {
		oak := oak
		if st, ok := trees[oak.Instance]; ok && st.depleted {
			oak.State = intp(ResourceStateDepleted)
		} else {
			oak.State = intp(ResourceStateDefault)
		}
		frames = append(frames, pkt(PacketSpawn, oak))
	}
	return frames
}

// Static vs entity trees: STATIC map trees are colliding tiles (a layer id in
// world.json `collisions`, flagged c:true by buildTile above). The client
// blocks those itself: grid defaults to 1, loadRegionTileData clears only on
// !tile.c (map.ts:169-172), and handleRequestPath refuses colliding targets
// (player/handler.ts:56). ENTITY trees (our oak t1) instead stand on a
// WALKABLE tile (c:false); nothing in the tile grid blocks them. The real
// server blocks them in map.isColliding via the entity grid — hasEntityAt +
// isResource (map.ts:246-254) — and enforces it in setPosition/verifyCollision
// (player.ts:1579-1580,693-716: teleport back + cheat score). `high` is NOT a
// collision source: the server never consults it for `c`, it only drives the
// foreground render layer (canopy drawn over the player). The stub mirrors
// this split: tiles carry c, entities are tracked below.

// resourceEntities are the blocking entity-grid occupants: instance -> x,y.
// Only RESOURCES block (server isColliding checks entity.isResource());
// players/mobs are Characters and never block movement. Derived from
// oakSpawns at init so tiles and entities can never drift apart.
var resourceEntities = func() map[string][2]int {
	m := make(map[string][2]int, len(oakSpawns))
	for _, o := range oakSpawns {
		m[o.Instance] = [2]int{o.X, o.Y}
	}
	return m
}()

// tileBlocked mirrors server map.isCollisionIndex + isOutOfBounds
// (map.ts:170,201-208): OOB or empty data blocks; otherwise any layer whose
// unflipped id is in `collisions` blocks. Objects are also treated as
// blocking here to match the c flag buildTile emits (client refuses those
// requests itself anyway). Dynamic areas/doors/noclip are out of scope.
// In test mode the same real-terrain rule applies on top of the cloned base,
// with overlays: pond water always blocks, forced-grass tiles (oaks, showcase
// grid, demos) always walk.
func tileBlocked(x, y int) bool {
	if testMode && !cleanMode && !combatMode {
		if x < 0 || y < 0 {
			return true
		}
		if isTestWater(x, y) {
			return true
		}
		if isTestGrass(x, y) {
			return false
		}
	}
	loadWorld()
	if x < 0 || y < 0 || x >= world.Width || y >= world.Height {
		return true
	}
	idx := y*world.Width + x
	if idx < 0 || idx >= len(world.Data) {
		return true
	}
	raw := world.Data[idx]
	if string(raw) == "0" || string(raw) == "[]" || string(raw) == "null" {
		return true
	}
	var layers []int
	var num float64
	if err := json.Unmarshal(raw, &num); err == nil {
		if num < 1 {
			return true
		}
		layers = []int{int(num)}
	} else {
		var arr []float64
		if err := json.Unmarshal(raw, &arr); err != nil || len(arr) == 0 {
			return true
		}
		layers = make([]int, len(arr))
		for i, v := range arr {
			layers[i] = int(v)
		}
	}
	for _, id := range layers {
		u := unflipTile(id)
		if collSet[u] || objSet[u] {
			return true
		}
	}
	return false
}

// resourceAt returns the instance occupying (x,y), if it is a resource.
func resourceAt(x, y int) string {
	for inst, pos := range resourceEntities {
		if pos[0] == x && pos[1] == y {
			return inst
		}
	}
	return ""
}

// blocked reports whether (x,y) rejects movement: static collision or a
// resource-entity occupant (server map.isColliding:246-254).
func blocked(x, y int) bool {
	return tileBlocked(x, y) || resourceAt(x, y) != ""
}

// session tracks one connection's player grid pos (from Welcome spawn,
// updated by Started/Step/Stop reports) for teleport-back on reject, plus
// the last resource target (Request/Started/Follow/Entity carry
// targetInstance; Step does not) so Step can apply the same chop-approach
// exception as Request. lastStep + cheatScore implement the M2 speed
// anticheat (movementSpeed ms/tile, 2-tile grace, teleport-back, >15
// disconnect — cf. player.ts handleMovementRequest/Step + handler.ts:809).
type session struct {
	playerX, playerY int
	target           string
	lastStep         time.Time
	movementSpeed    int // ms per tile (Welcome default 220)
	cheatScore       int
}

// stopPlayer mirrors server stopMovement/teleport-back: halt the client
// entity (connection.ts:461-463 entity.stop()) and snap it to the last
// valid tile (Teleport [12,{instance,x,y}], connection.ts:478-501),
// plus the authoritative List.Positions reply (regions.ts
// sendEntityPositions) so the client resyncs on mismatch.
func stopPlayer(conn *websocket.Conn, s *session, instance string) {
	_ = send(conn, pktOp(PacketMovement, MovementStop, serverMovement{Instance: instance}))
	_ = send(conn, pkt(PacketTeleport, teleportData{Instance: instance, X: s.playerX, Y: s.playerY}))
	_ = send(conn, pktOp(PacketList, ListPositions, map[string]any{
		"positions": map[string]any{instance: map[string]any{"x": s.playerX, "y": s.playerY}},
	}))
}

// List opcodes (Opcodes.List in opcodes.ts): Spawns0 Positions1.
const (
	ListSpawns    = 0
	ListPositions = 1
)

// targetsResource reports whether any of the given target instances is the
// resource occupying (x,y). Empty occupant never matches, so static
// collisions are always rejected even with no target set.
func targetsResource(x, y int, targets ...string) bool {
	occ := resourceAt(x, y)
	if occ == "" {
		return false
	}
	for _, t := range targets {
		if t != "" && t == occ {
			return true
		}
	}
	return false
}

// rejectLocked records one cheat/collision strike: teleport-back + Positions
// reply; over 15 disconnects the conn (handler.ts cheatScore gate).
// Returns true when the caller should drop the connection.
func rejectLocked(conn *websocket.Conn, c *playerConn, reason string) bool {
	c.sess.cheatScore++
	n := c.sess.cheatScore
	stopPlayer(conn, &c.sess, c.instance)
	updateClientRegion(c)
	log.Printf("anticheat: %s instance=%s score=%d", reason, c.instance, n)
	if n > 15 {
		log.Printf("anticheat: disconnecting %s (score %d > 15)", c.instance, n)
		return true
	}
	return false
}

// checkSpeed enforces max tiles/sec vs movementSpeed with a 2-tile grace
// (player.ts verifyMovement margin + handleMovementRequest diff>2 noclip).
// Returns true when the step is too fast (caller rejects).
// Divergence note: truth uses a 1.5s region grace with latency subtracted
// from the interval; here we keep the coarser 2-tile + 2s idle leniency.
func checkSpeed(s *session, tiles int) bool {
	if s.movementSpeed <= 0 {
		s.movementSpeed = 220
	}
	now := time.Now()
	if s.lastStep.IsZero() {
		s.lastStep = now
		return false
	}
	// Grace: first step after idle (>2s) always passes (region-change rule).
	if now.Sub(s.lastStep) > 2*time.Second {
		s.lastStep = now
		return false
	}
	if tiles < 1 {
		tiles = 1
	}
	minInterval := time.Duration(s.movementSpeed) * time.Millisecond
	// 5% margin like verifyMovement, per-tile with no +2 padding:
	// allow tiles worth of interval before flagging.
	allowance := time.Duration(float64(minInterval) * 0.95 * float64(tiles))
	if now.Sub(s.lastStep) < allowance {
		// Sliding window: advance lastStep even on reject so legit
		// players paced at the legal rate never accumulate cheatScore.
		s.lastStep = now
		return true
	}
	s.lastStep = now
	return false
}

// handleMovement enforces collisions the client grid cannot: resource-entity
// tiles (walkable c:false, e.g. oak) plus a backstop for static collisions.
// Request (the client already refuses static targets itself, handler.ts:56):
// reject only when the destination is blocked AND the player is not
// targeting the occupying resource (targeted approaches path adjacent via
// ignores, handler.ts:83). Step applies the SAME exception (chop approach):
// the client already walked locally, so an illegal next tile gets Stop +
// Teleport-back like verifyCollision (player.ts:693-716) — except a step
// onto the currently-targeted resource tile, which Request also allows.
// Request far jumps (>2 tiles, noclip) and too-fast Steps (speed check)
// bump cheatScore with teleport-back; >15 disconnects. Anything else silent.
func handleMovement(conn *websocket.Conn, c *playerConn, mv clientMovement) bool {
	s := &c.sess
	if mv.Opcode == nil {
		return false
	}
	if mv.TargetInstance != "" {
		s.target = mv.TargetInstance
	}
	switch *mv.Opcode {
	case MovementRequest:
		if mv.RequestX == nil || mv.RequestY == nil {
			return false
		}
		dx := abs(*mv.RequestX - s.playerX)
		dy := abs(*mv.RequestY - s.playerY)
		if dx > 2 || dy > 2 {
			// Noclip jump (player.ts handleMovementRequest diff>2).
			return rejectLocked(conn, c, fmt.Sprintf("noclip request %d,%d->%d,%d", s.playerX, s.playerY, *mv.RequestX, *mv.RequestY))
		}
		if checkSpeed(s, dx+dy) {
			return rejectLocked(conn, c, "speed request")
		}
		if blocked(*mv.RequestX, *mv.RequestY) && !targetsResource(*mv.RequestX, *mv.RequestY, mv.TargetInstance) {
			stopPlayer(conn, s, c.instance)
			updateClientRegion(c)
		}
	case MovementStarted:
		if mv.PlayerX != nil && mv.PlayerY != nil {
			dx := abs(*mv.PlayerX - s.playerX)
			dy := abs(*mv.PlayerY - s.playerY)
			if dx > 2 || dy > 2 {
				// Started mismatch: silent resync only (no cheatScore).
				stopPlayer(conn, s, c.instance)
				updateClientRegion(c)
				return false
			}
			s.playerX, s.playerY = *mv.PlayerX, *mv.PlayerY
			setEntityPos(c.instance, s.playerX, s.playerY)
			updateClientRegion(c)
		}
	case MovementStep:
		if mv.NextGridX != nil && mv.NextGridY != nil {
			dx := abs(*mv.NextGridX - s.playerX)
			dy := abs(*mv.NextGridY - s.playerY)
			if dx+dy > 0 && checkSpeed(s, dx+dy) {
				return rejectLocked(conn, c, "speed step")
			}
		}
		if mv.PlayerX != nil && mv.PlayerY != nil {
			s.playerX, s.playerY = *mv.PlayerX, *mv.PlayerY
			setEntityPos(c.instance, s.playerX, s.playerY)
			updateClientRegion(c)
		}
		if mv.NextGridX != nil && mv.NextGridY != nil &&
			blocked(*mv.NextGridX, *mv.NextGridY) &&
			!targetsResource(*mv.NextGridX, *mv.NextGridY, mv.TargetInstance, s.target) {
			stopPlayer(conn, s, c.instance)
			updateClientRegion(c)
		}
	case MovementFollow:
		// Log-only: a Follow carrying a tree targetInstance is just the
		// client pathing adjacent to the oak (approach). The single axe
		// hit comes from the explicit click-on-arrival Target packet.
		log.Printf("movement follow target=%s", mv.TargetInstance)
	case MovementEntity:
		log.Printf("movement entity target=%s", mv.TargetInstance)
	}
	return false
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// handleTarget accepts the click-to-interact packet the client sends on
// arrival: [14, [opcode, instance, x?, y?]] (player/handler.ts handleStopPathing
// -> getTargetType returns Object(3) for resources). ONLY Object(3) on a known
// tree counts as an axe hit (explicit click-on-arrival): walking to the oak
// (Request/Started/Step/Follow/Entity) is approach only and never chops.
// The 600ms per-instance debounce stays as a safety net.
func handleTarget(conn *websocket.Conn, c *playerConn, frame clientFrame) {
	if len(frame) < 2 {
		return
	}
	var msg []json.RawMessage
	if err := json.Unmarshal(frame[1], &msg); err != nil || len(msg) < 2 {
		return
	}
	var opcode int
	var instance string
	if err := json.Unmarshal(msg[0], &opcode); err != nil {
		return
	}
	if err := json.Unmarshal(msg[1], &instance); err != nil {
		return
	}
	log.Printf("target opcode=%d instance=%s", opcode, instance)
	if opcode == TargetObject && isTreeInstance(instance) {
		chopOak(c.instance, instance)
	}
}

// handleCombatReq is log-only: combat frames never chop (only Target Object
// counts as an axe hit).
func handleCombatReq(frame clientFrame) {
	var data json.RawMessage
	switch {
	case len(frame) >= 3:
		data = frame[2]
	case len(frame) >= 2:
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(frame[1], &probe); err != nil {
			return
		}
		data = frame[1]
	default:
		return
	}
	var cd struct {
		Instance string `json:"instance"`
		Target   string `json:"target"`
	}
	if err := json.Unmarshal(data, &cd); err != nil {
		return
	}
	log.Printf("combat instance=%s target=%s", cd.Instance, cd.Target)
}

// handleAnimationReq is log-only: animation echoes never chop (only Target
// Object counts as an axe hit).
func handleAnimationReq(frame clientFrame) {
	if len(frame) < 2 {
		return
	}
	var ad struct {
		Instance         string `json:"instance"`
		ResourceInstance string `json:"resourceInstance"`
	}
	if err := json.Unmarshal(frame[1], &ad); err != nil {
		return
	}
	log.Printf("animation instance=%s resourceInstance=%s", ad.Instance, ad.ResourceInstance)
}

// treeState tracks the chop/shake/depleted/respawn cycle for ONE tree
// instance. Real server flow (resourceskill.ts interact loop): each successful
// hit sends S Animation{instance:player, action:Attack, resourceInstance}
// (client shakes the tree + plays the chop sound, connection.ts
// handleAnimation) via player.sendToRegion (resourceskill.ts:104-110), and
// deplete() fires onStateChange -> Regions push of S Resource{instance,
// state:Depleted} (entities.ts:212-220, resource.ts:36-48) — both REGION
// broadcasts, never unicast. Respawn re-sends state Default the same way.
// Stub tuning (documented): 3 hits, 15 s respawn per instance, independent of
// all other instances. lastHit implements the per-instance 600ms debounce as
// a safety net against duplicate Target arrivals for one physical click.
const (
	oakMaxHits  = 3
	oakRespawn  = 15 * time.Second
	oakDebounce = 600 * time.Millisecond
)

type treeState struct {
	hits     int
	depleted bool
	timer    *time.Timer
	lastHit  time.Time
}

var (
	treesMu sync.Mutex
	trees   = func() map[string]*treeState {
		m := make(map[string]*treeState, len(oakSpawns))
		for _, o := range oakSpawns {
			m[o.Instance] = &treeState{hits: oakMaxHits}
		}
		return m
	}()
)

// isTreeInstance reports whether id is a known cuttable tree instance.
func isTreeInstance(id string) bool {
	treesMu.Lock()
	defer treesMu.Unlock()
	_, ok := trees[id]
	return ok
}

// subs tracks live connections so the respawn timer can restore the tree even
// if the chopping connection is gone. All WS writes flow through the central
// 20Hz tick loop (one bulk write per conn per tick); gorilla/websocket still
// forbids concurrent writers, guarded by writeMu.
var (
	writeMu sync.Mutex
	subsMu  sync.Mutex
	subs    = map[*websocket.Conn]struct{}{}
)

// Entity is the central registry record (M2): every spawned instance with
// its current tile. Covers statics (oaks, showcase, bots, adventurer,
// guest, rat, dummy) plus one entry per connected player.
type Entity struct {
	Instance string
	X, Y     int
}

// Player is the per-connection record (M2): session + queue state.
type playerConn struct {
	conn     *websocket.Conn
	instance string
	sess     session
	outbox   chan []any // queued S->C frames, flushed by the tick loop
	regions  []int      // current 9-region interest set
	dropped  int        // overflow drops (outbox full)
}

var (
	entitiesMu sync.Mutex
	entities   = map[string]*Entity{}

	playersMu sync.Mutex
	players   = map[*websocket.Conn]*playerConn{}
)

const outboxSize = 64

// regionOf maps a tile to its region id.
func regionOf(x, y int) int {
	if sideLen <= 0 {
		return 0
	}
	return (y/mapDivisionSize)*sideLen + (x / mapDivisionSize)
}

// setEntityPos upserts the registry position for an instance.
func setEntityPos(instance string, x, y int) {
	entitiesMu.Lock()
	defer entitiesMu.Unlock()
	if e, ok := entities[instance]; ok {
		e.X, e.Y = x, y
		return
	}
	entities[instance] = &Entity{Instance: instance, X: x, Y: y}
}

// entityPos returns the registry tile for an instance.
func entityPos(instance string) (int, int, bool) {
	entitiesMu.Lock()
	defer entitiesMu.Unlock()
	e, ok := entities[instance]
	if !ok {
		return 0, 0, false
	}
	return e.X, e.Y, true
}

// updateClientRegion recomputes a conn's 9-region interest set from its
// authoritative player tile (surroundingRegions + tile→region math).
func updateClientRegion(c *playerConn) {
	loadWorld()
	rid := regionOf(c.sess.playerX, c.sess.playerY)
	playersMu.Lock()
	c.regions = surroundingRegions(rid)
	playersMu.Unlock()
}

// clientInterested reports whether conn's regions include the entity tile.
func clientInterested(c *playerConn, x, y int) bool {
	rid := regionOf(x, y)
	playersMu.Lock()
	regions := c.regions
	playersMu.Unlock()
	for _, r := range regions {
		if r == rid {
			return true
		}
	}
	return false
}

// regionScoped reports whether a packet id is region-scoped (interest-routed)
// vs global fan-out. Matches the M2 scope: Spawn/Movement/Animation/Combat/
// Resource/Effect route by entity tile; everything else (banner, shutdown,
// Welcome/Map/Teleport/Points/Heal/Despawn/List/Sync/Equipment...) fans out
// or unicasts as before.
func regionScoped(id int) bool {
	switch id {
	case PacketSpawn, PacketMovement, PacketAnimation, PacketCombat, PacketResource, PacketEffect:
		return true
	}
	return false
}

// frameInstance extracts the entity instance from an S->C frame's data payload.
func frameInstance(frame []any) string {
	if len(frame) < 2 {
		return ""
	}
	data := frame[len(frame)-1]
	raw, err := json.Marshal(data)
	if err != nil {
		return ""
	}
	var probe struct {
		Instance string `json:"instance"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ""
	}
	return probe.Instance
}

// enqueueTo queues frames for one conn (drop+count on overflow, size 64).
func enqueueTo(conn *websocket.Conn, frames ...[]any) {
	playersMu.Lock()
	c, ok := players[conn]
	playersMu.Unlock()
	if !ok {
		return
	}
	for _, f := range frames {
		select {
		case c.outbox <- f:
		default:
			playersMu.Lock()
			c.dropped++
			n := c.dropped
			playersMu.Unlock()
			log.Printf("outbox overflow instance=%s dropped=%d", c.instance, n)
		}
	}
}

// enqueueGlobal queues frames for every live connection.
func enqueueGlobal(frames ...[]any) {
	playersMu.Lock()
	conns := make([]*playerConn, 0, len(players))
	for _, c := range players {
		conns = append(conns, c)
	}
	playersMu.Unlock()
	for _, c := range conns {
		for _, f := range frames {
			select {
			case c.outbox <- f:
			default:
				playersMu.Lock()
				c.dropped++
				n := c.dropped
				playersMu.Unlock()
				log.Printf("outbox overflow instance=%s dropped=%d", c.instance, n)
			}
		}
	}
}

// sendDirect writes one bulk message immediately (5s deadline), bypassing
// the tick outbox. Used only for the initial spawn burst (240 Spawn frames
// exceed the 64-slot outbox); all steady-state traffic goes via send().
func sendDirect(conn *websocket.Conn, frames ...[]any) error {
	msg := bulk(frames...)
	fmt.Printf("TX %s\n", msg)
	writeMu.Lock()
	defer writeMu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return conn.WriteMessage(websocket.TextMessage, msg)
}

// send queues unicast frames for one conn (flushed by the tick loop) and
// logs the bulk. Keeps the original signature so call sites are unchanged.
func send(conn *websocket.Conn, frames ...[]any) error {
	msg := bulk(frames...)
	fmt.Printf("TX %s\n", msg)
	enqueueTo(conn, frames...)
	return nil
}

// broadcast routes each frame: region-scoped packets only enqueue to conns
// whose interest set includes the entity tile; global events fan out.
// Queued into tick outboxes (never direct-written); shapes unchanged.
func broadcast(frames ...[]any) {
	msg := bulk(frames...)
	fmt.Printf("TX %s\n", msg)
	for _, f := range frames {
		if len(f) == 0 {
			continue
		}
		id, ok := f[0].(int)
		if !ok {
			enqueueGlobal(f)
			continue
		}
		if !regionScoped(id) {
			enqueueGlobal(f)
			continue
		}
		inst := frameInstance(f)
		x, y, found := entityPos(inst)
		if !found {
			enqueueGlobal(f)
			continue
		}
		playersMu.Lock()
		conns := make([]*playerConn, 0, len(players))
		for _, c := range players {
			conns = append(conns, c)
		}
		playersMu.Unlock()
		for _, c := range conns {
			if clientInterested(c, x, y) {
				select {
				case c.outbox <- f:
				default:
					playersMu.Lock()
					c.dropped++
					n := c.dropped
					playersMu.Unlock()
					log.Printf("outbox overflow instance=%s dropped=%d", c.instance, n)
				}
			}
		}
	}
}

// startTickLoop launches the central 20Hz flush loop (one ticker per
// process): every 50ms each conn's queued frames flush as a single bulk
// write (5s write deadline; dead conns dropped + Despawn broadcast).
var tickOnce sync.Once

func startTickLoop() {
	tickOnce.Do(func() {
		go func() {
			t := time.NewTicker(50 * time.Millisecond)
			defer t.Stop()
			for range t.C {
				playersMu.Lock()
				conns := make([]*playerConn, 0, len(players))
				for _, c := range players {
					conns = append(conns, c)
				}
				playersMu.Unlock()
				for _, c := range conns {
					var frames [][]any
					for {
						select {
						case f := <-c.outbox:
							frames = append(frames, f)
						default:
							goto drained
						}
					}
				drained:
					if len(frames) == 0 {
						continue
					}
					msg := bulk(frames...)
					fmt.Printf("TX %s\n", msg)
					writeMu.Lock()
					_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
					err := c.conn.WriteMessage(websocket.TextMessage, msg)
					writeMu.Unlock()
					if err != nil {
						log.Printf("tick write failed instance=%s: %v", c.instance, err)
						removeClient(c.conn)
					}
				}
			}
		}()
	})
}

// removeClient drops a dead conn: subs cleanup + registry removal +
// Despawn broadcast for its player (reconnect path).
func removeClient(conn *websocket.Conn) {
	playersMu.Lock()
	c, ok := players[conn]
	if ok {
		delete(players, conn)
	}
	playersMu.Unlock()
	subsMu.Lock()
	delete(subs, conn)
	subsMu.Unlock()
	if !ok {
		return
	}
	entitiesMu.Lock()
	delete(entities, c.instance)
	entitiesMu.Unlock()
	_ = conn.Close()
	broadcast(pkt(PacketDespawn, despawnData{Instance: c.instance}))
	log.Printf("client removed: instance=%s (despawn broadcast)", c.instance)
}

// chopOak registers one axe hit on the given tree instance: enqueue S
// Animation (client shake + chop sound), decrement its hits; at 0 enqueue S
// Resource{state:Depleted} (stump frame) and start its own respawn timer ->
// enqueue S Resource{state:Default} + reset hits. Enqueue (not direct write)
// matches resourceskill sendToRegion + entities onStateChange Regions push;
// the tick loop flushes. Unknown or depleted instances are ignored (logged);
// the per-instance 600ms debounce stays as a safety net; each instance
// depletes/respawns independently. ONLY handleTarget (TargetObject) calls
// this — walking to the oak never chops.
func chopOak(attacker, instance string) {
	treesMu.Lock()
	st, ok := trees[instance]
	if !ok {
		treesMu.Unlock()
		return
	}
	if st.depleted {
		log.Printf("oak %s chop ignored (depleted)", instance)
		treesMu.Unlock()
		return
	}
	now := time.Now()
	if !st.lastHit.IsZero() && now.Sub(st.lastHit) < oakDebounce {
		log.Printf("oak %s chop debounced (%v since last hit)", instance, now.Sub(st.lastHit))
		treesMu.Unlock()
		return
	}
	st.lastHit = now
	st.hits--
	hits := st.hits
	depleted := hits <= 0
	if depleted {
		st.depleted = true
	}
	treesMu.Unlock()

	broadcast(pkt(PacketAnimation, animationData{
		Instance:         attacker,
		Action:           ActionAttack,
		ResourceInstance: instance,
	}))
	log.Printf("oak %s chopped (chop sound), hits left=%d", instance, hits)

	if !depleted {
		return
	}
	broadcast(pkt(PacketResource, resourceData{Instance: instance, State: ResourceStateDepleted}))
	log.Printf("oak %s depleted -> stump frame, respawn in %v", instance, oakRespawn)
	treesMu.Lock()
	st.timer = time.AfterFunc(oakRespawn, func() {
		treesMu.Lock()
		st.hits = oakMaxHits
		st.depleted = false
		st.timer = nil
		treesMu.Unlock()
		broadcast(pkt(PacketResource, resourceData{Instance: instance, State: ResourceStateDefault}))
		log.Printf("oak %s respawned (state 0, hits reset to %d)", instance, oakMaxHits)
	})
	treesMu.Unlock()
}

// clientFrame is a generic C->S frame: [packetId, data?] (socket.ts send).
type clientFrame []json.RawMessage

// handleList answers a C->S List request (packet 6, no payload used —
// incoming.ts routes it to updateEntityList): S List Spawns lists every
// entity instance in the requester's regions, S List Positions carries the
// authoritative grid pos of each Character-like entity there (regions.ts
// sendEntities/sendEntityPositions). The client diffs Spawns vs its spawned
// set and asks for the missing ones via Who.
func handleList(conn *websocket.Conn, c *playerConn) {
	playersMu.Lock()
	regions := append([]int(nil), c.regions...)
	playersMu.Unlock()
	regionSet := make(map[int]bool, len(regions))
	for _, r := range regions {
		regionSet[r] = true
	}
	entitiesMu.Lock()
	var ids []string
	positions := make(map[string]any)
	for _, e := range entities {
		if regionSet[regionOf(e.X, e.Y)] {
			ids = append(ids, e.Instance)
			positions[e.Instance] = map[string]any{"x": e.X, "y": e.Y}
		}
	}
	entitiesMu.Unlock()
	if ids == nil {
		ids = []string{}
	}
	_ = send(conn, pktOp(PacketList, ListSpawns, map[string]any{"entities": ids}))
	_ = send(conn, pktOp(PacketList, ListPositions, map[string]any{"positions": positions}))
	log.Printf("list reply instance=%s entities=%d", c.instance, len(ids))
}

// handleWho answers C->S Who (packet 7, [ids]): one S Spawn per known live
// entity (incoming.ts handleWho). Unknown ids are ignored (logged).
func handleWho(conn *websocket.Conn, frame clientFrame) {
	if len(frame) < 2 {
		return
	}
	var ids []string
	if err := json.Unmarshal(frame[1], &ids); err != nil {
		log.Printf("who parse fail: %v", err)
		return
	}
	for _, id := range ids {
		payload, ok := spawnPayload(id)
		if !ok {
			log.Printf("who unknown instance=%s", id)
			continue
		}
		_ = send(conn, pkt(PacketSpawn, payload))
	}
	log.Printf("who reply instances=%d", len(ids))
}

// handleSyncReq forwards a C->S Sync (packet 10, PlayerData) to the other
// players whose interest includes the sender (other-player equip/appearance
// broadcast, connection.ts handleSync). Unicast echo is skipped.
func handleSyncReq(c *playerConn, frame clientFrame) {
	if len(frame) < 2 {
		return
	}
	var data map[string]any
	if err := json.Unmarshal(frame[1], &data); err != nil {
		return
	}
	inst, _ := data["instance"].(string)
	if inst == "" {
		inst = c.instance
		data["instance"] = inst
	}
	x, y, found := entityPos(inst)
	if !found {
		x, y = c.sess.playerX, c.sess.playerY
	}
	raw, _ := json.Marshal(data)
	var msg json.RawMessage = raw
	playersMu.Lock()
	conns := make([]*playerConn, 0, len(players))
	for _, o := range players {
		if o != c {
			conns = append(conns, o)
		}
	}
	playersMu.Unlock()
	for _, o := range conns {
		if clientInterested(o, x, y) {
			select {
			case o.outbox <- []any{PacketSync, msg}:
			default:
				playersMu.Lock()
				o.dropped++
				playersMu.Unlock()
			}
		}
	}
	log.Printf("sync forward instance=%s", inst)
}

// handleEquipmentReq records a C->S Equipment frame and emits the Sync
// other-player broadcast (server handler.ts handleEquipment -> sync()).
func handleEquipmentReq(c *playerConn, frame clientFrame) {
	if len(frame) < 2 {
		return
	}
	log.Printf("equipment instance=%s", c.instance)
	// Re-announce appearance to region neighbours as a Sync packet.
	ph := welcomePlayer(c.instance)
	ph.X, ph.Y = c.sess.playerX, c.sess.playerY
	broadcast(pkt(PacketSync, ph))
}

// spawnPayload rebuilds the Spawn payload for a known instance: live trees
// honour depleted state; players echo their Welcome shape at the registry
// pos; statics echo their scenario definition.
func spawnPayload(instance string) (any, bool) {
	x, y, found := entityPos(instance)
	if !found {
		return nil, false
	}
	treesMu.Lock()
	_, isTree := trees[instance]
	treesMu.Unlock()
	if isTree {
		treesMu.Lock()
		depleted := trees[instance].depleted
		treesMu.Unlock()
		st := ResourceStateDefault
		if depleted {
			st = ResourceStateDepleted
		}
		for _, o := range oakSpawns {
			if o.Instance == instance {
				o := o
				o.X, o.Y = x, y
				o.State = intp(st)
				return o, true
			}
		}
	}
	playersMu.Lock()
	for _, c := range players {
		if c.instance == instance {
			playersMu.Unlock()
			ph := welcomePlayer(instance)
			ph.X, ph.Y = x, y
			return ph, true
		}
	}
	playersMu.Unlock()
	if d, ok := staticPayload(instance); ok {
		return d, true
	}
	// Last resort: minimal entity shape so the client can spawn something.
	return EntityData{Instance: instance, Type: EntityMob, Key: "rat", Name: instance, X: x, Y: y}, true
}

// staticPayload returns the scenario Spawn definition for a static instance.
func staticPayload(instance string) (any, bool) {
	for _, f := range spawnFrames() {
		if len(f) < 2 {
			continue
		}
		raw, err := json.Marshal(f[1])
		if err != nil {
			continue
		}
		var probe struct {
			Instance string `json:"instance"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			continue
		}
		if probe.Instance == instance {
			var v any
			if err := json.Unmarshal(raw, &v); err == nil {
				return v, true
			}
			return f[1], true
		}
	}
	return nil, false
}

// initEntities seeds the central registry with every static spawn (oaks,
// showcase grid, demos, guest, bots, dummy, adventurer) so List/Who and
// region routing resolve before any client connects.
func initEntities() {
	loadWorld()
	gx, gy := 102, 98
	if testMode {
		gx, gy = 101, 96
	}
	setEntityPos("p2", gx, gy)
	if !testMode && !cleanMode && !combatMode {
		setEntityPos("m1", 104, 104)
	}
	for _, o := range oakSpawns {
		setEntityPos(o.Instance, o.X, o.Y)
	}
	if testMode && !cleanMode && !combatMode {
		for i, key := range showMobs {
			_ = key
			x, y := showPos(i)
			setEntityPos(fmt.Sprintf("m-show-%d", i+1), x, y)
		}
		for i, key := range showNPCs {
			_ = key
			x, y := showPos(len(showMobs) + i)
			setEntityPos(fmt.Sprintf("n-show-%d", i+1), x, y)
		}
		for _, p := range demoPlayers() {
			setEntityPos(p.Instance, p.X, p.Y)
		}
	}
	if cleanMode {
		setEntityPos("p-adv-1", 102, 96)
	}
	if combatMode {
		setEntityPos(combatBotInstance, combatBotX, combatBotY)
		setEntityPos(combatArcherInstance, combatArcherX, combatArcherY)
		setEntityPos(combatMageInstance, combatMageX, combatMageY)
		setEntityPos(combatSupInstance, combatSupX, combatSupY)
		setEntityPos(combatDummyInstance, combatDummyX, combatDummyY)
	}
	entitiesMu.Lock()
	n := len(entities)
	entitiesMu.Unlock()
	log.Printf("entity registry seeded: %d statics", n)
}

func handleConn(conn *websocket.Conn) {
	subsMu.Lock()
	subs[conn] = struct{}{}
	subsMu.Unlock()

	// Per-connection player record: random Welcome instance + queued outbox.
	inst := newPlayerInstance()
	c := &playerConn{
		conn:     conn,
		instance: inst,
		sess:     session{playerX: 100, playerY: 96, movementSpeed: 220},
		outbox:   make(chan []any, outboxSize),
	}
	playersMu.Lock()
	players[conn] = c
	playersMu.Unlock()
	setEntityPos(inst, 100, 96)
	updateClientRegion(c)
	defer removeClient(conn)

	// S Connected [0,null]: client answers with Handshake{gVer}.
	if err := send(conn, pkt(PacketConnected, nil)); err != nil {
		log.Printf("write connected: %v", err)
		return
	}

	spawnsSent := false
	sendSpawns := func() {
		if spawnsSent {
			return
		}
		spawnsSent = true
		// Perf guard: 240 Spawn frames in one bulk would be a ~40KB message;
		// split into 3 bulks (same Ready flow, back-to-back sends) and log
		// the total JSON bytes so the client OOM risk stays visible.
		all := spawnFrames()
		total := 0
		for i := 0; i < len(all); i += 80 {
			end := i + 80
			if end > len(all) {
				end = len(all)
			}
			chunk := all[i:end]
			raw, _ := json.Marshal(chunk)
			total += len(raw)
			if err := sendDirect(conn, chunk...); err != nil {
				log.Printf("write spawns chunk %d: %v", i/80, err)
				return
			}
		}
		log.Printf("spawns sent: %d frames in %d bulks, %d bytes JSON", len(all), (len(all)+79)/80, total)
	}

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			log.Printf("read: %v", err)
			return
		}
		fmt.Printf("RX %s\n", raw)

		// Accept single [id,data] and bulk [[id,data],...] inbound.
		var outer []json.RawMessage
		if err := json.Unmarshal(raw, &outer); err != nil || len(outer) == 0 {
			continue // non-[ messages are ignored, like the client side
		}
		var frames []clientFrame
		var probe int
		if err := json.Unmarshal(outer[0], &probe); err == nil {
			frames = []clientFrame{outer}
		} else if err := json.Unmarshal(raw, &frames); err != nil {
			continue
		}

		for _, frame := range frames {
			if len(frame) == 0 {
				continue
			}
			var id int
			if err := json.Unmarshal(frame[0], &id); err != nil {
				continue
			}

			switch id {
			case PacketHandshake: // C Handshake{gVer} -> S Handshake{type:client}
				reply := HandshakeData{
					Type:       "client",
					Instance:   c.instance,
					ServerID:   1,
					ServerTime: time.Now().UnixMilli(),
				}
				if err := send(conn, pkt(PacketHandshake, reply)); err != nil {
					log.Printf("write handshake: %v", err)
					return
				}
			case PacketLogin: // C Login (opcode lives inside data) -> Welcome + Map only
				if err := send(conn,
					pkt(PacketWelcome, welcomePlayer(c.instance)),
					buildMapFrame(),
				); err != nil {
					log.Printf("write welcome/map: %v", err)
					return
				}
			case PacketReady: // C Ready{regionsLoaded,userAgent} -> Spawn* (only here)
				sendSpawns()
			case PacketList: // C List request -> Spawns + Positions
				handleList(conn, c)
			case PacketWho: // C Who [newIds] -> Spawn each known
				handleWho(conn, frame)
			case PacketSync: // C Sync PlayerData -> forward to region neighbours
				handleSyncReq(c, frame)
			case PacketEquipment: // C Equipment -> Sync broadcast
				handleEquipmentReq(c, frame)
			case PacketMovement: // C [11,{opcode,...}] -> Stop/Teleport on blocked tiles
				if len(frame) < 2 {
					continue
				}
				var mv clientMovement
				if err := json.Unmarshal(frame[1], &mv); err != nil {
					continue
				}
				if disconnect := handleMovement(conn, c, mv); disconnect {
					return
				}
			case PacketTarget: // C Target [opcode, instance] -> chop on that oak
				handleTarget(conn, c, frame)
			case PacketCombat: // C Combat {instance,target} -> chop on that oak
				handleCombatReq(frame)
			case PacketAnimation: // C Animation {resourceInstance} -> chop on that oak
				handleAnimationReq(frame)
			default:
				// Log-only: stub answers nothing else.
			}
		}
	}
}

func main() {
	startTickLoop()
	initEntities()
	startShowcase()
	if combatMode {
		startCombat()
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("upgrade: %v", err)
			return
		}
		log.Printf("client connected: %s", r.RemoteAddr)
		handleConn(conn)
	})

	fmt.Printf("kaetram-stub listening on %s\n", addr)
	log.Printf("cleanMode=%v testMode=%v combatMode=%v (CLEAN/COMBAT env or --clean/--combat; TESTMAP env or --testmap flag, default ON)", cleanMode, testMode, combatMode)
	if err := http.ListenAndServe(addr, nil); err != nil {
		fmt.Fprintf(os.Stderr, "listen: %v\n", err)
		os.Exit(1)
	}
}
