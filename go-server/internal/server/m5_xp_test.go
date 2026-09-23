package server

import (
	"testing"

	"rpg-world-server/internal/player"
)

// Negative /addexp awards subtract through the real m5 seam with XP clamped
// at 0 (TS addexp subtraction parity: setExperience(experience + x), floored
// so expToLevel never sees a negative). Uses a nil conn (no frames) with a
// unique key so no live state is touched; MarkDirty is a no-op without a DB.
func TestNegativeAddXPSubtractsAndClamps(t *testing.T) {
	key := "xpneg-hero"
	pstateMu.Lock()
	delete(pstates, key)
	pstateMu.Unlock()
	t.Cleanup(func() {
		pstateMu.Lock()
		delete(pstates, key)
		pstateMu.Unlock()
	})

	d := xpDeps()
	if got := player.AddXP(d, nil, key, SkillFishing, 1000); got < 1 {
		t.Fatalf("AddXP(+1000) = %d, want a level", got)
	}
	st := m5StateFor(key)
	pstateMu.Lock()
	xp := st.Skills[SkillFishing].XP
	pstateMu.Unlock()
	if xp != 1000 {
		t.Fatalf("xp = %d, want 1000", xp)
	}

	player.AddXP(d, nil, key, SkillFishing, -300)
	pstateMu.Lock()
	xp = st.Skills[SkillFishing].XP
	pstateMu.Unlock()
	if xp != 700 {
		t.Fatalf("xp after -300 = %d, want 700", xp)
	}

	// Overshoot clamps at 0 with level floored at 1 (no negative store).
	player.AddXP(d, nil, key, SkillFishing, -99999)
	pstateMu.Lock()
	xp, level := st.Skills[SkillFishing].XP, st.Skills[SkillFishing].Level
	pstateMu.Unlock()
	if xp != 0 || level != 1 {
		t.Fatalf("xp/level after overshoot = %d/%d, want 0/1", xp, level)
	}

	// Zero stays a silent no-op returning 1.
	if got := player.AddXP(d, nil, key, SkillFishing, 0); got != 1 {
		t.Fatalf("AddXP(0) = %d, want 1", got)
	}
}
