package abilities

import (
	"testing"
	"time"

	"rpg-world-server/internal/status"
)

// Death clears every live status entry but keeps the ability session:
// poison/burning/freezing (+ the m10 freeze-suffix key) stop ticking while
// mana, last-target and unlock levels survive (player handleDeath parity:
// status.clear() + setPoison() cure, abilities kept).
func TestClearStatusOnDeath(t *testing.T) {
	inst, user := "death-victim", "death-user"
	t.Cleanup(func() {
		ClearStatus(inst)
		abMu.Lock()
		delete(abMana, inst)
		delete(abTarget, inst)
		delete(abLevels, user)
		abMu.Unlock()
	})

	now := time.Now().UnixMilli()
	abStatus.Apply(status.Instance(inst), status.KindPoison, 0, 0, now)
	abStatus.Apply(status.Instance(inst), status.KindBurning, 0, 0, now)
	abStatus.Apply(status.Instance(inst), status.KindFreezing, 0, 0, now)
	abStatus.Apply(status.Instance(inst+freezeSuffix), status.KindFreezing, 0, -1, now)
	abMu.Lock()
	abMana[inst] = 12
	abTarget[inst] = "m9test"
	abLevels[user] = map[string]int{"running": 2}
	abMu.Unlock()

	ClearStatus(inst)
	ClearStatus("") // empty instance is a no-op, never panics

	for _, k := range []status.Kind{status.KindPoison, status.KindBurning, status.KindFreezing} {
		if abStatus.Has(status.Instance(inst), k) {
			t.Fatalf("kind %d survived ClearStatus", int(k))
		}
	}
	if abStatus.Has(status.Instance(inst+freezeSuffix), status.KindFreezing) {
		t.Fatal("freeze-suffix entry survived ClearStatus")
	}
	abMu.Lock()
	defer abMu.Unlock()
	if abMana[inst] != 12 || abTarget[inst] != "m9test" {
		t.Fatalf("session state wiped: mana=%d target=%q", abMana[inst], abTarget[inst])
	}
	if abLevels[user]["running"] != 2 {
		t.Fatal("ability unlock wiped by ClearStatus")
	}
}
