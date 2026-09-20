// M11 slice: quests + achievements engine.
//
// Quests mirror packages/server/src/game/entity/character/player/quest/quest.ts
// (849 lines) driven by data/quests/*.json (21 files); achievements mirror
// achievement/achievement.ts driven by data/achievements.json. The client
// contract is connection.ts handleQuest/handleAchievement: [23] quest frames
// with opcodes Batch0 (login batch, QuestData + name/description/rewards/
// stageCount), Progress1 ([key,stage,subStage]), Finish2 (unused), Start3
// (quest interface prompt -> C Quest {key} accept -> handlePrompt progress).
// Achievement frames [24] mirror Batch0 + Progress1 (Progress carries
// name/description for client Task creation). Quest pointers ride Packet 36
// (Opcodes.Pointer.Location 0 / Remove 3) and popups ride Notification Popup
// (25,3). quest_bases/*.json are also loaded for parity of the registry —
// they are authoring drafts with no TS impl and no index wiring.
//
// Stage lifecycle per quest.ts: stage 0 + first progress → pendingStart
// (Start packet; tutorial skips prompts via noPrompts) → talk stages fire
// dialogue then progress on dialogue end (itemRequirements swap the text to
// hasItemText and consume items on the final click) → kill stages advance
// subStage per mob and stage at mobCountRequirement → resource stages match
// the resource type + key with a subStage count → itemRewards are granted on
// entry with a NO_SPACE notify when the inventory is full. isFinished is
// stage >= stageCount (Node's `>=` allows overflow stages to end the quest).
//
// Triggers: m6HandleNPCTarget calls m11Talk after the plain-NPC talk branch;
// m9KillMob calls m11Kill; hitResource calls m11Resource on exhaust;
// m5GetDrops/m5RollEntry consult m11DropGated to activate `quest`-gated
// entries (skeleton → skeletonkingtalisman only while codersglitch is
// started). Login queues the Quest/Achievement Batch after Welcome extras;
// disconnect persists stage state to SQLite (quests + achievements tables).
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// Packet shapes (common/network/impl/quest.ts + achievement.ts).
// ---------------------------------------------------------------------------

// Opcodes.Quest (opcodes.ts): Batch0 Progress1 Finish2 Start3.
const (
	QuestBatch    = 0
	QuestProgress = 1
	QuestFinish   = 2
	QuestStart    = 3
)

// Opcodes.Achievement: Batch0 Progress1.
const (
	AchievementBatch    = 0
	AchievementProgress = 1
)

// Opcodes.Pointer (Location0/Entity1/Remove3) + Notification Popup3 live in
// m8.go's shared opcode block — reused here for quest pointers/popups.
const NotificationPopup = 3

// questStageData mirrors RawStage (impl/quest.ts) for the fields the engine
// acts on; `noc` is codersglitch's misspelled npc key (quest.ts reads
// stage.npc, so the typo stage is unreachable there — we honor both).
type questStageData struct {
	Task                string             `json:"task"`
	NPC                 string             `json:"npc"`
	Noc                 string             `json:"noc"`
	Mob                 []string           `json:"mob"`
	MobCountRequirement int                `json:"mobCountRequirement"`
	ItemRequirements    []questItem        `json:"itemRequirements"`
	ItemRewards         []questItem        `json:"itemRewards"`
	Text                []string           `json:"text"`
	CompletedText       []string           `json:"completedText"`
	HasItemText         []string           `json:"hasItemText"`
	Pointer             *questPointer      `json:"pointer"`
	Popup               *questPopup        `json:"popup"`
	Ability             string             `json:"ability"`
	AbilityLevel        int                `json:"abilityLevel"`
	Tree                string             `json:"tree"`
	TreeCount           int                `json:"treeCount"`
	Fish                string             `json:"fish"`
	FishCount           int                `json:"fishCount"`
	Rock                string             `json:"rock"`
	RockCount           int                `json:"rockCount"`
	SkillRewards        []questSkillReward `json:"skillRewards"`
	SubStages           []questStageData   `json:"subStages"`
	Timer               int                `json:"timer"`
}

type questItem struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

type questPointer struct {
	Type int `json:"type"`
	X    int `json:"x"`
	Y    int `json:"y"`
}

type questPopup struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

type questSkillReward struct {
	Key        string `json:"key"`
	Experience int    `json:"experience"`
}

// questRaw mirrors RawQuest.
type questRaw struct {
	Name              string                    `json:"name"`
	Description       string                    `json:"description"`
	Rewards           []string                  `json:"rewards"`
	SkillRequirements map[string]int            `json:"skillRequirements"`
	QuestRequirements []string                  `json:"questRequirements"`
	Stages            map[string]questStageData `json:"stages"`
}

// m11Quest is one immutable quest definition shared by all players.
type m11Quest struct {
	Key        string
	Raw        questRaw
	StageCount int
	StageOrder []int // sorted stage indices
	NoPrompts  bool  // tutorial: skip the Start prompt interface
	NPCs       map[string]bool
}

// m11AchievementRaw mirrors RawAchievement.
type m11AchievementRaw struct {
	Name               string   `json:"name"`
	Description        string   `json:"description"`
	Region             string   `json:"region"`
	Hidden             bool     `json:"hidden"`
	Secret             bool     `json:"secret"`
	NPC                string   `json:"npc"`
	DialogueHidden     []string `json:"dialogueHidden"`
	DialogueStarted    []string `json:"dialogueStarted"`
	Mob                []string `json:"mob"`
	MobCount           int      `json:"mobCount"`
	Item               string   `json:"item"`
	ItemCount          int      `json:"itemCount"`
	RewardItem         string   `json:"rewardItem"`
	RewardItemCount    int      `json:"rewardItemCount"`
	RewardSkill        string   `json:"rewardSkill"`
	RewardExperience   int      `json:"rewardExperience"`
	RewardAbility      string   `json:"rewardAbility"`
	RewardAbilityLevel int      `json:"rewardAbilityLevel"`
}

// m11AchDef is one immutable achievement definition.
type m11AchDef struct {
	Key        string
	Raw        m11AchievementRaw
	StageCount int // mobCount+1 (discovery stage) else 1
}

// ---------------------------------------------------------------------------
// Data loading (quests/ dir + achievements.json; quest_bases/ for parity).
// ---------------------------------------------------------------------------

var (
	m11Once   sync.Once
	m11OK     bool
	m11Q      = map[string]*m11Quest{}
	m11A      = map[string]*m11AchDef{}
	m11QOrder []string // sorted keys (Node's impl/index.ts order has no client
	m11AOrder []string // observable effect — Task ids are positional only)
)

// m11DataDir resolves a data DIRECTORY: resourceDataPath is file-oriented
// (appends .json), so mirror its env/relative-path search for dirs.
func m11DataDir(name string) string {
	if p := os.Getenv("RES_" + name); p != "" {
		return p
	}
	for _, rel := range []string{
		filepath.Join("..", "packages", "server", "data", name),
		filepath.Join("..", "..", "packages", "server", "data", name),
	} {
		if st, err := os.Stat(rel); err == nil && st.IsDir() {
			return rel
		}
	}
	return filepath.Join("..", "packages", "server", "data", name)
}

func m11Load() {
	m11Once.Do(func() {
		// Quests: data/quests/*.json (TS imports 21 files explicitly; we read
		// the directory and sort for determinism).
		entries, err := os.ReadDir(m11DataDir("quests"))
		if err != nil {
			log.Printf("m11: read quests dir: %v (quests disabled)", err)
			return
		}
		var files []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
				files = append(files, e.Name())
			}
		}
		sort.Strings(files)
		questDir := m11DataDir("quests")
		for _, f := range files {
			key := strings.TrimSuffix(f, ".json")
			raw, err := os.ReadFile(filepath.Join(questDir, f))
			if err != nil {
				continue
			}
			var q questRaw
			if err := json.Unmarshal(raw, &q); err != nil {
				log.Printf("m11: parse %s: %v", f, err)
				continue
			}
			quest := &m11Quest{Key: key, Raw: q, NPCs: map[string]bool{}}
			quest.StageCount = len(q.Stages)
			// TS impl subclasses override noPrompts (impl/tutorial.ts:
			// `protected override noPrompts = true`) — the tutorial starts
			// without the accept interface. JSON carries no such flag.
			if key == "tutorial" {
				quest.NoPrompts = true
			}
			for id := range q.Stages {
				n, ok := atoiOk(id)
				if !ok {
					continue
				}
				quest.StageOrder = append(quest.StageOrder, n)
				st := q.Stages[id]
				npc := st.NPC
				if npc == "" {
					npc = st.Noc
				}
				if npc != "" {
					quest.NPCs[npc] = true
				}
				for _, ss := range st.SubStages {
					if ss.NPC != "" {
						quest.NPCs[ss.NPC] = true
					}
				}
			}
			sort.Ints(quest.StageOrder)
			if key == "tutorial" {
				quest.NoPrompts = true // Tutorial.noPrompts override
			}
			m11Q[key] = quest
		}
		// Quest bases: authoring drafts, registry parity only.
		if entries, err := os.ReadDir(m11DataDir("quest_bases")); err == nil {
			n := 0
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
					n++
				}
			}
			log.Printf("m11: quest_bases=%d (loaded, no impl — TS parity)", n)
		}
		// Achievements.
		raw, err := os.ReadFile(resourceDataPath("achievements"))
		if err != nil {
			log.Printf("m11: read achievements.json: %v (achievements disabled)", err)
			m11OK = len(m11Q) > 0
			log.Printf("m11: quests=%d achievements=0", len(m11Q))
			return
		}
		var achs map[string]m11AchievementRaw
		if err := json.Unmarshal(raw, &achs); err != nil {
			log.Printf("m11: parse achievements.json: %v", err)
			return
		}
		for k, v := range achs {
			sc := 1
			if v.MobCount > 0 {
				sc = v.MobCount + 1
			}
			m11A[k] = &m11AchDef{Key: k, Raw: v, StageCount: sc}
		}
		for k := range m11Q {
			m11QOrder = append(m11QOrder, k)
		}
		sort.Strings(m11QOrder)
		for k := range m11A {
			m11AOrder = append(m11AOrder, k)
		}
		sort.Strings(m11AOrder)
		m11OK = true
		log.Printf("m11: quests=%d achievements=%d", len(m11Q), len(m11A))
	})
}

func atoiOk(s string) (int, bool) {
	n := 0
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}

// ---------------------------------------------------------------------------
// Per-player state.
// ---------------------------------------------------------------------------

type m11QuestState struct {
	Stage    int
	SubStage int
}

type m11PlayerState struct {
	Username string
	Quests   map[string]*m11QuestState
	Achs     map[string]int
	// talk tracking per quest (mirrors player.talkIndex + npcTalk reset).
	TalkNPC   string
	TalkIndex int
	// Start-prompt queue (pendingStart): key = true while the Start packet is
	// outstanding; accept (C Quest) progresses the stage.
	PendingStart map[string]bool
}

var (
	m11Mu      sync.Mutex
	m11Players = map[string]*m11PlayerState{}
)

func m11StateFor(username string) *m11PlayerState {
	m11Load()
	st, ok := m11Players[username]
	if !ok {
		st = &m11PlayerState{
			Username:     username,
			Quests:       map[string]*m11QuestState{},
			Achs:         map[string]int{},
			PendingStart: map[string]bool{},
		}
		m11Players[username] = st
	}
	return st
}

// m11ForgetSession drops in-memory quest state (TESTMAP harness isolation).
func m11ForgetSession(username string) {
	m11Mu.Lock()
	delete(m11Players, username)
	m11Mu.Unlock()
}

func (st *m11PlayerState) quest(key string) *m11QuestState {
	q, ok := st.Quests[key]
	if !ok {
		q = &m11QuestState{}
		st.Quests[key] = q
	}
	return q
}

// ---------------------------------------------------------------------------
// Packets.
// ---------------------------------------------------------------------------

// questData mirrors QuestData (impl/quest.ts): batch adds the definition
// fields the client Task needs.
type questData struct {
	Key               string         `json:"key"`
	Stage             int            `json:"stage"`
	SubStage          int            `json:"subStage"`
	Name              *string        `json:"name,omitempty"`
	Description       *string        `json:"description,omitempty"`
	Rewards           []string       `json:"rewards,omitempty"`
	SkillRequirements map[string]int `json:"skillRequirements,omitempty"`
	QuestRequirements []string       `json:"questRequirements,omitempty"`
	StageCount        *int           `json:"stageCount,omitempty"`
}

// achievementData mirrors AchievementData.
type achievementData struct {
	Key         string  `json:"key"`
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Region      *string `json:"region,omitempty"`
	Stage       int     `json:"stage"`
	StageCount  *int    `json:"stageCount,omitempty"`
	Secret      *bool   `json:"secret,omitempty"`
}

func strp(s string) *string { return &s }
func intp2(v int) *int      { return &v }

// m11SendQuestProgress emits Quest Progress1 (quests.ts handleProgress).
func m11SendQuestProgress(c *playerConn, key string, q *m11QuestState) {
	_ = send(c.conn, pktOp(PacketQuest, QuestProgress, questData{
		Key: key, Stage: q.Stage, SubStage: q.SubStage,
	}))
}

// m11SendAchievementProgress emits Achievement Progress1 (name/description
// ride along for client Task creation — setAchievement contract).
func m11SendAchievementProgress(c *playerConn, key string, stage int) {
	def := m11A[key]
	name, desc := key, ""
	if def != nil {
		name, desc = def.Raw.Name, def.Raw.Description
	}
	_ = send(c.conn, pktOp(PacketAchievement, AchievementProgress, achievementData{
		Key: key, Stage: stage, Name: strp(name), Description: strp(desc),
	}))
}

// m11SendPointer mirrors player.pointer: Remove first, then Location.
func m11SendPointer(c *playerConn, p *questPointer) {
	_ = send(c.conn, pktOp(PacketPointer, PointerRemove, map[string]any{}))
	if p == nil || p.Type != PointerLocation {
		return
	}
	_ = send(c.conn, pktOp(PacketPointer, PointerLocation, map[string]any{
		"type": p.Type, "x": p.X, "y": p.Y,
	}))
}

// m11SendPopup mirrors player.popup: Notification Popup3 {title,message,colour}.
func m11SendPopup(c *playerConn, title, message, colour string) {
	_ = send(c.conn, pktOp(PacketNotification, NotificationPopup, notificationPacketData{
		Title: strp(title), Message: message, Colour: strp(colour),
	}))
}

// ---------------------------------------------------------------------------
// Login: batches + initial pointer (quests.ts load + tutorial.loaded).
// ---------------------------------------------------------------------------

// m11LoginBatches builds the Quest Batch + Achievement Batch frames queued
// after the Welcome extras (handler.handleQuests/handleAchievements on
// quests.onLoaded). Batched questData carries the definition fields.
func m11LoginBatches(username string) [][]any {
	m11Load()
	if !m11OK {
		return nil
	}
	st := m11StateFor(username)
	qs := make([]questData, 0, len(m11QOrder))
	for _, key := range m11QOrder {
		def := m11Q[key]
		q := st.quest(key)
		qs = append(qs, questData{
			Key: key, Stage: q.Stage, SubStage: q.SubStage,
			Name: strp(def.Raw.Name), Description: strp(def.Raw.Description),
			Rewards: def.Raw.Rewards, SkillRequirements: def.Raw.SkillRequirements,
			QuestRequirements: def.Raw.QuestRequirements, StageCount: intp2(def.StageCount),
		})
	}
	achs := make([]achievementData, 0, len(m11AOrder))
	for _, key := range m11AOrder {
		def := m11A[key]
		secret := def.Raw.Secret
		d := achievementData{Key: key, Stage: st.Achs[key], StageCount: intp2(def.StageCount)}
		if secret {
			d.Secret = &secret
		} else {
			d.Name = strp(def.Raw.Name)
			d.Description = strp(def.Raw.Description)
			if def.Raw.Region != "" {
				d.Region = strp(def.Raw.Region)
			}
		}
		achs = append(achs, d)
	}
	return [][]any{
		pktOp(PacketQuest, QuestBatch, map[string]any{"quests": qs}),
		pktOp(PacketAchievement, AchievementBatch, map[string]any{"achievements": achs}),
	}
}

// m11LoginPointer sends the current stage's quest pointer after Ready
// (Tutorial.loaded → setStage(0,0,false) → pointerCallback; for other quests
// Node only re-points on stage changes, we mirror that by pointing only when
// the tutorial is unfinished).
func m11LoginPointer(c *playerConn) {
	m11Load()
	if !m11OK {
		return
	}
	st := m11StateFor(c.username)
	q := st.quest("tutorial")
	def := m11Q["tutorial"]
	if def == nil || q.Stage >= def.StageCount {
		return
	}
	if p := m11StageDef(def, q.Stage).Pointer; p != nil {
		m11SendPointer(c, p)
	}
}

// ---------------------------------------------------------------------------
// Stage helpers.
// ---------------------------------------------------------------------------

func m11StageDef(q *m11Quest, stage int) questStageData {
	if stage < 0 || stage >= q.StageCount {
		return questStageData{}
	}
	// Stages are serialized with numeric-string keys; find by index.
	for id, st := range q.Raw.Stages {
		if n, ok := atoiOk(id); ok && n == stage {
			return st
		}
	}
	return questStageData{}
}

// m11NpcOf returns the stage's npc key honoring the `noc` typo.
func m11NpcOf(st questStageData) string {
	if st.NPC != "" {
		return st.NPC
	}
	return st.Noc
}

func (st *m11PlayerState) isFinished(key string) bool {
	def := m11Q[key]
	if def == nil {
		return false
	}
	return st.quest(key).Stage >= def.StageCount
}

func (st *m11PlayerState) isStarted(key string) bool {
	return st.quest(key).Stage > 0
}

// ---------------------------------------------------------------------------
// Quest progression core (quest.ts setStage/progress).
// ---------------------------------------------------------------------------

// m11SetStage applies the new stage and emits Progress/pointer/popup side
// effects. progress=false mirrors setStage(..., false) for DB loads.
func m11SetStage(c *playerConn, st *m11PlayerState, key string, stage, subStage int, progress bool) {
	def := m11Q[key]
	if def == nil {
		return
	}
	q := st.quest(key)
	isProgress := q.Stage != stage

	// Popup of the stage we are LEAVING fires before the new stage is set.
	if progress && isProgress {
		if p := m11StageDef(def, q.Stage).Popup; p != nil {
			m11SendPopup(c, p.Title, p.Text, "#33cc33")
		}
		// Clear the pointer preemptively (quest.ts setStage).
		m11SendPointer(c, nil)
	}

	q.Stage, q.SubStage = stage, subStage
	if !progress || !isProgress {
		return
	}
	markDirty(st.Username)
	if c != nil {
		m11SendQuestProgress(c, key, q)
	}

	// Completion: Node sends no extra frame here (client colours the list);
	// drop the position-tracking state for parity hooks that read it.
	if q.Stage >= def.StageCount {
		log.Printf("m11: %s finished quest %s", st.Username, key)
		return
	}

	// Entering a new stage: fire its pointer (quest.ts setStage).
	if c != nil {
		if p := m11StageDef(def, q.Stage).Pointer; p != nil {
			m11SendPointer(c, p)
		}
	}
}

// m11GiveRewards grants stage itemRewards (givePlayerRewards): NO_SPACE
// notify when the inventory cannot fit every entry, else add each item and
// emit Container Add.
func m11GiveRewards(c *playerConn, st *m11PlayerState, rewards []questItem) bool {
	if len(rewards) == 0 {
		return false
	}
	free := ModulesInventorySize - len(m5StateFor(st.Username).Inv)
	if free < len(rewards) {
		if c != nil {
			m6Notify(c, "misc:NO_SPACE")
		}
		return false
	}
	for _, it := range rewards {
		idx := m5AddItem(st.Username, it.Key, it.Count)
		if c != nil {
			_ = send(c.conn, pktOp(PacketContainer, ContainerAdd, containerData{
				Type: ContainerTypeInventory,
				Slot: &slotData{Index: idx, Key: it.Key, Count: it.Count, Enchantments: map[string]any{}},
			}))
		}
		markDirty(st.Username)
	}
	return true
}

// m11GrantExperience ports givePlayerExperience: skillRewards by name.
func m11GrantExperience(c *playerConn, st *m11PlayerState, rewards []questSkillReward) {
	for _, r := range rewards {
		id, ok := map[string]int{
			"lumberjacking": SkillLumberjacking, "mining": SkillMining,
			"fishing": SkillFishing, "foraging": SkillForaging,
			"accuracy": SkillAccuracy, "archery": SkillArchery,
			"health": SkillHealth, "magic": SkillMagic, "strength": SkillStrength,
			"defense": SkillDefense,
		}[strings.ToLower(r.Key)]
		if !ok {
			log.Printf("m11: unknown skill reward %q (quest stage)", r.Key)
			continue
		}
		if c != nil {
			m5AddXP(c, st.Username, id, r.Experience)
		}
	}
}

// m11HasAllItems checks inventory counts (hasAllItems).
func m11HasAllItems(username string, items []questItem) bool {
	for _, it := range items {
		if m6InvCount(username, it.Key) < it.Count {
			return false
		}
	}
	return true
}

// m11TakeItems removes required items from the inventory (removeItem loop).
func m11TakeItems(st *m11PlayerState, items []questItem) {
	for _, it := range items {
		m6RemoveItem(st.Username, it.Key, it.Count)
	}
	markDirty(st.Username)
}

// m11Progress advances one stage (quest.ts progress).
func m11Progress(c *playerConn, st *m11PlayerState, key string) {
	def := m11Q[key]
	if def == nil {
		return
	}
	q := st.quest(key)
	// pendingStart: the first progression surfaces the Start interface
	// instead of moving (quest.ts setStage startCallback).
	if !def.NoPrompts && !st.isStarted(key) && q.Stage == 0 && !st.PendingStart[key] {
		st.PendingStart[key] = true
		if c != nil {
			_ = send(c.conn, pktOp(PacketQuest, QuestStart, questData{Key: key}))
		}
		log.Printf("m11: %s prompted to start %s", st.Username, key)
		return
	}
	st.PendingStart[key] = false
	m11SetStage(c, st, key, q.Stage+1, 0, true)
}

// m11ProgressSub advances the substage (quest.ts progress(true)).
func m11ProgressSub(c *playerConn, st *m11PlayerState, key string) {
	q := st.quest(key)
	m11SetStage(c, st, key, q.Stage, q.SubStage+1, true)
}

// ---------------------------------------------------------------------------
// Talk trigger (quest.ts handleTalk + getNPCDialogue).
// ---------------------------------------------------------------------------

// m11Talk routes an NPC interaction through quests then achievements
// (handler.handleTalkToNPC order). Returns true when the quest/achievement
// consumed the interaction (caller skips the default dialogue).
func m11Talk(c *playerConn, npcKey string) bool {
	m11Load()
	if !m11OK || c == nil {
		return false
	}
	st := m11StateFor(c.username)

	// Quests first: the unfinished quest whose NPC set contains the key and
	// whose requirements hold (quests.ts getQuestFromNPC — first match wins).
	for _, key := range m11QOrder {
		def := m11Q[key]
		if st.isFinished(key) || !def.NPCs[npcKey] || !m11RequirementsOK(st, def) {
			continue
		}
		if m11HandleQuestTalk(c, st, key, npcKey) {
			return true
		}
	}
	// Achievements: unfinished + npc match (getAchievementFromEntity).
	for _, key := range m11AOrder {
		def := m11A[key]
		if def.Raw.NPC != npcKey && !(def.Raw.NPC == "" && false) {
			continue
		}
		if st.Achs[key] >= def.StageCount {
			continue
		}
		if m11HandleAchTalk(c, st, key) {
			return true
		}
	}
	return false
}

// m11RequirementsOK mirrors hasRequirements: skill levels + finished quests.
func m11RequirementsOK(st *m11PlayerState, def *m11Quest) bool {
	for skill, level := range def.Raw.SkillRequirements {
		id, ok := map[string]int{
			"lumberjacking": SkillLumberjacking, "mining": SkillMining,
			"fishing": SkillFishing, "foraging": SkillForaging,
		}[strings.ToLower(skill)]
		if !ok {
			return false
		}
		if s := m5StateFor(st.Username).Skills[id]; s == nil || s.Level < level {
			return false
		}
	}
	for _, req := range def.Raw.QuestRequirements {
		if !st.isFinished(req) {
			return false
		}
	}
	return true
}

// m11HandleQuestTalk ports handleTalk + getNPCDialogue: dialogue selection
// (stage text / hasItemText / completedText by search order), progression on
// dialogue end, item requirement consumption and reward grants.
func m11HandleQuestTalk(c *playerConn, st *m11PlayerState, key, npcKey string) bool {
	def := m11Q[key]
	q := st.quest(key)

	// Dialogue resolution (getNPCDialogue): iterate stages backwards from the
	// current one, first stage that references the NPC wins.
	var dialogue []string
	seen := false
	for i := q.Stage; i >= 0; i-- {
		sd := m11StageDef(def, i)
		if m11NpcOf(sd) != npcKey {
			continue
		}
		seen = true
		if i < q.Stage {
			dialogue = sd.CompletedText // past reference: post-stage text
			break
		}
		// Current stage: hasItemText when requirements are met, else text.
		if len(sd.ItemRequirements) > 0 {
			if m11HasAllItems(st.Username, sd.ItemRequirements) {
				dialogue = sd.HasItemText
				break
			}
			continue // items missing: try older references
		}
		dialogue = sd.Text
		break
	}
	if !seen {
		// NPC is involved in a *later* stage: Node falls back to [''] (empty
		// bubble) and swallows the default text. Mirroring that makes early
		// quest NPCs stay silent — keep it but log.
		dialogue = []string{""}
	}
	if len(dialogue) == 0 {
		dialogue = []string{""}
	}

	// Dialogue advancement uses the player's talkIndex (npc.talk).
	if st.TalkNPC != npcKey {
		st.TalkNPC = npcKey
		st.TalkIndex = 0
	}
	text := dialogue[min(st.TalkIndex, len(dialogue)-1)]
	endOfDialogue := st.TalkIndex == len(dialogue)-1
	st.TalkIndex++
	_ = send(c.conn, pktOp(PacketNPC, NPCTalk, npcPacketData{Instance: strp(npcKey), Text: &text}))

	// quest.ts handleTalk: progression fires when the final line shows.
	if !endOfDialogue {
		return true
	}

	sd := m11StageDef(def, q.Stage)
	isStageNPC := m11NpcOf(sd) == npcKey

	// Substage NPC (royalpet): completing a substage NPC consumes its items
	// and advances the substage (handleItemRequirement with substage data).
	for i := range sd.SubStages {
		ss := &sd.SubStages[i]
		if m11NpcOf(*ss) != npcKey {
			continue
		}
		if len(ss.ItemRequirements) > 0 {
			if !m11HasAllItems(st.Username, ss.ItemRequirements) {
				return true // dialogue shown, items missing
			}
			m11TakeItems(st, ss.ItemRequirements)
			m11GiveRewards(c, st, ss.ItemRewards)
		}
		m11ProgressSub(c, st, key)
		return true
	}

	if !isStageNPC {
		return true // involved NPC, but the current stage points elsewhere
	}

	// Current stage talk: item requirement → consumption path; item rewards
	// → grant + progress; plain → progress (handleTalk else-chain).
	if len(sd.ItemRequirements) > 0 {
		if !m11HasAllItems(st.Username, sd.ItemRequirements) {
			return true // hasItemText path already rejected above; stay put
		}
		if sd.ItemRewards != nil {
			free := ModulesInventorySize - len(m5StateFor(st.Username).Inv)
			if free < len(sd.ItemRewards) {
				if c != nil {
					m6Notify(c, "misc:NO_SPACE")
				}
				return true
			}
		}
		m11TakeItems(st, sd.ItemRequirements)
		m11GiveRewards(c, st, sd.ItemRewards)
		m11GrantExperience(c, st, sd.SkillRewards)
		m11Progress(c, st, key)
		return true
	}
	if len(sd.ItemRewards) > 0 {
		// Rewards granted WITHOUT consumption (guard stage 2 pattern): Node
		// grants then progresses via givePlayerRewards(progress=true).
		if !m11GiveRewards(c, st, sd.ItemRewards) {
			return true // NO_SPACE: quest holds until room is made
		}
		m11GrantExperience(c, st, sd.SkillRewards)
		m11Progress(c, st, key)
		return true
	}
	if sd.Ability != "" {
		// Ability rewards have no engine slice yet — log-only divergence.
		log.Printf("m11: stage ability reward %q ignored (no ability engine)", sd.Ability)
	}
	m11GrantExperience(c, st, sd.SkillRewards)
	m11Progress(c, st, key)
	return true
}

// m11HandleAchTalk ports achievement.handleTalk: hidden/started dialogue,
// progress on dialogue end (discover stage), item requirements consumed.
func m11HandleAchTalk(c *playerConn, st *m11PlayerState, key string) bool {
	def := m11A[key]
	dialogue := def.Raw.DialogueHidden
	if st.Achs[key] > 0 {
		dialogue = def.Raw.DialogueStarted
	}
	if len(dialogue) == 0 {
		return false // no dialogue: fall through to default NPC talk
	}
	if st.TalkNPC != key {
		st.TalkNPC = key
		st.TalkIndex = 0
	}
	text := dialogue[min(st.TalkIndex, len(dialogue)-1)]
	endOfDialogue := st.TalkIndex == len(dialogue)-1
	st.TalkIndex++
	_ = send(c.conn, pktOp(PacketNPC, NPCTalk, npcPacketData{Text: &text}))
	if !endOfDialogue {
		return true
	}
	// Item requirement achievements resolve at the NPC (handleItemRequirement).
	if def.Raw.Item != "" {
		if m6InvCount(st.Username, def.Raw.Item) < def.Raw.ItemCount {
			return true
		}
		m6RemoveItem(st.Username, def.Raw.Item, def.Raw.ItemCount)
	}
	m11AchProgress(c, st, key)
	return true
}

// ---------------------------------------------------------------------------
// Kill trigger (quest.ts handleKill + achievement.handleKill).
// ---------------------------------------------------------------------------

// m11Kill fires on mob death credited to the killer.
func m11Kill(c *playerConn, mobKey string) {
	if c == nil {
		return
	}
	m11Load()
	if !m11OK {
		return
	}
	st := m11StateFor(c.username)

	// Achievement first (handler.handleDeath order — achievements.ts kill
	// routing rides the same mob-death signal; first unfinished match wins).
	for _, key := range m11AOrder {
		def := m11A[key]
		if st.Achs[key] >= def.StageCount || !m11AchHasMob(def, mobKey) || st.Achs[key] == 0 {
			continue
		}
		m11AchProgress(c, st, key)
	}

	// Quest kill stages: the started, unfinished quest whose current stage is
	// a kill task listing the mob (quests.ts getQuestFromMob — single match).
	for _, key := range m11QOrder {
		def := m11Q[key]
		if st.isFinished(key) || !st.isStarted(key) {
			continue
		}
		sd := m11StageDef(def, st.quest(key).Stage)
		if sd.Task != "kill" || len(sd.Mob) == 0 || !containsStr(sd.Mob, mobKey) {
			continue
		}
		// subStage per kill, stage at mobCountRequirement (handleKill).
		m11ProgressSub(c, st, key)
		if st.quest(key).SubStage >= sd.MobCountRequirement {
			m11Progress(c, st, key)
		}
		break
	}
}

func m11AchHasMob(def *m11AchDef, mobKey string) bool {
	return containsStr(def.Raw.Mob, mobKey)
}

// ---------------------------------------------------------------------------
// Resource trigger (quest.ts handleResource).
// ---------------------------------------------------------------------------

// m11Resource fires when a gather exhausts. skill is the Go skill name
// (lumberjacking/mining/fishing/foraging), resourceKey the resource def key
// (quest.ts handleResource: match stage.<tree|fish|rock> then count down the
// substage; no count = single-stage progress).
func m11Resource(c *playerConn, skill, resourceKey string) {
	if c == nil {
		return
	}
	m11Load()
	if !m11OK {
		return
	}
	st := m11StateFor(c.username)
	field := map[string]string{
		"lumberjacking": "tree", "fishing": "fish", "mining": "rock",
	}[skill]
	if field == "" {
		return
	}
	for _, key := range m11QOrder {
		def := m11Q[key]
		if st.isFinished(key) || !st.isStarted(key) {
			continue
		}
		q := st.quest(key)
		sd := m11StageDef(def, q.Stage)
		if sd.Task != field {
			continue
		}
		var resKey string
		var resCount int
		switch field {
		case "tree":
			resKey, resCount = sd.Tree, sd.TreeCount
		case "fish":
			resKey, resCount = sd.Fish, sd.FishCount
		case "rock":
			resKey, resCount = sd.Rock, sd.RockCount
		}
		if resKey == "" || resKey != resourceKey {
			continue
		}
		if resCount == 0 {
			m11Progress(c, st, key)
			break
		}
		m11ProgressSub(c, st, key)
		if st.quest(key).SubStage >= resCount {
			m11Progress(c, st, key)
		}
		break
	}
}

// ---------------------------------------------------------------------------
// C→S accept (incoming.handleQuest → quest.handlePrompt).
// ---------------------------------------------------------------------------

// m11HandleAccept processes the C Quest {key} frame: the pending start
// interface accepted → progress past stage 0 (handlePrompt setStage+1).
// m11HeroDamageMult multiplies hero damage vs engine mobs when M11_HERODMG is
// set (debug accelerator, mirrors M9_MOBDMG) — keeps 140-HP e2e mobs in a
// few-swing kill range without touching XP accounting.
func m11HeroDamageMult() float64 {
	if v := os.Getenv("M11_HERODMG"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return f
		}
	}
	return 1
}

func m11HandleAccept(c *playerConn, data []byte) {
	var d struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(data, &d); err != nil || d.Key == "" {
		return
	}
	m11Load()
	if !m11OK || m11Q[d.Key] == nil {
		return
	}
	st := m11StateFor(c.username)
	if !st.PendingStart[d.Key] {
		return
	}
	st.PendingStart[d.Key] = false
	// handlePrompt: setStage(stage+1, 0, true, skipPrompts=true) — NOT
	// progress(), whose unstarted guard would re-surface the interface.
	q := st.quest(d.Key)
	m11SetStage(c, st, d.Key, q.Stage+1, 0, true)
}

// ---------------------------------------------------------------------------
// Quest-gated drops (mob.getDrops fullfillsQuest + getDropTable quest gates).
// ---------------------------------------------------------------------------

// m11DropGated reports whether a drop entry's quest gate passes. status
// semantics (mob.fullfillsQuest): empty status = require finished;
// notstarted = require not started; started = started && not finished.
// achievements gate the same way on stage >= stageCount.
func m11DropGated(username, questKey, achievementKey, status string) bool {
	if questKey == "" && achievementKey == "" {
		return true
	}
	m11Load()
	if !m11OK {
		return false
	}
	st := m11StateFor(username)
	if achievementKey != "" {
		def := m11A[achievementKey]
		if def == nil || st.Achs[achievementKey] < def.StageCount {
			return false
		}
	}
	if questKey == "" {
		return true
	}
	def := m11Q[questKey]
	if def == nil {
		return false
	}
	q := st.quest(questKey)
	switch status {
	case "notstarted":
		return q.Stage == 0
	case "started":
		return q.Stage > 0 && q.Stage < def.StageCount
	default:
		return q.Stage >= def.StageCount
	}
}

// ---------------------------------------------------------------------------
// Achievement progression core.
// ---------------------------------------------------------------------------

// m11AchProgress advances an achievement one stage and fires popups/rewards
// at the finish stage (achievement.setStage).
func m11AchProgress(c *playerConn, st *m11PlayerState, key string) {
	def := m11A[key]
	if def == nil {
		return
	}
	stage := st.Achs[key] + 1
	st.Achs[key] = stage
	markDirty(st.Username)
	if c != nil {
		m11SendAchievementProgress(c, key, stage)
		if stage == 1 {
			m11SendPopup(c, "Achievement Discovered", def.Raw.Name+" has been discovered!", "#33cc33")
		}
		if stage >= def.StageCount {
			m11SendPopup(c, "Achievement Completed!",
				"@green@You have completed the achievement @crimson@"+def.Raw.Name+"@green@!", "#33cc33")
		}
	}
	if stage >= def.StageCount {
		log.Printf("m11: %s finished achievement %s", st.Username, key)
		if c != nil && def.Raw.RewardExperience > 0 {
			if id, ok := map[string]int{
				"lumberjacking": SkillLumberjacking, "mining": SkillMining,
				"fishing": SkillFishing, "foraging": SkillForaging,
				"accuracy": SkillAccuracy, "archery": SkillArchery,
				"health": SkillHealth, "magic": SkillMagic, "strength": SkillStrength,
				"defense": SkillDefense,
			}[strings.ToLower(def.Raw.RewardSkill)]; ok {
				m5AddXP(c, st.Username, id, def.Raw.RewardExperience)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Persistence (SQLite quests + achievements tables, m5 dirty-flush style).
// ---------------------------------------------------------------------------

// m11EnsureTables creates the quest/achievement tables (M5 DDL order).
func m11EnsureTables() {
	if dbConn == nil {
		return
	}
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS quests(player TEXT, quest TEXT, stage INT, substage INT, PRIMARY KEY(player, quest))`,
		`CREATE TABLE IF NOT EXISTS achievements(player TEXT, ach TEXT, stage INT, PRIMARY KEY(player, ach))`,
	} {
		if _, err := dbConn.Exec(ddl); err != nil {
			log.Printf("m11: ddl: %v", err)
		}
	}
}

// m11PersistQuests writes the quest rows for one player (called from the
// disconnect/flush path).
func m11PersistQuests(username string) {
	if dbConn == nil || username == "" {
		return
	}
	m11Mu.Lock()
	st, ok := m11Players[username]
	m11Mu.Unlock()
	if !ok {
		return
	}
	m11Mu.Lock()
	defer m11Mu.Unlock()
	if _, err := dbConn.Exec(`DELETE FROM quests WHERE player=?`, username); err != nil {
		return
	}
	for key, q := range st.Quests {
		if _, err := dbConn.Exec(`INSERT INTO quests(player,quest,stage,substage) VALUES(?,?,?,?)`,
			username, key, q.Stage, q.SubStage); err != nil {
			log.Printf("m11: save quest %s: %v", key, err)
		}
	}
	if _, err := dbConn.Exec(`DELETE FROM achievements WHERE player=?`, username); err != nil {
		return
	}
	for key, stage := range st.Achs {
		if stage == 0 {
			continue
		}
		if _, err := dbConn.Exec(`INSERT INTO achievements(player,ach,stage) VALUES(?,?,?)`,
			username, key, stage); err != nil {
			log.Printf("m11: save achievement %s: %v", key, err)
		}
	}
}

// m11LoadQuests restores quest/achievement rows into the in-memory state
// (called before the login batches are built).
func m11LoadQuests(username string) {
	m11Load()
	if !m11OK || dbConn == nil || username == "" {
		return
	}
	st := m11StateFor(username)
	rows, err := dbConn.Query(`SELECT quest,stage,substage FROM quests WHERE player=?`, username)
	if err == nil {
		for rows.Next() {
			var key string
			var stage, sub int
			if rows.Scan(&key, &stage, &sub) == nil {
				st.quest(key).Stage = stage
				st.quest(key).SubStage = sub
			}
		}
		rows.Close()
	}
	if rows, err := dbConn.Query(`SELECT ach,stage FROM achievements WHERE player=?`, username); err == nil {
		for rows.Next() {
			var key string
			var stage int
			if rows.Scan(&key, &stage) == nil {
				st.Achs[key] = stage
			}
		}
		rows.Close()
	}
}

// ---------------------------------------------------------------------------
// TESTMAP debug dispatcher (m9test precedent, rides [46 {m11test}]).
// ---------------------------------------------------------------------------

// m11HandleTest processes TESTMAP debug ops: stage/substage injection,
// achievement stage injection, and state echoes for the e2e harness.
func m11HandleTest(c *playerConn, data []byte) {
	if !testMode {
		return
	}
	var d struct {
		M11Test string `json:"m11test"`
		Key     string `json:"key"`
		Stage   int    `json:"stage"`
		Sub     int    `json:"sub"`
		Echo    string `json:"echo"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return
	}
	if d.M11Test == "" || c == nil {
		return
	}
	m11Load()
	st := m11StateFor(c.username)
	switch d.M11Test {
	case "setstage": // inject quest stage (progress semantics: full setStage)
		if m11Q[d.Key] == nil {
			return
		}
		st.PendingStart[d.Key] = false
		m11SetStage(c, st, d.Key, d.Stage, d.Sub, true)
	case "setach":
		if m11A[d.Key] == nil {
			return
		}
		for st.Achs[d.Key] < d.Stage {
			m11AchProgress(c, st, d.Key)
		}
	case "echo": // reply "m11:<key>=<stage>/<sub>" via notify (harness probe)
		var reply string
		switch d.Echo {
		case "quest":
			q := st.quest(d.Key)
			def := m11Q[d.Key]
			fin := def != nil && q.Stage >= def.StageCount
			reply = fmt.Sprintf("m11:%s=%d/%d fin=%v", d.Key, q.Stage, q.SubStage, fin)
		case "ach":
			reply = fmt.Sprintf("m11:ach:%s=%d/%d", d.Key, st.Achs[d.Key], m11A[d.Key].StageCount)
		case "pending":
			reply = fmt.Sprintf("m11:pending:%s=%v", d.Key, st.PendingStart[d.Key])
		case "drops": // gate probe: how many codersglitch drops available
			reply = fmt.Sprintf("m11:gate:%s=%v", d.Key, m11DropGated(c.username, d.Key, "", "started"))
		}
		if reply != "" {
			m6Notify(c, reply)
		}
	}
}

// ---------------------------------------------------------------------------
// Small helpers shared with the m6 slice.
// ---------------------------------------------------------------------------

func containsStr(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

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
