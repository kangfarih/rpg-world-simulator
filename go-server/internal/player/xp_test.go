package player

import "testing"

// xpState is the minimal in-memory skill state behind the test Deps.
type xpState struct {
	xp    map[int]int
	level map[int]int
	sent  [][]any
	casts [][]any
	dirty []string
}

func testXPDeps(st *xpState) Deps {
	return Deps{
		ApplyAward: func(key string, skill, amount int) AwardResult {
			prev := st.level[skill]
			if prev == 0 {
				prev = 1
			}
			xp := st.xp[skill] + amount
			level := ExpToLevel(xp)
			if level < 1 {
				level = 1
			}
			st.xp[skill], st.level[skill] = xp, level
			return AwardResult{Prev: prev, Level: level, XP: xp, X: 100, Y: 96}
		},
		Broadcast: func(frames ...[]any) { st.casts = append(st.casts, frames...) },
		MarkDirty: func(key string) { st.dirty = append(st.dirty, key) },
		SyncFrame: func(instance string, x, y, combatLevel int) []any {
			return []any{"sync", instance}
		},
		XPBoost: func() bool { return false },
	}
}

func newXPState() *xpState {
	return &xpState{xp: map[int]int{}, level: map[int]int{}}
}

// Positive awards emit the Experience Skill + Skill Update pair.
func TestAddXPPositiveFrames(t *testing.T) {
	st := newXPState()
	d := testXPDeps(st)
	c := &Conn{Instance: "i1", Username: "hero", Send: func(frames ...[]any) {
		st.sent = append(st.sent, frames...)
	}}
	AddXP(d, c, "hero", SkillFishing, 500)
	if len(st.sent) != 2 {
		t.Fatalf("sent frames = %d, want 2 (Experience Skill + Skill Update)", len(st.sent))
	}
	if st.xp[SkillFishing] != 500 {
		t.Fatalf("xp = %d, want 500", st.xp[SkillFishing])
	}
}

// Zero is a silent no-op (TS addexp `if (!key || !x) return` parity).
func TestAddXPZeroNoop(t *testing.T) {
	st := newXPState()
	d := testXPDeps(st)
	c := &Conn{Instance: "i1", Username: "hero", Send: func(frames ...[]any) {
		st.sent = append(st.sent, frames...)
	}}
	if got := AddXP(d, c, "hero", SkillFishing, 0); got != 1 {
		t.Fatalf("AddXP(0) = %d, want 1", got)
	}
	if len(st.sent) != 0 || len(st.dirty) != 0 {
		t.Fatalf("zero award had effects: sent=%d dirty=%v", len(st.sent), st.dirty)
	}
}

// Negative amounts subtract through the same frame path as the positive
// awards (TS addexp subtraction: addExperience(negative) -> setExperience +
// the experience callback frames).
func TestAddXPNegativeSubtracts(t *testing.T) {
	st := newXPState()
	d := testXPDeps(st)
	c := &Conn{Instance: "i1", Username: "hero", Send: func(frames ...[]any) {
		st.sent = append(st.sent, frames...)
	}}
	AddXP(d, c, "hero", SkillFishing, 500)
	st.sent = nil
	AddXP(d, c, "hero", SkillFishing, -200)
	if st.xp[SkillFishing] != 300 {
		t.Fatalf("xp after -200 = %d, want 300", st.xp[SkillFishing])
	}
	if len(st.sent) != 2 {
		t.Fatalf("sent frames after subtract = %d, want 2 like the positive path", len(st.sent))
	}
}
