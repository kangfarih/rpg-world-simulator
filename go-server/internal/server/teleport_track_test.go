package server

import (
	"testing"

	"rpg-world-server/internal/entity"
)

// Every server-side position set must flow through m5TrackPos (the same
// helper walked movement uses), or saves persist the stale pre-teleport
// tile: death-save, disconnect-save and the 10s dirty flush all read
// pstates, never the session.

// Warp/door/mod/admin teleports funnel through m7Teleport.
func TestM7TeleportTracksPosition(t *testing.T) {
	const user, inst = "tp-m7-user", "tp-m7-inst"
	c, _ := deathConn(t, inst, user)
	t.Cleanup(func() {
		pstateMu.Lock()
		delete(pstates, user)
		pstateMu.Unlock()
	})
	c.Sess.PlayerX, c.Sess.PlayerY = 100, 96
	m5TrackPos(c) // baseline, as a walked step would

	m7Teleport(c, 150, 150)

	if c.Sess.PlayerX != 150 || c.Sess.PlayerY != 150 {
		t.Fatalf("session pos = %d,%d, want 150,150", c.Sess.PlayerX, c.Sess.PlayerY)
	}
	if st := m5Snapshot(user); st == nil || st.X != 150 || st.Y != 150 {
		t.Fatalf("tracked pos = %+v, want 150,150", st)
	}
}

// Minigame moves funnel through m8Teleport.
func TestM8TeleportTracksPosition(t *testing.T) {
	const user, inst = "tp-m8-user", "tp-m8-inst"
	c, _ := deathConn(t, inst, user)
	t.Cleanup(func() {
		pstateMu.Lock()
		delete(pstates, user)
		pstateMu.Unlock()
	})
	c.Sess.PlayerX, c.Sess.PlayerY = 100, 96
	m5TrackPos(c)

	m8Teleport(c, 151, 151)

	if st := m5Snapshot(user); st == nil || st.X != 151 || st.Y != 151 {
		t.Fatalf("tracked pos = %+v, want 151,151", st)
	}
}

// Respawn must track the spawn tile: a disconnect right after respawn
// relogins at spawn, not at the death tile.
func TestRespawnTracksSpawnTile(t *testing.T) {
	const user, inst = "tp-respawn-user", "tp-respawn-inst"
	c, _ := deathConn(t, inst, user)
	t.Cleanup(func() {
		pstateMu.Lock()
		delete(pstates, user)
		pstateMu.Unlock()
		m9PlayerHPs.Delete(inst)
		m9DeathFired.Delete(inst)
	})
	// Die at 150,150 (tracked, as a walked arrival would).
	c.Sess.PlayerX, c.Sess.PlayerY = 150, 150
	m5TrackPos(c)
	m9PlayerHPs.Store(inst, 0)

	m9HandleRespawn(c)

	if c.Sess.PlayerX != entity.HeroSpawnX || c.Sess.PlayerY != entity.HeroSpawnY {
		t.Fatalf("session pos = %d,%d, want spawn %d,%d",
			c.Sess.PlayerX, c.Sess.PlayerY, entity.HeroSpawnX, entity.HeroSpawnY)
	}
	if st := m5Snapshot(user); st == nil || st.X != entity.HeroSpawnX || st.Y != entity.HeroSpawnY {
		t.Fatalf("tracked pos = %+v, want spawn %d,%d", st, entity.HeroSpawnX, entity.HeroSpawnY)
	}
}
