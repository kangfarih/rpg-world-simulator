package events

import (
	"testing"
	"time"
)

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

// weekendDay returns a fixed noon-UTC date for one TS getDay() value:
// 2026-09-27 is a Sunday (getDay 0), so day offsets 0..6 cover Sun..Sat.
func weekendDay(getDay int) time.Time {
	return time.Date(2026, time.September, 27+getDay, 12, 0, 0, 0, time.UTC)
}

// IsWeekend mirrors the TS gate getDay() % 6 == 0 exactly: only Sunday (0)
// and Saturday (6) pass.
func TestIsWeekendAllDays(t *testing.T) {
	for day := 0; day < 7; day++ {
		d := weekendDay(day)
		if int(d.Weekday()) != day {
			t.Fatalf("weekendDay(%d) = %s, want getDay %d", day, d.Weekday(), day)
		}
		want := day == 0 || day == 6
		if got := IsWeekend(d); got != want {
			t.Errorf("IsWeekend(getDay %d) = %v, want %v", day, got, want)
		}
	}
}

// Without a clock (SetNow never called) Due keeps the flat cadence on any
// calendar day — the pre-gate behavior other callers rely on.
func TestDueWithoutClockIgnoresCalendar(t *testing.T) {
	s := NewScheduler([]Event{{Key: "e", Name: "e", IntervalMs: 1000}})
	s.Start()
	if got := s.Due(0); len(got) != 0 {
		t.Fatalf("seeding Due(0) = %v, want empty", got)
	}
	if got := s.Due(1000); len(got) != 1 {
		t.Fatalf("Due(1000) without clock = %v, want 1 event (no gating)", got)
	}
}

// Gate-open days (Saturday/Sunday) fire at cadence once a clock is set.
func TestDueWeekendGateOpen(t *testing.T) {
	for _, day := range []int{0, 6} {
		at := weekendDay(day)
		s := NewScheduler([]Event{{Key: "e", Name: "e", IntervalMs: 1000}})
		s.SetNow(func() time.Time { return at })
		s.Start()
		if got := s.Due(0); len(got) != 0 {
			t.Fatalf("day %d: seeding Due(0) = %v, want empty", day, got)
		}
		if got := s.Due(1000); len(got) != 1 {
			t.Fatalf("day %d: Due(1000) = %v, want 1 event (gate open)", day, got)
		}
	}
}

// Gate-closed days (Monday..Friday) never fire, even far past cadence.
func TestDueWeekendGateClosed(t *testing.T) {
	for _, day := range []int{1, 2, 3, 4, 5} {
		at := weekendDay(day)
		s := NewScheduler([]Event{{Key: "e", Name: "e", IntervalMs: 1000}})
		s.SetNow(func() time.Time { return at })
		s.Start()
		if got := s.Due(0); len(got) != 0 {
			t.Fatalf("day %d: Due(0) = %v, want empty (gate closed)", day, got)
		}
		if got := s.Due(100 * CheckIntervalMs); len(got) != 0 {
			t.Fatalf("day %d: Due(far future) = %v, want empty (gate closed)", day, got)
		}
	}
}

// Weekday Due calls neither seed nor advance the clocks: after closed days,
// the first open-day Due re-seeds and firing resumes one cadence later.
func TestDueWeekendGateReopens(t *testing.T) {
	saturday := weekendDay(6)
	wednesday := weekendDay(3)
	now := wednesday
	s := NewScheduler([]Event{{Key: "e", Name: "e", IntervalMs: 1000}})
	s.SetNow(func() time.Time { return now })
	s.Start()
	if got := s.Due(50_000); len(got) != 0 {
		t.Fatalf("closed Due(50000) = %v, want empty", got)
	}
	now = saturday
	if got := s.Due(50_000); len(got) != 0 {
		t.Fatalf("first open Due(50000) = %v, want empty (re-seeds)", got)
	}
	if got := s.Due(51_000); len(got) != 1 {
		t.Fatalf("open Due(51000) = %v, want 1 event (gate open)", got)
	}
	// A weekday in the middle suppresses firing without moving the clocks.
	now = wednesday
	if got := s.Due(500_000); len(got) != 0 {
		t.Fatalf("mid-week Due(500000) = %v, want empty", got)
	}
	now = saturday
	if got := s.Due(52_000); len(got) != 1 {
		t.Fatalf("open Due(52000) after weekday = %v, want 1 event (clocks unmoved)", got)
	}
}
