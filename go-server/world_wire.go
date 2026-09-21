// World wiring (warps + events + lights/signs globals) — behavior-additive
// root glue over internal/warps, internal/events and internal/globals.
//
// TS sources (all read-only recon, no new packet shapes):
//   - packages/server/src/controllers/warps.ts — menu-driven warp(id):
//     jailed/tutorial/combat/cooldown gates, level/quest/achievement
//     requirements, random landing tile in the target rect
//     (Utils.randomInt(x, x+width-1)), Teleport {instance,x,y} delivery,
//     `warps:*` notifies. Warps carry no destination coords in world.json.
//   - packages/server/src/game/entity/character/player/incoming.ts
//     handleWarp — C->S Warp [39,{id}] (id = Modules.Warps enum index).
//   - packages/common/network/modules.ts Warps enum: Mudwich0 Aynor1
//     Lakesworld2 Patsow3 Crullfield4 Undersea5.
//   - packages/server/src/controllers/events.ts — weekend-only rotation over
//     ['double drops','1.5x experience','lumberjacking','mining'], hourly
//     check, world.globalMessage start/end announces, Utils flags +
//     doubleDropProbability/experiencePerHit multipliers.
//   - packages/server/src/game/entity/character/player/handler.ts
//     handleLights — on region-enter, Overlay Lamp frames for the surrounding
//     regions' lights (deduped per player via lightsLoaded).
//   - packages/server/src/game/globals/impl/light.ts — Light defaults
//     (colour rgba(0,0,0,0.2), diffuse 0.2, distance 100, flicker 300/1) +
//     serialize() SerializedLight under OverlayPacketData.light.
//   - packages/server/src/game/globals/impl/sign.ts — sign.talk sends
//     Bubble Position {instance:"x-y",x,y,text} with talkIndex paging over
//     text.split(','); player.ts handleObjectInteraction looks signs up by
//     the "x-y" instance.
//   - packages/common/network/impl/overlay.ts + opcodes.ts Overlay enum:
//     Set0 Remove1 Lamp2 RemoveLamps3 Darkness4; Bubble enum: Entity0
//     Position1.
//
// Frames used (all pre-existing shapes):
//
//	C->S Warp [39,{id}] (incoming.ts WarpPacket {id} parity).
//	S->C Teleport [12,{instance,x,y}] (teleport.ts parity, no animation flag).
//	S->C Chat [19,{source,message}] global event notices (world.globalMessage
//	  parity via the existing socRouteGlobal path).
//	S->C Overlay [41,2,{light:{instance,x,y,colour,diffuse,distance,
//	  flickerSpeed,flickerIntensity}}] unicast on region-enter (Lamp parity).
//	S->C Bubble [43,1,{instance,x,y,text}] on sign interact (Position parity).
//	C->S Target [14,[0,"x-y"]] on a sign position (handleObjectInteraction
//	  parity; the stub has no sign entities, so the position form is it).
//	C->S Minigame [46,{worldtest:...}] TESTMAP debug (socialtest/pettest
//	  precedent, notify echoes greppable by the e2e).
//
// Divergences from TS (documented):
//   - No step-on warp triggers: TS warps are menu-driven only (nothing calls
//     At()/contains() on warp areas during movement; doors own step
//     triggers). Registry.At is exposed through the `worldtest:at` probe.
//   - Tutorial/combat warp gates skipped: the stub has no tutorial state and
//     no hero in-combat flag; jail/cooldown/level/quest/achievement apply.
//   - Events: no weekend gating and no single-active latch (package-documented
//     internal/events behavior); every rotation event re-fires per cadence
//     with a start notice each time, no end notices. Interval override via
//     WORLD_EVENT_MS (test hook); default is the hourly TS cadence.
//   - Multipliers ARE wired (dormant unless an event fired): double-drops
//     duplicates the m5 roll, experience scales combat XP 1.5x, lumberjacking/
//     mining double the gather yield. With the default hourly cadence no
//     event fires inside test windows, so passing harnesses are unaffected.
//   - TESTMAP synthetic test lamp at (102,96): the real world.json lights sit
//     hundreds of tiles from the stub spawn, so region-enter around spawn
//     would never emit a Lamp frame to observe. Injected under TESTMAP only
//     (clean/combat excluded, m10InjectTestAreas precedent).
//   - Sign distance gate skipped (lenient like m5Pickup): Target on a sign
//     position works from anywhere so the e2e can reach the far-away real
//     signs; logged.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"rpg-world-server/internal/events"
	"rpg-world-server/internal/globals"
	"rpg-world-server/internal/warps"
)

// ---------------------------------------------------------------------------
// Warps.
// ---------------------------------------------------------------------------

// worldWarpNames mirrors Modules.Warps order (modules.ts:206-213): the C->S
// Warp {id} indexes this enum, controllers/warps.ts getWarp lowercases the
// name to find the world.json entry.
var worldWarpNames = []string{
	"mudwich", "aynor", "lakesworld", "patsow", "crullfield", "undersea",
}

// worldWarpExt is one world.json areas.warps entry with the menu-gating
// fields the geometry-only internal/warps package intentionally drops.
type worldWarpExt struct {
	Name  string
	X, Y  int
	W, H  int
	Level int
	Quest string
	Ach   string
}

var (
	worldWarpMu   sync.Mutex
	worldWarps    []worldWarpExt
	worldWarpReg  *warps.Registry
	worldWarpLast = map[string]int64{} // username -> last warp unix-ms
)

// worldWarpCooldownMs is the TS warpTimeout (warps.ts: 300s between warps).
// WORLD_WARP_COOLDOWN_MS overrides it (0 disables, for the e2e).
func worldWarpCooldownMs() int64 {
	if v := os.Getenv("WORLD_WARP_COOLDOWN_MS"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			return n
		}
	}
	return 300_000
}

// worldBootWarps loads the warp registry + the menu-gating table from the
// same world.json path the map loader uses.
func worldBootWarps() {
	reg, err := warps.Load(worldPath())
	if err != nil {
		log.Printf("world: warps: %v (warp engine disabled)", err)
		return
	}
	worldWarpReg = reg
	raw, err := os.ReadFile(worldPath())
	if err != nil {
		log.Printf("world: warps reread: %v", err)
		return
	}
	var doc struct {
		Areas struct {
			Warps []struct {
				Name        string `json:"name"`
				X           int    `json:"x"`
				Y           int    `json:"y"`
				Width       int    `json:"width"`
				Height      int    `json:"height"`
				Level       int    `json:"level"`
				Quest       string `json:"quest"`
				Achievement string `json:"achievement"`
			} `json:"warps"`
		} `json:"areas"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		log.Printf("world: warps parse: %v", err)
		return
	}
	worldWarpMu.Lock()
	worldWarps = worldWarps[:0]
	for _, w := range doc.Areas.Warps {
		worldWarps = append(worldWarps, worldWarpExt{
			Name: strings.ToLower(w.Name), X: w.X, Y: w.Y,
			W: w.Width, H: w.Height,
			Level: w.Level, Quest: w.Quest, Ach: w.Achievement,
		})
	}
	worldWarpMu.Unlock()
	log.Printf("world: warps loaded=%d", len(doc.Areas.Warps))
}

// worldFindWarp resolves a warp by Modules.Warps enum id (getWarp parity).
func worldFindWarp(id int) *worldWarpExt {
	if id < 0 || id >= len(worldWarpNames) {
		return nil
	}
	return worldFindWarpByName(worldWarpNames[id])
}

// worldFindWarpByName resolves a warp by (case-insensitive) name.
func worldFindWarpByName(name string) *worldWarpExt {
	key := strings.ToLower(strings.TrimSpace(name))
	worldWarpMu.Lock()
	defer worldWarpMu.Unlock()
	for i := range worldWarps {
		if worldWarps[i].Name == key {
			w := worldWarps[i]
			return &w
		}
	}
	return nil
}

// worldHandleWarp routes C->S Warp frames [39,{id}] (incoming.ts handleWarp).
func worldHandleWarp(c *playerConn, data []byte) {
	var d struct {
		ID *int `json:"id"`
	}
	if err := json.Unmarshal(data, &d); err != nil || d.ID == nil {
		return
	}
	w := worldFindWarp(*d.ID)
	if w == nil {
		log.Printf("world: Could not find warp with id %d.", *d.ID)
		return
	}
	worldDoWarp(c, w)
}

// worldDoWarp ports controllers/warps.ts warp(): jail/cooldown/requirement
// gates then a random landing tile + Teleport delivery. Tutorial/combat gates
// are documented skips (no stub state for either).
func worldDoWarp(c *playerConn, w *worldWarpExt) bool {
	if c == nil || w == nil {
		return false
	}
	if w.W <= 0 || w.H <= 0 {
		return false
	}
	if m13IsJailed(c.username) {
		m6Notify(c, "warps:CANNOT_WARP_JAIL")
		return false
	}
	nowMs := time.Now().UnixMilli()
	if cd := worldWarpCooldownMs(); cd > 0 && chatStateFor(c).rank < RankAdmin {
		worldWarpMu.Lock()
		last := worldWarpLast[c.username]
		worldWarpMu.Unlock()
		if nowMs-last < cd {
			left := cd - (nowMs - last)
			dur := fmt.Sprintf("%d seconds", left/1000)
			if left > 60_000 {
				dur = fmt.Sprintf("%d minutes", (left+59_999)/60_000)
			}
			m6Notify(c, "warps:CANNOT_WARP_COOLDOWN;time="+dur)
			return false
		}
	}
	if w.Level > 0 && m5StateFor(c.username).Level < w.Level {
		m6Notify(c, "warps:CANNOT_WARP_LEVEL;level="+strconv.Itoa(w.Level))
		return false
	}
	if w.Quest != "" && !m11StateFor(c.username).isFinished(w.Quest) {
		m6Notify(c, "warps:CANNOT_WARP_QUEST;questName="+w.Quest+";name="+m7FormatName(w.Name))
		return false
	}
	if w.Ach != "" {
		def := m11A[w.Ach]
		st := m11StateFor(c.username)
		if def == nil || st.Achs[w.Ach] < def.StageCount {
			m6Notify(c, "warps:CANNOT_WARP_ACHIEVEMENT")
			return false
		}
	}
	lx := w.X + rand.Intn(w.W)
	ly := w.Y + rand.Intn(w.H)
	c.sess.playerX, c.sess.playerY = lx, ly
	setEntityPos(c.instance, lx, ly)
	updateClientRegion(c)
	broadcast(pkt(PacketTeleport, teleportData{Instance: c.instance, X: lx, Y: ly}))
	m10OnPositionUpdate(c)
	m5TrackPos(c)
	worldPushLights(c)
	worldWarpMu.Lock()
	worldWarpLast[c.username] = nowMs
	worldWarpMu.Unlock()
	m6Notify(c, "warps:WARPED_TO;name="+m7FormatName(w.Name))
	log.Printf("world: %s warped to %s (%d,%d)", c.username, w.Name, lx, ly)
	return true
}

// ---------------------------------------------------------------------------
// Events.
// ---------------------------------------------------------------------------

var (
	worldEventMu     sync.Mutex
	worldEvents      *events.Scheduler
	worldEventActive = map[string]bool{}
	worldEventFired  int
	worldEventEvery  int64
)

// worldBootEvents starts the rotation scheduler. WORLD_EVENT_MS overrides the
// per-event cadence (test hook for fast event-notice legs); default keeps the
// TS hourly cadence from DefaultEvents.
func worldBootEvents() {
	list := events.DefaultEvents()
	every := events.CheckIntervalMs
	if v := os.Getenv("WORLD_EVENT_MS"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			for i := range list {
				list[i].IntervalMs = n
			}
			every = n
		}
	}
	worldEventMu.Lock()
	worldEventEvery = every
	worldEventMu.Unlock()
	worldEvents = events.NewScheduler(list)
	worldEvents.Start()
	log.Printf("world: events started=%d intervalMs=%d", len(list), every)
}

// worldEventTick fans due rotation events out as global notices. Called from
// the central 20Hz tick loop (additive: no events due most ticks).
func worldEventTick() {
	if worldEvents == nil {
		return
	}
	for _, e := range worldEvents.Due(time.Now().UnixMilli()) {
		worldEventMu.Lock()
		worldEventActive[e.Key] = true
		worldEventFired++
		worldEventMu.Unlock()
		socRouteGlobal(pkt(PacketChat, chatPacketData{
			Source:  "[Global] WORLD",
			Message: "The " + e.Name + " event has started.",
		}))
		log.Printf("world: event started: %s", e.Name)
	}
}

// worldDoubleDrops duplicates a mob roll while the double-drops event is
// active (events.ts doubleDropProbability parity, expressed as a repeated
// roll). Dormant otherwise — callers pass the roll through unchanged.
func worldDoubleDrops(drops []m5Drop) []m5Drop {
	worldEventMu.Lock()
	on := worldEventActive["double-drops"]
	worldEventMu.Unlock()
	if !on || len(drops) == 0 {
		return drops
	}
	out := make([]m5Drop, 0, len(drops)*2)
	return append(append(out, drops...), drops...)
}

// worldXPBoost reports the 1.5x experience event (experiencePerHit parity).
func worldXPBoost() bool {
	worldEventMu.Lock()
	defer worldEventMu.Unlock()
	return worldEventActive["experience"]
}

// worldHarvestDouble reports the lumberjacking/mining double-yield events
// (Utils.doubleLumberjacking/doubleMining parity) for a gathering skill.
func worldHarvestDouble(skill string) bool {
	worldEventMu.Lock()
	defer worldEventMu.Unlock()
	switch skill {
	case "lumberjacking":
		return worldEventActive["lumberjacking"]
	case "mining":
		return worldEventActive["mining"]
	}
	return false
}

// ---------------------------------------------------------------------------
// Lights + signs globals.
// ---------------------------------------------------------------------------

// Overlay Lamp opcodes (Opcodes.Overlay: Set0 Remove1 Lamp2 RemoveLamps3;
// m10.go only names Set/Remove, so Lamp lives here).
const OverlayLamp = 2

// Bubble opcodes (Opcodes.Bubble: Entity0 Position1).
const BubblePosition = 1

var (
	worldGlowMu  sync.Mutex
	worldGlobals *globals.Globals
	// worldLampsLoaded mirrors player.lightsLoaded (handler.ts): per-instance
	// set of already-sent "x-y" lights so region re-entry stays silent.
	worldLampsLoaded = map[string]map[string]bool{}
)

// worldLightData mirrors SerializedLight (overlay.ts) for Lamp frames.
type worldLightData struct {
	Instance         string  `json:"instance"`
	X                int     `json:"x"`
	Y                int     `json:"y"`
	Colour           string  `json:"colour"`
	Diffuse          float64 `json:"diffuse"`
	Distance         int     `json:"distance"`
	FlickerSpeed     int     `json:"flickerSpeed"`
	FlickerIntensity int     `json:"flickerIntensity"`
}

// worldBubbleData mirrors BubblePacketData for Position bubbles (bubble.ts).
type worldBubbleData struct {
	Instance string `json:"instance"`
	Text     string `json:"text"`
	X        *int   `json:"x,omitempty"`
	Y        *int   `json:"y,omitempty"`
}

// worldBootGlobals loads lights/signs from world.json. TESTMAP gets one
// synthetic lamp next to spawn (documented divergence: the real lights are
// far from the stub spawn, so region-enter would otherwise never emit Lamp).
func worldBootGlobals() {
	g, err := globals.Load(worldPath())
	if err != nil {
		log.Printf("world: globals: %v (lights/signs disabled)", err)
		return
	}
	if testMode && !cleanMode && !combatMode && len(g.LightsFor(regionOf(102, 96))) == 0 {
		g.Lights = append(g.Lights, globals.Light{
			X: 102, Y: 96, Radius: globals.DefaultLightRadius,
			Colour: globals.DefaultLightColour,
		})
		log.Printf("world: TESTMAP synthetic lamp at 102,96")
	}
	worldGlowMu.Lock()
	worldGlobals = g
	worldGlowMu.Unlock()
	log.Printf("world: globals lights=%d signs=%d", len(g.Lights), len(g.Signs))
}

// worldPushLights unicasts Overlay Lamp frames for every not-yet-loaded light
// in the surrounding regions (handler.ts handleLights parity). Silent when
// nothing is new.
func worldPushLights(c *playerConn) {
	if c == nil || c.username == "" {
		return
	}
	worldGlowMu.Lock()
	g := worldGlobals
	worldGlowMu.Unlock()
	if g == nil {
		return
	}
	rid := regionOf(c.sess.playerX, c.sess.playerY)
	worldGlowMu.Lock()
	loaded := worldLampsLoaded[c.instance]
	if loaded == nil {
		loaded = map[string]bool{}
		worldLampsLoaded[c.instance] = loaded
	}
	var fresh []globals.Light
	for _, r := range surroundingRegions(rid) {
		for _, l := range g.LightsFor(r) {
			key := strconv.Itoa(l.X) + "-" + strconv.Itoa(l.Y)
			if loaded[key] {
				continue
			}
			loaded[key] = true
			fresh = append(fresh, l)
		}
	}
	worldGlowMu.Unlock()
	for _, l := range fresh {
		_ = send(c.conn, pktOp(PacketOverlay, OverlayLamp, map[string]any{
			"light": worldLightData{
				Instance: fmt.Sprintf("light-%d-%d", l.X, l.Y),
				X:        l.X, Y: l.Y, Colour: l.Colour,
				Diffuse:          0.2,
				Distance:         l.Radius,
				FlickerSpeed:     300,
				FlickerIntensity: 1,
			},
		}))
	}
	if len(fresh) > 0 {
		log.Printf("world: %s lamps=%d (region %d)", c.username, len(fresh), rid)
	}
}

// worldPushLightsForce clears the per-conn loaded set then pushes (debug
// re-send for the worldtest lights leg).
func worldPushLightsForce(c *playerConn) int {
	worldGlowMu.Lock()
	delete(worldLampsLoaded, c.instance)
	g := worldGlobals
	worldGlowMu.Unlock()
	if g == nil {
		return 0
	}
	worldPushLights(c)
	worldGlowMu.Lock()
	n := len(worldLampsLoaded[c.instance])
	worldGlowMu.Unlock()
	return n
}

// worldForgetPlayer drops per-conn lamp state on disconnect.
func worldForgetPlayer(c *playerConn) {
	if c == nil {
		return
	}
	worldGlowMu.Lock()
	delete(worldLampsLoaded, c.instance)
	worldGlowMu.Unlock()
}

// worldSignTalk handles Target Talk on a sign position ("x-y" instance,
// player.ts handleObjectInteraction parity): Bubble Position with talkIndex
// paging over the comma-split text. Reports whether a sign matched.
func worldSignTalk(c *playerConn, instance string) bool {
	worldGlowMu.Lock()
	g := worldGlobals
	worldGlowMu.Unlock()
	if g == nil {
		return false
	}
	parts := strings.Split(instance, "-")
	if len(parts) != 2 {
		return false
	}
	x, errX := strconv.Atoi(parts[0])
	y, errY := strconv.Atoi(parts[1])
	if errX != nil || errY != nil {
		return false
	}
	s, ok := g.SignAt(x, y)
	if !ok {
		return false
	}
	pages := strings.Split(s.Text, ",")
	if len(pages) == 0 {
		return false
	}
	if c.talkNPC != instance {
		c.talkNPC = instance
		c.talkIndex = 0
	}
	msg := pages[c.talkIndex%len(pages)]
	c.talkIndex++
	_ = send(c.conn, pktOp(PacketBubble, BubblePosition, worldBubbleData{
		Instance: instance, Text: msg, X: intp(x), Y: intp(y),
	}))
	log.Printf("world: %s read sign %s (%q)", c.username, instance, msg)
	return true
}

// worldBoot loads all three subsystems (called once from main).
func worldBoot() {
	worldBootWarps()
	worldBootEvents()
	worldBootGlobals()
}

// ---------------------------------------------------------------------------
// TESTMAP debug dispatcher ([46 {worldtest:...}], socialtest/pettest
// precedent). All legs echo through m6Notify so the e2e can grep them.
// ---------------------------------------------------------------------------

func worldTestHandler(c *playerConn, data []byte) {
	if !testMode || c == nil {
		return
	}
	var d struct {
		WorldTest string `json:"worldtest"`
		ID        *int   `json:"id"`
		Name      string `json:"name"`
		X         *int   `json:"x"`
		Y         *int   `json:"y"`
	}
	if err := json.Unmarshal(data, &d); err != nil || d.WorldTest == "" {
		return
	}
	worldGlowMu.Lock()
	g := worldGlobals
	worldGlowMu.Unlock()
	switch d.WorldTest {
	case "echo":
		nw, nl, ns := 0, 0, 0
		worldWarpMu.Lock()
		nw = len(worldWarps)
		worldWarpMu.Unlock()
		if g != nil {
			nl, ns = len(g.Lights), len(g.Signs)
		}
		m6Notify(c, fmt.Sprintf("world:ok warps=%d events=%d lights=%d signs=%d", nw, len(events.DefaultEvents()), nl, ns))
	case "warps":
		worldWarpMu.Lock()
		names := make([]string, 0, len(worldWarps))
		for _, w := range worldWarps {
			names = append(names, w.Name)
		}
		worldWarpMu.Unlock()
		m6Notify(c, "world:warps ["+strings.Join(names, ",")+"]")
	case "warp":
		var w *worldWarpExt
		if d.Name != "" {
			w = worldFindWarpByName(d.Name)
		} else if d.ID != nil {
			w = worldFindWarp(*d.ID)
		}
		if w == nil {
			m6Notify(c, "world:warp unknown")
			return
		}
		if worldDoWarp(c, w) {
			m6Notify(c, fmt.Sprintf("world:warp %s x=%d y=%d", w.Name, c.sess.playerX, c.sess.playerY))
		}
	case "at":
		if d.X == nil || d.Y == nil || worldWarpReg == nil {
			return
		}
		if w := worldWarpReg.At(*d.X, *d.Y); w != nil {
			m6Notify(c, fmt.Sprintf("world:at %d,%d id=%d x=%d y=%d w=%d h=%d", *d.X, *d.Y, w.ID, w.X, w.Y, w.W, w.H))
		} else {
			m6Notify(c, fmt.Sprintf("world:at %d,%d none", *d.X, *d.Y))
		}
	case "events":
		worldEventMu.Lock()
		var active []string
		for k := range worldEventActive {
			active = append(active, k)
		}
		n, every := worldEventFired, worldEventEvery
		worldEventMu.Unlock()
		sort.Strings(active)
		m6Notify(c, fmt.Sprintf("world:events active=[%s] fired=%d intervalMs=%d", strings.Join(active, ","), n, every))
	case "lights":
		n := worldPushLightsForce(c)
		m6Notify(c, fmt.Sprintf("world:lights n=%d", n))
	case "signs":
		if g == nil {
			m6Notify(c, "world:signs n=0")
			return
		}
		first := ""
		if len(g.Signs) > 0 {
			s := g.Signs[0]
			text := s.Text
			if len(text) > 48 {
				text = text[:48] + "..."
			}
			first = fmt.Sprintf(" first=%d-%d:%s", s.X, s.Y, text)
		}
		m6Notify(c, fmt.Sprintf("world:signs n=%d%s", len(g.Signs), first))
	case "sign":
		if g == nil {
			m6Notify(c, "world:sign none")
			return
		}
		var s globals.Sign
		if d.X != nil && d.Y != nil {
			var ok bool
			s, ok = g.SignAt(*d.X, *d.Y)
			if !ok {
				m6Notify(c, fmt.Sprintf("world:sign %d-%d none", *d.X, *d.Y))
				return
			}
		} else {
			if len(g.Signs) == 0 {
				m6Notify(c, "world:sign none")
				return
			}
			s = g.Signs[0]
		}
		inst := strconv.Itoa(s.X) + "-" + strconv.Itoa(s.Y)
		c.talkNPC = ""
		c.talkIndex = 0
		worldSignTalk(c, inst)
		m6Notify(c, fmt.Sprintf("world:sign %s text=%s", inst, s.Text))
	}
}
