package entity

import (
	"testing"
	"time"
)

// Top-damager kill credit (TS mob/handler.ts:85-109 + character.ts:987-996
// + mob.ts getDamageTable): loot ownership + quest kill go to the
// highest-damage dealer that still resolves to an existing player — never
// to the last hitter.

func creditMob() *testMob {
	p := ratProfile()
	p.HitPoints = 100
	return newTestMob("mob-credit", "rat", p, 100, 100)
}

func TestTopDamagerCreditedOverLastHitter(t *testing.T) {
	resetAreas()
	w := newSimFake()
	w.withPlayer("heroA-inst", "heroA", 100, 101, 1, 1)
	w.withPlayer("heroB-inst", "heroB", 100, 102, 1, 1)
	a := &PlayerView{Instance: "heroA-inst", Username: "heroA"}
	b := &PlayerView{Instance: "heroB-inst", Username: "heroB"}
	m := creditMob()
	now := time.Now()

	HitMob(m, a, 90, w, now, func() bool { return true }) // hp 10
	HitMob(m, b, 10, w, now, func() bool { return true }) // killing blow, hp 0

	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.lootDrops) != 1 || w.lootDrops[0].owner != "heroA" {
		t.Fatalf("loot owner must be top damager heroA: %+v", w.lootDrops)
	}
	if len(w.quests) != 1 || w.quests[0].killer != "heroA-inst" {
		t.Fatalf("quest kill must credit heroA-inst: %+v", w.quests)
	}
}

// Overkill is clamped to remaining HP (addToDamageTable parity), so a huge
// last swing cannot steal credit from the true top damager.
func TestOverkillClampedForCredit(t *testing.T) {
	resetAreas()
	w := newSimFake()
	w.withPlayer("heroA-inst", "heroA", 100, 101, 1, 1)
	w.withPlayer("heroB-inst", "heroB", 100, 102, 1, 1)
	a := &PlayerView{Instance: "heroA-inst", Username: "heroA"}
	b := &PlayerView{Instance: "heroB-inst", Username: "heroB"}
	m := creditMob()
	now := time.Now()

	HitMob(m, a, 30, w, now, func() bool { return true }) // hp 70, A=30
	HitMob(m, b, 50, w, now, func() bool { return true }) // hp 20, B=50
	if got := m.DamageRank(); len(got) != 2 || got[0].Instance != "heroB-inst" || got[0].Damage != 50 || got[1].Damage != 30 {
		t.Fatalf("rank = %+v, want B=50 then A=30", got)
	}
	HitMob(m, a, 9999, w, now, func() bool { return true }) // clamped to 20, A=50 total... tie!

	w.mu.Lock()
	defer w.mu.Unlock()
	// A totals 30+20=50, B totals 50: tie breaks by first hit (A opened
	// the table first), so A is credited despite landing the last blow.
	if len(w.lootDrops) != 1 || w.lootDrops[0].owner != "heroA" {
		t.Fatalf("tied table must credit first-hitter heroA: %+v", w.lootDrops)
	}
	if len(w.quests) != 1 || w.quests[0].killer != "heroA-inst" {
		t.Fatalf("quest kill must credit heroA-inst: %+v", w.quests)
	}
}

// A disconnected (stale) top entry is skipped WITHOUT dropping; the next
// resolvable entry in rank order is credited.
func TestStaleTopEntrySkippedToNextPlayer(t *testing.T) {
	resetAreas()
	w := newSimFake()
	w.withPlayer("heroA-inst", "heroA", 100, 101, 1, 1)
	w.withPlayer("heroB-inst", "heroB", 100, 102, 1, 1)
	a := &PlayerView{Instance: "heroA-inst", Username: "heroA"}
	b := &PlayerView{Instance: "heroB-inst", Username: "heroB"}
	m := creditMob()
	now := time.Now()

	HitMob(m, a, 90, w, now, func() bool { return true }) // hp 10, A top
	w.removePlayer("heroA-inst")                          // A disconnects (damage entry persists)
	HitMob(m, b, 10, w, now, func() bool { return true }) // killing blow

	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.lootDrops) != 1 || w.lootDrops[0].owner != "heroB" {
		t.Fatalf("stale top must skip to heroB: %+v", w.lootDrops)
	}
	if len(w.quests) != 1 || w.quests[0].killer != "heroB-inst" {
		t.Fatalf("quest kill must credit heroB-inst: %+v", w.quests)
	}
}

// All-stale table: TS-exact outcome — the death pipeline still runs
// (despawn + respawn timer) but NO loot drops and NO quest credit fires.
func TestAllStaleTableDropsNothing(t *testing.T) {
	resetAreas()
	w := newSimFake()
	w.withPlayer("heroA-inst", "heroA", 100, 101, 1, 1)
	w.withPlayer("heroB-inst", "heroB", 100, 102, 1, 1)
	a := &PlayerView{Instance: "heroA-inst", Username: "heroA"}
	b := &PlayerView{Instance: "heroB-inst", Username: "heroB"}
	m := creditMob()
	now := time.Now()

	HitMob(m, a, 90, w, now, func() bool { return true }) // hp 10
	HitMob(m, b, 5, w, now, func() bool { return true })  // hp 5
	w.removePlayer("heroA-inst")
	w.removePlayer("heroB-inst")
	HitMob(m, nil, 999, w, now, func() bool { return true }) // environmental finishing blow

	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.despawns) != 1 {
		t.Fatalf("despawns = %v", w.despawns)
	}
	if len(w.lootDrops) != 0 {
		t.Fatalf("all-stale table must drop no loot: %+v", w.lootDrops)
	}
	if len(w.quests) != 0 {
		t.Fatalf("all-stale table must fire no quest credit: %+v", w.quests)
	}
	if len(w.delays) != 1 {
		t.Fatalf("respawn timer missing: %+v", w.delays)
	}
}

// Attacker pruning (DropAttacker) touches only the timestamp map — damage
// totals survive it (TS removeAttacker parity), so a pruned top damager is
// still credited.
func TestPrunedAttackerKeepsDamageCredit(t *testing.T) {
	resetAreas()
	w := newSimFake()
	w.withPlayer("heroA-inst", "heroA", 100, 101, 1, 1)
	w.withPlayer("heroB-inst", "heroB", 100, 102, 1, 1)
	a := &PlayerView{Instance: "heroA-inst", Username: "heroA"}
	b := &PlayerView{Instance: "heroB-inst", Username: "heroB"}
	m := creditMob()
	now := time.Now()

	HitMob(m, a, 90, w, now, func() bool { return true }) // hp 10
	m.Lock()
	m.DropAttacker("heroA-inst") // prune simulation (far/stale attacker)
	m.Unlock()
	HitMob(m, b, 10, w, now, func() bool { return true }) // killing blow

	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.lootDrops) != 1 || w.lootDrops[0].owner != "heroA" {
		t.Fatalf("pruned top damager must keep credit: %+v", w.lootDrops)
	}
	if len(w.quests) != 1 || w.quests[0].killer != "heroA-inst" {
		t.Fatalf("quest kill must credit heroA-inst: %+v", w.quests)
	}
}

// The killing-blow dealer still feeds the chest-area hook (TS
// removeEntity(mob, attacker) parity) even when credit goes elsewhere —
// pinned at the engine level via the blow instance reaching KillHookForMob
// (area achievement), not via loot/quest.
func TestKillHookStillSeesKillingBlow(t *testing.T) {
	resetAreas()
	w := newSimFake()
	w.withPlayer("heroA-inst", "heroA", 100, 101, 1, 1)
	w.withPlayer("heroB-inst", "heroB", 100, 102, 1, 1)
	a := &PlayerView{Instance: "heroA-inst", Username: "heroA"}
	b := &PlayerView{Instance: "heroB-inst", Username: "heroB"}
	m := creditMob()
	now := time.Now()

	HitMob(m, a, 90, w, now, func() bool { return true })
	HitMob(m, b, 10, w, now, func() bool { return true })

	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.lootDrops) != 1 || w.lootDrops[0].owner != "heroA" {
		t.Fatalf("loot owner must be heroA: %+v", w.lootDrops)
	}
}

// DamageTable unit shape: accumulate, clamp is the caller's job, rank is
// damage-desc with first-hit tiebreak, Clear empties.
func TestDamageTableRank(t *testing.T) {
	var dt DamageTable
	if dt.Len() != 0 {
		t.Fatal("new table must be empty")
	}
	dt.Add("b", 10, "B")
	dt.Add("a", 10, "A") // tie with b, but b hit first
	dt.Add("b", 5, "B")  // b=15
	dt.Add("c", 30, "C") // c=30 top
	if dt.Len() != 3 {
		t.Fatalf("len = %d", dt.Len())
	}
	rank := dt.Rank()
	if len(rank) != 3 || rank[0].Instance != "c" || rank[0].Damage != 30 || rank[0].Username != "C" {
		t.Fatalf("rank[0] = %+v", rank)
	}
	if rank[1].Instance != "b" || rank[1].Damage != 15 {
		t.Fatalf("rank[1] = %+v", rank)
	}
	if rank[2].Instance != "a" || rank[2].Damage != 10 {
		t.Fatalf("rank[2] = %+v (tie must keep first-hit order b before a)", rank)
	}
	dt.Clear()
	if dt.Len() != 0 || len(dt.Rank()) != 0 {
		t.Fatal("Clear must empty the table")
	}
}
