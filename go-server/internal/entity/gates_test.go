package entity

import (
	"testing"
	"time"
)

// Door linking + stage-shape matrix (map.ts loadDoors parity).
const doorFixture = `{"width":1152,"areas":{"doors":[
	{"id":255,"x":173,"y":98,"width":1,"height":1,"achievement":"ahiddenpath","destination":258},
	{"id":258,"x":200,"y":300,"width":1,"height":1,"destination":255,"orientation":"u"},
	{"id":536,"x":693,"y":836,"width":1,"height":1,"destination":537,"quest":"seaactivities","stage":4},
	{"id":537,"x":858,"y":808,"width":1,"height":1,"destination":536,"quest":"seaactivities","stage":4},
	{"id":1115,"x":201,"y":168,"width":1,"height":1,"destination":1114,"npc":"blacksmith","quest":"anvilsechoes","stage":"2"},
	{"id":1114,"x":202,"y":168,"width":1,"height":1,"destination":1115},
	{"id":1050,"x":654,"y":648,"width":1,"height":1,"destination":1051,"level":50,"skill":"Mining"},
	{"id":1051,"x":644,"y":602,"width":1,"height":1,"destination":1050,"level":50,"skill":"Mining"},
	{"id":223,"x":326,"y":891,"width":1,"height":1},
	{"id":999,"x":1,"y":1,"width":1,"height":1,"destination":12345}
]}}`

func loadDoorFixture(t *testing.T) {
	t.Helper()
	resetAreas()
	LoadAreas([]byte(doorFixture))
}

func TestDoorLinking(t *testing.T) {
	loadDoorFixture(t)
	if got := DoorCount(); got != 8 {
		t.Fatalf("linked doors = %d, want 8 (no-dest + dangling skipped)", got)
	}
	// Entry tile carries the DESTINATION coordinates + its own gates.
	d := DoorAt(173, 98)
	if d == nil {
		t.Fatal("DoorAt(173,98) = nil")
	}
	if d.DestX != 200 || d.DestY != 300 {
		t.Fatalf("dest = %d,%d, want 200,300", d.DestX, d.DestY)
	}
	if d.Achievement != "ahiddenpath" {
		t.Fatalf("achievement = %q", d.Achievement)
	}
	if d.Orientation != "u" {
		t.Fatalf("orientation = %q, want destination's u", d.Orientation)
	}
	// Reverse pair links back.
	r := DoorAt(200, 300)
	if r == nil || r.DestX != 173 || r.DestY != 98 {
		t.Fatalf("reverse link = %+v", r)
	}
	if r.Orientation != "d" {
		t.Fatalf("default orientation = %q, want d", r.Orientation)
	}
	// Plain tiles are not doors; no-dest and dangling entries are skipped.
	if IsDoor(0, 0) || IsDoor(326, 891) || IsDoor(1, 1) {
		t.Fatal("non-door tile reported as door")
	}
	if !IsDoor(173, 98) {
		t.Fatal("door entry not reported")
	}
}

func TestDoorStageShapes(t *testing.T) {
	loadDoorFixture(t)
	if d := DoorAt(693, 836); d == nil || d.Quest != "seaactivities" || d.Stage != 4 {
		t.Fatalf("numeric stage = %+v", d)
	}
	if d := DoorAt(201, 168); d == nil || d.Quest != "anvilsechoes" || d.Stage != 2 || d.NPC != "blacksmith" {
		t.Fatalf("string stage = %+v", d)
	}
	if d := DoorAt(654, 648); d == nil || d.Level != 50 || d.Skill != "Mining" {
		t.Fatalf("level gate = %+v", d)
	}
	if doorStage(nil) != 0 {
		t.Fatal("absent stage must be 0")
	}
}

// Dynamic per-player gate matrix (area.ts fulfillsRequirement +
// getMappedTile parity).
type stubProg struct {
	quests map[string]bool
	achs   map[string]bool
}

func (s stubProg) QuestFinished(key string) bool { return s.quests[key] }
func (s stubProg) AchFinished(key string) bool   { return s.achs[key] }

const dynFixture = `{"width":100,"areas":{"dynamic":[
	{"id":7,"x":1,"y":1,"width":2,"height":2,"mapping":8,"achievement":"crabproblem"},
	{"id":8,"x":5,"y":5,"width":2,"height":2},
	{"id":9,"x":10,"y":10,"width":1,"height":1,"mapping":10,"quest":"ancientlands"},
	{"id":10,"x":20,"y":20,"width":1,"height":1},
	{"id":11,"x":30,"y":30,"width":1,"height":1}
]}}`

func loadDynFixture(t *testing.T) {
	t.Helper()
	resetAreas()
	LoadAreas([]byte(dynFixture))
}

func TestDynamicGateMatrix(t *testing.T) {
	loadDynFixture(t)

	// Quest/achievement fields parse onto the areas.
	var achArea *Area
	for _, a := range DynamicAreas() {
		if a.ID == 7 {
			achArea = a
		}
	}
	if achArea == nil || achArea.Achievement != "crabproblem" {
		t.Fatalf("dynamic achievement = %+v", achArea)
	}

	done := stubProg{achs: map[string]bool{"crabproblem": true}}
	todo := stubProg{}

	if !FulfillsRequirement(achArea, done) {
		t.Fatal("finished achievement must fulfill")
	}
	if FulfillsRequirement(achArea, todo) {
		t.Fatal("unfinished achievement must not fulfill")
	}
	if FulfillsRequirement(nil, done) || FulfillsRequirement(achArea, nil) {
		t.Fatal("nil area/progression must not fulfill")
	}
	// Ungated areas never fulfill (TS returns false).
	plain := &Area{ID: 99}
	if FulfillsRequirement(plain, done) {
		t.Fatal("gateless area must not fulfill")
	}
	// Quest gate branch.
	questArea := &Area{ID: 100, Quest: "ancientlands"}
	if !FulfillsRequirement(questArea, stubProg{quests: map[string]bool{"ancientlands": true}}) {
		t.Fatal("finished quest must fulfill")
	}
	if FulfillsRequirement(questArea, todo) {
		t.Fatal("unfinished quest must not fulfill")
	}
	// Quest takes precedence when both are set (TS if/else order).
	both := &Area{ID: 101, Quest: "q", Achievement: "a"}
	if FulfillsRequirement(both, stubProg{achs: map[string]bool{"a": true}}) {
		t.Fatal("achievement must not satisfy a quest-gated area")
	}
}

func TestDynamicRemapMatrix(t *testing.T) {
	loadDynFixture(t)
	done := stubProg{achs: map[string]bool{"crabproblem": true}}
	todo := stubProg{}

	// (1,1) in area 7 (mapping 8 at 5,5): fulfilled -> mapped tile.
	mx, my, ok := DynamicRemap(1, 1, done)
	if !ok || mx != 5 || my != 5 {
		t.Fatalf("remap(1,1) = %d,%d,%v, want 5,5,true", mx, my, ok)
	}
	// Relative offset preserved: (2,2) -> (6,6).
	mx, my, ok = DynamicRemap(2, 2, done)
	if !ok || mx != 6 || my != 6 {
		t.Fatalf("remap(2,2) = %d,%d,%v, want 6,6,true", mx, my, ok)
	}
	// Unfulfilled requirement -> no remap (static collision applies).
	if _, _, ok = DynamicRemap(1, 1, todo); ok {
		t.Fatal("unfulfilled remap must be false")
	}
	// Counterpart side (area 8, no mapping of its own): no remap.
	if _, _, ok = DynamicRemap(5, 5, done); ok {
		t.Fatal("counterpart tile must not remap")
	}
	// Gateless area 11: no remap even when "fulfilled".
	if _, _, ok = DynamicRemap(30, 30, done); ok {
		t.Fatal("gateless area must not remap")
	}
	// Outside any dynamic area.
	if _, _, ok = DynamicRemap(50, 50, done); ok {
		t.Fatal("plain tile must not remap")
	}
	// Quest-gated pair 9->10.
	qdone := stubProg{quests: map[string]bool{"ancientlands": true}}
	if mx, my, ok = DynamicRemap(10, 10, qdone); !ok || mx != 20 || my != 20 {
		t.Fatalf("quest remap = %d,%d,%v, want 20,20,true", mx, my, ok)
	}
	if _, _, ok = DynamicRemap(10, 10, todo); ok {
		t.Fatal("unfinished quest must not remap")
	}
}

// MappedAnimTile matrix (area.ts getMappedAnimationTile parity): the
// animation tile relative to the area re-based onto the mapped-animation
// counterpart; ok=false without an animation link.
func TestMappedAnimTileMatrix(t *testing.T) {
	resetAreas()
	LoadAreas([]byte(`{"width":100,"areas":{"dynamic":[
		{"id":21,"x":1,"y":1,"width":2,"height":2,"mapping":22,"animation":23,"quest":"q"},
		{"id":22,"x":5,"y":5,"width":2,"height":2},
		{"id":23,"x":9,"y":9,"width":2,"height":2},
		{"id":24,"x":30,"y":30,"width":1,"height":1,"mapping":22,"quest":"q"}
	]}}`))
	var linked, unlinked *Area
	for _, a := range DynamicAreas() {
		switch a.ID {
		case 21:
			linked = a
		case 24:
			unlinked = a
		}
	}
	if linked == nil || linked.MappedAnimation() == nil {
		t.Fatalf("animation link = %+v", linked)
	}
	if ax, ay, ok := MappedAnimTile(linked, 1, 1); !ok || ax != 9 || ay != 9 {
		t.Fatalf("anim(1,1) = %d,%d,%v, want 9,9,true", ax, ay, ok)
	}
	if ax, ay, ok := MappedAnimTile(linked, 2, 2); !ok || ax != 10 || ay != 10 {
		t.Fatalf("anim(2,2) = %d,%d,%v, want 10,10,true", ax, ay, ok)
	}
	if _, _, ok := MappedAnimTile(unlinked, 30, 30); ok {
		t.Fatal("area without animation link must not map")
	}
	if _, _, ok := MappedAnimTile(nil, 1, 1); ok {
		t.Fatal("nil area must not map")
	}
}

// Chest clear awards the area achievement to the attacker (chest.ts onEmpty
// parity); killerless clears and achievement-less areas award nothing.
func TestChestClearAwardsAchievement(t *testing.T) {
	resetAreas()
	LoadAreas([]byte(`{"width":100,"areas":{"chests":[
		{"id":6,"x":10,"y":10,"width":2,"height":2,"items":"bronzesword",
		 "spawnX":10,"spawnY":10,"achievement":"hiddenreward"}
	]}}`))
	w := newSimFake()
	area := ChestAreas()[0]

	AddChestMob(area, "m1", 0, w)
	AddChestMob(area, "m2", 0, w)
	RemoveChestMob(area, "m1", "hero-1", w) // not empty: no award
	w.mu.Lock()
	if len(w.finAchs) != 0 {
		w.mu.Unlock()
		t.Fatalf("partial clear awarded: %+v", w.finAchs)
	}
	w.mu.Unlock()

	RemoveChestMob(area, "m2", "hero-1", w) // cleared by hero-1
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.finAchs) != 1 || w.finAchs[0].instance != "hero-1" || w.finAchs[0].key != "hiddenreward" {
		t.Fatalf("clear awards = %+v", w.finAchs)
	}
}

func TestChestClearKillerlessAwardsNothing(t *testing.T) {
	resetAreas()
	LoadAreas([]byte(`{"width":100,"areas":{"chests":[
		{"id":6,"x":10,"y":10,"width":2,"height":2,"items":"bronzesword",
		 "spawnX":10,"spawnY":10,"achievement":"hiddenreward"}
	]}}`))
	w := newSimFake()
	area := ChestAreas()[0]

	AddChestMob(area, "m1", 0, w)
	KillHookForMob(10, 10, "m1", "", w) // killerless clear
	w.mu.Lock()
	defer w.mu.Unlock()
	if area.LiveChest() == nil {
		t.Fatal("killerless clear spawned no chest")
	}
	if len(w.finAchs) != 0 {
		t.Fatalf("killerless clear awarded: %+v", w.finAchs)
	}
}

// Plateau gate matrix: roam steps changing plateau are refused
// (mob/handler.ts:184); strikes use RangedBlocked (melee ungated, only
// ranged shooting UP refused).
func TestPlateauGateMatrix(t *testing.T) {
	// Roam: mob bound to plateau 1 inside a 5x5 plateau-1 box around its
	// spawn; every draw outside the box must be refused, draws inside land.
	w := newSimFake()
	w.plateau = func(x, y int) int {
		if x >= 98 && x <= 102 && y >= 98 && y <= 102 {
			return 1
		}
		return 0
	}
	m := newTestMob("mob-plat", "rat", ratProfile(), 100, 100)
	m.plateau = 1
	for i := 0; i < 200; i++ {
		m.Lock()
		roamMob(m, w)
		m.Unlock()
	}
	w.mu.Lock()
	if len(w.moves) == 0 {
		w.mu.Unlock()
		t.Fatal("plateau-bound mob never roamed inside its box")
	}
	for _, mv := range w.moves {
		if mv.x < 98 || mv.x > 102 || mv.y < 98 || mv.y > 102 {
			w.mu.Unlock()
			t.Fatalf("roam escaped plateau: %+v", mv)
		}
	}
	w.mu.Unlock()

	// A refused draw leaves the mob in place (single draw refused when the
	// whole neighbourhood differs — mob on plateau 1 surrounded by 0).
	w2 := newSimFake()
	w2.plateau = func(x, y int) int { return 0 }
	m2 := newTestMob("mob-plat-cave", "rat", ratProfile(), 50, 50)
	m2.plateau = 1
	for i := 0; i < 20; i++ {
		m2.Lock()
		roamMob(m2, w2)
		m2.Unlock()
	}
	w2.mu.Lock()
	defer w2.mu.Unlock()
	if len(w2.moves) != 0 {
		t.Fatalf("cave-bound mob roamed out: %+v", w2.moves)
	}
}

func TestRangedBlockedMatrix(t *testing.T) {
	// TS-exact matrix (character.ts:785 isRanged = attackRange > 1;
	// isNearTarget: ranged requires attacker.plateauLevel >=
	// target.plateauLevel, melee has NO plateau check).
	// Default range 1 is melee: never blocked, even across plateaus.
	if RangedBlocked(1, 0, 1) || RangedBlocked(1, 1, 0) || RangedBlocked(1, 0, 0) {
		t.Fatal("melee (range 1) must never be plateau-blocked")
	}
	// Zero/negative ranges are also melee.
	if RangedBlocked(0, 0, 1) || RangedBlocked(-1, 0, 1) {
		t.Fatal("non-positive range must count as melee (never blocked)")
	}
	// Ranged shooting UP is refused.
	if !RangedBlocked(2, 0, 1) || !RangedBlocked(8, 0, 1) {
		t.Fatal("ranged shooting UP a plateau must block")
	}
	// Ranged level or DOWN is allowed.
	if RangedBlocked(8, 1, 1) || RangedBlocked(8, 1, 0) || RangedBlocked(9, 2, 0) {
		t.Fatal("ranged level/down must pass")
	}
}

func TestStrikeMeleeCrossPlateauAllowed(t *testing.T) {
	// Melee mob on plateau 0 strikes a plateau-1 hero: allowed (the
	// e2e leash-demo shape: hero on 1, rat on 0).
	w := newSimFake()
	w.withPlayer("hero-1", "hero", 101, 100, 1, 1)
	w.players[0].Plateau = 1 // hero upstairs, mob on 0
	m := newTestMob("mob-melee", "rat", ratProfile(), 100, 100)
	m.plateau = 0
	m.mu.Lock()
	m.target = "hero-1"
	m.lastAtk = m.lastAtk.Add(-time.Hour) // attack clock ready
	m.mu.Unlock()
	StepMob(m, w, time.Now())
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.strikes) == 0 {
		t.Fatal("melee cross-plateau strike must fire")
	}
	if len(w.heroPts) == 0 {
		t.Fatal("melee cross-plateau strike must damage hero")
	}
}

func TestStrikeRangedUpRefused(t *testing.T) {
	// Ranged mob (archer range 8) on plateau 0 vs plateau-1 hero:
	// refused silently (new correct behavior).
	w := newSimFake()
	w.withPlayer("hero-1", "hero", 101, 100, 1, 1)
	w.players[0].Plateau = 1
	prof := ratProfile()
	prof.AttackRange = 8
	m := newTestMob("mob-ranged-up", "rat", prof, 100, 100)
	m.plateau = 0
	m.mu.Lock()
	m.target = "hero-1"
	m.lastAtk = m.lastAtk.Add(-time.Hour) // attack clock ready
	m.mu.Unlock()
	StepMob(m, w, time.Now())
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.strikes) != 0 {
		t.Fatalf("ranged-up strike fired: %+v", w.strikes)
	}
	if len(w.heroPts) != 0 {
		t.Fatalf("ranged-up strike damaged hero: %+v", w.heroPts)
	}
}

func TestStrikeRangedLevelDownAllowed(t *testing.T) {
	// Ranged mob shooting level or DOWN: allowed.
	for _, tc := range []struct {
		name       string
		mobPlateau int
		heroPlat   int
	}{
		{"level", 1, 1},
		{"down", 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newSimFake()
			w.withPlayer("hero-1", "hero", 101, 100, 1, 1)
			w.players[0].Plateau = tc.heroPlat
			prof := ratProfile()
			prof.AttackRange = 8
			m := newTestMob("mob-ranged", "rat", prof, 100, 100)
			m.plateau = tc.mobPlateau
			m.mu.Lock()
			m.target = "hero-1"
			m.lastAtk = m.lastAtk.Add(-time.Hour)
			m.mu.Unlock()
			StepMob(m, w, time.Now())
			w.mu.Lock()
			defer w.mu.Unlock()
			if len(w.strikes) == 0 {
				t.Fatalf("ranged %s strike must fire", tc.name)
			}
		})
	}
}
