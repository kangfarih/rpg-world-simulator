package status

import "testing"

// Poison applies at its cadence with the applied power, then expiry stops
// ticks (TS handlePoison: flat damage per rate interval; expired poison is
// cleared with no final hit).
func TestPoisonApplyTickExpiry(t *testing.T) {
	tr := NewTracker()
	tr.Apply("p1", KindPoison, 5, 6000, 0)

	if got := tr.Tick(1000); len(got) != 0 {
		t.Fatalf("Tick(1000) = %v, want no ticks before cadence", got)
	}
	got := tr.Tick(2000)
	if len(got) != 1 || got[0].Instance != "p1" || got[0].Kind != KindPoison || got[0].Damage != 5 {
		t.Fatalf("Tick(2000) = %v, want one {p1 poison 5} tick", got)
	}
	got = tr.Tick(4000)
	if len(got) != 1 || got[0].Damage != 5 {
		t.Fatalf("Tick(4000) = %v, want one 5-damage tick", got)
	}
	// Duration reached at 6000: entry reaped silently, no damage.
	if got := tr.Tick(6000); len(got) != 0 {
		t.Fatalf("Tick(6000) = %v, want no ticks at expiry", got)
	}
	if tr.Has("p1", KindPoison) {
		t.Fatal("Has(poison) = true after expiry Tick, want false")
	}
	if got := tr.Tick(8000); len(got) != 0 {
		t.Fatalf("Tick(8000) = %v, want no ticks after expiry", got)
	}
}

// Zero power/duration selects Venom defaults (damage 5, 30s, 2s cadence).
func TestPoisonDefaults(t *testing.T) {
	tr := NewTracker()
	tr.Apply("p2", KindPoison, 0, 0, 0)

	got := tr.Tick(2000)
	if len(got) != 1 || got[0].Damage != PoisonDamageDefault {
		t.Fatalf("Tick(2000) = %v, want one %d-damage tick", got, PoisonDamageDefault)
	}
	if !tr.Has("p2", KindPoison) {
		t.Fatal("Has(poison) = false well within the 30s default duration, want true")
	}
}

// Clear silences all pending ticks for the instance.
func TestClearSilences(t *testing.T) {
	tr := NewTracker()
	tr.Apply("p1", KindPoison, 5, 30_000, 0)
	if got := tr.Tick(2000); len(got) != 1 {
		t.Fatalf("Tick(2000) = %v, want one tick before Clear", got)
	}
	tr.Clear("p1")
	if tr.Has("p1", KindPoison) {
		t.Fatal("Has(poison) = true after Clear, want false")
	}
	if got := tr.Tick(4000); len(got) != 0 {
		t.Fatalf("Tick(4000) = %v, want no ticks after Clear", got)
	}
}

// Freeze is present for its duration (movement/heal gates consult Has) and
// ticks cold damage on the 10s EFFECT_RATE cadence; burning ticks 20.
func TestFreezePresenceAndColdDamage(t *testing.T) {
	tr := NewTracker()
	tr.Apply("p3", KindFreezing, 0, 60_000, 0)
	if !tr.Has("p3", KindFreezing) {
		t.Fatal("Has(freezing) = false right after Apply, want true")
	}
	if got := tr.Tick(9999); len(got) != 0 {
		t.Fatalf("Tick(9999) = %v, want no cold tick before 10s cadence", got)
	}
	got := tr.Tick(10_000)
	if len(got) != 1 || got[0].Damage != FreezingDamageDefault {
		t.Fatalf("Tick(10000) = %v, want one %d cold-damage tick", got, FreezingDamageDefault)
	}
	if !tr.Has("p3", KindFreezing) {
		t.Fatal("Has(freezing) = false mid-duration, want true")
	}
	if got := tr.Tick(60_000); len(got) != 0 {
		t.Fatalf("Tick(60000) = %v, want no ticks at freezing expiry", got)
	}
	if tr.Has("p3", KindFreezing) {
		t.Fatal("Has(freezing) = true after expiry Tick, want false")
	}

	tr.Apply("p4", KindBurning, 0, 60_000, 0)
	got = tr.Tick(10_000)
	if len(got) != 1 || got[0].Kind != KindBurning || got[0].Damage != BurningDamageDefault {
		t.Fatalf("Tick(10000) = %v, want one %d burning tick", got, BurningDamageDefault)
	}
}

// Stun/terror/bleed are timed non-DoT keys: present via Has, silent on Tick,
// reaped at expiry. Persistent (negative-duration) poison ticks indefinitely.
func TestNonDoTKindsAndPersistentPoison(t *testing.T) {
	tr := NewTracker()
	tr.Apply("p5", KindStun, 0, 10_000, 0)
	tr.Apply("p5", KindTerror, 0, 60_000, 0)
	tr.Apply("p5", KindBleed, 0, 30_000, 0)
	tr.Apply("p6", KindPoison, 2, -1, 0) // Persistent poison parity

	if got := tr.Tick(5000); len(got) != 1 || got[0].Instance != "p6" || got[0].Damage != 2 {
		t.Fatalf("Tick(5000) = %v, want only the persistent poison tick", got)
	}
	for _, k := range []Kind{KindStun, KindTerror, KindBleed} {
		if !tr.Has("p5", k) {
			t.Fatalf("Has(p5, %d) = false, want true", int(k))
		}
	}
	if got := tr.Tick(10_000); len(got) != 1 {
		t.Fatalf("Tick(10000) = %v, want only the persistent poison tick (stun expires silently)", got)
	}
	if tr.Has("p5", KindStun) {
		t.Fatal("Has(stun) = true after expiry Tick, want false")
	}
	if !tr.Has("p5", KindTerror) || !tr.Has("p5", KindBleed) {
		t.Fatal("terror/bleed should still be present, want true")
	}
	// Persistent poison never expires.
	if got := tr.Tick(1_000_000); len(got) != 1 || got[0].Damage != 2 {
		t.Fatalf("Tick(1000000) = %v, want the persistent poison tick", got)
	}
	if !tr.Has("p6", KindPoison) {
		t.Fatal("Has(persistent poison) = false, want true")
	}
}

// Re-applying the same kind replaces the entry and restarts its cadence.
func TestReapplyReplaces(t *testing.T) {
	tr := NewTracker()
	tr.Apply("p1", KindPoison, 5, 30_000, 0)
	tr.Apply("p1", KindPoison, 9, 30_000, 1000)
	got := tr.Tick(3000)
	if len(got) != 1 || got[0].Damage != 9 {
		t.Fatalf("Tick(3000) = %v, want one 9-damage tick on the restarted cadence", got)
	}
}
