// Statistics adapter: wires the pure internal/player/stats counters into
// the live flows (gather exhaust, mob kill, pickup, examine) and finishes
// milestone achievements through the m11/quest path.
//
// TS sources: statistics.ts (handleSkill/addMobKill/addMobExamine/addDrop),
// resourceskill.ts:121 (handleSkill on exhaust), handler.ts:765 (addMobKill
// on mob death), player.ts:1268 (addDrop on owner pickup).
package server

import (
	"rpg-world-server/internal/persist"
	"rpg-world-server/internal/player/stats"
)

// statsCopyOf snapshots a player's counters for the persist write path
// (m5ToPersist).
func statsCopyOf(username string) stats.Snapshot {
	return stats.CopyOf(username)
}

// statsInstall restores a player's counters from the persist load path
// (persistToM5), converting the storage blob to the domain snapshot.
func statsInstall(username string, blob persist.StatsBlob) {
	stats.Install(username, stats.Snapshot{
		MobKills:    blob.MobKills,
		MobExamines: blob.MobExamines,
		Resources:   blob.Resources,
		Drops:       blob.Drops,
	})
}

// statsHandleSkill records one successful gather for skill and finishes the
// milestone achievement when one is reached (statistics.handleSkill parity:
// foraging skipped, key `<skill><N>`). Unknown achievement keys are ignored
// (TS `?.finish()` nil parity). Nil-safe.
func statsHandleSkill(c *playerConn, skill string) {
	if c == nil || c.Username == "" {
		return
	}
	var ach string
	var fired bool
	stats.Update(c.Username, func(st *stats.State) {
		ach, fired = stats.HandleSkill(st, skill)
	})
	if !fired {
		// No milestone: still persist the advanced counter (foraging never
		// advances — HandleSkill early-returns — so this is a gather tick).
		if skill != "foraging" {
			markDirty(c.Username)
		}
		return
	}
	markDirty(c.Username)
	if m11A[ach] == nil {
		return
	}
	m11FinishAchievement(c, ach)
}

// statsRecordKill records a mob kill for the killer (statistics.addMobKill
// parity: counter only — mobKills feeds NO achievement, verified against
// statistics.ts). Nil-safe.
func statsRecordKill(c *playerConn, mobKey string) {
	if c == nil || c.Username == "" {
		return
	}
	stats.Update(c.Username, func(st *stats.State) {
		stats.AddMobKill(st, mobKey)
	})
	markDirty(c.Username)
}

// statsAddDrop records a pickup for the owner (player.ts:1268 parity: only
// when the loot owner matches the picker). Nil-safe.
func statsAddDrop(c *playerConn, key string, count int) {
	if c == nil || c.Username == "" {
		return
	}
	stats.Update(c.Username, func(st *stats.State) {
		stats.AddDrop(st, key, count)
	})
	markDirty(c.Username)
}

// statsAddMobExamine records examining key and finishes the examiner
// achievement at 10/25/50 distinct examines (statistics.addMobExamine
// parity). Nil-safe.
func statsAddMobExamine(c *playerConn, key string) {
	if c == nil || c.Username == "" {
		return
	}
	var ach string
	var fired bool
	stats.Update(c.Username, func(st *stats.State) {
		ach, fired = stats.AddMobExamine(st, key)
	})
	markDirty(c.Username)
	if !fired || m11A[ach] == nil {
		return
	}
	m11FinishAchievement(c, ach)
}
