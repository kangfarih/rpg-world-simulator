package minigame

import (
	"sort"
	"testing"
)

func TestCoursingLeaderIsClosestToCentre(t *testing.T) {
	g := NewGame(KeyCoursing, MinigameCoursing, "Coursing", CoursingCountdown)
	g.CentreX, g.CentreY = 10, 10
	pos := map[string][2]int{
		"b": {20, 10}, // dist 10
		"c": {12, 11}, // dist 3
		"a": {10, 10}, // dist 0 -> leader
	}
	if got := g.CoursingLeader(pos); got != "a" {
		t.Fatalf("CoursingLeader = %q, want %q", got, "a")
	}
	// Score grows with distance-from-centre (divisor 10).
	if s := CoursingScore(20, 10, 10, 10); s != 1 {
		t.Fatalf("CoursingScore(far) = %d, want 1", s)
	}
	if s := CoursingScore(10, 10, 10, 10); s != 0 {
		t.Fatalf("CoursingScore(centre) = %d, want 0", s)
	}
}

func TestTeamWarWinnerIsMostKills(t *testing.T) {
	g := NewGame(KeyTeamWar, MinigameTeamWar, "TeamWar", TeamWarCountdown)
	g.ApplyKill(TeamWarTeamBlue)
	g.ApplyKill(TeamWarTeamBlue)
	g.ApplyKill(TeamWarTeamBlue)
	g.ApplyKill(TeamWarTeamRed)
	if g.BlueKills != 3 || g.RedKills != 1 {
		t.Fatalf("kills = red %d blue %d, want red 1 blue 3", g.RedKills, g.BlueKills)
	}
	if got := g.Winner(); got != WinnerBlue {
		t.Fatalf("Winner = %q, want %q", got, WinnerBlue)
	}
	// Unknown teams score nothing; equal kills tie.
	g.ApplyKill(99)
	if got := g.Winner(); got != WinnerBlue {
		t.Fatalf("Winner after stray kill = %q, want %q", got, WinnerBlue)
	}
	tied := NewGame(KeyTeamWar, MinigameTeamWar, "TeamWar", TeamWarCountdown)
	if got := tied.Winner(); got != WinnerTie {
		t.Fatalf("Winner scoreless = %q, want %q", got, WinnerTie)
	}
}

func TestDeadPenaltyClampsAtZero(t *testing.T) {
	if got := ApplyDeadPenalty(25); got != 15 {
		t.Fatalf("ApplyDeadPenalty(25) = %d, want 15", got)
	}
	if got := ApplyDeadPenalty(5); got != 0 {
		t.Fatalf("ApplyDeadPenalty(5) = %d, want 0 (clamped)", got)
	}
}

func TestSplitCoursingIgnoresOddPlayerOut(t *testing.T) {
	h, p, left := SplitCoursingKeys([]string{"a", "b", "c"})
	if len(h) != 1 || len(p) != 1 || left != "c" {
		t.Fatalf("split(3) = %v %v leftover %q, want 1v1 + leftover c", h, p, left)
	}
	h, p, left = SplitCoursingKeys([]string{"a", "b", "c", "d"})
	if len(h) != 2 || len(p) != 2 || left != "" {
		t.Fatalf("split(4) = %v %v leftover %q, want 2v2 no leftover", h, p, left)
	}
}

type fakeEffects struct {
	notified  []string
	teleports map[string][2]int
}

func (f *fakeEffects) Notify(instance, msg string) { f.notified = append(f.notified, instance+":"+msg) }
func (f *fakeEffects) Teleport(instance string, x, y int) {
	if f.teleports == nil {
		f.teleports = map[string][2]int{}
	}
	f.teleports[instance] = [2]int{x, y}
}
func (f *fakeEffects) Broadcast(frames ...[]any) {}

func TestEffectsSeamNotifiesAndTeleports(t *testing.T) {
	g := NewGame(KeyCoursing, MinigameCoursing, "Coursing", CoursingCountdown)
	g.LobbyWait["b"] = &Member{Instance: "b"}
	g.LobbyWait["a"] = &Member{Instance: "a"}
	fx := &fakeEffects{}
	g.NotifyMinimumPlayers(fx, CoursingMinPlayers)
	if !sort.StringsAreSorted([]string{fx.notified[0], fx.notified[1]}) || len(fx.notified) != 2 {
		t.Fatalf("NotifyMinimumPlayers = %v, want 2 deterministic notifies", fx.notified)
	}
	got := g.TeleportToLobby(fx, []string{"a"})
	pt, ok := got["a"]
	if !ok || fx.teleports["a"] != pt {
		t.Fatalf("TeleportToLobby = %v teleports %v, want matching point", got, fx.teleports)
	}
}
