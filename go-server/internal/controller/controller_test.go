package controller

import (
	"testing"
	"time"
)

type warpFakeStore struct {
	jailed   bool
	admin    bool
	level    int
	quests   map[string]bool
	achs     map[string]bool
	formated map[string]string
}

func (f *warpFakeStore) IsJailed(string) bool   { return f.jailed }
func (f *warpFakeStore) IsAdmin(string) bool    { return f.admin }
func (f *warpFakeStore) PlayerLevel(string) int { return f.level }
func (f *warpFakeStore) QuestFinished(_ string, q string) bool {
	return f.quests[q]
}
func (f *warpFakeStore) AchievementDone(_ string, a string) bool { return f.achs[a] }
func (f *warpFakeStore) FormatName(n string) string {
	if s, ok := f.formated[n]; ok {
		return s
	}
	return n
}

func testWarp() *WarpEntry {
	return &WarpEntry{Name: "mudwich", X: 188, Y: 157, W: 4, H: 4}
}

func TestAuthorizeJailDenies(t *testing.T) {
	t.Setenv("WORLD_WARP_COOLDOWN_MS", "0")
	c := NewWarpController()
	st := &warpFakeStore{jailed: true}
	if msg := c.Authorize("u", testWarp(), time.Now().UnixMilli(), st); msg != "warps:CANNOT_WARP_JAIL" {
		t.Fatalf("Authorize jailed = %q, want jail deny", msg)
	}
}

func TestAuthorizeCooldownDeniesAndRecords(t *testing.T) {
	t.Setenv("WORLD_WARP_COOLDOWN_MS", "300000")
	c := NewWarpController()
	st := &warpFakeStore{}
	now := time.Now().UnixMilli()
	if msg := c.Authorize("u", testWarp(), now, st); msg != "" {
		t.Fatalf("first Authorize = %q, want allow", msg)
	}
	c.Record("u", now)
	msg := c.Authorize("u", testWarp(), now+1000, st)
	if msg == "" || len(msg) < len("warps:CANNOT_WARP_COOLDOWN") {
		t.Fatalf("second Authorize = %q, want cooldown deny", msg)
	}
	// Admins bypass cooldown.
	admin := &warpFakeStore{admin: true}
	if msg := c.Authorize("u", testWarp(), now+1000, admin); msg != "" {
		t.Fatalf("admin Authorize = %q, want allow", msg)
	}
}

func TestAuthorizeLevelQuestAch(t *testing.T) {
	t.Setenv("WORLD_WARP_COOLDOWN_MS", "0")
	c := NewWarpController()
	w := &WarpEntry{Name: "aynor", X: 1, Y: 1, W: 2, H: 2, Level: 5, Quest: "q", Ach: "a"}
	st := &warpFakeStore{level: 1}
	if msg := c.Authorize("u", w, 0, st); msg != "warps:CANNOT_WARP_LEVEL;level=5" {
		t.Fatalf("level gate = %q", msg)
	}
	st.level = 9
	if msg := c.Authorize("u", w, 0, st); msg == "" || !contains(msg, "CANNOT_WARP_QUEST") {
		t.Fatalf("quest gate = %q, want quest deny", msg)
	}
	st.quests = map[string]bool{"q": true}
	if msg := c.Authorize("u", w, 0, st); msg != "warps:CANNOT_WARP_ACHIEVEMENT" {
		t.Fatalf("ach gate = %q", msg)
	}
	st.achs = map[string]bool{"a": true}
	if msg := c.Authorize("u", w, 0, st); msg != "" {
		t.Fatalf("all met = %q, want allow", msg)
	}
}

func TestLandingDegenerate(t *testing.T) {
	if _, _, ok := Landing(&WarpEntry{W: 0, H: 4}, func(int) int { return 0 }); ok {
		t.Fatal("Landing degenerate = ok, want false")
	}
	x, y, ok := Landing(testWarp(), func(n int) int { return n - 1 })
	if !ok || x != 188+3 || y != 157+3 {
		t.Fatalf("Landing = %d,%d ok=%v, want 191,160 true", x, y, ok)
	}
}

func TestEventBootAndDue(t *testing.T) {
	t.Setenv("WORLD_EVENT_MS", "50")
	c := NewEventController()
	n, every := c.Boot()
	if n != 4 || every != 50 {
		t.Fatalf("Boot = %d,%d want 4,50", n, every)
	}
	now := time.Now().UnixMilli()
	if due := c.Due(now); len(due) != 0 {
		t.Fatalf("seeding Due = %v, want nil", due)
	}
	due := c.Due(now + 60)
	if len(due) != 4 {
		t.Fatalf("Due = %d events, want 4", len(due))
	}
	if !c.IsActive("double-drops") || !c.IsActive("experience") {
		t.Fatal("IsActive missing fired keys")
	}
	if c.Fired() != 4 || c.Interval() != 50 {
		t.Fatalf("Fired=%d Interval=%d want 4,50", c.Fired(), c.Interval())
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && search(s, sub)
}

func search(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
