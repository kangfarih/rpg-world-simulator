package quest

import (
	"testing"

	"rpg-world-server/internal/protocol"
)

// Finish jumps a single-stage achievement straight to done with one
// progress frame + the finish popup (achievement.finish parity: no
// discovery popup when discovery and finish coincide).
func TestFinishSingleStage(t *testing.T) {
	d, store, bus, _ := withFixture(t)
	Achs["fin1"] = &AchDef{Key: "fin1", StageCount: 1, Raw: AchievementRaw{Name: "Fin"}}
	c := &fakeConn{instance: "i1", username: "u-fin"}
	st := StateFor("u-fin")

	Finish(c, d, st, "fin1")
	if st.Achs["fin1"] != 1 {
		t.Fatalf("stage = %d, want 1", st.Achs["fin1"])
	}
	if store.dirty["u-fin"] != 1 {
		t.Fatal("finish must mark the player dirty")
	}
	prog, popups := 0, 0
	for _, f := range bus.sent["i1"] {
		id, op := frameOp(f)
		switch {
		case id == protocol.PacketAchievement && op == AchievementProgress:
			prog++
		case id == protocol.PacketNotification && op == NotificationPopup:
			popups++
		}
	}
	if prog != 1 {
		t.Fatalf("progress frames = %d, want exactly 1", prog)
	}
	if popups != 1 {
		t.Fatalf("popups = %d, want exactly 1 (Completed, no Discovered)", popups)
	}

	// Re-finishing is a no-op (isFinished guard): no new frames.
	before := len(bus.sent["i1"])
	Finish(c, d, st, "fin1")
	if len(bus.sent["i1"]) != before {
		t.Fatal("re-finish must send nothing")
	}

	// Unknown keys are ignored.
	Finish(c, d, st, "nope")
	if len(bus.sent["i1"]) != before {
		t.Fatal("finish of unknown key must send nothing")
	}
}

// Finish jumps multi-stage achievements from any partial stage to done.
func TestFinishMultiStage(t *testing.T) {
	d, _, bus, _ := withFixture(t)
	Achs["fin3"] = &AchDef{Key: "fin3", StageCount: 3, Raw: AchievementRaw{Name: "Fin3"}}
	c := &fakeConn{instance: "i3", username: "u-fin3"}
	st := StateFor("u-fin3")
	st.Achs["fin3"] = 1

	Finish(c, d, st, "fin3")
	if st.Achs["fin3"] != 3 {
		t.Fatalf("stage = %d, want 3", st.Achs["fin3"])
	}
	prog := 0
	for _, f := range bus.sent["i3"] {
		if id, op := frameOp(f); id == protocol.PacketAchievement && op == AchievementProgress {
			prog++
		}
	}
	if prog != 1 {
		t.Fatalf("progress frames = %d, want exactly 1 at the finish stage", prog)
	}
}
