// Glow serving (lights/signs globals remainder), extracted behavior-frozen
// from the root world_wire.go adapter (task D2b item 2: world_wire.go ->
// internal/worldmap; the Glow orchestration core already lives in lights.go
// — this moves the remainder: the shared singleton, boot, push wrappers
// and per-conn forget, plus the TESTMAP debug accessors).
//
// TS sources (see the root adapter for the full tour): player/handler.ts
// handleLights (Overlay Lamp on region-enter, deduped per player),
// globals/impl/light.ts defaults + serialize, globals/impl/sign.ts talk
// (Bubble Position with talkIndex paging).
//
// The packet-building World implementation stays with the root adapter
// (worldGlowWorld, like the pets petWorld precedent): this file takes the
// GlowWorld at each call. Log strings are kept verbatim.
package worldmap

import (
	"log"
)

// sharedGlow is the lights/signs globals snapshot + per-conn lightsLoaded
// dedupe (world_wire.go worldGlow parity, owned here now).
var sharedGlow = NewGlow()

// Shared exposes the globals snapshot owner (sign TalkWith parity for the
// root adapter's session-locked cursor path, plus the TESTMAP debug legs).
func Shared() *Glow { return sharedGlow }

// BootGlobals loads lights/signs from world.json. TESTMAP gets one
// synthetic lamp next to spawn (documented divergence: the real lights are
// far from the stub spawn, so region-enter around spawn would otherwise
// never emit Lamp).
func BootGlobals(worldPath string, testMode, cleanMode, combatMode bool, tileRegion func(x, y int) int) {
	if err := sharedGlow.Load(worldPath); err != nil {
		log.Printf("world: globals: %v (lights/signs disabled)", err)
		return
	}
	if sharedGlow.EnsureTestLamp(testMode, cleanMode, combatMode, tileRegion) {
		log.Printf("world: TESTMAP synthetic lamp at 102,96")
	}
	g := sharedGlow.Globals()
	log.Printf("world: globals lights=%d signs=%d", len(g.Lights), len(g.Signs))
}

// PushLights unicasts Overlay Lamp frames for every not-yet-loaded light
// in the surrounding regions (handler.ts handleLights parity). Silent when
// nothing is new.
func PushLights(world GlowWorld, instance, username string, px, py int) {
	if instance == "" || username == "" {
		return
	}
	if sharedGlow.Globals() == nil {
		return
	}
	n := sharedGlow.Push(world, instance, px, py)
	if n > 0 {
		log.Printf("world: %s lamps=%d (region %d)", username, n, world.RegionOf(px, py))
	}
}

// PushLightsForce clears the per-conn loaded set then pushes (debug
// re-send for the worldtest lights leg).
func PushLightsForce(world GlowWorld, instance string, px, py int) int {
	return sharedGlow.PushForce(world, instance, px, py)
}

// ForgetGlow drops per-conn lamp state on disconnect.
func ForgetGlow(instance string) {
	if instance == "" {
		return
	}
	sharedGlow.Forget(instance)
}

// GlowStats reports the globals counts for the worldtest echo leg
// (lights, signs, loaded-nil flag).
func GlowStats() (lights, signs int, ok bool) {
	g := sharedGlow.Globals()
	if g == nil {
		return 0, 0, false
	}
	return len(g.Lights), len(g.Signs), true
}

// FirstSign reports the first global sign for the worldtest signs leg.
func FirstSign() (x, y int, text string, n int, ok bool) {
	g := sharedGlow.Globals()
	if g == nil {
		return 0, 0, "", 0, false
	}
	if len(g.Signs) == 0 {
		return 0, 0, "", 0, true
	}
	s := g.Signs[0]
	return s.X, s.Y, s.Text, len(g.Signs), true
}

// SignAt reports the sign at (x, y) for the worldtest sign leg.
func SignAt(x, y int) (text string, ok bool) {
	g := sharedGlow.Globals()
	if g == nil {
		return "", false
	}
	s, ok := g.SignAt(x, y)
	if !ok {
		return "", false
	}
	return s.Text, true
}
