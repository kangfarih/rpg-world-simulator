package entity

import (
	"testing"
	"time"
)

const areaFixture = `{"areas":{
	"camera":[{"id":1,"x":98,"y":105,"width":10,"height":4,"type":"lockX"}],
	"music":[{"id":2,"x":98,"y":100,"width":10,"height":4,"song":"beach"}],
	"pvp":[{"id":3,"x":98,"y":105,"width":11,"height":4}],
	"overlay":[
		{"id":4,"x":110,"y":100,"width":11,"height":4,"type":"dark","darkness":0.75},
		{"id":5,"x":50,"y":50,"width":4,"height":4,"type":"freezing"}
	],
	"chests":[{"id":6,"x":110,"y":105,"width":11,"height":3,"items":"bronzesword","spawnX":114,"spawnY":106}],
	"dynamic":[
		{"id":7,"x":1,"y":1,"width":2,"height":2,"mapping":8},
		{"id":8,"x":5,"y":5,"width":2,"height":2,"animation":7}
	]}}`

func loadFixture(t *testing.T) {
	t.Helper()
	resetAreas()
	LoadAreas([]byte(areaFixture))
}

func countCalls(w *simFake) [7]int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return [7]int{len(w.notifies), len(w.pvps), len(w.overlays), len(w.cameras), len(w.musics), len(w.effects), len(w.freezes)}
}

func TestAreaEnterExit(t *testing.T) {
	loadFixture(t)
	w := newSimFake()

	// Enter pvp + camera in one step (100,106 is in both bands).
	OnPositionUpdate("hero-1", "hero", 100, 106, w)
	w.mu.Lock()
	if len(w.notifies) != 1 || w.notifies[0].msg != "misc:IN_PVP_ZONE" {
		w.mu.Unlock()
		t.Fatalf("pvp enter notifies = %+v", w.notifies)
	}
	if len(w.pvps) != 1 || !w.pvps[0].state {
		w.mu.Unlock()
		t.Fatalf("pvp enter sends = %+v", w.pvps)
	}
	if len(w.cameras) != 1 || w.cameras[0].opcode != CameraLockX {
		w.mu.Unlock()
		t.Fatalf("camera enter sends = %+v", w.cameras)
	}
	if len(w.musics) != 0 {
		w.mu.Unlock()
		t.Fatalf("music fired outside its band = %+v", w.musics)
	}
	w.mu.Unlock()

	// Step into the music band (100,101): song starts, pvp/camera exit.
	OnPositionUpdate("hero-1", "hero", 100, 101, w)
	w.mu.Lock()
	if len(w.musics) != 1 || w.musics[0].song != "beach" {
		w.mu.Unlock()
		t.Fatalf("music enter sends = %+v", w.musics)
	}
	if len(w.notifies) != 2 || w.notifies[1].msg != "misc:NOT_IN_PVP_ZONE" {
		w.mu.Unlock()
		t.Fatalf("pvp exit notifies = %+v", w.notifies)
	}
	if len(w.pvps) != 2 || w.pvps[1].state {
		w.mu.Unlock()
		t.Fatalf("pvp exit sends = %+v", w.pvps)
	}
	if len(w.cameras) != 2 || w.cameras[1].opcode != CameraFreeFlow {
		w.mu.Unlock()
		t.Fatalf("camera exit sends = %+v", w.cameras)
	}
	w.mu.Unlock()

	// Same tile again: change-detected, no duplicate frames.
	n0 := countCalls(w)
	OnPositionUpdate("hero-1", "hero", 100, 101, w)
	if n1 := countCalls(w); n1 != n0 {
		t.Fatalf("repeat tile emitted frames: %v -> %v", n0, n1)
	}

	// Leave everything: music stops.
	OnPositionUpdate("hero-1", "hero", 0, 0, w)
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.musics) != 2 || w.musics[1].song != "" {
		t.Fatalf("music exit sends = %+v", w.musics)
	}
}

func TestOverlayEnterExit(t *testing.T) {
	loadFixture(t)
	w := newSimFake()

	OnPositionUpdate("hero-1", "hero", 112, 101, w) // dark overlay band
	w.mu.Lock()
	if len(w.overlays) != 1 || w.overlays[0].remove {
		w.mu.Unlock()
		t.Fatalf("overlay enter = %+v", w.overlays)
	}
	ov := w.overlays[0]
	w.mu.Unlock()
	if ov.image != "blank" || ov.colour != "rgba(0, 0, 0, 0.75)" {
		t.Fatalf("overlay set = %+v", ov)
	}

	OnPositionUpdate("hero-1", "hero", 0, 0, w)
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.overlays) != 2 || !w.overlays[1].remove {
		t.Fatalf("overlay exit = %+v", w.overlays)
	}
}

func TestFreezingArea(t *testing.T) {
	loadFixture(t)
	w := newSimFake()

	OnPositionUpdate("hero-1", "hero", 51, 51, w) // freezing overlay
	w.mu.Lock()
	if len(w.freezes) != 1 || !w.freezes[0].on {
		w.mu.Unlock()
		t.Fatalf("freeze enter = %+v", w.freezes)
	}
	if len(w.effects) != 1 || !w.effects[0].add || w.effects[0].effect != EffectFreezing {
		w.mu.Unlock()
		t.Fatalf("freeze enter effects = %+v", w.effects)
	}
	w.mu.Unlock()

	OnPositionUpdate("hero-1", "hero", 0, 0, w)
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.freezes) != 2 || w.freezes[1].on {
		t.Fatalf("freeze exit = %+v", w.freezes)
	}
	if len(w.effects) != 2 || w.effects[1].add {
		t.Fatalf("freeze exit effects = %+v", w.effects)
	}
}

func TestForgetPlayerResetsDetection(t *testing.T) {
	loadFixture(t)
	w := newSimFake()
	OnPositionUpdate("hero-1", "hero", 100, 106, w)
	ForgetPlayer("hero-1", w)
	w.mu.Lock()
	nPvps := len(w.pvps)
	// FreezeClear rides disconnect (abFreezeClear parity).
	foundClear := false
	for _, fr := range w.freezes {
		if !fr.on {
			foundClear = true
		}
	}
	w.mu.Unlock()
	if !foundClear {
		t.Fatal("ForgetPlayer skipped FreezeClear")
	}
	// State dropped: re-entering the same tile notifies again.
	OnPositionUpdate("hero-1", "hero", 100, 106, w)
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pvps) != nPvps+1 {
		t.Fatalf("re-enter after forget sent no PVP (pvps=%v)", w.pvps)
	}
}

func TestPVPState(t *testing.T) {
	loadFixture(t)
	w := newSimFake()
	if PVPState("hero-1") {
		t.Fatal("initial PVP state true")
	}
	UpdatePVP("hero-1", "hero", true, w)
	if !PVPState("hero-1") {
		t.Fatal("PVP state not set")
	}
	UpdatePVP("hero-1", "hero", true, w) // no flip: no new frame
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pvps) != 1 {
		t.Fatalf("pvp flip-flop sent %d frames", len(w.pvps))
	}
}

func TestChestClearSpawnOpen(t *testing.T) {
	loadFixture(t)
	w := newSimFake()
	areas := ChestAreas()
	if len(areas) != 1 {
		t.Fatalf("chest areas = %d", len(areas))
	}
	area := areas[0]

	AddChestMob(area, "m1", 4000*time.Millisecond, w)
	AddChestMob(area, "m2", 4000*time.Millisecond, w)
	// Duplicate adoption is a no-op.
	AddChestMob(area, "m1", 4000*time.Millisecond, w)

	// First kill: area not empty, no chest.
	RemoveChestMob(area, "m1", "", w)
	w.mu.Lock()
	if len(w.chests) != 0 {
		w.mu.Unlock()
		t.Fatalf("chest spawned before clear: %+v", w.chests)
	}
	w.mu.Unlock()

	// Last kill: reward chest spawns at the area spawn tile.
	RemoveChestMob(area, "m2", "", w)
	ch := area.LiveChest()
	w.mu.Lock()
	if len(w.chests) != 1 {
		w.mu.Unlock()
		t.Fatal("no chest after area clear")
	}
	sp := w.chests[0]
	w.mu.Unlock()
	if ch == nil || sp.X != 114 || sp.Y != 106 || !hasPrefix(sp.Instance, "chest-6-") {
		t.Fatalf("chest spawn = %+v live=%+v", sp, ch)
	}
	if ChestFor(sp.Instance) == nil || !ChestAt(114, 106) || ChestAt(0, 0) {
		t.Fatal("chest lookup mismatch")
	}

	// Opening despawns the chest and drops the rolled item persistently.
	OpenChest(ch, "hero", w)
	w.mu.Lock()
	defer w.mu.Unlock()
	if area.LiveChest() != nil {
		t.Fatal("chest still live after open")
	}
	found := false
	for _, d := range w.despawns {
		if d == sp.Instance {
			found = true
		}
	}
	if !found {
		t.Fatalf("open did not despawn chest: %v", w.despawns)
	}
	if len(w.regLoot) != 1 || w.regLoot[0].key != "bronzesword" || w.regLoot[0].count != 1 || w.regLoot[0].owner != "hero" {
		t.Fatalf("chest loot = %+v", w.regLoot)
	}
	if len(w.lootItems) != 1 || w.lootItems[0].Key != "bronzesword" {
		t.Fatalf("chest loot items = %+v", w.lootItems)
	}
}

func TestChestRepopulateRemovesAndGuards(t *testing.T) {
	loadFixture(t)
	w := newSimFake()
	area := ChestAreas()[0]

	AddChestMob(area, "m1", 4000*time.Millisecond, w)
	RemoveChestMob(area, "m1", "", w)
	ch := area.LiveChest()
	if ch == nil {
		t.Fatal("no chest after clear")
	}

	// A new mob spawn inside the area removes the unlooted chest.
	AddChestMob(area, "m2", 4000*time.Millisecond, w)
	w.mu.Lock()
	if area.LiveChest() != nil {
		w.mu.Unlock()
		t.Fatal("unlooted chest survived repopulation")
	}
	removed := false
	for _, d := range w.despawns {
		if d == ch.Instance {
			removed = true
		}
	}
	nChests := len(w.chests)
	w.mu.Unlock()
	if !removed {
		t.Fatal("repopulate did not despawn the chest")
	}

	// Clearing again at once is guarded by the adopted respawn delay.
	RemoveChestMob(area, "m2", "", w)
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.chests) != nChests {
		t.Fatalf("guard failed: second chest spawned within the delay")
	}
}

func TestKillHookSpawnsChest(t *testing.T) {
	loadFixture(t)
	w := newSimFake()
	area := ChestAreas()[0]
	AddChestMob(area, "mob-1", 0, w) // zero delay: immediate spawn on clear

	KillHookForMob(112, 106, "mob-1", "", w) // inside the chest area
	if area.LiveChest() == nil {
		t.Fatal("kill hook spawned no chest")
	}
	w.mu.Lock()
	n := len(w.chests)
	w.mu.Unlock()

	KillHookForMob(0, 0, "mob-1", "", w) // outside any area: no-op
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.chests) != n {
		t.Fatal("kill hook outside areas spawned a chest")
	}
}

func TestRollChestItem(t *testing.T) {
	if got := RollChestItem(nil); got != nil {
		t.Fatalf("empty roll = %+v", got)
	}
	if got := RollChestItem([]string{""}); got != nil {
		t.Fatalf("blank roll = %+v", got)
	}
	for i := 0; i < 20; i++ {
		got := RollChestItem([]string{"bronzesword"})
		if got == nil || got.Key != "bronzesword" || got.Count != 1 {
			t.Fatalf("plain roll = %+v", got)
		}
		got = RollChestItem([]string{"gold:25:100"})
		if got == nil || got.Key != "gold" || got.Count != 25 {
			t.Fatalf("count roll = %+v", got)
		}
	}
}

func TestDynamicLinking(t *testing.T) {
	loadFixture(t)
	areasMu.Lock()
	defer areasMu.Unlock()
	a7, a8 := dynamicByID[7], dynamicByID[8]
	if a7 == nil || a8 == nil {
		t.Fatal("dynamic areas not registered")
	}
	if a7.MappedArea() != a8 {
		t.Fatal("mapping link missing")
	}
	if a8.MappedAnimation() != a7 {
		t.Fatal("animation link missing")
	}
}

func TestAreaGeometry(t *testing.T) {
	a := &Area{ID: 1, X: 10, Y: 10, Width: 5, Height: 5}
	if !a.Inside(10, 10) || !a.Inside(14, 14) || a.Inside(15, 10) || a.Inside(9, 10) {
		t.Fatal("rect contains wrong")
	}
	ignored := &Area{ID: 2, X: 10, Y: 10, Width: 5, Height: 5, Ignore: true}
	if ignored.Inside(12, 12) {
		t.Fatal("ignored area contains")
	}
	poly := &Area{ID: 3}
	poly.Polygon = []struct {
		X int `json:"x"`
		Y int `json:"y"`
	}{{0, 0}, {10, 0}, {10, 10}, {0, 10}}
	if !poly.Inside(5, 5) || poly.Inside(20, 20) {
		t.Fatal("polygon contains wrong")
	}
	if c := poly.OverlayColour(); c != "rgba(0, 0, 0, 0)" {
		t.Fatalf("default colour = %q", c)
	}
	rgb := &Area{RGB: "255,0,0", Darkness: 0.5}
	if c := rgb.OverlayColour(); c != "rgba(255, 0, 0, 0.5)" {
		t.Fatalf("rgb colour = %q", c)
	}
	if img := rgb.OverlayImage(); img != "blank" {
		t.Fatalf("image = %q", img)
	}
}

func TestInjectTestAreasIdempotent(t *testing.T) {
	resetAreas()
	InjectTestAreas()
	InjectTestAreas()
	if got := len(ChestAreas()); got != 1 {
		t.Fatalf("chest bands = %d", got)
	}
	if FirstChestArea() == nil {
		t.Fatal("no first chest area")
	}
	if !ChestAreaAt(112, 106).Inside(112, 106) {
		t.Fatal("TESTMAP chest band missing")
	}
}

func TestLoadAreasEmpty(t *testing.T) {
	resetAreas()
	LoadAreas(nil)
	if !AreasLoaded() {
		t.Fatal("empty load did not mark loaded")
	}
	LoadAreas([]byte(areaFixture)) // second call: no-op (single boot load)
	if got := len(ChestAreas()); got != 0 {
		t.Fatalf("reload filled %d chest areas", got)
	}
}
