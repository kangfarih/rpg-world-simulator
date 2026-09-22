// M11 slice: quests + achievements engine.
//
// Thin adapter over internal/player/quest (behavior-frozen move, task E7):
// all quest/achievement logic lives in the quest package operating on the
// Conn/Store/Bus/Abilities/DB seams below. This file only wires those seams
// to the root globals (players map, send, pstates, m5/m6 helpers, ability
// grants, dbConn) and keeps the entry points main.go/m5.go/m6.go/m9.go/m13.go
// and world_wire.go call — with UNCHANGED signatures — delegating to the
// quest package. Quest data, gates, frames and the SQLite schema are
// identical.
//
// Stayed in root (cannot move cleanly): m6InvCount/m6RemoveItem (m6-owned
// inventory helpers over pstates/pstateMu, also called by m12.go/m13.go
// directly), playerConn transport (send/connByInstance), m5 XP/item helpers,
// m6Notify, abGrantAbility, dbConn, and the testMode gate.
package main

import (
	gnet "rpg-world-server/internal/net"
	"rpg-world-server/internal/player/quest"
	worldcore "rpg-world-server/internal/world"
)

// Type aliases so existing names keep resolving to the moved types.
type (
	m11Quest          = quest.Quest
	m11AchievementRaw = quest.AchievementRaw
	m11AchDef         = quest.AchDef
	questRaw          = quest.Raw
	questStageData    = quest.StageData
	questItem         = quest.Item
	questPointer      = quest.Pointer
	questPopup        = quest.Popup
	questSkillReward  = quest.SkillReward
	questData         = quest.QuestData
	achievementData   = quest.AchievementData
)

// Opcode aliases (quest/achievement frame values, unchanged).
const (
	QuestBatch          = quest.QuestBatch
	QuestProgress       = quest.QuestProgress
	QuestFinish         = quest.QuestFinish
	QuestStart          = quest.QuestStart
	AchievementBatch    = quest.AchievementBatch
	AchievementProgress = quest.AchievementProgress
	NotificationPopup   = quest.NotificationPopup
)

// Registry mirrors for direct map readers (m13/world_wire parity). The
// authoritative maps live in the quest package; these reference the same
// underlying maps so external call sites compile untouched.
var (
	m11Q = quest.Quests
	m11A = quest.Achs
)

// m11PlayerState wraps the canonical quest.PlayerState so the lowercase
// accessors used by m13.go/world_wire.go (st.quest, st.isFinished,
// st.isStarted) keep compiling; the embedded pointer promotes the shared
// fields (Username/Quests/Achs/TalkNPC/TalkIndex/PendingStart).
type m11PlayerState struct {
	*quest.PlayerState
}

// m11QuestState wraps the canonical quest.QuestState; Stage/SubStage
// promote through the embedded pointer.
type m11QuestState struct {
	*quest.QuestState
}

func (st *m11PlayerState) quest(key string) *m11QuestState {
	return &m11QuestState{st.PlayerState.Quest(key)}
}

func (st *m11PlayerState) isFinished(key string) bool {
	return st.PlayerState.IsFinished(key)
}

func (st *m11PlayerState) isStarted(key string) bool {
	return st.PlayerState.IsStarted(key)
}

// ---------------------------------------------------------------------------
// quest.Conn seam (*playerConn satisfies it, so identity is preserved).
// ---------------------------------------------------------------------------

// m11qc boxes a root conn as a quest.Conn, preserving nil (a nil *playerConn
// becomes a nil interface so the quest package's c == nil guards fire).
func m11qc(c *playerConn) quest.Conn {
	if c == nil {
		return nil
	}
	return c
}

// m11conn unwraps a quest.Conn back to the root conn (XP/ability side
// effects need it); falls back to an instance lookup for foreign impls.
func m11conn(c quest.Conn) *playerConn {
	if pc, ok := c.(*playerConn); ok {
		return pc
	}
	if c == nil {
		return nil
	}
	pc, _ := worldcore.Find[*playerConn](c.InstanceID())
	return pc
}

// ---------------------------------------------------------------------------
// quest.Store / quest.Bus / quest.Abilities seams.
// ---------------------------------------------------------------------------

type m11store struct{}

func (m11store) InventoryLen(username string) int {
	st := m5StateFor(username)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	return len(st.Inv)
}

func (m11store) CountItem(username, itemKey string) int {
	return m6InvCount(username, itemKey)
}

func (m11store) AddItem(username, itemKey string, count int) int {
	return m5AddItem(username, itemKey, count)
}

func (m11store) RemoveItem(username, itemKey string, count int) {
	m6RemoveItem(username, itemKey, count)
}

func (m11store) SkillLevel(username string, skill int) (int, bool) {
	st := m5StateFor(username)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	if s := st.Skills[skill]; s != nil {
		return s.Level, true
	}
	return 0, false
}

func (m11store) AddXP(c quest.Conn, username string, skill, amount int) int {
	return m5AddXP(m11conn(c), username, skill, amount)
}

func (m11store) MarkDirty(username string) { markDirty(username) }

type m11bus struct{}

func (m11bus) SendTo(instance string, frames ...[]any) {
	c, _ := worldcore.Find[*playerConn](instance)
	if c == nil {
		return
	}
	_ = gnet.Send(c.Conn, frames...)
}

func (m11bus) Notify(instance string, message string) {
	c, _ := worldcore.Find[*playerConn](instance)
	if c == nil {
		return
	}
	m6Notify(c, message)
}

type m11abilities struct{}

func (m11abilities) GrantAbility(c quest.Conn, username, key string, level int) {
	abGrantAbility(m11conn(c), username, key, level)
}

// m11deps wires the quest seams to the root globals (a nil dbConn becomes a
// nil quest.DB so persistence no-ops, dbConn == nil parity).
func m11deps() quest.Deps {
	var db quest.DB
	if dbConn != nil {
		db = dbConn
	}
	return quest.Deps{Store: m11store{}, Bus: m11bus{}, Abilities: m11abilities{}, DB: db}
}

// ---------------------------------------------------------------------------
// Entry points (signatures UNCHANGED; main.go/m5.go/m6.go/m9.go/m13.go and
// world_wire.go call sites compile as-is). The testMode gate stays here —
// it reads a root global.
// ---------------------------------------------------------------------------

func m11StateFor(username string) *m11PlayerState {
	return &m11PlayerState{quest.StateFor(username)}
}

// m11ForgetSession drops in-memory quest state (TESTMAP harness isolation).
func m11ForgetSession(username string) {
	quest.ForgetSession(username)
}

// m11SendQuestProgress emits Quest Progress1 (quests.ts handleProgress).
func m11SendQuestProgress(c *playerConn, key string, q *m11QuestState) {
	quest.SendQuestProgress(m11qc(c), m11deps(), key, q.QuestState)
}

// m11SendAchievementProgress emits Achievement Progress1 (name/description
// ride along for client Task creation — setAchievement contract).
func m11SendAchievementProgress(c *playerConn, key string, stage int) {
	quest.SendAchievementProgress(m11qc(c), m11deps(), key, stage)
}

// m11SendPointer mirrors player.pointer: Remove first, then Location.
func m11SendPointer(c *playerConn, p *questPointer) {
	quest.SendPointer(m11qc(c), m11deps(), p)
}

// m11SendPopup mirrors player.popup: Notification Popup3 {title,message,colour}.
func m11SendPopup(c *playerConn, title, message, colour string) {
	quest.SendPopup(m11qc(c), m11deps(), title, message, colour)
}

// m11LoginBatches builds the Quest Batch + Achievement Batch frames queued
// after the Welcome extras (handler.handleQuests/handleAchievements on
// quests.onLoaded). Batched questData carries the definition fields.
func m11LoginBatches(username string) [][]any {
	return quest.LoginBatches(username)
}

// m11LoginPointer sends the current stage's quest pointer after Ready
// (Tutorial.loaded → setStage(0,0,false) → pointerCallback; for other quests
// Node only re-points on stage changes, we mirror that by pointing only when
// the tutorial is unfinished).
func m11LoginPointer(c *playerConn) {
	quest.LoginPointer(m11qc(c), m11deps())
}

func m11StageDef(q *m11Quest, stage int) questStageData {
	return quest.StageDef(q, stage)
}

// m11NpcOf returns the stage's npc key honoring the `noc` typo.
func m11NpcOf(st questStageData) string {
	return quest.NpcOf(st)
}

// m11SetStage applies the new stage and emits Progress/pointer/popup side
// effects. progress=false mirrors setStage(..., false) for DB loads.
func m11SetStage(c *playerConn, st *m11PlayerState, key string, stage, subStage int, progress bool) {
	quest.SetStage(m11qc(c), m11deps(), st.PlayerState, key, stage, subStage, progress)
}

// m11GiveRewards grants stage itemRewards (givePlayerRewards): NO_SPACE
// notify when the inventory cannot fit every entry, else add each item and
// emit Container Add.
func m11GiveRewards(c *playerConn, st *m11PlayerState, rewards []questItem) bool {
	return quest.GiveRewards(m11qc(c), m11deps(), st.PlayerState, rewards)
}

// m11GrantExperience ports givePlayerExperience: skillRewards by name.
func m11GrantExperience(c *playerConn, st *m11PlayerState, rewards []questSkillReward) {
	quest.GrantExperience(m11qc(c), m11deps(), st.PlayerState, rewards)
}

// m11HasAllItems checks inventory counts (hasAllItems).
func m11HasAllItems(username string, items []questItem) bool {
	return quest.HasAllItems(m11deps(), username, items)
}

// m11TakeItems removes required items from the inventory (removeItem loop).
func m11TakeItems(st *m11PlayerState, items []questItem) {
	quest.TakeItems(m11deps(), st.PlayerState, items)
}

// m11Progress advances one stage (quest.ts progress).
func m11Progress(c *playerConn, st *m11PlayerState, key string) {
	quest.Progress(m11qc(c), m11deps(), st.PlayerState, key)
}

// m11ProgressSub advances the substage (quest.ts progress(true)).
func m11ProgressSub(c *playerConn, st *m11PlayerState, key string) {
	quest.ProgressSub(m11qc(c), m11deps(), st.PlayerState, key)
}

// m11Talk routes an NPC interaction through quests then achievements
// (handler.handleTalkToNPC order). Returns true when the quest/achievement
// consumed the interaction (caller skips the default dialogue).
func m11Talk(c *playerConn, npcKey string) bool {
	return quest.Talk(m11qc(c), m11deps(), npcKey)
}

// m11RequirementsOK mirrors hasRequirements: skill levels + finished quests.
func m11RequirementsOK(st *m11PlayerState, def *m11Quest) bool {
	return quest.RequirementsOK(m11deps(), st.PlayerState, def)
}

// m11HandleQuestTalk ports handleTalk + getNPCDialogue: dialogue selection
// (stage text / hasItemText / completedText by search order), progression on
// dialogue end, item requirement consumption and reward grants.
func m11HandleQuestTalk(c *playerConn, st *m11PlayerState, key, npcKey string) bool {
	return quest.HandleQuestTalk(m11qc(c), m11deps(), st.PlayerState, key, npcKey)
}

// m11HandleAchTalk ports achievement.handleTalk: hidden/started dialogue,
// progress on dialogue end (discover stage), item requirements consumed.
func m11HandleAchTalk(c *playerConn, st *m11PlayerState, key string) bool {
	return quest.HandleAchTalk(m11qc(c), m11deps(), st.PlayerState, key)
}

// m11Kill fires on mob death credited to the killer.
func m11Kill(c *playerConn, mobKey string) {
	quest.Kill(m11qc(c), m11deps(), mobKey)
}

func m11AchHasMob(def *m11AchDef, mobKey string) bool {
	return quest.AchHasMob(def, mobKey)
}

// m11Resource fires when a gather exhausts. skill is the Go skill name
// (lumberjacking/mining/fishing/foraging), resourceKey the resource def key
// (quest.ts handleResource: match stage.<tree|fish|rock> then count down the
// substage; no count = single-stage progress).
func m11Resource(c *playerConn, skill, resourceKey string) {
	quest.Resource(m11qc(c), m11deps(), skill, resourceKey)
}

// m11HeroDamageMult multiplies hero damage vs engine mobs when M11_HERODMG is
// set (debug accelerator, mirrors M9_MOBDMG) — keeps 140-HP e2e mobs in a
// few-swing kill range without touching XP accounting.
func m11HeroDamageMult() float64 {
	return quest.HeroDamageMult()
}

// m11HandleAccept processes the C Quest {key} frame: the pending start
// interface accepted → progress past stage 0 (handlePrompt setStage+1).
func m11HandleAccept(c *playerConn, data []byte) {
	quest.HandleAccept(m11qc(c), m11deps(), data)
}

// m11DropGated reports whether a drop entry's quest gate passes. status
// semantics (mob.fullfillsQuest): empty status = require finished;
// notstarted = require not started; started = started && not finished.
// achievements gate the same way on stage >= stageCount.
func m11DropGated(username, questKey, achievementKey, status string) bool {
	return quest.DropGated(username, questKey, achievementKey, status)
}

// m11AchProgress advances an achievement one stage and fires popups/rewards
// at the finish stage (achievement.setStage).
func m11AchProgress(c *playerConn, st *m11PlayerState, key string) {
	quest.AchProgress(m11qc(c), m11deps(), st.PlayerState, key)
}

// m11EnsureTables creates the quest/achievement tables (M5 DDL order).
func m11EnsureTables() {
	quest.EnsureTables(m11deps())
}

// m11PersistQuests writes the quest rows for one player (called from the
// disconnect/flush path).
func m11PersistQuests(username string) {
	quest.PersistQuests(m11deps(), username)
}

// m11LoadQuests restores quest/achievement rows into the in-memory state
// (called before the login batches are built).
func m11LoadQuests(username string) {
	quest.LoadQuests(m11deps(), username)
}

// m11HandleTest processes TESTMAP debug ops: stage/substage injection,
// achievement stage injection, and state echoes for the e2e harness.
func m11HandleTest(c *playerConn, data []byte) {
	if !testMode {
		return
	}
	quest.HandleTest(m11qc(c), m11deps(), data)
}

// ---------------------------------------------------------------------------
// Small helpers shared with the m6 slice (m6-owned inventory helpers over
// pstates/pstateMu; also called by m12.go/m13.go directly — intentionally
// left in root).
// ---------------------------------------------------------------------------

// m6InvCount counts inventory copies of a key (inventory.getIndex parity).
func m6InvCount(username, itemKey string) int {
	st := m5StateFor(username)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	n := 0
	for _, s := range st.Inv {
		if s.Key == itemKey {
			n += s.Count
		}
	}
	return n
}

// m6RemoveItem removes count copies of a key across stacks (inventory.
// removeItem parity): decrement stacks, then compact zeros. Container Remove
// frames ride the harness ClientContainerSync drain instead — matches Node,
// where quest item consumption never sends container frames.
func m6RemoveItem(username, itemKey string, count int) {
	st := m5StateFor(username)
	pstateMu.Lock()
	for i := 0; i < len(st.Inv) && count > 0; i++ {
		if st.Inv[i].Key != itemKey {
			continue
		}
		take := st.Inv[i].Count
		if take > count {
			take = count
		}
		st.Inv[i].Count -= take
		count -= take
	}
	out := st.Inv[:0]
	for _, s := range st.Inv {
		if s.Count > 0 {
			out = append(out, s)
		}
	}
	st.Inv = out
	pstateMu.Unlock()
}
