package abilities

import (
	"path/filepath"
	"testing"
	"time"

	"rpg-world-server/internal/status"
)

// cureDeps installs an isolated session seam set capturing DoT damage.
func cureDeps(t *testing.T, dmg *[]string) SessionDeps {
	t.Helper()
	return SessionDeps{
		DamagePlayer: func(instance string, amount int) (int, bool) {
			*dmg = append(*dmg, instance)
			return 100, true
		},
		DamageMob: func(instance string, amount int) bool { return false },
		Broadcast: func(frames ...[]any) {},
	}
}

// Apply -> cure -> tick emits nothing (the /poison toggle-off cure must
// stop the 30s Venom DoT instead of leaving it running). A still-poisoned
// control instance keeps ticking through the same StatusTick.
func TestRemovePoisonStopsTicks(t *testing.T) {
	old := sdeps
	t.Cleanup(func() { sdeps = old })

	var dmg []string
	ConfigureSessions(cureDeps(t, &dmg))

	past := time.Now().UnixMilli() - 5000 // next tick already due
	abStatus.Apply(status.Instance("cured"), status.KindPoison, 0, 0, past)
	abStatus.Apply(status.Instance("sick"), status.KindPoison, 0, 0, past)

	RemovePoison("cured")
	RemovePoison("") // empty instance is a no-op, never panics

	StatusTick()

	for _, inst := range dmg {
		if inst == "cured" {
			t.Fatalf("cured instance took DoT damage: %v", dmg)
		}
	}
	found := false
	for _, inst := range dmg {
		if inst == "sick" {
			found = true
		}
	}
	if !found {
		t.Fatalf("control poisoned instance took no damage: %v", dmg)
	}

	// Cleanup: cure the control so no state leaks to other tests.
	RemovePoison("sick")
}

// ResetAbilities clears the server-side unlock map (abilities.ts reset()
// parity): Has flips false, LoginBatch goes empty, and a re-grant sends a
// fresh Add frame (not Update).
func TestResetAbilitiesClearsUnlocks(t *testing.T) {
	path := filepath.Join("..", "..", "..", "packages", "server", "data", "abilities.json")
	old := sdeps
	t.Cleanup(func() { sdeps = old })
	ConfigureSessions(SessionDeps{
		DataPath: func(name string) string {
			if name == "abilities" {
				return path
			}
			return name
		},
		Broadcast: func(frames ...[]any) {},
	})
	if loadRegistry() == nil {
		t.Skip("abilities.json not present, skipping unlock-map test")
	}

	user := "reset-test-user"
	t.Cleanup(func() { ResetAbilities(user) })
	var sent [][]any
	c := &Conn{
		Instance: "reset-i1",
		Username: user,
		Send:     func(frames ...[]any) { sent = append(sent, frames...) },
		Notify:   func(string) {},
	}

	if !GrantAbility(c, user, "run", 1) {
		t.Fatal("GrantAbility(run) refused")
	}
	if !Has(user, "run") {
		t.Fatal("Has(run) = false after grant, want true")
	}
	// Store a quick slot through the same path the C->S QuickSlot opcode
	// writes, so reset must clear it too.
	HandleAbility(c, []byte(`{"opcode":4,"key":"run","index":2}`))

	ResetAbilities(user)
	ResetAbilities("") // empty username is a no-op, never panics
	if Has(user, "run") {
		t.Fatal("Has(run) = true after reset, want false (unlock map cleared)")
	}
	batch := LoginBatch(user)
	if len(batch) != 3 {
		t.Fatalf("LoginBatch frame = %v, want [id opcode data]", batch)
	}
	abilities, ok := batch[2].(map[string]any)["abilities"].([]abilityEntry)
	if !ok {
		t.Fatalf("LoginBatch payload = %#v, want abilities list", batch[2])
	}
	if len(abilities) != 0 {
		t.Fatalf("LoginBatch after reset = %v, want empty list", abilities)
	}

	// Re-grant works and sends a fresh Add frame (unlock, not re-grant).
	sent = nil
	if !GrantAbility(c, user, "run", 1) {
		t.Fatal("re-grant after reset refused")
	}
	if len(sent) != 1 || len(sent[0]) != 3 || sent[0][1] != AbilityAdd {
		t.Fatalf("re-grant frames = %v, want one Ability Add frame", sent)
	}
}
