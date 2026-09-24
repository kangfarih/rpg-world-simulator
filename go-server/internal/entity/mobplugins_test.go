package entity

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// pluginFake: GameWorld (via *simFake) + PluginHost double.
// ---------------------------------------------------------------------------

type spawnedMinion struct {
	boss, inst, key string
	x, y            int
	opts            MinionOpts
}

type pluginFake struct {
	*simFake
	seq       int
	minions   []spawnedMinion
	kills     []string
	died      [][2]string
	targets   [][2]string
	teleports []moveCall
	clears    []string
	talks     []notifyCall
	heals     []ptsCall // instance + hp(amount recorded in max)
	follows   []moveCall
}

func newPluginFake() *pluginFake {
	return &pluginFake{simFake: newSimFake()}
}

func (f *pluginFake) SpawnMinion(boss, key string, x, y int, opts MinionOpts) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	name := boss + "-minion-" + itoa(f.seq)
	f.minions = append(f.minions, spawnedMinion{boss, name, key, x, y, opts})
	f.entityAt[name] = [2]int{x, y}
	return name
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	b := []byte{}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func (f *pluginFake) KillMinion(instance string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kills = append(f.kills, instance)
	f.despawns = append(f.despawns, instance)
	delete(f.entityAt, instance)
}

func (f *pluginFake) MinionDied(boss, min string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.died = append(f.died, [2]string{boss, min})
}

func (f *pluginFake) SetMobTarget(mob, player string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.targets = append(f.targets, [2]string{mob, player})
}

func (f *pluginFake) TeleportMob(mob string, x, y int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.teleports = append(f.teleports, moveCall{mob, x, y})
	f.entityAt[mob] = [2]int{x, y}
}

func (f *pluginFake) ClearMobCombat(mob string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clears = append(f.clears, mob)
}

func (f *pluginFake) MobPos(instance string) (int, int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.entityAt[instance]; ok {
		return p[0], p[1], true
	}
	for _, v := range f.players {
		if v.Instance == instance {
			return v.X, v.Y, true
		}
	}
	return 0, 0, false
}

func (f *pluginFake) MobTalk(mob, msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.talks = append(f.talks, notifyCall{mob, msg})
}

func (f *pluginFake) HealMob(instance string, amount int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heals = append(f.heals, ptsCall{instance, amount, 0})
}

func (f *pluginFake) FollowStep(mob string, tx, ty int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur := f.entityAt[mob]
	nx, ny := cur[0], cur[1]
	if nx < tx {
		nx++
	} else if nx > tx {
		nx--
	}
	if ny < ty {
		ny++
	} else if ny > ty {
		ny--
	}
	f.entityAt[mob] = [2]int{nx, ny}
	f.follows = append(f.follows, moveCall{mob, nx, ny})
}

// --- helpers ---

func bossProfile(hp int) MobProfile {
	p := ratProfile()
	p.HitPoints = hp
	p.AggroRange = 6
	p.RoamDistance = 7
	return p
}

func hitBoss(t *testing.T, w *pluginFake, m *testMob, atk *PlayerView, dmg, n int) {
	t.Helper()
	now := time.Now()
	for i := 0; i < n; i++ {
		HitMob(m, atk, dmg, w, now.Add(time.Duration(i)*time.Millisecond), func() bool { return true })
		m.mu.Lock()
		dead := m.dead
		m.mu.Unlock()
		if dead {
			break
		}
	}
}

func resetPlugins() {
	resetAreas()
	resetPluginStates()
}

func TestPluginRegistryTable(t *testing.T) {
	for _, k := range []string{"ogrelord", "skeletonking", "queenant", "ant", "forestdragon", "santa", "piratecaptain", "hellhound"} {
		if !HasMobPlugin(k) {
			t.Fatalf("HasMobPlugin(%q) = false", k)
		}
	}
	// Spider's TS subclass is empty: default path, provably unregistered.
	if HasMobPlugin("spider") || HasMobPlugin("rat") || HasMobPlugin("") || HasMobPlugin("nope") {
		t.Fatal("default keys must have no plugin")
	}
	for _, k := range []string{"ogrelord", "queenant", "ant", "forestdragon", "hellhound"} {
		if !HasMobPluginTick(k) {
			t.Fatalf("HasMobPluginTick(%q) = false", k)
		}
	}
	for _, k := range []string{"santa", "piratecaptain", "skeletonking", "spider", "rat"} {
		if HasMobPluginTick(k) {
			t.Fatalf("HasMobPluginTick(%q) = true, want false", k)
		}
	}
}

func TestPluginRandIntInclusive(t *testing.T) {
	SetPluginRandSeed(7)
	if got := PluginRandInt(5, 5); got != 5 {
		t.Fatalf("degenerate range = %d", got)
	}
	seen := map[int]bool{}
	for i := 0; i < 500; i++ {
		v := PluginRandInt(1, 4)
		if v < 1 || v > 4 {
			t.Fatalf("out of range: %d", v)
		}
		seen[v] = true
	}
	for v := 1; v <= 4; v++ {
		if !seen[v] {
			t.Fatalf("value %d never drawn in 500 samples", v)
		}
	}
}

func TestSkeletonKingHitDeath(t *testing.T) {
	resetPlugins()
	SetPluginRandSeed(1)
	w := newPluginFake()
	w.withPlayer("hero-1", "hero", 100, 100, 5, 1)
	m := newTestMob("sk-1", "skeletonking", bossProfile(10000), 100, 100)
	atk := &PlayerView{Instance: "hero-1", Username: "hero"}

	// 1/4 spawn chance must fire within 200 hits.
	hitBoss(t, w, m, atk, 1, 200)
	w.mu.Lock()
	n := len(w.minions)
	w.mu.Unlock()
	if n == 0 {
		t.Fatal("skeletonking spawned no minion in 200 hits")
	}
	// TS-exact spawn shape.
	w.mu.Lock()
	mn := w.minions[0]
	w.mu.Unlock()
	if mn.key != "skeleton" {
		t.Fatalf("minion key = %q", mn.key)
	}
	okPos := (mn.x == 139 && mn.y == 782) || (mn.x == 125 && mn.y == 785)
	if !okPos {
		t.Fatalf("minion pos = %d,%d", mn.x, mn.y)
	}
	if mn.opts.RoamDistance != 24 || !mn.opts.AlwaysAggressive || !mn.opts.NoRespawn {
		t.Fatalf("minion opts = %+v", mn.opts)
	}
	// Minion attacks a random attacker.
	w.mu.Lock()
	nt := len(w.targets)
	w.mu.Unlock()
	if nt == 0 {
		t.Fatal("minion got no attack target")
	}

	// Lifetime cap: 1000 more hits must never exceed 6 total spawns.
	hitBoss(t, w, m, atk, 1, 1000)
	if got := pluginSpawnedTotal("sk-1"); got > 6 {
		t.Fatalf("spawned = %d, cap is 6", got)
	}
	w.mu.Lock()
	total := len(w.minions)
	w.mu.Unlock()
	if total > 6 {
		t.Fatalf("SpawnMinion calls = %d, cap is 6", total)
	}

	// Death clears live minions (Despawn path) and resets the counter.
	live := pluginMinionCount("sk-1")
	HitMob(m, atk, 999999, w, time.Now(), func() bool { return true })
	w.mu.Lock()
	kills := append([]string(nil), w.kills...)
	w.mu.Unlock()
	if len(kills) != live {
		t.Fatalf("death kills = %d, live minions = %d", len(kills), live)
	}
	if got := pluginSpawnedTotal("sk-1"); got != 0 {
		t.Fatalf("counter not reset: %d", got)
	}
}

func TestHellhoundHitAndRange(t *testing.T) {
	resetPlugins()
	SetPluginRandSeed(3)
	w := newPluginFake()
	w.withPlayer("hero-1", "hero", 100, 100, 5, 1)
	m := newTestMob("hh-1", "hellhound", bossProfile(10000), 100, 100)
	atk := &PlayerView{Instance: "hero-1", Username: "hero"}

	hitBoss(t, w, m, atk, 1, 300)
	w.mu.Lock()
	n := len(w.minions)
	w.mu.Unlock()
	if n == 0 {
		t.Fatal("hellhound spawned no minion in 300 hits")
	}
	w.mu.Lock()
	for _, mn := range w.minions {
		switch mn.key {
		case "darkwolf":
			if mn.x != 100 || mn.y != 100 {
				t.Fatalf("darkwolf at %d,%d, want boss pos", mn.x, mn.y)
			}
		case "blackwizard":
			if mn.opts.AttackRange != 16 {
				t.Fatalf("blackwizard range = %d", mn.opts.AttackRange)
			}
			okPos := (mn.x == 1059 && mn.y == 765) || (mn.x == 1047 && mn.y == 768)
			if !okPos {
				t.Fatalf("blackwizard at %d,%d", mn.x, mn.y)
			}
		default:
			t.Fatalf("unexpected minion key %q", mn.key)
		}
	}
	w.mu.Unlock()
	if got := pluginSpawnedTotal("hh-1"); got > 8 {
		t.Fatalf("spawned = %d, cap is 8", got)
	}

	// CombatLoop range: far target -> 12, adjacent -> 1.
	m.mu.Lock()
	m.target = "hero-1"
	m.mu.Unlock()
	w.mu.Lock()
	w.players[0].X, w.players[0].Y = 110, 100
	w.mu.Unlock()
	PluginTick(m, w, time.Now())
	if ar, _, _ := pluginCombatOverride("hellhound", "hh-1"); ar != 12 {
		t.Fatalf("far range = %d, want 12", ar)
	}
	w.mu.Lock()
	w.players[0].X, w.players[0].Y = 101, 100
	w.mu.Unlock()
	PluginTick(m, w, time.Now())
	if ar, _, _ := pluginCombatOverride("hellhound", "hh-1"); ar != 1 {
		t.Fatalf("melee range = %d, want 1", ar)
	}

	// Death clears minions + counter.
	live := pluginMinionCount("hh-1")
	HitMob(m, atk, 999999, w, time.Now(), func() bool { return true })
	w.mu.Lock()
	nk := len(w.kills)
	w.mu.Unlock()
	if nk != live {
		t.Fatalf("death kills = %d, live = %d", nk, live)
	}
}

func TestOgreLordWaves(t *testing.T) {
	resetPlugins()
	SetPluginRandSeed(5)
	w := newPluginFake()
	w.withPlayer("hero-1", "hero", 100, 100, 5, 1)
	m := newTestMob("og-1", "ogrelord", bossProfile(1000), 100, 100)
	atk := &PlayerView{Instance: "hero-1", Username: "hero"}
	now := time.Now()

	// Above half: no wave.
	HitMob(m, atk, 100, w, now, func() bool { return true })
	w.mu.Lock()
	if len(w.minions) != 0 {
		w.mu.Unlock()
		t.Fatal("wave fired above half HP")
	}
	w.mu.Unlock()

	// Cross half: wave 0 = 4 ogres at exact positions + talk.
	HitMob(m, atk, 401, w, now, func() bool { return true })
	w.mu.Lock()
	if len(w.minions) != 4 {
		w.mu.Unlock()
		t.Fatalf("wave0 minions = %d", len(w.minions))
	}
	want := map[[2]int]string{{338, 160}: "ogre", {346, 164}: "ogre", {342, 169}: "ogre", {335, 164}: "ogre"}
	for _, mn := range w.minions {
		if want[[2]int{mn.x, mn.y}] != mn.key || mn.opts.RoamDistance != 24 {
			t.Fatalf("wave0 minion = %+v", mn)
		}
	}
	if len(w.talks) != 1 || w.talks[0].msg != "My minions will surely help defeat you!" {
		t.Fatalf("wave talk = %+v", w.talks)
	}
	w.mu.Unlock()

	// More hits above quarter: no second wave yet, no repeat of first.
	HitMob(m, atk, 100, w, now, func() bool { return true })
	w.mu.Lock()
	if len(w.minions) != 4 {
		w.mu.Unlock()
		t.Fatal("wave refired")
	}
	w.mu.Unlock()

	// Cross quarter: wave 1 = 4 ironogres.
	HitMob(m, atk, 200, w, now, func() bool { return true }) // hp 199/1000
	w.mu.Lock()
	if len(w.minions) != 8 {
		t.Fatalf("wave1 total = %d", len(w.minions))
	}
	for _, mn := range w.minions[4:] {
		if mn.key != "ironogre" {
			t.Fatalf("wave1 key = %q", mn.key)
		}
	}
	w.mu.Unlock()

	// Death clears all 8 + resets waves (respawn refires wave 0).
	HitMob(m, atk, 9999, w, now, func() bool { return true })
	w.mu.Lock()
	if len(w.kills) != 8 {
		t.Fatalf("death kills = %d, want 8", len(w.kills))
	}
	w.mu.Unlock()
	w.fireDelays() // respawn timer
	HitMob(m, atk, 501, w, now, func() bool { return true })
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.minions) != 12 {
		t.Fatalf("post-respawn wave0 did not refire (minions=%d)", len(w.minions))
	}
}

func TestPirateCaptainSpawnTeleport(t *testing.T) {
	resetPlugins()
	SetPluginRandSeed(11)
	w := newPluginFake()
	w.withPlayer("hero-1", "hero", 100, 100, 5, 1)
	m := newTestMob("pc-1", "piratecaptain", bossProfile(20000), 100, 100)
	atk := &PlayerView{Instance: "hero-1", Username: "hero"}

	hitBoss(t, w, m, atk, 1, 600)
	w.mu.Lock()
	ns, nt := len(w.minions), len(w.teleports)
	w.mu.Unlock()
	if ns == 0 {
		t.Fatal("piratecaptain spawned no minion in 600 hits")
	}
	if nt == 0 {
		t.Fatal("piratecaptain never teleported in 600 hits")
	}
	w.mu.Lock()
	mn := w.minions[0]
	tp := w.teleports[0]
	w.mu.Unlock()
	if mn.key != "pirateskeleton" {
		t.Fatalf("minion key = %q", mn.key)
	}
	okSpot := false
	for _, s := range [][2]int{{923, 745}, {931, 746}, {929, 754}, {923, 752}} {
		if mn.x == s[0] && mn.y == s[1] {
			okSpot = true
		}
	}
	if !okSpot {
		t.Fatalf("minion at %d,%d", mn.x, mn.y)
	}
	if mn.opts.RoamDistance != 20 {
		t.Fatalf("roam = %d", mn.opts.RoamDistance)
	}
	okTp := false
	for _, s := range [][2]int{{935, 743}, {930, 755}, {920, 753}, {918, 743}} {
		if tp.x == s[0] && tp.y == s[1] {
			okTp = true
		}
	}
	if !okTp {
		t.Fatalf("teleport to %d,%d", tp.x, tp.y)
	}
	// Teleport switches to ranged (11) and clears combat.
	w.mu.Lock()
	nc := len(w.clears)
	w.mu.Unlock()
	if nc == 0 {
		t.Fatal("teleport did not clear combat")
	}
	if ar, _, _ := pluginCombatOverride("piratecaptain", "pc-1"); ar != 11 {
		t.Fatalf("post-teleport range = %d, want 11", ar)
	}
	if got := pluginSpawnedTotal("pc-1"); got > 8 {
		t.Fatalf("spawned = %d, cap is 8", got)
	}

	// Death clears minions + counter + range override.
	HitMob(m, atk, 999999, w, time.Now(), func() bool { return true })
	if _, _, ok := pluginCombatOverride("piratecaptain", "pc-1"); ok {
		t.Fatal("range override survived death")
	}
}

func TestQueenAntWaveAndSpecial(t *testing.T) {
	resetPlugins()
	SetPluginRandSeed(21)
	w := newPluginFake()
	w.withPlayer("hero-1", "hero", 100, 100, 5, 1)
	w.withPlayer("hero-2", "hero2", 101, 100, 5, 1)
	m := newTestMob("qa-1", "queenant", bossProfile(1000), 100, 100)
	atk := &PlayerView{Instance: "hero-1", Username: "hero"}
	now := time.Now()

	// Above half: nothing.
	HitMob(m, atk, 100, w, now, func() bool { return true })
	w.mu.Lock()
	if len(w.minions) != 0 {
		w.mu.Unlock()
		t.Fatal("wave fired above half HP")
	}
	w.mu.Unlock()

	// Cross half: 4 ants at spawn +/-3 + attackRate 750.
	HitMob(m, atk, 401, w, now, func() bool { return true })
	w.mu.Lock()
	if len(w.minions) != 4 {
		w.mu.Unlock()
		t.Fatalf("ants = %d", len(w.minions))
	}
	want := map[[2]int]bool{{103, 100}: true, {97, 100}: true, {100, 103}: true, {100, 97}: true}
	for _, mn := range w.minions {
		if mn.key != "ant" || !want[[2]int{mn.x, mn.y}] || !mn.opts.NoRoam {
			t.Fatalf("ant = %+v", mn)
		}
	}
	w.mu.Unlock()
	if _, rate, _ := pluginCombatOverride("queenant", "qa-1"); rate != 750 {
		t.Fatalf("attackRate override = %d, want 750", rate)
	}

	// No refire on further hits.
	HitMob(m, atk, 50, w, now, func() bool { return true })
	w.mu.Lock()
	if len(w.minions) != 4 {
		w.mu.Unlock()
		t.Fatal("wave refired")
	}
	w.mu.Unlock()

	// Special attack (1/6 per swing) hits ALL attackers via attackAll.
	m.mu.Lock()
	m.target = "hero-1"
	m.attackers = map[string]time.Time{"hero-1": now, "hero-2": now}
	m.mu.Unlock()
	fired := false
	for i := 0; i < 120 && !fired; i++ {
		w.mu.Lock()
		w.heroPts = nil
		w.mu.Unlock()
		strikeMob(m, m.prof, PlayerView{Instance: "hero-1", Username: "hero", X: 101, Y: 100}, w)
		w.mu.Lock()
		seen := map[string]bool{}
		for _, p := range w.heroPts {
			seen[p.instance] = true
		}
		w.mu.Unlock()
		if seen["hero-1"] && seen["hero-2"] {
			fired = true
		}
	}
	if !fired {
		t.Fatal("queen special (attackAll) never fired in 120 swings")
	}

	// CombatLoop range when idle-special clears: far -> 10.
	w.mu.Lock()
	w.players[0].X, w.players[0].Y = 110, 100
	w.mu.Unlock()
	// Drain the special latch first (next swing resets it).
	strikeMob(m, m.prof, PlayerView{Instance: "hero-1", Username: "hero", X: 110, Y: 100}, w)
	PluginTick(m, w, time.Now())
	if ar, _, _ := pluginCombatOverride("queenant", "qa-1"); ar != 10 {
		t.Fatalf("far range = %d, want 10", ar)
	}

	// Death clears ants.
	HitMob(m, atk, 9999, w, now, func() bool { return true })
	w.mu.Lock()
	if len(w.kills) != 4 {
		t.Fatalf("death kills = %d, want 4", len(w.kills))
	}
	w.mu.Unlock()
}

func TestSantaHealOnce(t *testing.T) {
	resetPlugins()
	SetPluginRandSeed(31)
	w := newPluginFake()
	w.withPlayer("hero-1", "hero", 100, 100, 5, 1)
	m := newTestMob("sa-1", "santa", bossProfile(900), 100, 100)
	atk := &PlayerView{Instance: "hero-1", Username: "hero"}
	now := time.Now()

	HitMob(m, atk, 100, w, now, func() bool { return true })
	w.mu.Lock()
	if len(w.heals) != 0 || len(w.talks) != 0 {
		w.mu.Unlock()
		t.Fatal("heal fired above half HP")
	}
	w.mu.Unlock()

	HitMob(m, atk, 351, w, now, func() bool { return true }) // 449/900
	w.mu.Lock()
	if len(w.heals) != 1 || w.heals[0].hp != 300 {
		t.Fatalf("heal = %+v, want 300", w.heals)
	}
	if len(w.talks) != 1 || w.talks[0].msg != "The power of Christmas heals me!" {
		t.Fatalf("talk = %+v", w.talks)
	}
	w.mu.Unlock()

	// One-shot: further hits never heal again.
	HitMob(m, atk, 100, w, now, func() bool { return true })
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.heals) != 1 {
		t.Fatalf("heal refired: %+v", w.heals)
	}
}

func TestForestDragonSpecialCycle(t *testing.T) {
	resetPlugins()
	SetPluginRandSeed(41)
	w := newPluginFake()
	m := newTestMob("fd-1", "forestdragon", bossProfile(5000), 100, 100)
	v := PlayerView{Instance: "hero-1", Username: "hero", X: 101, Y: 100}

	firedAt := -1
	for i := 0; i < 120; i++ {
		strikeMob(m, m.prof, v, w)
		if ar, _, _ := pluginCombatOverride("forestdragon", "fd-1"); ar == 9 {
			firedAt = i
			break
		}
	}
	if firedAt < 0 {
		t.Fatal("special never fired in 120 swings")
	}
	// Next swing resets the latch (TS early-return reset).
	strikeMob(m, m.prof, v, w)
	if ar, _, _ := pluginCombatOverride("forestdragon", "fd-1"); ar == 9 {
		t.Fatal("special latch did not reset on next swing")
	}
	// CombatLoop then picks melee/ranged: adjacent -> 1.
	m.mu.Lock()
	m.target = "hero-1"
	m.mu.Unlock()
	w.withPlayer("hero-1", "hero", 101, 100, 5, 1)
	PluginTick(m, w, time.Now())
	if ar, _, _ := pluginCombatOverride("forestdragon", "fd-1"); ar != 1 {
		t.Fatalf("melee range = %d, want 1", ar)
	}
}

func TestAntWorkerVsWild(t *testing.T) {
	resetPlugins()
	SetPluginRandSeed(51)
	w := newPluginFake()
	now := time.Now()
	atk := &PlayerView{Instance: "hero-1", Username: "hero"}

	// Tracked worker ant: hit does NOT retaliate (TS handleHit no-op).
	noteMinionSpawned("qa-9", "ant-1", "ant")
	worker := newTestMob("ant-1", "ant", bossProfile(200), 100, 100)
	HitMob(worker, atk, 10, w, now, func() bool { return true })
	worker.mu.Lock()
	tgt := worker.target
	worker.mu.Unlock()
	if tgt != "" {
		t.Fatalf("worker ant retaliated: target=%q", tgt)
	}

	// Wild ant: default retaliation intact.
	wild := newTestMob("ant-9", "ant", bossProfile(200), 100, 100)
	HitMob(wild, atk, 10, w, now, func() bool { return true })
	wild.mu.Lock()
	tgt = wild.target
	wild.mu.Unlock()
	if tgt != "hero-1" {
		t.Fatalf("wild ant target = %q, want hero-1", tgt)
	}

	// Worker heals its queen (35) when near, on its 1-5s cadence.
	w.entityAt["qa-9"] = [2]int{100, 100}
	w.entityAt["ant-1"] = [2]int{101, 100}
	fired := false
	base := time.Now()
	for i := 0; i < 40 && !fired; i++ {
		PluginTick(worker, w, base.Add(time.Duration(i)*500*time.Millisecond))
		w.mu.Lock()
		for _, h := range w.heals {
			if h.instance == "qa-9" && h.hp == 35 {
				fired = true
			}
		}
		w.mu.Unlock()
	}
	if !fired {
		t.Fatal("worker ant never healed its queen")
	}
}

func TestDefaultMobPathUntouched(t *testing.T) {
	resetPlugins()
	SetPluginRandSeed(61)
	w := newPluginFake()
	w.withPlayer("hero-1", "hero", 103, 100, 1, 1)
	atk := &PlayerView{Instance: "hero-1", Username: "hero"}
	now := time.Now()

	// Rat + spider: retaliate, Points, death pipeline — and zero host calls.
	for _, key := range []string{"rat", "spider"} {
		m := newTestMob("mob-"+key, key, ratProfile(), 100, 100)
		HitMob(m, atk, 5, w, now, func() bool { return true })
		m.mu.Lock()
		tgt := m.target
		m.mu.Unlock()
		if tgt != "hero-1" {
			t.Fatalf("%s: target = %q", key, tgt)
		}
		HitMob(m, atk, 999, w, now, func() bool { return true })
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.minions) != 0 || len(w.kills) != 0 || len(w.talks) != 0 ||
		len(w.teleports) != 0 || len(w.heals) != 0 || len(w.targets) != 0 {
		t.Fatalf("default path touched the plugin host: minions=%d kills=%d talks=%d tps=%d heals=%d targets=%d",
			len(w.minions), len(w.kills), len(w.talks), len(w.teleports), len(w.heals), len(w.targets))
	}
	if len(w.despawns) != 2 || len(w.lootDrops) != 2 || len(w.quests) != 2 {
		t.Fatalf("default death pipeline changed: despawns=%d loot=%d quests=%d",
			len(w.despawns), len(w.lootDrops), len(w.quests))
	}
}

func TestMinionDeathPrunesAndRestores(t *testing.T) {
	resetPlugins()
	w := newPluginFake()
	// Simulate a queen with two live ants; one dies naturally.
	// (NoRespawn mirrors what the adapter sets for spawned minions.)
	noteMinionSpawned("qa-2", "qa-2-minion-1", "ant")
	noteMinionSpawned("qa-2", "qa-2-minion-2", "ant")
	pluginMu.Lock()
	pluginStates["qa-2"].atkRateOverride = queenAttackRate
	pluginMu.Unlock()

	m1 := newTestMob("qa-2-minion-1", "ant", bossProfile(50), 100, 100)
	m1.over.NoRespawn = true
	HitMob(m1, nil, 999, w, time.Now(), func() bool { return true })
	if got := pluginMinionCount("qa-2"); got != 1 {
		t.Fatalf("live minions = %d, want 1", got)
	}
	// Rate stays boosted while an ant lives...
	if _, rate, _ := pluginCombatOverride("queenant", "qa-2"); rate != 750 {
		t.Fatalf("rate dropped early: %d", rate)
	}
	w.mu.Lock()
	if len(w.died) != 1 || w.died[0] != [2]string{"qa-2", "qa-2-minion-1"} {
		t.Fatalf("MinionDied calls = %+v", w.died)
	}
	// ...and minions never schedule respawn timers.
	if len(w.delays) != 0 {
		t.Fatalf("minion death scheduled %d respawn timers", len(w.delays))
	}
	w.mu.Unlock()

	m2 := newTestMob("qa-2-minion-2", "ant", bossProfile(50), 100, 100)
	m2.over.NoRespawn = true
	HitMob(m2, nil, 999, w, time.Now(), func() bool { return true })
	if _, rate, _ := pluginCombatOverride("queenant", "qa-2"); rate != 0 {
		t.Fatalf("queen rate not restored: %d", rate)
	}
}
