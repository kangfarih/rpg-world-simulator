// ---------------------------------------------------------------------------
// M10 — the area system: thin root adapter.
//
// The system lives in internal/entity (areas.go); this file keeps
// UNCHANGED public signatures so main.go, world_wire.go, m9.go and m13.go
// call sites compile untouched. No m10 state stays here: the old
// m10Area/m10Chest structs are entity.Area/entity.Chest aliases (no code
// outside this file touched their fields), the registries + per-player
// detection state live in entity, and packet shapes are frozen in the
// shared gameWorld adapter (m9.go).
//
// The m9<->m10 call cycle (m9SpawnMob->m10ChestAreaAt/m10AddChestMob;
// m9KillMob->m10KillHooks) is gone: both engines live in ONE package
// (internal/entity) behind the GameWorld seam.
//
// Only the TESTMAP dispatchers keep logic here (frame parsing + test
// wiring call the same public functions, so the debug frames are frozen).
// ---------------------------------------------------------------------------

package server

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"rpg-world-server/internal/entity"
)

// m10Area/m10Chest are entity-owned now (aliases — opaque uses in main.go
// keep compiling; no external code touches their fields).
type (
	m10Area  = entity.Area
	m10Chest = entity.Chest
)

// m10LoadAreas parses world.json `areas` groups at boot (Node world.ts
// constructor builds one Areas subclass per group from map.areas).
func m10LoadAreas() {
	loadWorld()
	var raw []byte
	if world != nil {
		var err error
		raw, err = os.ReadFile(worldPath())
		if err != nil {
			return
		}
	}
	entity.LoadAreas(raw)
	entity.SpawnStaticChests(gameWorld)
}

// m10ChestAreaAt ports Mob.addToChestArea (mob.ts: chestAreas.inArea).
func m10ChestAreaAt(x, y int) *m10Area {
	return entity.ChestAreaAt(x, y)
}

// m10AddChestMob ports Area.addEntity (mob.ts addToChestArea). Records the
// mob in the area and (first mob only) adopts its respawn delay as the
// chest spawn guard; a live unlooted chest is removed (chest.ts onSpawn ->
// removeChest).
func m10AddChestMob(area *m10Area, instance string, respawnDelay time.Duration) {
	entity.AddChestMob(area, instance, respawnDelay, gameWorld)
}

// m10KillHooks fires the M10 chest-area death path for a killed mob
// (handler.ts: mob.area?.removeEntity(mob, attacker) -> onEmpty chest spawn
// + attacker achievement). killer is nil for killerless kills (no award).
func m10KillHooks(m *m9Mob, killer *playerConn) {
	killerInstance := ""
	if killer != nil {
		killerInstance = killer.Instance
	}
	entity.KillHookForMob(m.x, m.y, m.instance, killerInstance, gameWorld)
}

// m10ChestFor finds a live chest entity by instance.
func m10ChestFor(instance string) *m10Chest {
	return entity.ChestFor(instance)
}

// m10ChestItemsAt reports the chest occupying a tile (movement-block check).
func m10ChestItemsAt(x, y int) bool {
	return entity.ChestAt(x, y)
}

// m10OpenChest ports entities.ts spawnChest onOpen in TS order: despawn
// the chest, spawn the mimic mob when flagged and opened by a player
// (non-respawnable, linked so its death re-spawns the chest), roll one
// entry and spawn it at the chest tile as a persistent M5 loot entity,
// then finish the chest's own achievement for the opener when set (static
// chests). Area achievements still fire at CLEAR time (RemoveChestMob,
// chest.ts onEmpty parity), never on open.
func m10OpenChest(c *playerConn, chest *m10Chest) {
	entity.OpenChest(chest, c.Instance, c.Username, gameWorld)
}

// m10OnPositionUpdate is the M10 hook on the movement path (Node
// handleMovement -> detectAreas). Change-detection is per player per group.
func m10OnPositionUpdate(c *playerConn) {
	entity.OnPositionUpdate(c.Instance, c.Username, c.Sess.PlayerX, c.Sess.PlayerY, gameWorld)
}

// m10UpdatePVP ports player.updatePVP: notify + PVP packet on state flip.
func m10UpdatePVP(c *playerConn, inPVP bool) {
	entity.UpdatePVP(c.Instance, c.Username, inPVP, gameWorld)
}

// m10SetFreezing applies/removes the Freezing status effect
// (Area.addPlayer/removePlayer -> player.status Effects.Freezing).
func m10SetFreezing(c *playerConn, on bool) {
	entity.SetFreezing(c.Instance, on, gameWorld)
}

// m10PVPState reports the player's current pvp flag (Spawn PlayerData.pvp).
func m10PVPState(instance string) bool {
	return entity.PVPState(instance)
}

// m10ForgetPlayer drops per-player area state on disconnect.
func m10ForgetPlayer(instance string) {
	entity.ForgetPlayer(instance, gameWorld)
}

// m10InjectTestAreas ensures the TESTMAP synthetic area bands (TESTMAP
// mode only; the per-group "don't shadow the real world" rule lives in
// entity.InjectTestAreas).
func m10InjectTestAreas() {
	if !testMode || cleanMode || combatMode {
		return
	}
	entity.InjectTestAreas()
}

// ---------------------------------------------------------------------------
// M10TEST debug frame (TESTMAP-only): chest-mob adoption + area echo for the
// e2e. Shape: C->S [46, {"m10test":"..."}] (rides the Minigame dispatcher).
// ---------------------------------------------------------------------------

func m10HandleTest(c *playerConn, frame clientFrame) {
	if !testMode || cleanMode || combatMode || len(frame) < 2 {
		return
	}
	var data struct {
		M10Test  string `json:"m10test"`
		Instance string `json:"instance"`
		Key      string `json:"key"`
		Delay    int    `json:"delay"`
		X        int    `json:"x"`
		Y        int    `json:"y"`
	}
	if err := json.Unmarshal(frame[1], &data); err != nil {
		return
	}
	// Default key preserved for the M9-era rat spawn; the M10 chest harness
	// picks a passive low-HP mob (crab) so the kill lands inside the area
	// before the engine's roam pass can move it out.
	mobKey := data.Key
	if mobKey == "" {
		mobKey = "rat"
	}
	switch data.M10Test {
	case "chestmob":
		// Spawn a mob inside the chest area and adopt it (Mob.addToChestArea
		// parity — the harness spawns at coordinates inside the area). The
		// Respawn override rides the spawn call (no post-spawn mutation).
		area := entity.FirstChestArea()
		if area == nil {
			return
		}
		over := m9Overrides{}
		if data.Delay > 0 {
			over.Respawn = time.Duration(data.Delay) * time.Millisecond
		}
		if !m9SpawnMob(data.Instance, mobKey, data.X, data.Y, over) {
			return
		}
		if m := m9MobFor(data.Instance); m != nil {
			m10AddChestMob(area, data.Instance, m.respawnDelay())
			m6Notify(c, fmt.Sprintf("m10:chestmob=%s", data.Instance))
		}
	case "mobhp":
		// Echo mob state (mirrors m9's mobhp for the kill leg).
		m := m9MobFor(data.Instance)
		if m == nil || c == nil {
			return
		}
		m.mu.Lock()
		echo := fmt.Sprintf("m10:mob=%s hp=%d/%d", data.Instance, m.hp, m.maxHP)
		m.mu.Unlock()
		m6Notify(c, echo)
	case "chest":
		// Echo live chest state for the chest leg (introspection).
		var echo string
		for _, a := range entity.ChestAreas() {
			if ch := a.LiveChest(); ch != nil {
				echo = fmt.Sprintf("m10:chest=%s x=%d y=%d", ch.Instance, ch.X, ch.Y)
			}
		}
		if echo == "" {
			echo = "m10:chest=none"
		}
		m6Notify(c, echo)
	}
}
