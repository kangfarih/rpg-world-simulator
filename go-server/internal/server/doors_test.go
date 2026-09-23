package server

import (
	"testing"

	"rpg-world-server/internal/entity"
)

// Door gate matrix (handler.ts handleDoor parity): exact notify strings,
// order (level -> quest -> achievement -> req gates -> key), and the
// achievement-fires-before-later-blocks rule.
func doorForGate() *entity.Door {
	return &entity.Door{ID: 1, X: 1, Y: 1, DestX: 9, DestY: 9}
}

func TestPlanDoorLevelGates(t *testing.T) {
	// Combat-level gate.
	d := doorForGate()
	d.Level = 45
	if p := planDoor(d, doorCtx{CombatLevel: 50}); p.BlockMsg != "" {
		t.Fatalf("pass = %+v", p)
	}
	if p := planDoor(d, doorCtx{CombatLevel: 10}); p.BlockMsg != "misc:NO_COMBAT_DOOR;level=45" {
		t.Fatalf("combat gate = %+v", p)
	}
	// Named-skill gate.
	d.Skill = "Mining"
	if p := planDoor(d, doorCtx{CombatLevel: 99, HasSkill: true, SkillLevel: 50}); p.BlockMsg != "" {
		t.Fatalf("skill pass = %+v", p)
	}
	if p := planDoor(d, doorCtx{CombatLevel: 99, HasSkill: true, SkillLevel: 10}); p.BlockMsg != "misc:NO_SKILL_DOOR;skill=Mining;level=45" {
		t.Fatalf("skill gate = %+v", p)
	}
	if p := planDoor(d, doorCtx{CombatLevel: 99}); p.BlockMsg != "misc:NO_SKILL_DOOR;skill=Mining;level=45" {
		t.Fatalf("unknown skill must block = %+v", p)
	}
}

func TestPlanDoorQuestGate(t *testing.T) {
	d := doorForGate()
	d.Quest = "seaactivities"
	d.Stage = 4
	// Stage short: CANNOT_PASS_DOOR, later gates skipped.
	if p := planDoor(d, doorCtx{QuestKnown: true, QuestStage: 2}); p.BlockMsg != "misc:CANNOT_PASS_DOOR" {
		t.Fatalf("quest stage gate = %+v", p)
	}
	// Unknown quest: safe block.
	if p := planDoor(d, doorCtx{}); p.BlockMsg != "misc:CANNOT_PASS_DOOR" {
		t.Fatalf("unknown quest = %+v", p)
	}
	// Stage met: pass (quest doors skip the remaining gates, TS return).
	if p := planDoor(d, doorCtx{QuestKnown: true, QuestStage: 4}); p.BlockMsg != "" || p.FinishAch != "" {
		t.Fatalf("quest pass = %+v", p)
	}
	// Stage overflow passes too (TS `stage < door.stage` comparison).
	if p := planDoor(d, doorCtx{QuestKnown: true, QuestStage: 9}); p.BlockMsg != "" {
		t.Fatalf("quest overflow = %+v", p)
	}
}

func TestPlanDoorAchReqItemChain(t *testing.T) {
	d := doorForGate()
	d.Achievement = "ahiddenpath"
	d.ReqAchievement = "roadtohell"
	d.ReqQuest = "ancientlands"
	d.ReqItem = "candykey"
	d.ReqItemCount = 2

	full := doorCtx{
		ReqAchKnown: true, ReqAchFinished: true, ReqAchName: "Road To Hell",
		ReqQuestKnown: true, ReqQuestFinished: true, ReqQuestName: "Ancient Lands",
		InvCount: 2,
	}
	p := planDoor(d, full)
	if p.BlockMsg != "" || p.FinishAch != "ahiddenpath" || !p.ConsumeItem || !p.Crumble {
		t.Fatalf("full pass = %+v", p)
	}

	// reqAchievement failure: achievement still finished (TS order), then block.
	p = planDoor(d, doorCtx{ReqAchKnown: true, ReqAchName: "Road To Hell"})
	if p.FinishAch != "ahiddenpath" {
		t.Fatalf("blocked plan must still finish ach = %+v", p)
	}
	if p.BlockMsg != "misc:NO_ACHIEVEMENT_DOOR;achievement=Road To Hell" {
		t.Fatalf("reqAchievement = %+v", p)
	}

	// reqQuest failure string carries the quest display name.
	ctx := full
	ctx.ReqQuestFinished = false
	if p := planDoor(d, ctx); p.BlockMsg != "misc:NO_QUEST_DOOR;quest=Ancient Lands" {
		t.Fatalf("reqQuest = %+v", p)
	}

	// Key item: short count blocks, exact count passes + consumes.
	ctx = full
	ctx.InvCount = 1
	if p := planDoor(d, ctx); p.BlockMsg != "misc:NO_KEY_DOOR" {
		t.Fatalf("key short = %+v", p)
	}
	// reqItemCount 0 defaults to 1 (TS `door.reqItemCount || 1`).
	d2 := doorForGate()
	d2.ReqItem = "candykey"
	if p := planDoor(d2, doorCtx{InvCount: 1}); p.BlockMsg != "" || !p.ConsumeItem {
		t.Fatalf("default count = %+v", p)
	}
	if p := planDoor(d2, doorCtx{InvCount: 0}); p.BlockMsg != "misc:NO_KEY_DOOR" {
		t.Fatalf("no key = %+v", p)
	}
}

func TestDoorSkillID(t *testing.T) {
	if id, ok := doorSkillID("Mining"); !ok || id != SkillMining {
		t.Fatalf("Mining = %d,%v", id, ok)
	}
	if _, ok := doorSkillID("mining"); !ok {
		t.Fatal("skill names must match case-insensitively")
	}
	if _, ok := doorSkillID("Nope"); ok {
		t.Fatal("unknown skill must not resolve")
	}
}

func TestPlateauTrackDefault(t *testing.T) {
	if got := plateauGet("nobody"); got != 0 {
		t.Fatalf("untracked plateau = %d, want 0", got)
	}
	c := lootTestConn("p-plat", "plat-user", 100, 96)
	plateauTrack(c)
	plateauForget("p-plat")
	if got := plateauGet("p-plat"); got != 0 {
		t.Fatalf("forgotten plateau = %d, want 0", got)
	}
}
