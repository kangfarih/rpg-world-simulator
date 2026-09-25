package worldmap

import (
	"os"
	"path/filepath"
	"testing"
)

// worldmapTestDataPath resolves the checkout world.json from the package
// dir (tests run with cwd = internal/worldmap).
func worldmapTestDataPath() string {
	for _, p := range []string{
		filepath.Join("..", "..", "packages", "server", "data", "map", "world.json"),
		filepath.Join("..", "..", "..", "packages", "server", "data", "map", "world.json"),
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return filepath.Join("..", "..", "packages", "server", "data", "map", "world.json")
}

func TestUnflipTileStripsFlipBits(t *testing.T) {
	plain := 5
	if got := UnflipTile(plain); got != plain {
		t.Fatalf("UnflipTile(%d) = %d, want %d", plain, got, plain)
	}
	flipped := 0x80000000 | 0x40000000 | 0x20000000 | 5
	if got := UnflipTile(flipped); got != 5 {
		t.Fatalf("UnflipTile(0x%x) = %d, want 5", flipped, got)
	}
}

func TestSurroundingRegionsInteriorReturnsNine(t *testing.T) {
	// 8x8 regions (384x384 tiles): region 50 is interior (row 6, col 2).
	w := &World{Width: 8 * MapDivisionSize, Height: 8 * MapDivisionSize, SideLen: 8}
	got := w.SurroundingRegions(50)
	if len(got) != 9 {
		t.Fatalf("SurroundingRegions(50) = %v, want 9 regions", got)
	}
	seen := make(map[int]bool, len(got))
	for _, rid := range got {
		seen[rid] = true
	}
	if !seen[50] {
		t.Fatalf("SurroundingRegions(50) = %v, missing region 50 itself", got)
	}
}

func TestRegionDataMissingFileReturnsError(t *testing.T) {
	if _, err := Load("/nonexistent/world.json"); err == nil {
		t.Fatal("Load(missing file) = nil error, want error")
	}
}

func TestMarkerCoordTileIndexMath(t *testing.T) {
	// tileIndex = y*width+x with the real width 1152 (map.ts indexToCoord).
	w := &World{Width: 1152, Height: 1008}
	cases := []struct {
		idx  int
		x, y int
	}{
		{0, 0, 0},
		{1151, 1151, 0},
		{1152, 0, 1},
		{117058, 706, 101}, // sorcerer marker
		{115372, 172, 100}, // oak marker: 115372 = 100*1152+172
	}
	for _, c := range cases {
		if x, y := w.MarkerCoord(c.idx); x != c.x || y != c.y {
			t.Fatalf("MarkerCoord(%d) = %d,%d, want %d,%d", c.idx, x, y, c.x, c.y)
		}
	}
}

func TestMarkerAtLoadsEntitiesDict(t *testing.T) {
	w, err := Load(worldmapTestDataPath())
	if err != nil {
		t.Skipf("world.json unavailable: %v", err)
	}
	if len(w.Entities) == 0 {
		t.Fatal("Entities is empty, want the 4226 world.json markers")
	}
	if len(w.Entities) != 4226 {
		t.Fatalf("Entities = %d, want 4226 world.json markers", len(w.Entities))
	}
	key, ok := w.MarkerAt(117058)
	if !ok || key != "sorcerer" {
		t.Fatalf("MarkerAt(117058) = %q,%v, want sorcerer,true", key, ok)
	}
	if _, ok := w.MarkerAt(0); ok {
		t.Fatal("MarkerAt(0) = found, want no marker on tile 0")
	}
	n := 0
	w.ForEachMarker(func(x, y int, key string) {
		n++
		if w.Entities[y*w.Width+x] != key {
			t.Fatalf("ForEachMarker(%d,%d) = %q, want dict value", x, y, key)
		}
	})
	if n != len(w.Entities) {
		t.Fatalf("ForEachMarker visited %d, want %d", n, len(w.Entities))
	}
}
