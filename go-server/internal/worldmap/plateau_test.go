package worldmap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Plateau table + gated collision matrix (map.ts getPlateauLevel +
// isColliding dynamic branch parity).
func TestPlateauLevelTableDefaultOOB(t *testing.T) {
	w := &World{
		Width: 8, Height: 8, SideLen: 1,
		Data:    make([]json.RawMessage, 64),
		Plateau: map[int]int{10: 2, 63: 1},
	}
	if got := w.PlateauLevel(2, 1); got != 2 { // index 1*8+2=10
		t.Fatalf("PlateauLevel(2,1) = %d, want 2", got)
	}
	if got := w.PlateauLevel(0, 0); got != 0 {
		t.Fatalf("PlateauLevel(0,0) = %d, want default 0", got)
	}
	for _, c := range [][2]int{{-1, 0}, {0, -1}, {8, 0}, {0, 8}} {
		if got := w.PlateauLevel(c[0], c[1]); got != 0 {
			t.Fatalf("PlateauLevel(%d,%d) = %d, want OOB 0", c[0], c[1], got)
		}
	}
	var nilWorld *World
	if got := nilWorld.PlateauLevel(0, 0); got != 0 {
		t.Fatalf("nil PlateauLevel = %d, want 0", got)
	}
}

func TestPlateauLoadsFromWorldJSON(t *testing.T) {
	raw := `{"width":4,"height":4,"data":[0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0],
		"collisions":[],"objects":[],"cursors":{},"plateau":{"5":3,"bad-key":9}}`
	path := filepath.Join(t.TempDir(), "world.json")
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := w.PlateauLevel(1, 1); got != 3 { // index 1*4+1=5
		t.Fatalf("PlateauLevel(1,1) = %d, want 3", got)
	}
	if got := w.PlateauLevel(0, 0); got != 0 {
		t.Fatalf("PlateauLevel(0,0) = %d, want 0", got)
	}
	if len(w.Plateau) != 1 {
		t.Fatalf("plateau entries = %d, want 1 (bad key skipped)", len(w.Plateau))
	}
}

func TestPlateauLoadAbsentIsEmpty(t *testing.T) {
	raw := `{"width":2,"height":2,"data":[1,1,1,1],"collisions":[],"objects":[]}`
	path := filepath.Join(t.TempDir(), "world.json")
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := w.PlateauLevel(0, 0); got != 0 {
		t.Fatalf("PlateauLevel = %d, want 0 without a plateau table", got)
	}
}

// IsBlockedRemapped matrix: no remap = static; resolved remap evaluates the
// mapped tile (map.ts:240 `return this.isColliding(mappedTile.x, mappedTile.y)`).
func TestIsBlockedRemappedMatrix(t *testing.T) {
	// 2x2: (0,0) walkable tile 10, (1,0) colliding tile 99, rest empty.
	w := &World{
		Width: 2, Height: 2, SideLen: 1,
		Data: []json.RawMessage{
			json.RawMessage(`10`), json.RawMessage(`99`),
			json.RawMessage(`0`), json.RawMessage(`0`),
		},
		Collisions: map[int]bool{99: true},
		Objects:    map[int]bool{},
		Plateau:    map[int]int{},
	}
	if w.IsBlocked(0, 0) {
		t.Fatal("(0,0) static must be walkable")
	}
	if !w.IsBlocked(1, 0) {
		t.Fatal("(1,0) static must block")
	}
	// Nil remap = static.
	if w.IsBlockedRemapped(0, 0, nil) {
		t.Fatal("nil-remap (0,0) must be walkable")
	}
	// Unresolved remap = static.
	none := func(x, y int) (int, int, bool) { return 0, 0, false }
	if w.IsBlockedRemapped(0, 0, none) {
		t.Fatal("unresolved-remap (0,0) must be walkable")
	}
	// Resolved onto a colliding tile blocks (dynamic gate fulfilled).
	toBlocked := func(x, y int) (int, int, bool) { return 1, 0, true }
	if !w.IsBlockedRemapped(0, 0, toBlocked) {
		t.Fatal("remapped (0,0)->(1,0) must block")
	}
	// Resolved onto a walkable tile opens (dynamic gate on a wall tile).
	toOpen := func(x, y int) (int, int, bool) { return 0, 0, true }
	if w.IsBlockedRemapped(1, 0, toOpen) {
		t.Fatal("remapped (1,0)->(0,0) must be walkable")
	}
}
