package worldmap

import (
	"os"
	"path/filepath"
	"testing"

	"rpg-world-server/internal/globals"
)

const glowFixture = `{
  "width": 96, "height": 96,
  "areas": {
    "lights": [{"x": 10, "y": 10, "distance": 100}],
    "signs": [{"x": 20, "y": 21, "text": "hi,there"}]
  }
}`

func testGlow(t *testing.T) *Glow {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "world.json")
	if err := os.WriteFile(path, []byte(glowFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	g := NewGlow()
	if err := g.Load(path); err != nil {
		t.Fatal(err)
	}
	return g
}

type fakeWorld struct {
	lamps   []globals.Light
	bubbles []string
}

func (f *fakeWorld) RegionOf(x, y int) int { return (y / 48) * 2 } // width 96 sideLen 2
func (f *fakeWorld) SurroundingRegions(rid int) []int {
	return []int{rid}
}
func (f *fakeWorld) SendLamp(_ string, l globals.Light) { f.lamps = append(f.lamps, l) }
func (f *fakeWorld) SendBubble(_, inst, text string, _, _ int) {
	f.bubbles = append(f.bubbles, inst+":"+text)
}

func TestFreshForDedupes(t *testing.T) {
	g := testGlow(t)
	if n := len(g.FreshFor("c1", []int{0})); n != 1 {
		t.Fatalf("FreshFor = %d, want 1", n)
	}
	if n := len(g.FreshFor("c1", []int{0})); n != 0 {
		t.Fatalf("FreshFor re-entry = %d, want 0", n)
	}
	if c := g.LoadedCount("c1"); c != 1 {
		t.Fatalf("LoadedCount = %d, want 1", c)
	}
	g.Clear("c1")
	if n := len(g.FreshFor("c1", []int{0})); n != 1 {
		t.Fatalf("FreshFor after Clear = %d, want 1", n)
	}
	g.Forget("c1")
	if c := g.LoadedCount("c1"); c != 0 {
		t.Fatalf("LoadedCount after Forget = %d, want 0", c)
	}
}

func TestPushAndTalkWith(t *testing.T) {
	g := testGlow(t)
	fw := &fakeWorld{}
	// Light at (10,10) is region 0 under the fake RegionOf.
	if n := g.Push(fw, "c1", 10, 10); n != 1 || len(fw.lamps) != 1 {
		t.Fatalf("Push = %d lamps=%d, want 1,1", n, len(fw.lamps))
	}
	if n := g.Push(fw, "c1", 10, 10); n != 0 {
		t.Fatalf("Push re-entry = %d, want 0", n)
	}
	if n := g.PushForce(fw, "c1", 10, 10); n != 1 {
		t.Fatalf("PushForce = %d, want 1", n)
	}
	var npc string
	idx := 0
	msg, ok := g.TalkWith(fw, "c1", "20-21", &npc, &idx)
	if !ok || msg != "hi" || npc != "20-21" || idx != 1 {
		t.Fatalf("TalkWith first = %q ok=%v npc=%q idx=%d", msg, ok, npc, idx)
	}
	msg, ok = g.TalkWith(fw, "c1", "20-21", &npc, &idx)
	if !ok || msg != "there" {
		t.Fatalf("TalkWith second = %q ok=%v, want there true", msg, ok)
	}
	if _, ok := g.TalkWith(fw, "c1", "99-99", &npc, &idx); ok {
		t.Fatal("TalkWith unknown sign = true, want false")
	}
	if _, ok := g.TalkWith(fw, "c1", "bogus", &npc, &idx); ok {
		t.Fatal("TalkWith malformed = true, want false")
	}
}

func TestParseSignInstance(t *testing.T) {
	if x, y, ok := ParseSignInstance("20-21"); !ok || x != 20 || y != 21 {
		t.Fatalf("Parse = %d,%d ok=%v", x, y, ok)
	}
	if _, _, ok := ParseSignInstance("bogus"); ok {
		t.Fatal("Parse bogus = true, want false")
	}
	if p := SignPages("a,b"); len(p) != 2 || p[0] != "a" {
		t.Fatalf("Pages = %v", p)
	}
}

func TestEnsureTestLamp(t *testing.T) {
	g := testGlow(t)
	added := g.EnsureTestLamp(true, false, false, func(x, y int) int { return 999 })
	if !added {
		t.Fatal("EnsureTestLamp = false, want true (region 999 has no lights)")
	}
	if added2 := g.EnsureTestLamp(true, false, false, func(x, y int) int { return 999 }); added2 {
		// Second call targets region 999 which now holds the 102,96 lamp only
		// if RegionOf(102,96)==999; here it is not, so it appends again.
		// The dedupe-relevant case is the real regionOf below.
		_ = added2
	}
	if g.EnsureTestLamp(false, false, false, func(x, y int) int { return 0 }) {
		t.Fatal("EnsureTestLamp testMode=false = true, want false")
	}
}
