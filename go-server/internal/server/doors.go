// Doors dispatch (handler.ts handleDoor port, trigger player.ts
// handleMovementStop).
//
// Trigger: the server's MovementStep authoritative-position update landing
// exactly on a door entry tile (TS checks map.isDoor AFTER setPosition in
// handleMovementStop; teleports never trigger doors, so the paired
// destination landing cannot loop).
//
// Gate order mirrors handler.ts handleDoor exactly:
//  1. talkIndex reset,
//  2. level gate (combat level, or the named skill level) with
//     NO_COMBAT_DOOR / NO_SKILL_DOOR notifies,
//  3. quest doorCallback (stage gate),
//  4. achievement finish,
//  5. reqAchievement / 6. reqQuest / 7. reqItem (+DOOR_KEY_CRUMBLES),
//  8. teleport to the linked destination.
//
// Parity notes (documented divergences):
//   - Quest doorCallback: the Go quest engine has NO per-quest doorCallback
//     seam. TS quest/impl defines two custom handleDoor overrides
//     (anvilsechoes: NPC "don't go in there" talk instead of the plain
//     notify; evilsanta: WHY_GO_THERE / DONT_THINK_GO_IN notifies + its own
//     reqItem leg before super). The port applies the DEFAULT Quest
//     handleDoor semantics for every quest door: stage < door.stage blocks
//     with misc:CANNOT_PASS_DOOR, otherwise the door teleports. Door-task
//     progression (isDoorTask -> progress) is skipped — the Go engine
//     models quest progress via talk/kill/resource only.
//   - Unknown quest/achievement keys block (CANNOT_PASS_DOOR /
//     NO_*_DOOR with an empty name). TS would dereference undefined
//     (quest.doorCallback on a missing quest); blocking is the safe port.
package server

import (
	"fmt"

	"rpg-world-server/internal/entity"
)

// doorCtx carries everything the gate reads (built from globals by the
// handler so the evaluation stays pure and unit-testable).
type doorCtx struct {
	CombatLevel int
	HasSkill    bool // door.Skill resolved to a known skill id
	SkillLevel  int

	QuestKnown bool // quest def exists in the engine
	QuestStage int
	QuestName  string

	ReqAchKnown    bool
	ReqAchFinished bool
	ReqAchName     string

	ReqQuestKnown    bool
	ReqQuestFinished bool
	ReqQuestName     string

	InvCount int // reqItem copies held
}

// doorPlan is the evaluated outcome: finish the achievement first (TS
// finishes door.achievement BEFORE the req gates, so it applies even when a
// later gate blocks), then either block with a notify or pass (teleport,
// consuming the key item when required).
type doorPlan struct {
	FinishAch   string
	BlockMsg    string // "" = pass
	ConsumeItem bool
	Crumble     bool
}

// planDoor evaluates the handleDoor gate chain for a linked door (pure).
func planDoor(d *entity.Door, ctx doorCtx) doorPlan {
	var plan doorPlan

	// Level gate: named skill level, else combat level.
	if d.Level > 0 {
		lvl := ctx.CombatLevel
		msg := fmt.Sprintf("misc:NO_COMBAT_DOOR;level=%d", d.Level)
		if d.Skill != "" {
			if ctx.HasSkill {
				lvl = ctx.SkillLevel
			} else {
				lvl = -1 // unknown skill can never satisfy the gate
			}
			msg = fmt.Sprintf("misc:NO_SKILL_DOOR;skill=%s;level=%d", d.Skill, d.Level)
		}
		if lvl < d.Level {
			plan.BlockMsg = msg
			return plan
		}
	}

	// Quest door: stage gate (default Quest.handleDoor semantics), then
	// teleport on pass — remaining gates are skipped (TS returns).
	if d.Quest != "" {
		if !ctx.QuestKnown || ctx.QuestStage < d.Stage {
			plan.BlockMsg = "misc:CANNOT_PASS_DOOR"
			return plan
		}
		return plan
	}

	// Achievement finish rides the quest.Finish path (m11FinishAchievement).
	if d.Achievement != "" {
		plan.FinishAch = d.Achievement
	}

	if d.ReqAchievement != "" {
		if !ctx.ReqAchKnown || !ctx.ReqAchFinished {
			plan.BlockMsg = fmt.Sprintf("misc:NO_ACHIEVEMENT_DOOR;achievement=%s", ctx.ReqAchName)
			return plan
		}
	}

	if d.ReqQuest != "" {
		if !ctx.ReqQuestKnown || !ctx.ReqQuestFinished {
			plan.BlockMsg = fmt.Sprintf("misc:NO_QUEST_DOOR;quest=%s", ctx.ReqQuestName)
			return plan
		}
	}

	if d.ReqItem != "" {
		need := d.ReqItemCount
		if need < 1 {
			need = 1
		}
		if ctx.InvCount < need {
			plan.BlockMsg = "misc:NO_KEY_DOOR"
			return plan
		}
		plan.ConsumeItem = true
		plan.Crumble = true
	}

	return plan
}

// doorCtxFor builds the gate context for a live connection.
func doorCtxFor(c *playerConn, d *entity.Door) doorCtx {
	var ctx doorCtx
	st := m5StateFor(c.Username)
	pstateMu.Lock()
	ctx.CombatLevel = st.Level
	if d.Skill != "" {
		if id, ok := doorSkillID(d.Skill); ok {
			ctx.HasSkill = true
			if s := st.Skills[id]; s != nil {
				ctx.SkillLevel = s.Level
			}
		}
	}
	pstateMu.Unlock()

	if d.Quest != "" {
		if def := m11Q[d.Quest]; def != nil {
			ctx.QuestKnown = true
			ctx.QuestName = def.Raw.Name
			ctx.QuestStage = m11StateFor(c.Username).PlayerState.Quest(d.Quest).Stage
		}
	}
	if d.ReqAchievement != "" {
		if def := m11A[d.ReqAchievement]; def != nil {
			ctx.ReqAchKnown = true
			ctx.ReqAchName = def.Raw.Name
			ctx.ReqAchFinished = m11StateFor(c.Username).PlayerState.Achs[d.ReqAchievement] >= def.StageCount
		}
	}
	if d.ReqQuest != "" {
		if def := m11Q[d.ReqQuest]; def != nil {
			ctx.ReqQuestKnown = true
			ctx.ReqQuestName = def.Raw.Name
			ctx.ReqQuestFinished = m11StateFor(c.Username).PlayerState.IsFinished(d.ReqQuest)
		}
	}
	if d.ReqItem != "" {
		ctx.InvCount = m6InvCount(c.Username, d.ReqItem)
	}
	return ctx
}

// handleDoorStep runs the door flow when the authoritative Step position
// lands on a door entry tile (player.ts handleMovementStop parity).
func handleDoorStep(c *playerConn) {
	if c == nil || c.Username == "" {
		return
	}
	d := entity.DoorAt(c.Sess.PlayerX, c.Sess.PlayerY)
	if d == nil {
		return
	}
	// Reset talking index when passing through any door (handler.ts:252).
	c.SetTalkIndex(0)

	plan := planDoor(d, doorCtxFor(c, d))

	// Achievement finish fires before the req gates (TS order).
	if plan.FinishAch != "" {
		m11FinishAchievement(c, plan.FinishAch)
	}
	if plan.BlockMsg != "" {
		m6Notify(c, plan.BlockMsg)
		return
	}
	if plan.ConsumeItem {
		need := d.ReqItemCount
		if need < 1 {
			need = 1
		}
		m6RemoveItem(c.Username, d.ReqItem, need)
		if plan.Crumble {
			m6Notify(c, "misc:DOOR_KEY_CRUMBLES")
		}
	}
	m7Teleport(c, d.DestX, d.DestY)
}
