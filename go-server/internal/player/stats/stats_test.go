package stats

import (
	"fmt"
	"testing"
)

// Milestone matrix: every gather skill (except foraging) fires exactly
// `<skill><N>` at each of the 7 TS milestones and nothing in between.
func TestHandleSkillMilestoneMatrix(t *testing.T) {
	for _, skill := range []string{"lumberjacking", "mining", "fishing"} {
		st := &State{}
		for n := 1; n <= 10_000; n++ {
			key, ok := HandleSkill(st, skill)
			wantMilestone := false
			for _, m := range Milestones {
				if n == m {
					wantMilestone = true
				}
			}
			if wantMilestone {
				if !ok || key != fmt.Sprintf("%s%d", skill, n) {
					t.Fatalf("%s swing %d: got (%q,%v), want (%q,true)",
						skill, n, key, ok, fmt.Sprintf("%s%d", skill, n))
				}
			} else if ok {
				t.Fatalf("%s swing %d: unexpected achievement %q", skill, n, key)
			}
		}
		if got := st.Resources[skill]; got != 10_000 {
			t.Fatalf("%s counter = %d, want 10000", skill, got)
		}
	}
}

// Foraging is skipped entirely (TS early return: no counter, no achievement).
func TestHandleSkillForagingSkipped(t *testing.T) {
	st := &State{}
	for i := 0; i < 20; i++ {
		if key, ok := HandleSkill(st, "foraging"); ok || key != "" {
			t.Fatalf("foraging swing %d fired %q", i, key)
		}
	}
	if len(st.Resources) != 0 {
		t.Fatalf("foraging must not advance counters, got %v", st.Resources)
	}
}

// Examiner path: dedupe, 10/25/50 milestones, no re-fire past 50.
func TestAddMobExamineMilestones(t *testing.T) {
	st := &State{}
	fired := map[string]int{}
	for i := 0; i < 60; i++ {
		if ach, ok := AddMobExamine(st, fmt.Sprintf("mob%d", i)); ok {
			fired[ach]++
		}
	}
	for _, want := range []string{"examiner10", "examiner25", "examiner50"} {
		if fired[want] != 1 {
			t.Fatalf("%s fired %d times, want 1 (fired=%v)", want, fired[want], fired)
		}
	}
	if len(fired) != 3 {
		t.Fatalf("unexpected examiner achievements: %v", fired)
	}
	// Re-examining known keys never advances or fires.
	before := len(st.MobExamines)
	for i := 0; i < 60; i++ {
		if ach, ok := AddMobExamine(st, fmt.Sprintf("mob%d", i)); ok || ach != "" {
			t.Fatalf("duplicate examine fired %q", ach)
		}
	}
	if len(st.MobExamines) != before {
		t.Fatal("duplicate examines must not grow the list")
	}
}

// Mob kills are counters only (TS addMobKill feeds no achievement).
func TestAddMobKillCounterOnly(t *testing.T) {
	st := &State{}
	for i := 0; i < 15; i++ {
		AddMobKill(st, "rat")
	}
	if st.MobKills["rat"] != 15 {
		t.Fatalf("mobKills[rat] = %d, want 15", st.MobKills["rat"])
	}
}

// Drops accumulate per key.
func TestAddDropAccumulates(t *testing.T) {
	st := &State{}
	AddDrop(st, "logs", 1)
	AddDrop(st, "logs", 3)
	AddDrop(st, "gold", 5)
	if st.Drops["logs"] != 4 || st.Drops["gold"] != 5 {
		t.Fatalf("drops = %v, want logs:4 gold:5", st.Drops)
	}
}

// Nil-state calls are safe (adapter may race a fresh username).
func TestNilStateSafe(t *testing.T) {
	if k, ok := HandleSkill(nil, "mining"); ok || k != "" {
		t.Fatal("nil HandleSkill must not fire")
	}
	AddMobKill(nil, "rat")
	if a, ok := AddMobExamine(nil, "rat"); ok || a != "" {
		t.Fatal("nil AddMobExamine must not fire")
	}
	AddDrop(nil, "gold", 1)
}
