// Lights + signs orchestration state over the pure internal/globals
// package.
//
// Mirrors packages/server/src/game/entity/character/player/handler.ts
// handleLights (on region-enter, Overlay Lamp frames for the surrounding
// regions' lights, deduped per player via lightsLoaded), impl/light.ts
// defaults + serialize, impl/sign.ts talk (Bubble Position with talkIndex
// paging over text.split(',')), and player.ts handleObjectInteraction
// sign lookup by the "x-y" instance.
//
// This type owns the globals snapshot plus the per-connection
// lightsLoaded dedupe sets. Sign talk cursors (talkNPC/talkIndex) stay
// with the root playerConn (shared with NPC talk); this package supplies
// pure paging helpers plus World-callback fan-out so the root adapter
// keeps packet construction (Overlay Lamp / Bubble Position shapes) and
// transport.
//
// Transport-free except through the GlowWorld callbacks: no websocket,
// no timers. Region ids are caller-supplied (the root computes them from
// its own sideLen) so tick cadence and TESTMAP behavior are unchanged.
package worldmap

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"rpg-world-server/internal/globals"
)

// Overlay Lamp / Bubble Position opcodes (Opcodes.Overlay Lamp2,
// Opcodes.Bubble Position1). The root aliases these names so packet
// call sites are unchanged.
const (
	OverlayLamp    = 2
	BubblePosition = 1
)

// GlowWorld is the transport seam the root adapter implements: region
// math stays with the root (its sideLen), sends stay with the root
// (packet shapes + conn lookup), and this package computes which lights
// are fresh and which sign page is due.
type GlowWorld interface {
	// RegionOf maps a tile to its region id.
	RegionOf(x, y int) int
	// SurroundingRegions returns the 9-region interest set for rid.
	SurroundingRegions(rid int) []int
	// SendLamp unicasts one Overlay Lamp frame for light l to conn
	// instance.
	SendLamp(instance string, l globals.Light)
	// SendBubble unicasts one Bubble Position frame to conn instance.
	SendBubble(instance, bubbleInstance, text string, x, y int)
}

// Glow owns the globals snapshot and per-conn dedupe state.
type Glow struct {
	mu      sync.Mutex
	g       *globals.Globals
	loaded  map[string]map[string]bool
	talkNPC map[string]string
	talkIdx map[string]int
}

// NewGlow returns an empty controller (Load populates it).
func NewGlow() *Glow {
	return &Glow{
		loaded:  map[string]map[string]bool{},
		talkNPC: map[string]string{},
		talkIdx: map[string]int{},
	}
}

// Load reads the globals snapshot from path (one uncached read; the
// caller owns caching like the root worldBootGlobals).
func (g *Glow) Load(path string) error {
	gg, err := globals.Load(path)
	if err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.g = gg
	return nil
}

// SetGlobals installs an already-loaded snapshot (tests / adapter).
func (g *Glow) SetGlobals(gg *globals.Globals) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.g = gg
}

// Globals returns the loaded snapshot (nil when disabled).
func (g *Glow) Globals() *globals.Globals {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.g
}

// EnsureTestLamp injects the TESTMAP synthetic lamp at (102,96) when the
// surrounding regions carry no light there. It reports whether a lamp
// was appended. regionOf is the root tile->region function so the probe
// matches updateClientRegion exactly.
func (g *Glow) EnsureTestLamp(testMode, cleanMode, combatMode bool, regionOf func(x, y int) int) bool {
	if !testMode || cleanMode || combatMode || regionOf == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.g == nil {
		return false
	}
	if len(g.g.LightsFor(regionOf(102, 96))) != 0 {
		return false
	}
	g.g.Lights = append(g.g.Lights, globals.Light{
		X: 102, Y: 96, Radius: globals.DefaultLightRadius,
		Colour: globals.DefaultLightColour,
	})
	return true
}

// FreshFor unions LightsFor over regions, marks unseen "x-y" lights
// loaded for instance, and returns the fresh subset (handleLights
// parity). Pure dedupe; the caller sends frames itself.
func (g *Glow) FreshFor(instance string, regions []int) []globals.Light {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.g == nil {
		return nil
	}
	loaded := g.loaded[instance]
	if loaded == nil {
		loaded = map[string]bool{}
		g.loaded[instance] = loaded
	}
	var fresh []globals.Light
	for _, r := range regions {
		for _, l := range g.g.LightsFor(r) {
			key := strconv.Itoa(l.X) + "-" + strconv.Itoa(l.Y)
			if loaded[key] {
				continue
			}
			loaded[key] = true
			fresh = append(fresh, l)
		}
	}
	return fresh
}

// LoadedCount reports how many lights are marked loaded for instance.
func (g *Glow) LoadedCount(instance string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.loaded[instance])
}

// Clear drops the per-conn loaded set (debug re-send path).
func (g *Glow) Clear(instance string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.loaded, instance)
}

// Forget drops per-conn state on disconnect (lamps + sign paging).
func (g *Glow) Forget(instance string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.loaded, instance)
	delete(g.talkNPC, instance)
	delete(g.talkIdx, instance)
}

// Push fans out Overlay Lamp frames for the fresh lights around
// (px,py) through world, and returns the fresh count. Silent when
// nothing is new.
func (g *Glow) Push(world GlowWorld, instance string, px, py int) int {
	if world == nil || instance == "" {
		return 0
	}
	rid := world.RegionOf(px, py)
	fresh := g.FreshFor(instance, world.SurroundingRegions(rid))
	for _, l := range fresh {
		world.SendLamp(instance, l)
	}
	return len(fresh)
}

// PushForce clears the per-conn loaded set then pushes (debug re-send).
func (g *Glow) PushForce(world GlowWorld, instance string, px, py int) int {
	g.Clear(instance)
	if g.Globals() == nil {
		return 0
	}
	n := g.Push(world, instance, px, py)
	if n == 0 {
		return g.LoadedCount(instance)
	}
	return n
}

// ParseSignInstance parses an "x-y" sign instance (player.ts
// handleObjectInteraction parity).
func ParseSignInstance(instance string) (int, int, bool) {
	parts := strings.Split(instance, "-")
	if len(parts) != 2 {
		return 0, 0, false
	}
	x, errX := strconv.Atoi(parts[0])
	y, errY := strconv.Atoi(parts[1])
	if errX != nil || errY != nil {
		return 0, 0, false
	}
	return x, y, true
}

// SignPages splits sign text into Bubble pages (impl/sign.ts
// text.split(',') parity).
func SignPages(text string) []string {
	return strings.Split(text, ",")
}

// SignTalk advances the per-conn talk cursor over the sign's pages and
// sends the due Bubble Position through world. It reports the page text
// and whether a sign matched. Talk cursors live here (keyed by conn
// instance) so the Bubble send stays behind the World seam; the root
// adapter keeps its own playerConn talkNPC/talkIndex for NPC talk and
// passes through via TalkWith (below) where the two must stay in sync.
func (g *Glow) SignTalk(world GlowWorld, instance, signInstance string) (string, bool) {
	gg := g.Globals()
	if gg == nil || world == nil {
		return "", false
	}
	x, y, ok := ParseSignInstance(signInstance)
	if !ok {
		return "", false
	}
	s, hit := gg.SignAt(x, y)
	if !hit {
		return "", false
	}
	pages := SignPages(s.Text)
	if len(pages) == 0 {
		return "", false
	}
	g.mu.Lock()
	if g.talkNPC[instance] != signInstance {
		g.talkNPC[instance] = signInstance
		g.talkIdx[instance] = 0
	}
	msg := pages[g.talkIdx[instance]%len(pages)]
	g.talkIdx[instance]++
	g.mu.Unlock()
	world.SendBubble(instance, signInstance, msg, x, y)
	return msg, true
}

// TalkWith is the root-synced variant: the caller owns the talk cursor
// (playerConn talkNPC/talkIndex, shared with NPC talk) and this method
// computes the due page, updates the cursor in place, and sends through
// world. It reports whether a sign matched.
func (g *Glow) TalkWith(world GlowWorld, instance, signInstance string, talkNPC *string, talkIndex *int) (string, bool) {
	gg := g.Globals()
	if gg == nil || world == nil || talkNPC == nil || talkIndex == nil {
		return "", false
	}
	x, y, ok := ParseSignInstance(signInstance)
	if !ok {
		return "", false
	}
	s, hit := gg.SignAt(x, y)
	if !hit {
		return "", false
	}
	pages := SignPages(s.Text)
	if len(pages) == 0 {
		return "", false
	}
	if *talkNPC != signInstance {
		*talkNPC = signInstance
		*talkIndex = 0
	}
	msg := pages[*talkIndex%len(pages)]
	*talkIndex++
	world.SendBubble(instance, signInstance, msg, x, y)
	return msg, true
}

// LightInstance formats the Overlay Lamp instance ("light-x-y").
func LightInstance(l globals.Light) string {
	return fmt.Sprintf("light-%d-%d", l.X, l.Y)
}
