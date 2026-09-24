package server

import (
	"testing"
	"time"

	"rpg-world-server/internal/entity"
)

// A DoT tick kills through the DamagePlayer seam with no mob attacker
// (StatusTick -> m9DamagePlayer(c, dmg, nil) parity): the full HeroDied
// funnel must run — Death unicast to the victim only, Despawn broadcast,
// synchronous save — exactly once per life (a second lethal tick is
// silent).
func TestDotKillHeroRunsDeathFunnelOnce(t *testing.T) {
	openDeathDB(t)
	petConfigure() // production companion wiring (disconnect parity)

	user, inst := "dot-hero", "dot-hero-inst"
	st := m5StateFor(user)
	pstateMu.Lock()
	st.Skills[SkillDefense] = &m5Skill{Level: 5, XP: 4321}
	pstateMu.Unlock()
	t.Cleanup(func() {
		pstateMu.Lock()
		delete(pstates, user)
		pstateMu.Unlock()
	})

	victim, _ := deathConn(t, inst, user)
	observer, _ := deathConn(t, "dot-observer-inst", "dot-observer-user")
	t.Cleanup(func() {
		m9DeathFired.Delete(inst)
		m9PlayerHPs.Delete(inst)
	})

	m9PlayerHPs.Store(inst, 5)
	drainOutbox(victim)
	drainOutbox(observer)

	// Poison-scale environmental damage: no mob involved.
	m9DamagePlayer(victim, 50, nil)
	// A second lethal tick on the corpse must be silent (exactly-once).
	m9DamagePlayer(victim, 50, nil)

	if got := m9PlayerHP(victim); got != 0 {
		t.Fatalf("hero HP = %d, want 0", got)
	}

	count := func(frames [][]any, id int, instance string) int {
		n := 0
		for _, f := range frames {
			if fid, fi, ok := frameInstance(f); ok && fid == id && fi == instance {
				n++
			}
		}
		return n
	}
	victimFrames := drainOutbox(victim)
	observerFrames := drainOutbox(observer)

	if got := count(victimFrames, PacketDeath, inst); got != 1 {
		t.Fatalf("victim Death frames = %d, want exactly 1", got)
	}
	if got := count(observerFrames, PacketDeath, inst); got != 0 {
		t.Fatalf("observer saw %d Death frames, want 0 (victim-unicast)", got)
	}
	if got := count(victimFrames, PacketDespawn, inst); got != 1 {
		t.Fatalf("victim Despawn frames = %d, want exactly 1", got)
	}
	if got := count(observerFrames, PacketDespawn, inst); got < 1 {
		t.Fatal("observer missing Despawn broadcast for the victim")
	}

	// Synchronous flush: the pre-death XP row is readable immediately.
	ps, ok := persistStore.LoadPlayer(user)
	if !ok {
		t.Fatal("victim row missing after death save")
	}
	sk, ok := ps.Skills[SkillDefense]
	if !ok || sk.XP != 4321 || sk.Level != 5 {
		t.Fatalf("persisted defense skill = %+v, want level 5 / 4321 XP", sk)
	}
}

// A DoT tick killing a mob runs the full KillMob path with no attacker:
// Despawn, unowned loot at the corpse, and the respawn timer (no killer
// credit is invented).
func TestDotKillMobDropsLootAndRespawns(t *testing.T) {
	const inst = "dot-mob"
	m := &m9Mob{
		instance: inst, key: "rat",
		prof:   entity.MobProfile{Name: "Rat", Level: 1, HitPoints: 10, RespawnDelay: 0},
		spawnX: 104, spawnY: 104, x: 104, y: 104,
		hp: 10, maxHP: 10,
		attackers: map[string]time.Time{},
		over:      m9Overrides{Respawn: 30 * time.Millisecond},
	}
	m9Mu.Lock()
	m9Mobs[inst] = m
	m9Mu.Unlock()
	t.Cleanup(func() { m9Remove(inst) })

	// Lethal DoT-scale hit with no attacker.
	m9PlayerHit(m, nil, 9999)

	m.mu.Lock()
	dead := m.dead
	m.mu.Unlock()
	if !dead {
		t.Fatal("mob survived lethal killerless hit")
	}

	// Loot spawned at/near the corpse tile (unowned environmental drop).
	found := ""
	for dx := -3; dx <= 3 && found == ""; dx++ {
		for dy := -3; dy <= 3 && found == ""; dy++ {
			if id, ok := entity.FindLootAt(104+dx, 104+dy); ok {
				found = id
			}
		}
	}
	if found == "" {
		t.Fatal("no loot spawned for the DoT-killed mob")
	}
	t.Cleanup(func() { entity.DestroyLoot(found, "test") })
	if l, ok := entity.FindLoot(found); !ok || l.Owner != "" {
		t.Fatalf("environmental loot must be unowned: %+v", l)
	}

	// Respawn timer fires: full HP at spawn.
	deadline := time.Now().Add(3 * time.Second)
	for {
		m.mu.Lock()
		dead = m.dead
		hp := m.hp
		m.mu.Unlock()
		if !dead {
			if hp != 10 {
				t.Fatalf("respawned hp = %d, want 10", hp)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("mob did not respawn after DoT kill")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
