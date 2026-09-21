package warps

import (
	"os"
	"path/filepath"
	"testing"
)

func testRegistry() *Registry {
	return &Registry{Warps: []Warp{
		{ID: 285, X: 188, Y: 157, W: 4, H: 4},
		{ID: 334, X: 190, Y: 158, W: 5, H: 5},
	}}
}

func TestAtHit(t *testing.T) {
	r := testRegistry()
	got := r.At(189, 158)
	if got == nil || got.ID != 285 {
		t.Fatalf("At(189,158) = %+v, want warp 285", got)
	}
}

func TestAtMiss(t *testing.T) {
	r := testRegistry()
	if got := r.At(0, 0); got != nil {
		t.Fatalf("At(0,0) = %+v, want nil", got)
	}
	// Half-open upper bound (area.ts inRectangularArea): X+W is outside.
	if got := r.At(192, 157); got != nil {
		t.Fatalf("At(192,157) = %+v, want nil (right edge exclusive)", got)
	}
	if got := r.At(188, 161); got != nil {
		t.Fatalf("At(188,161) = %+v, want nil (bottom edge exclusive)", got)
	}
}

func TestAtOverlapFirstMatch(t *testing.T) {
	r := testRegistry()
	// (191,159) lies in both rects; file order wins (areas.ts inArea).
	got := r.At(191, 159)
	if got == nil || got.ID != 285 {
		t.Fatalf("At(191,159) = %+v, want first match warp 285", got)
	}
}

func TestLoadBadFileReturnsError(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("Load(missing file) = nil error, want error")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bad); err == nil {
		t.Fatal("Load(invalid JSON) = nil error, want error")
	}
}

func TestLoadValidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "world.json")
	data := `{"areas":{"warps":[` +
		`{"id":285,"name":"mudwich","x":188,"y":157,"width":4,"height":4,"level":1},` +
		`{"id":334,"name":"aynor","x":411,"y":288,"width":5,"height":5,"quest":"ancientlands"}` +
		`]}}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Load(path)
	if err != nil {
		t.Fatalf("Load(valid) error = %v", err)
	}
	if len(r.Warps) != 2 {
		t.Fatalf("len(Warps) = %d, want 2", len(r.Warps))
	}
	got := r.At(189, 158)
	if got == nil || got.ID != 285 {
		t.Fatalf("At(189,158) = %+v, want warp 285", got)
	}
}
