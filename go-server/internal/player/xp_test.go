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

// Style/level matrix: player.handleExperience (player.ts:990-1107)
// TS-exact splits. Damage 9 -> xp 18 (no boost, full mana): Health
// ceil(18/4)=5 always; single shares ceil(18*0.75)=14; crush/hack
// ceil(18*0.375)=7 each; shared ceil(18*0.25)=5 each; chop
// floor(18*0.375)=6 each (note floor).
func TestAwardCombatXPStyleMatrix(t *testing.T) {
	cases := []struct {
		name   string
		style  int
		archer bool
		mage   bool
		want   map[int]int
	}{
		{"default unarmed", StyleNone, false, false, map[int]int{
			SkillHealth: 5, SkillStrength: 14}},
		{"slash", StyleSlash, false, false, map[int]int{
			SkillHealth: 5, SkillStrength: 14}},
		{"stab", StyleStab, false, false, map[int]int{
			SkillHealth: 5, SkillAccuracy: 14}},
		{"defensive", StyleDefensive, false, false, map[int]int{
			SkillHealth: 5, SkillDefense: 14}},
		{"crush", StyleCrush, false, false, map[int]int{
			SkillHealth: 5, SkillAccuracy: 7, SkillStrength: 7}},
		{"shared", StyleShared, false, false, map[int]int{
			SkillHealth: 5, SkillAccuracy: 5, SkillStrength: 5, SkillDefense: 5}},
		{"hack", StyleHack, false, false, map[int]int{
			SkillHealth: 5, SkillStrength: 7, SkillDefense: 7}},
		{"chop floors", StyleChop, false, false, map[int]int{
			SkillHealth: 5, SkillAccuracy: 6, SkillDefense: 6}},
		{"archer class", StyleSlash, true, false, map[int]int{
			SkillHealth: 5, SkillArchery: 14}},
		{"mage class", StyleSlash, false, true, map[int]int{
			SkillHealth: 5, SkillMagic: 14}},
		// Class routing precedes the style switch (TS isArcher/isMagic
		// early-returns): an archer-flagged stab swing still trains Archery.
		{"archer beats stab", StyleStab, true, false, map[int]int{
			SkillHealth: 5, SkillArchery: 14}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newXPState()
			d := testXPDeps(st)
			d.Style = func() int { return tc.style }
			d.HasMana = func() bool { return true }
			AwardCombatXP(d, nil, "hero", 9, tc.archer, tc.mage)
			if len(st.xp) != len(tc.want) {
				t.Fatalf("awarded skills = %v, want %v", st.xp, tc.want)
			}
			for skill, want := range tc.want {
				if st.xp[skill] != want {
					t.Fatalf("skill %s xp = %d, want %d (all=%v)",
						SkillName(skill), st.xp[skill], want, st.xp)
				}
			}
		})
	}
}

// Low-mana halving: !hasManaForAttack -> Math.floor(experience/2) applied
// to the (possibly boosted) pool BEFORE the splits. Damage 9 slash:
// 18 -> 9, Health ceil(9/4)=3, Strength ceil(6.75)=7.
func TestAwardCombatXPLowManaHalves(t *testing.T) {
	st := newXPState()
	d := testXPDeps(st)
	d.Style = func() int { return StyleSlash }
	d.HasMana = func() bool { return false }
	AwardCombatXP(d, nil, "hero", 9, false, false)
	if st.xp[SkillHealth] != 3 || st.xp[SkillStrength] != 7 {
		t.Fatalf("halved award = %v, want Health=3 Strength=7", st.xp)
	}
	if len(st.xp) != 2 {
		t.Fatalf("halved award touched %d skills, want 2: %v", len(st.xp), st.xp)
	}
}

// Boost-then-halve order with an odd intermediate: damage 9, XPBoost on,
// no mana: 18*3/2=27 -> floor(27/2)=13, Health ceil(13/4)=4,
// Strength ceil(13*0.75)=ceil(9.75)=10.
func TestAwardCombatXPBoostThenHalve(t *testing.T) {
	st := newXPState()
	d := testXPDeps(st)
	d.XPBoost = func() bool { return true }
	d.Style = func() int { return StyleSlash }
	d.HasMana = func() bool { return false }
	AwardCombatXP(d, nil, "hero", 9, false, false)
	if st.xp[SkillHealth] != 4 || st.xp[SkillStrength] != 10 {
		t.Fatalf("boosted+halved award = %v, want Health=4 Strength=10", st.xp)
	}
}

// Nil Style/HasMana preserve the legacy behavior: Strength default, no
// halving. Damage 8 -> xp 16: Health 4, Strength 12 — the exact amounts
// the combat e2e asserts for unarmed hero swings.
func TestAwardCombatXPLegacyDefaults(t *testing.T) {
	st := newXPState()
	d := testXPDeps(st)
	AwardCombatXP(d, nil, "hero", 8, false, false)
	if st.xp[SkillHealth] != 4 || st.xp[SkillStrength] != 12 {
		t.Fatalf("legacy award = %v, want Health=4 Strength=12", st.xp)
	}
}

// damage < 1 awards nothing (TS early return).
func TestAwardCombatXPZeroDamageNoop(t *testing.T) {
	st := newXPState()
	d := testXPDeps(st)
	d.Style = func() int { return StyleSlash }
	d.HasMana = func() bool { return true }
	AwardCombatXP(d, nil, "hero", 0, false, false)
	if len(st.xp) != 0 {
		t.Fatalf("zero-damage award = %v, want none", st.xp)
	}
}
