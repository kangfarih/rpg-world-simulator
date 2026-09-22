// World wiring (warps + events + lights/signs globals) — behavior-additive
// thin adapter over internal/controller (warps + events orchestration) and
// internal/worldmap glow (lights/sign orchestration), reusing the pure
// internal/warps, internal/events and internal/globals packages.
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
//
// Split notes (E1c): stateful orchestration moved WITHOUT behavior change:
//   - internal/controller WarpController owns the warp registry, the
//     menu-gating table and cooldown clocks (gate outcome computation +
//     WORLD_WARP_COOLDOWN_MS semantics); this file keeps the World impl
//     (worldWarpStore over m13/m5/m11/m7 globals) plus teleport side
//     effects, delegating gates/landing to the controller.
//   - internal/controller EventController owns the rotation scheduler plus
//     the active set/fired counters (WORLD_EVENT_MS semantics); this file
//     keeps the global-notice fan-out and the m5 multiplier probes.
//   - internal/worldmap Glow owns the globals snapshot plus per-conn
//     lightsLoaded dedupe (and sign paging helpers); this file keeps packet
//     construction (worldLightData/worldBubbleData) and transport through
//     the GlowWorld seam.
//   - Stayed in root (cannot move cleanly): playerConn transport (send/
//     broadcast/updateClientRegion/setEntityPos), m5/m6/m7/m10/m11/m13
//     state reads, TESTMAP/clean/combat mode flags, worldPath resolution,
//     packet shapes, tick cadence, TESTMAP debug strings.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"

	"rpg-world-server/internal/controller"
	"rpg-world-server/internal/events"
	"rpg-world-server/internal/globals"
	gnet "rpg-world-server/internal/net"
	worldcore "rpg-world-server/internal/world"
	"rpg-world-server/internal/worldmap"
)

// ---------------------------------------------------------------------------
// Registries (thin singletons delegating to the new packages).
// ---------------------------------------------------------------------------

var (
	worldWarpCtl  = controller.NewWarpController()
	worldEventCtl = controller.NewEventController()
	worldGlow     = worldmap.NewGlow()
)

// worldWarpExt aliases the controller gating entry so call sites keep
// their signatures.
type worldWarpExt = controller.WarpEntry

// worldWarpNames mirrors Modules.Warps order (modules.ts:206-213): the C->S
// Warp {id} indexes this enum, controllers/warps.ts getWarp lowercases the
// name to find the world.json entry.
var worldWarpNames = controller.WarpNames

// ---------------------------------------------------------------------------
// Warps.
// ---------------------------------------------------------------------------

// worldWarpCooldownMs is the TS warpTimeout (warps.ts: 300s between warps).
// WORLD_WARP_COOLDOWN_MS overrides it (0 disables, for the e2e).
func worldWarpCooldownMs() int64 {
	return controller.CooldownMs()
}

// worldWarpStore implements controller.WarpStore over the root globals.
// IsAdmin reads the warping conn's rank (chatStateFor parity); the rest
// read by username like the pre-split inline checks.
type worldWarpStore struct {
	c *playerConn
}

func (s worldWarpStore) IsJailed(username string) bool {
	return m13IsJailed(username)
}

func (s worldWarpStore) IsAdmin(username string) bool {
	if s.c == nil {
		return false
	}
	return chatStateFor(s.c).rank >= RankAdmin
}

func (s worldWarpStore) PlayerLevel(username string) int {
	return m5StateFor(username).Level
}

func (s worldWarpStore) QuestFinished(username, quest string) bool {
	return m11StateFor(username).isFinished(quest)
}

func (s worldWarpStore) AchievementDone(username, ach string) bool {
	def := m11A[ach]
	st := m11StateFor(username)
	return def != nil && st.Achs[ach] >= def.StageCount
}

func (s worldWarpStore) FormatName(name string) string {
	return m7FormatName(name)
}

// worldBootWarps loads the warp registry + the menu-gating table from the
// same world.json path the map loader uses.
func worldBootWarps() {
	if err := worldWarpCtl.Load(worldPath()); err != nil {
		log.Printf("world: warps: %v (warp engine disabled)", err)
		return
	}
	log.Printf("world: warps loaded=%d", worldWarpCtl.Count())
}

// worldFindWarp resolves a warp by Modules.Warps enum id (getWarp parity).
func worldFindWarp(id int) *worldWarpExt {
	return worldWarpCtl.FindByID(id)
}

// worldFindWarpByName resolves a warp by (case-insensitive) name.
func worldFindWarpByName(name string) *worldWarpExt {
	return worldWarpCtl.FindByName(name)
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
	nowMs := time.Now().UnixMilli()
	if deny := worldWarpCtl.Authorize(c.Username, w, nowMs, worldWarpStore{c: c}); deny != "" {
		m6Notify(c, deny)
		return false
	}
	lx, ly, ok := controller.Landing(w, rand.Intn)
	if !ok {
		return false
	}
	c.Sess.PlayerX, c.Sess.PlayerY = lx, ly
	worldcore.SetEntityPos(c.Instance, lx, ly)
	worldcore.UpdateRegion(c, lx, ly)
	worldcore.Broadcast(pkt(PacketTeleport, teleportData{Instance: c.Instance, X: lx, Y: ly}))
	m10OnPositionUpdate(c)
	m5TrackPos(c)
	worldPushLights(c)
	worldWarpCtl.Record(c.Username, nowMs)
	m6Notify(c, "warps:WARPED_TO;name="+m7FormatName(w.Name))
	log.Printf("world: %s warped to %s (%d,%d)", c.Username, w.Name, lx, ly)
	return true
}

// ---------------------------------------------------------------------------
// Events.
// ---------------------------------------------------------------------------

// worldBootEvents starts the rotation scheduler. WORLD_EVENT_MS overrides the
// per-event cadence (test hook for fast event-notice legs); default keeps the
// TS hourly cadence from DefaultEvents.
func worldBootEvents() {
	n, every := worldEventCtl.Boot()
	log.Printf("world: events started=%d intervalMs=%d", n, every)
}

// worldEventTick fans due rotation events out as global notices. Called from
// the central 20Hz tick loop (additive: no events due most ticks).
func worldEventTick() {
	for _, e := range worldEventCtl.Due(time.Now().UnixMilli()) {
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
	if !worldEventCtl.IsActive("double-drops") || len(drops) == 0 {
		return drops
	}
	out := make([]m5Drop, 0, len(drops)*2)
	return append(append(out, drops...), drops...)
}

// worldXPBoost reports the 1.5x experience event (experiencePerHit parity).
func worldXPBoost() bool {
	return worldEventCtl.IsActive("experience")
}

// worldHarvestDouble reports the lumberjacking/mining double-yield events
// (Utils.doubleLumberjacking/doubleMining parity) for a gathering skill.
func worldHarvestDouble(skill string) bool {
	switch skill {
	case "lumberjacking":
		return worldEventCtl.IsActive("lumberjacking")
	case "mining":
		return worldEventCtl.IsActive("mining")
	}
	return false
}

// ---------------------------------------------------------------------------
// Lights + signs globals.
// ---------------------------------------------------------------------------

// Overlay Lamp opcodes (Opcodes.Overlay: Set0 Remove1 Lamp2 RemoveLamps3;
// m10.go only names Set/Remove, so Lamp lives here).
const OverlayLamp = worldmap.OverlayLamp

// Bubble opcodes (Opcodes.Bubble: Entity0 Position1).
const BubblePosition = worldmap.BubblePosition

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

// worldGlowWorld implements worldmap.GlowWorld over the root transport:
// region math via regionOf/surroundingRegions, sends via send() unicast to
// the conn holding the instance. Packet shapes are unchanged.
type worldGlowWorld struct{}

func (worldGlowWorld) RegionOf(x, y int) int { return worldcore.TileRegion(x, y) }

func (worldGlowWorld) SurroundingRegions(rid int) []int { return surroundingRegions(rid) }

func (worldGlowWorld) SendLamp(instance string, l globals.Light) {
	c, _ := worldcore.Find[*playerConn](instance)
	if c == nil {
		return
	}
	_ = gnet.Send(c.Conn, pktOp(PacketOverlay, OverlayLamp, map[string]any{
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

func (worldGlowWorld) SendBubble(instance, bubbleInstance, text string, x, y int) {
	c, _ := worldcore.Find[*playerConn](instance)
	if c == nil {
		return
	}
	_ = gnet.Send(c.Conn, pktOp(PacketBubble, BubblePosition, worldBubbleData{
		Instance: bubbleInstance, Text: text, X: intp(x), Y: intp(y),
	}))
}

// worldBootGlobals loads lights/signs from world.json. TESTMAP gets one
// synthetic lamp next to spawn (documented divergence: the real lights are
// far from the stub spawn, so region-enter would otherwise never emit Lamp).
func worldBootGlobals() {
	if err := worldGlow.Load(worldPath()); err != nil {
		log.Printf("world: globals: %v (lights/signs disabled)", err)
		return
	}
	if worldGlow.EnsureTestLamp(testMode, cleanMode, combatMode, worldcore.TileRegion) {
		log.Printf("world: TESTMAP synthetic lamp at 102,96")
	}
	g := worldGlow.Globals()
	log.Printf("world: globals lights=%d signs=%d", len(g.Lights), len(g.Signs))
}

// worldPushLights unicasts Overlay Lamp frames for every not-yet-loaded light
// in the surrounding regions (handler.ts handleLights parity). Silent when
// nothing is new.
func worldPushLights(c *playerConn) {
	if c == nil || c.Username == "" {
		return
	}
	if worldGlow.Globals() == nil {
		return
	}
	n := worldGlow.Push(worldGlowWorld{}, c.Instance, c.Sess.PlayerX, c.Sess.PlayerY)
	if n > 0 {
		log.Printf("world: %s lamps=%d (region %d)", c.Username, n, worldcore.TileRegion(c.Sess.PlayerX, c.Sess.PlayerY))
	}
}

// worldPushLightsForce clears the per-conn loaded set then pushes (debug
// re-send for the worldtest lights leg).
func worldPushLightsForce(c *playerConn) int {
	return worldGlow.PushForce(worldGlowWorld{}, c.Instance, c.Sess.PlayerX, c.Sess.PlayerY)
}

// worldForgetPlayer drops per-conn lamp state on disconnect.
func worldForgetPlayer(c *playerConn) {
	if c == nil {
		return
	}
	worldGlow.Forget(c.Instance)
}

// worldSignTalk handles Target Talk on a sign position ("x-y" instance,
// player.ts handleObjectInteraction parity): Bubble Position with talkIndex
// paging over the comma-split text. Reports whether a sign matched.
func worldSignTalk(c *playerConn, instance string) bool {
	msg, ok := worldGlow.TalkWith(worldGlowWorld{}, c.Instance, instance, &c.talkNPC, &c.talkIndex)
	if !ok {
		return false
	}
	log.Printf("world: %s read sign %s (%q)", c.Username, instance, msg)
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
	g := worldGlow.Globals()
	switch d.WorldTest {
	case "echo":
		nw := worldWarpCtl.Count()
		nl, ns := 0, 0
		if g != nil {
			nl, ns = len(g.Lights), len(g.Signs)
		}
		m6Notify(c, fmt.Sprintf("world:ok warps=%d events=%d lights=%d signs=%d", nw, len(events.DefaultEvents()), nl, ns))
	case "warps":
		names := worldWarpCtl.Names()
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
			m6Notify(c, fmt.Sprintf("world:warp %s x=%d y=%d", w.Name, c.Sess.PlayerX, c.Sess.PlayerY))
		}
	case "at":
		if d.X == nil || d.Y == nil || !worldWarpCtl.Loaded() {
			return
		}
		if w := worldWarpCtl.At(*d.X, *d.Y); w != nil {
			m6Notify(c, fmt.Sprintf("world:at %d,%d id=%d x=%d y=%d w=%d h=%d", *d.X, *d.Y, w.ID, w.X, w.Y, w.W, w.H))
		} else {
			m6Notify(c, fmt.Sprintf("world:at %d,%d none", *d.X, *d.Y))
		}
	case "events":
		active := worldEventCtl.ActiveKeys()
		n, every := worldEventCtl.Fired(), worldEventCtl.Interval()
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
