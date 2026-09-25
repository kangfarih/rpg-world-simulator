package entity

import (
	"testing"
	"time"
)

const mimicFixture = `{"width":1000,"areas":{
	"chests":[{"id":248,"x":110,"y":108,"width":5,"height":3,"items":"bronzesword","spawnX":110,"spawnY":106}],
	"chest":[
		{"id":304,"x":271,"y":731,"width":1,"height":1,"mimic":true},
		{"id":273,"x":188,"y":746,"width":1,"height":1,"achievement":"sandtreasure","items":"boweggplant"}
	]}}`

func loadMimicFixture(t *testing.T) {
	t.Helper()
	resetAreas()
	LoadAreas([]byte(mimicFixture))
}

func TestMimicFlagParsing(t *testing.T) {
	loadMimicFixture(t)

	defs := StaticChests()
	if len(defs) != 2 {
		t.Fatalf("static chests = %d, want 2", len(defs))
	}
	var mimic, plain *Area
	for _, d := range defs {
		switch d.ID {
		case 304:
			mimic = d
		case 273:
			plain = d
		}
	}
	if mimic == nil || plain == nil {
		t.Fatalf("static defs = %+v, want ids 304+273", defs)
	}
	if !mimic.Mimic {
		t.Fatal("id 304 mimic flag not parsed")
	}
	if plain.Mimic {
		t.Fatal("id 273 parsed mimic=true, want false")
	}
	if plain.Achievement != "sandtreasure" {
		t.Fatalf("id 273 achievement = %q", plain.Achievement)
	}
	// Chest AREAS share the struct: zero value, and the area flow never
	// forwards it (spawnChestEntity leaves Mimic false).
	for _, a := range ChestAreas() {
		if a.Mimic {
			t.Fatalf("area %d parsed mimic=true, want false", a.ID)
		}
	}
}

func TestSpawnStaticChests(t *testing.T) {
	loadMimicFixture(t)
	w := newSimFake()

	SpawnStaticChests(w)

	w.mu.Lock()
	if len(w.chests) != 2 {
		w.mu.Unlock()
		t.Fatalf("static chest spawns = %d, want 2", len(w.chests))
	}
	w.mu.Unlock()

	m := ChestFor("chest-static-304")
	if m == nil || m.X != 271 || m.Y != 731 || !m.Mimic {
		t.Fatalf("chest-static-304 = %+v", m)
	}
	if len(m.Items) != 0 {
		t.Fatalf("chest-static-304 items = %v, want none (entry has no items)", m.Items)
	}
	s := ChestFor("chest-static-273")
	if s == nil || s.Achievement != "sandtreasure" || s.Mimic {
		t.Fatalf("chest-static-273 = %+v", s)
	}
	if !ChestAt(271, 731) || ChestAt(0, 0) {
		t.Fatal("ChestAt mismatch for static chests")
	}

	// Idempotent: a second pass spawns nothing (no doubles).
	SpawnStaticChests(w)
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.chests) != 2 {
		t.Fatalf("re-spawn doubled static chests: %d", len(w.chests))
	}
}

func TestMimicOpenSpawnsMimicAndStillLoots(t *testing.T) {
	loadMimicFixture(t)
	w := newSimFake()
	SpawnStaticChests(w)

	// Deterministic loot for the 273 chest (single plain entry).
	ch := ChestFor("chest-static-273")
	if ch == nil {
		t.Fatal("no live chest-static-273")
	}
	OpenChest(ch, "hero-1", "hero", w)

	w.mu.Lock()
	defer w.mu.Unlock()
	// Chest removed...
	if ChestFor("chest-static-273") != nil {
		t.Fatal("chest still live after open")
	}
	foundDespawn := false
	for _, d := range w.despawns {
		if d == "chest-static-273" {
			foundDespawn = true
		}
	}
	if !foundDespawn {
		t.Fatal("open did not despawn the chest")
	}
	// ...no mimic (plain chest)...
	for _, s := range w.spawns {
		if s.Key == "mimic" {
			t.Fatalf("plain chest spawned a mimic: %+v", s)
		}
	}
	// ...loot still rolled...
	if len(w.regLoot) != 1 || w.regLoot[0].key != "boweggplant" || w.regLoot[0].owner != "hero" {
		t.Fatalf("chest loot = %+v", w.regLoot)
	}
	// ...and the static open achievement finished for the opener instance.
	if len(w.finAchs) != 1 || w.finAchs[0].instance != "hero-1" || w.finAchs[0].key != "sandtreasure" {
		t.Fatalf("finish achievements = %+v", w.finAchs)
	}
}

func TestMimicChestOpenSpawnsMimic(t *testing.T) {
	loadMimicFixture(t)
	w := newSimFake()
	SpawnStaticChests(w)

	ch := ChestFor("chest-static-304") // mimic, no items
	if ch == nil {
		t.Fatal("no live chest-static-304")
	}
	OpenChest(ch, "hero-1", "hero", w)

	w.mu.Lock()
	if ChestFor("chest-static-304") != nil {
		w.mu.Unlock()
		t.Fatal("mimic chest still live after open")
	}
	var mSpawn *MobSpawn
	for i, s := range w.spawns {
		if s.Key == "mimic" {
			mSpawn = &w.spawns[i]
		}
	}
	w.mu.Unlock()
	if mSpawn == nil {
		t.Fatal("mimic chest open spawned no mimic")
	}
	if mSpawn.X != 271 || mSpawn.Y != 731 {
		t.Fatalf("mimic spawned at %d,%d, want the chest tile 271,731", mSpawn.X, mSpawn.Y)
	}
	// No items on the entry: no loot, no achievement — but the mimic link
	// (mob.chest parity) must be in place for the death hook.
	if _, ok := TakeMimicChest(mSpawn.Instance); !ok {
		t.Fatal("mimic chest link missing after open")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.regLoot) != 0 || len(w.finAchs) != 0 {
		t.Fatalf("itemless mimic open emitted loot/achievements: %+v %+v", w.regLoot, w.finAchs)
	}
}

func TestMimicOpenWithoutOpenerSkipsMimic(t *testing.T) {
	loadMimicFixture(t)
	w := newSimFake()
	SpawnStaticChests(w)

	ch := ChestFor("chest-static-304")
	if ch == nil {
		t.Fatal("no live chest-static-304")
	}
	OpenChest(ch, "", "", w) // TS onOpen(player=undefined) parity

	w.mu.Lock()
	defer w.mu.Unlock()
	for _, s := range w.spawns {
		if s.Key == "mimic" {
			t.Fatal("openerless open spawned a mimic")
		}
	}
	if ChestFor("chest-static-304") != nil {
		t.Fatal("chest still live after openerless open")
	}
}

func TestMimicSpawnFailureStillLoots(t *testing.T) {
	loadMimicFixture(t)
	w := newSimFake()
	w.spawnMimicOK = false // TS spawnMob miss parity (`if (mimic)`)
	SpawnStaticChests(w)

	ch := ChestFor("chest-static-273")
	if ch == nil {
		t.Fatal("no live chest-static-273")
	}
	// Flag it mimic: spawn fails, the item flow must still run.
	ch.Mimic = true
	OpenChest(ch, "hero-1", "hero", w)

	w.mu.Lock()
	defer w.mu.Unlock()
	for _, s := range w.spawns {
		if s.Key == "mimic" {
			t.Fatal("failed mimic spawn recorded")
		}
	}
	if len(w.regLoot) != 1 || w.regLoot[0].key != "boweggplant" {
		t.Fatalf("loot skipped after mimic failure: %+v", w.regLoot)
	}
	if len(w.finAchs) != 1 {
		t.Fatalf("achievement skipped after mimic failure: %+v", w.finAchs)
	}
}

func TestMimicDeathRespawnsChest(t *testing.T) {
	loadMimicFixture(t)
	w := newSimFake()
	SpawnStaticChests(w)

	ch := ChestFor("chest-static-304")
	if ch == nil {
		t.Fatal("no live chest-static-304")
	}
	// Open (links mimic-1 -> chest) then kill the mimic with no attacker
	// (killerless KillMob still runs the mimic hook, TS parity).
	OpenChest(ch, "hero-1", "hero", w)
	m := newTestMob("mimic-test-1", "mimic", ratProfile(), 271, 731)
	m.over.NoRespawn = true // TS mimic.respawnable = false
	KillMob(m, nil, w, func() bool { return true })

	w.mu.Lock()
	if len(w.removed) != 1 || w.removed[0] != "mimic-test-1" {
		w.mu.Unlock()
		t.Fatalf("dead mimic not dropped from registry: %+v", w.removed)
	}
	if len(w.delays) != 1 || w.delays[0].d != ChestRespawnStatic {
		w.mu.Unlock()
		t.Fatalf("mimic chest timer = %+v", w.delays)
	}
	if ChestFor("chest-static-304") != nil {
		w.mu.Unlock()
		t.Fatal("chest back before its respawn timer")
	}
	w.mu.Unlock()

	// Fire the 50s timer: THIS chest (same instance/items/mimic) returns.
	w.fireDelays()
	back := ChestFor("chest-static-304")
	if back == nil {
		t.Fatal("mimic death did not re-spawn the chest")
	}
	if back != ch || !back.Mimic || back.Area == nil {
		t.Fatalf("re-spawned chest mismatch: %+v", back)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	found := false
	for _, c := range w.chests {
		if c.Instance == "chest-static-304" && c.X == 271 && c.Y == 731 {
			found = true
		}
	}
	if !found {
		t.Fatalf("no re-spawn frame for the mimic chest: %+v", w.chests)
	}
}

func TestPlainMobDeathSchedulesNoChest(t *testing.T) {
	loadMimicFixture(t)
	w := newSimFake()

	m := newTestMob("rat-1", "rat", ratProfile(), 0, 0)
	m.over.Respawn = 30 * time.Millisecond
	KillMob(m, nil, w, func() bool { return true })

	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.removed) != 0 {
		t.Fatalf("plain mob removed from registry: %+v", w.removed)
	}
	if len(w.delays) != 1 { // only its own respawn timer
		t.Fatalf("delays = %+v, want just the mob respawn", w.delays)
	}
	if len(w.chests) != 0 {
		t.Fatalf("plain death spawned chests: %+v", w.chests)
	}
}

func TestMimicDeathInsideAreaRunsBothHooks(t *testing.T) {
	loadMimicFixture(t)
	w := newSimFake()
	area := ChestAreas()[0]

	// A mimic linked to a static chest dies INSIDE chest area 248: the
	// area onEmpty hook (reward chest) and the mimic hook (its own chest
	// back after 50s) must both run, TS handleDeath order.
	AddChestMob(area, "mimic-test-9", 0, w)
	ch := ChestFor("chest-static-304")
	if ch == nil {
		SpawnStaticChests(w)
		ch = ChestFor("chest-static-304")
	}
	LinkMimicChest("mimic-test-9", ch)
	m := newTestMob("mimic-test-9", "mimic", ratProfile(), 112, 109)
	m.over.NoRespawn = true
	KillMob(m, nil, w, func() bool { return true })

	if area.LiveChest() == nil {
		t.Fatal("area onEmpty hook skipped for mimic death inside the area")
	}
	w.mu.Lock()
	nDelays := len(w.delays)
	w.mu.Unlock()
	if nDelays != 1 || w.delays[0].d != ChestRespawnStatic {
		t.Fatalf("mimic chest timer missing: %+v", w.delays)
	}
}

func TestNonMimicOpenUnchanged(t *testing.T) {
	loadMimicFixture(t)
	w := newSimFake()
	area := ChestAreas()[0]

	AddChestMob(area, "m1", 0, w)
	RemoveChestMob(area, "m1", "hero-1", w) // clear: achievement fires HERE
	ch := area.LiveChest()
	if ch == nil {
		t.Fatal("no chest after clear")
	}
	w.mu.Lock()
	if len(w.finAchs) != 0 {
		// Fixture area 248 carries no achievement; clear awards nothing.
		w.mu.Unlock()
		t.Fatalf("clear finished achievements: %+v", w.finAchs)
	}
	w.mu.Unlock()

	OpenChest(ch, "hero-1", "hero", w)

	w.mu.Lock()
	defer w.mu.Unlock()
	for _, s := range w.spawns {
		if s.Key == "mimic" {
			t.Fatal("area chest open spawned a mimic")
		}
	}
	if len(w.finAchs) != 0 {
		t.Fatalf("area chest open finished achievements: %+v", w.finAchs)
	}
	if len(w.regLoot) != 1 || w.regLoot[0].key != "bronzesword" {
		t.Fatalf("area chest loot = %+v", w.regLoot)
	}
}

func TestStaticOpenNoItemSkipsAchievement(t *testing.T) {
	loadMimicFixture(t)
	w := newSimFake()

	// Blank entry: getItem returns nil deterministically (TS `if (!item)
	// return` — the open achievement is skipped with it).
	ch := &Chest{Instance: "chest-x", X: 1, Y: 1, Items: []string{""}, Achievement: "sandtreasure", Area: &Area{}}
	OpenChest(ch, "hero-1", "hero", w)

	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.regLoot) != 0 || len(w.lootItems) != 0 {
		t.Fatalf("blank roll emitted loot: %+v", w.regLoot)
	}
	if len(w.finAchs) != 0 {
		t.Fatalf("no-item open finished achievements: %+v", w.finAchs)
	}
}
