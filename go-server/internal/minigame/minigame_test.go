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
	sends     []fakeSend
	pointers  []string
	positions map[string][2]int
}

type fakeSend struct {
	instance string
	opcode   int
	action   any
}

func (f *fakeEffects) Notify(instance, msg string) { f.notified = append(f.notified, instance+":"+msg) }
func (f *fakeEffects) Teleport(instance string, x, y int) {
	if f.teleports == nil {
		f.teleports = map[string][2]int{}
	}
	f.teleports[instance] = [2]int{x, y}
}
func (f *fakeEffects) Broadcast(frames ...[]any) {}
func (f *fakeEffects) SendMinigame(instance string, opcode int, data map[string]any) {
	f.sends = append(f.sends, fakeSend{instance: instance, opcode: opcode, action: data["action"]})
}
func (f *fakeEffects) SendPointer(instance string, target string) {
	f.pointers = append(f.pointers, instance+"->"+target)
}
func (f *fakeEffects) EntityPos(instance string) (int, int, bool) {
	if f.positions == nil {
		return 0, 0, false
	}
	pt, ok := f.positions[instance]
	if !ok {
		return 0, 0, false
	}
	return pt[0], pt[1], true
}

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

func TestManagerJoinStartScoreStop(t *testing.T) {
	m := NewManager()
	m.EnsureGame(KeyCoursing, MinigameCoursing, "Coursing", CoursingCountdown)
	m.EnsureGame(KeyTeamWar, MinigameTeamWar, "TeamWar", TeamWarCountdown)
	m.games[KeyCoursing].CentreX, m.games[KeyCoursing].CentreY = 10, 10
	fx := &fakeEffects{positions: map[string][2]int{}}

	// join → lobby packets + notifies.
	if !m.AddPlayer(KeyCoursing, "a", fx) || !m.AddPlayer(KeyCoursing, "b", fx) {
		t.Fatalf("AddPlayer failed")
	}
	if m.AddPlayer(KeyCoursing, "a", fx) {
		t.Fatalf("duplicate AddPlayer should be rejected")
	}
	if !m.IsInLobby(KeyCoursing, "a") || !m.IsInLobby(KeyCoursing, "b") {
		t.Fatalf("lobby membership missing")
	}

	// start → hunter/prey split, targets, teleports.
	assignments := m.StartCoursing(fx)
	if len(assignments) != 2 {
		t.Fatalf("StartCoursing assignments = %d, want 2", len(assignments))
	}
	hunters, prey := 0, 0
	var preyInst string
	for inst, mem := range assignments {
		switch mem.Team {
		case CoursingTeamHunter:
			hunters++
		case CoursingTeamPrey:
			prey++
			preyInst = inst
		default:
			t.Fatalf("bad team %d for %s", mem.Team, inst)
		}
		if mem.Target == "" {
			t.Fatalf("missing target for %s", inst)
		}
		if _, ok := assignments[mem.Target]; !ok {
			t.Fatalf("target %q of %s not in game", mem.Target, inst)
		}
	}
	if hunters != 1 || prey != 1 {
		t.Fatalf("split = %d hunters %d prey, want 1v1", hunters, prey)
	}
	if m.IsInLobby(KeyCoursing, "a") || m.InGameCount(KeyCoursing) != 2 {
		t.Fatalf("lobby should drain into game")
	}
	if len(fx.teleports) != 2 {
		t.Fatalf("teleports = %v, want 2 spawns", fx.teleports)
	}

	// score → prey earns distance/divisor on a score tick.
	fx.positions[preyInst] = [2]int{30, 10} // dist 20 → +2
	m.SetCountdown(KeyCoursing, 5)          // decrements to 4 → score tick
	res := m.Tick(KeyCoursing, fx)
	got, ok := res.Scored[preyInst]
	if !ok {
		t.Fatalf("Tick scored = %v, want prey %s scored", res.Scored, preyInst)
	}
	if want := CoursingScore(30, 10, 10, 10); got != want {
		t.Fatalf("prey score = %d, want %d", got, want)
	}
	if mem, ok := m.MemberCopy(KeyCoursing, preyInst); !ok || mem.Score != got {
		t.Fatalf("member score = %+v, want %d", mem, got)
	}

	// stop → End packets, cleared state, lobby teleports.
	stopped := m.Stop(KeyCoursing, fx)
	if len(stopped) != 2 || m.InGameCount(KeyCoursing) != 0 {
		t.Fatalf("stop = %v count %d, want 2 cleared", stopped, m.InGameCount(KeyCoursing))
	}
	if m.SnapshotCountdown(KeyCoursing) != 0 {
		t.Fatalf("stop should re-arm countdown to 0")
	}

	// teamwar kill → score → stop lifecycle.
	fx2 := &fakeEffects{}
	m.AddPlayer(KeyTeamWar, "x", fx2)
	m.AddPlayer(KeyTeamWar, "y", fx2)
	tw := m.StartTeamWar(fx2)
	if len(tw) != 2 {
		t.Fatalf("StartTeamWar assignments = %d, want 2", len(tw))
	}
	var blueInst string
	for inst, mem := range tw {
		if mem.Team == TeamWarTeamBlue {
			blueInst = inst
		}
	}
	m.RecordKill(KeyTeamWar, blueInst)
	if r, b := m.KillCounts(KeyTeamWar); b != 1 || r != 0 {
		t.Fatalf("kills = red %d blue %d, want red 0 blue 1", r, b)
	}
	if got := m.Winner(KeyTeamWar); got != WinnerBlue {
		t.Fatalf("Winner = %q, want blue", got)
	}
	_, _, remaining := m.Disconnect(KeyTeamWar, "x")
	if !ShouldStop(remaining) {
		t.Fatalf("remaining = %d, want stop", remaining)
	}
	m.Stop(KeyTeamWar, fx2)
	if r, b := m.KillCounts(KeyTeamWar); r != 0 || b != 0 {
		t.Fatalf("stop should reset kills, got red %d blue %d", r, b)
	}
}

func TestManagerLeaderAndSplits(t *testing.T) {
	m := NewManager()
	m.EnsureGame(KeyCoursing, MinigameCoursing, "Coursing", CoursingCountdown)
	m.games[KeyCoursing].CentreX, m.games[KeyCoursing].CentreY = 10, 10
	pos := map[string][2]int{"a": {10, 10}, "b": {20, 10}}
	if got := m.Leader(KeyCoursing, pos); got != "a" {
		t.Fatalf("Leader = %q, want a", got)
	}
	h, p, left := SplitCoursingKeys([]string{"a", "b", "c"})
	if len(h) != 1 || len(p) != 1 || left == "" {
		t.Fatalf("SplitCoursingKeys odd = %v %v %q", h, p, left)
	}
	blue, red := SplitTeamWarKeys([]string{"a", "b", "c", "d"})
	if len(blue) != 2 || len(red) != 2 {
		t.Fatalf("SplitTeamWarKeys = %v %v, want 2v2", blue, red)
	}
}
