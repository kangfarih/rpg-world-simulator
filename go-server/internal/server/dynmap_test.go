package server

import (
	"reflect"
	"testing"

	"rpg-world-server/internal/entity"
)

// Hermetic dynmap tests: the overlay builder runs over dynAreaView fakes +
// a stub Progression, so no world.json, registry, or conn state is needed.
// (The entity registry stays untouched — production wiring is covered by
// the live quest-finish leg.)

// dynStubProg is a fake entity.Progression (gates_test.go stubProg parity).
type dynStubProg struct {
	quests map[string]bool
	achs   map[string]bool
}

func (s dynStubProg) QuestFinished(key string) bool { return s.quests[key] }
func (s dynStubProg) AchFinished(key string) bool   { return s.achs[key] }

// dynFakeArea is a fake mapping area: outer rect (x,y,w,h) with quest/
// achievement gate, outer->mapped by origin delta (MappedTile parity),
// optional explicit animation targets.
type dynFakeArea struct {
	x, y, w, h int
	quest, ach string
	mx, my     int // mapped counterpart origin (MapTo ok=false when noMap)
	noMap      bool
	anim       map[[2]int][2]int // outer -> animation tile
}

func (a *dynFakeArea) EachTile(fn func(x, y int)) {
	for y := a.y; y < a.y+a.h; y++ {
		for x := a.x; x < a.x+a.w; x++ {
			fn(x, y)
		}
	}
}

func (a *dynFakeArea) GateKey() (string, bool) {
	if a.quest != "" {
		return "q:" + a.quest, true
	}
	if a.ach != "" {
		return "a:" + a.ach, true
	}
	return "", false
}

func (a *dynFakeArea) Fulfills(p entity.Progression) bool {
	if a.quest != "" {
		return p.QuestFinished(a.quest)
	}
	if a.ach != "" {
		return p.AchFinished(a.ach)
	}
	return false
}

func (a *dynFakeArea) MapTo(x, y int) (int, int, bool) {
	if a.noMap {
		return 0, 0, false
	}
	return a.mx + (x - a.x), a.my + (y - a.y), true
}

func (a *dynFakeArea) AnimTo(x, y int) (int, int, bool) {
	if a.anim == nil {
		return 0, 0, false
	}
	t, ok := a.anim[[2]int{x, y}]
	return t[0], t[1], ok
}

// dynFetch builds a fetch over a tile grid (missing = empty, buildTile
// false parity).
func dynFetch(grid map[[2]int]RegionTile) func(x, y int) (RegionTile, bool) {
	return func(x, y int) (RegionTile, bool) {
		t, ok := grid[[2]int{x, y}]
		return t, ok
	}
}

func dynAllScope(x, y int) bool { return true }

// TestDynamicOverlayMatrix is the remap matrix with fake progression
// states (buildDynamicTile parity: fulfilled -> mapped content at original
// coords, unfulfilled -> absent).
func TestDynamicOverlayMatrix(t *testing.T) {
	questArea := &dynFakeArea{x: 10, y: 10, w: 1, h: 1, quest: "ancientlands", mx: 20, my: 20}
	achArea := &dynFakeArea{x: 1, y: 1, w: 2, h: 2, ach: "crabproblem", mx: 5, my: 5}
	areas := []dynAreaView{questArea, achArea}
	grid := map[[2]int]RegionTile{
		{20, 20}: {X: 20, Y: 20, Data: 7, C: true},
		{5, 5}:   {X: 5, Y: 5, Data: 9},
		{6, 6}:   {X: 6, Y: 6, Data: 10, C: true},
	}
	fetch := dynFetch(grid)

	// Quest fulfilled: mapped content at the ORIGINAL coords.
	ov, sig := dynamicOverlayTiles(areas, dynStubProg{quests: map[string]bool{"ancientlands": true}}, fetch, dynAllScope)
	tile, ok := ov[[2]int{10, 10}]
	if !ok {
		t.Fatal("fulfilled quest tile missing from overlay")
	}
	if tile.X != 10 || tile.Y != 10 || tile.Data != 7 || !tile.C {
		t.Fatalf("quest tile = %+v, want original 10,10 with mapped data=7 c=true", tile)
	}
	if sig != "q:ancientlands" {
		t.Fatalf("sig = %q, want q:ancientlands", sig)
	}

	// Achievement fulfilled: 2x2 block with relative offsets preserved.
	ov, sig = dynamicOverlayTiles(areas, dynStubProg{achs: map[string]bool{"crabproblem": true}}, fetch, dynAllScope)
	if len(ov) != 4 {
		t.Fatalf("ach overlay tiles = %d, want 4", len(ov))
	}
	if tile := ov[[2]int{2, 2}]; tile.Data != 10 || !tile.C || tile.X != 2 || tile.Y != 2 {
		t.Fatalf("offset tile = %+v, want 2,2 data=10 c=true", tile)
	}
	if sig != "a:crabproblem" {
		t.Fatalf("sig = %q, want a:crabproblem", sig)
	}

	// Unfulfilled: empty overlay, empty signature (base serves originals).
	ov, sig = dynamicOverlayTiles(areas, dynStubProg{}, fetch, dynAllScope)
	if len(ov) != 0 || sig != "" {
		t.Fatalf("unfulfilled overlay = %v sig = %q, want empty", ov, sig)
	}
}

// TestDynamicOverlayGates covers gateless areas, mapping-less counterparts,
// out-of-scope tiles, mapped-empty tiles, animation, overlap order, and
// multi-gate signature sorting.
func TestDynamicOverlayGates(t *testing.T) {
	grid := map[[2]int]RegionTile{
		{20, 20}: {X: 20, Y: 20, Data: 7},
		{30, 30}: {X: 30, Y: 30, Data: 8},
		{40, 40}: {X: 40, Y: 40, Data: 11},
	}
	fetch := dynFetch(grid)

	// Gateless area: never fulfills, even for a "done" progression.
	plain := &dynFakeArea{x: 50, y: 50, w: 1, h: 1, mx: 20, my: 20}
	ov, sig := dynamicOverlayTiles(
		[]dynAreaView{plain},
		dynStubProg{quests: map[string]bool{"q": true}, achs: map[string]bool{"a": true}},
		fetch, dynAllScope,
	)
	if len(ov) != 0 || sig != "" {
		t.Fatalf("gateless overlay = %v sig = %q, want empty", ov, sig)
	}

	// Counterpart side without its own mapping: skipped.
	nomap := &dynFakeArea{x: 60, y: 60, w: 1, h: 1, quest: "q", noMap: true}
	ov, _ = dynamicOverlayTiles([]dynAreaView{nomap},
		dynStubProg{quests: map[string]bool{"q": true}}, fetch, dynAllScope)
	if len(ov) != 0 {
		t.Fatalf("mapping-less overlay = %v, want empty", ov)
	}

	// Out-of-scope tile: skipped and its gate stays out of the signature.
	scoped := &dynFakeArea{x: 70, y: 70, w: 1, h: 1, ach: "far", mx: 20, my: 20}
	ov, sig = dynamicOverlayTiles([]dynAreaView{scoped},
		dynStubProg{achs: map[string]bool{"far": true}}, fetch,
		func(x, y int) bool { return false })
	if len(ov) != 0 || sig != "" {
		t.Fatalf("out-of-scope overlay = %v sig = %q, want empty", ov, sig)
	}

	// Mapped tile with no content: data:0 tile (TS empty-index parity).
	empty := &dynFakeArea{x: 80, y: 80, w: 1, h: 1, quest: "void", mx: 99, my: 99}
	ov, sig = dynamicOverlayTiles([]dynAreaView{empty},
		dynStubProg{quests: map[string]bool{"void": true}}, fetch, dynAllScope)
	tile, ok := ov[[2]int{80, 80}]
	if !ok {
		t.Fatal("mapped-empty tile missing")
	}
	if tile.Data != 0 || tile.X != 80 || tile.Y != 80 {
		t.Fatalf("mapped-empty tile = %+v, want 80,80 data=0", tile)
	}
	if sig != "q:void" {
		t.Fatalf("sig = %q, want q:void", sig)
	}

	// Animation: mapped-animation data rides along when linked.
	animArea := &dynFakeArea{x: 10, y: 10, w: 1, h: 1, quest: "animq", mx: 20, my: 20,
		anim: map[[2]int][2]int{{10, 10}: {40, 40}}}
	ov, _ = dynamicOverlayTiles([]dynAreaView{animArea},
		dynStubProg{quests: map[string]bool{"animq": true}}, fetch, dynAllScope)
	if tile := ov[[2]int{10, 10}]; tile.Animation != 11 {
		t.Fatalf("animation = %v, want 11", tile.Animation)
	}
	// No animation link: field omitted.
	ov, _ = dynamicOverlayTiles(
		[]dynAreaView{&dynFakeArea{x: 10, y: 10, w: 1, h: 1, quest: "animq", mx: 20, my: 20}},
		dynStubProg{quests: map[string]bool{"animq": true}}, fetch, dynAllScope)
	if tile := ov[[2]int{10, 10}]; tile.Animation != nil {
		t.Fatalf("animation = %v, want nil", tile.Animation)
	}

	// Overlap: first area wins (DynamicAt order parity).
	first := &dynFakeArea{x: 10, y: 10, w: 1, h: 1, quest: "q1", mx: 20, my: 20}
	second := &dynFakeArea{x: 10, y: 10, w: 1, h: 1, quest: "q2", mx: 30, my: 30}
	ov, sig = dynamicOverlayTiles([]dynAreaView{first, second},
		dynStubProg{quests: map[string]bool{"q1": true, "q2": true}}, fetch, dynAllScope)
	if tile := ov[[2]int{10, 10}]; tile.Data != 7 {
		t.Fatalf("overlap tile = %+v, want first-area data=7", tile)
	}
	if sig != "q:q1" {
		t.Fatalf("overlap sig = %q, want q:q1 (winner only)", sig)
	}

	// Multi-gate signature sorts.
	multi := []dynAreaView{
		&dynFakeArea{x: 1, y: 1, w: 1, h: 1, quest: "zebra", mx: 20, my: 20},
		&dynFakeArea{x: 2, y: 2, w: 1, h: 1, ach: "apple", mx: 30, my: 30},
	}
	_, sig = dynamicOverlayTiles(multi,
		dynStubProg{quests: map[string]bool{"zebra": true}, achs: map[string]bool{"apple": true}},
		fetch, dynAllScope)
	if sig != "a:apple,q:zebra" {
		t.Fatalf("sig = %q, want sorted a:apple,q:zebra", sig)
	}
}

// TestApplyDynamicTiles covers replace, append-missing (new region), and
// base immutability.
func TestApplyDynamicTiles(t *testing.T) {
	base := map[int][]RegionTile{
		50: {{X: 100, Y: 100, Data: 1}, {X: 101, Y: 100, Data: 2, C: true}},
	}
	ridOf := func(x, y int) int { return (y/48)*24 + (x / 48) }
	before := map[int][]RegionTile{
		50: {{X: 100, Y: 100, Data: 1}, {X: 101, Y: 100, Data: 2, C: true}},
	}
	overlay := map[[2]int]RegionTile{
		{101, 100}: {X: 101, Y: 100, Data: 9}, // replace
		{200, 200}: {X: 200, Y: 200, Data: 5}, // append (region 100 new)
		{102, 100}: {X: 102, Y: 100, Data: 3}, // append (region 50)
	}
	out := applyDynamicTiles(base, overlay, ridOf)
	if got := out[50][1]; got.Data != 9 {
		t.Fatalf("replaced tile = %+v, want data=9", got)
	}
	if len(out[50]) != 3 || out[50][2].Data != 3 {
		t.Fatalf("appended tile missing: %+v", out[50])
	}
	if len(out[100]) != 1 || out[100][0].Data != 5 {
		t.Fatalf("new region tiles = %+v", out[100])
	}
	if !reflect.DeepEqual(base, before) {
		t.Fatalf("base mutated: %+v", base)
	}
}

// TestBuildMapFrameForNoRemap proves the zero-regression path: a player
// with no fulfilled gates gets the byte-identical cached frame
// (same backing array), not a rebuild.
func TestBuildMapFrameForNoRemap(t *testing.T) {
	static := []any{4, "eJwDAA==", 3}
	old := dynStaticFrame
	dynStaticFrame = func() []any { return static }
	defer func() { dynStaticFrame = old }()

	frame, sig, remapped := buildMapFrameFor("dynmap-nobody-fresh")
	if remapped || sig != "" {
		t.Fatalf("no-remap = remapped=%v sig=%q, want false/\"\"", remapped, sig)
	}
	if len(frame) != len(static) || &frame[0] != &static[0] {
		t.Fatal("no-remap frame is not the cached static frame (must alias it)")
	}
	// Unknown quest/achievement gates never fulfill either.
	frame2, _, remapped2 := buildMapFrameFor("dynmap-nobody-fresh")
	if remapped2 || &frame2[0] != &static[0] {
		t.Fatal("repeat no-remap call must hit the same static frame")
	}
}

// TestMaybePushNilSafe guards the quest-path push with no live conn.
func TestMaybePushNilSafe(t *testing.T) {
	maybePushDynamicMap(nil)
	maybePushDynamicMap(&playerConn{})
	dynMapForget("dynmap-ghost")
}
