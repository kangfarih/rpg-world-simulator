package worldmap

import (
	"testing"
)

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
