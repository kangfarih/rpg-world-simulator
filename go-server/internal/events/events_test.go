package events

import "testing"

func TestDefaultEventsRotationOrder(t *testing.T) {
	got := DefaultEvents()
	want := []string{"double drops", "1.5x experience", "lumberjacking", "mining"}
	if len(got) != len(want) {
		t.Fatalf("DefaultEvents len = %d, want %d", len(got), len(want))
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Fatalf("DefaultEvents[%d].Name = %q, want %q", i, got[i].Name, name)
		}
		if got[i].IntervalMs != CheckIntervalMs {
			t.Fatalf("DefaultEvents[%d].IntervalMs = %d, want %d", i, got[i].IntervalMs, CheckIntervalMs)
		}
	}
}

func TestDueFiresAtCadenceNotBefore(t *testing.T) {
	s := NewScheduler([]Event{{Key: "e", Name: "e", IntervalMs: 1000}})
	s.Start()
	if got := s.Due(0); len(got) != 0 {
		t.Fatalf("seeding Due(0) = %v, want empty", got)
	}
	if got := s.Due(500); len(got) != 0 {
		t.Fatalf("Due(500) = %v, want empty (not before cadence)", got)
	}
	if got := s.Due(999); len(got) != 0 {
		t.Fatalf("Due(999) = %v, want empty", got)
	}
	if got := s.Due(1000); len(got) != 1 {
		t.Fatalf("Due(1000) = %v, want 1 event at cadence", got)
	}
	if got := s.Due(1500); len(got) != 0 {
		t.Fatalf("Due(1500) = %v, want empty (only 500ms since last fire)", got)
	}
	if got := s.Due(2000); len(got) != 1 {
		t.Fatalf("Due(2000) = %v, want 1 event at next cadence", got)
	}
}

func TestStartStopGating(t *testing.T) {
	s := NewScheduler([]Event{{Key: "e", Name: "e", IntervalMs: 1000}})
	if s.IsActive() {
		t.Fatal("IsActive before Start = true, want false")
	}
	if got := s.Due(10_000); len(got) != 0 {
		t.Fatalf("Due before Start = %v, want empty", got)
	}
	s.Start()
	if !s.IsActive() {
		t.Fatal("IsActive after Start = false, want true")
	}
	if got := s.Due(0); len(got) != 0 {
		t.Fatalf("seeding Due after Start = %v, want empty", got)
	}
	s.Stop()
	if s.IsActive() {
		t.Fatal("IsActive after Stop = true, want false")
	}
	if got := s.Due(1_000_000); len(got) != 0 {
		t.Fatalf("Due while stopped = %v, want empty", got)
	}
	s.Start()
	if got := s.Due(2_000_000); len(got) != 0 {
		t.Fatalf("Due right after re-Start = %v, want empty (re-seeded)", got)
	}
	if got := s.Due(2_001_000); len(got) != 1 {
		t.Fatalf("Due one cadence after re-Start = %v, want 1 event", got)
	}
}
