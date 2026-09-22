package entity

import (
	"encoding/json"
	"testing"
)

// Take lifecycle: open records the opener, slots keep stable indices
// across takes (holes), invalid takes are rejected, emptying destroys.
func TestLootBagTakeLifecycle(t *testing.T) {
	inst := SpawnLootBag("hero", 100, 96, []Drop{
		{Key: "gold", Count: 5}, {Key: "logs", Count: 2}, {Key: "arrow", Count: 9},
	})
	if inst == "" {
		t.Fatal("SpawnLootBag returned empty instance")
	}
	t.Cleanup(func() { DestroyLoot(inst, "test") })
	if !IsBag(inst) {
		t.Fatal("multi-drop spawn must be a bag")
	}

	if OpenBag("p1", inst) != true {
		t.Fatal("OpenBag on a live bag must succeed")
	}
	if open, ok := ActiveBag("p1"); !ok || open != inst {
		t.Fatalf("ActiveBag(p1) = %q,%v, want %q,true", open, ok, inst)
	}

	slots, ok := BagSlots(inst)
	if !ok || len(slots) != 3 {
		t.Fatalf("BagSlots = %v,%v, want 3 slots", slots, ok)
	}
	for i, s := range slots {
		if s.Index != i {
			t.Fatalf("slot %d has index %d, want stable identity", i, s.Index)
		}
	}

	// Take the middle slot: survivors keep their indices (hole parity).
	taken, remaining, ok := TakeBagItem(inst, 1)
	if !ok || taken.Key != "logs" || taken.Count != 2 || remaining != 2 {
		t.Fatalf("TakeBagItem(1) = %+v,%d,%v, want logs x2, 2 left", taken, remaining, ok)
	}
	slots, _ = BagSlots(inst)
	if len(slots) != 2 || slots[0].Index != 0 || slots[1].Index != 2 {
		t.Fatalf("slots after take = %+v, want stable indices [0,2]", slots)
	}

	// Retaking the hole and out-of-range indices fail.
	if _, _, ok := TakeBagItem(inst, 1); ok {
		t.Fatal("retaking a hole must fail")
	}
	if _, _, ok := TakeBagItem(inst, 5); ok {
		t.Fatal("out-of-range take must fail")
	}
	if _, _, ok := TakeBagItem(inst, -1); ok {
		t.Fatal("negative take must fail")
	}
	if _, _, ok := TakeBagItem("nope", 0); ok {
		t.Fatal("take from unknown bag must fail")
	}

	// Emptying leaves no live slots; destroy clears the opener.
	if _, _, ok := TakeBagItem(inst, 0); !ok {
		t.Fatal("take 0 must succeed")
	}
	if _, _, ok := TakeBagItem(inst, 2); !ok {
		t.Fatal("take 2 must succeed")
	}
	if slots, _ := BagSlots(inst); len(slots) != 0 {
		t.Fatalf("emptied bag slots = %+v, want none", slots)
	}
	DestroyLoot(inst, "emptied")
	if _, ok := ActiveBag("p1"); ok {
		t.Fatal("destroy must clear the opener")
	}
	if IsLoot(inst) {
		t.Fatal("destroyed bag must be gone")
	}
}

// Single items are not bags: open/slots/take all refuse.
func TestSingleItemIsNotABag(t *testing.T) {
	inst := SpawnLootAt("hero", "logs", 1, 101, 96)
	if inst == "" {
		t.Fatal("SpawnLootAt returned empty instance")
	}
	t.Cleanup(func() { DestroyLoot(inst, "test") })
	if IsBag(inst) {
		t.Fatal("single drop must not be a bag")
	}
	if OpenBag("p1", inst) {
		t.Fatal("OpenBag on an item must fail")
	}
	if _, ok := BagSlots(inst); ok {
		t.Fatal("BagSlots on an item must fail")
	}
	if _, _, ok := TakeBagItem(inst, 0); ok {
		t.Fatal("TakeBagItem on an item must fail")
	}
}

// DescText accepts string or string[] (mobs.json carries both: rat vs eye),
// and Pick draws uniformly from arrays / reports absence.
func TestDescText(t *testing.T) {
	var s DescText
	if err := json.Unmarshal([]byte(`"just a rat"`), &s); err != nil {
		t.Fatalf("string desc: %v", err)
	}
	if text, ok := s.Pick(); !ok || text != "just a rat" {
		t.Fatalf("Pick = %q,%v, want the string", text, ok)
	}
	var arr DescText
	if err := json.Unmarshal([]byte(`["one","two"]`), &arr); err != nil {
		t.Fatalf("array desc: %v", err)
	}
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		text, ok := arr.Pick()
		if !ok {
			t.Fatal("array Pick must succeed")
		}
		seen[text] = true
	}
	if !seen["one"] || !seen["two"] {
		t.Fatalf("array Pick never drew both entries: %v", seen)
	}
	var empty DescText
	if _, ok := empty.Pick(); ok {
		t.Fatal("empty Pick must report absence")
	}
	var missing MobProfile
	if err := json.Unmarshal([]byte(`{"name":"Golem"}`), &missing); err != nil {
		t.Fatalf("profile without description: %v", err)
	}
	if _, ok := missing.Description.Pick(); ok {
		t.Fatal("missing description must report absence")
	}
}
