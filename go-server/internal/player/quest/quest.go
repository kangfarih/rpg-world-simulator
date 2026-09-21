// Package quest holds the M11 slice — the quests + achievements engine.
//
// Behavior-frozen move of the root m11.go logic: identical registry,
// stage lifecycle, gate semantics, frame shapes, popup/pointer side
// channels, TESTMAP debug ops and SQLite schema.
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
// Triggers: the economy NPC-talk path calls Talk after the plain-NPC talk
// branch; the mob-death path calls Kill; gather-exhaust calls Resource; the
// drop roller consults DropGated to activate `quest`-gated entries
// (skeleton → skeletonkingtalisman only while codersglitch is started).
// Login queues the Quest/Achievement Batch after the Welcome extras;
// disconnect persists stage state to SQLite (quests + achievements tables).
//
// Transport and shared player state stay with the root server: this package
// touches them only through the Conn/Store/Bus/Abilities/DB seams, which the
// root adapter (m11.go) implements over its globals. Packet shapes and the DB
// schema are unchanged — frames are built with internal/protocol, the same
// constructors the root uses.
package quest

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"rpg-world-server/internal/protocol"
)

// ---------------------------------------------------------------------------
// Seams (implemented by the root adapter; never by this package).
// ---------------------------------------------------------------------------

// Conn is the minimal per-connection view the quest logic needs. The root
// *playerConn satisfies it, so the same pointer flows through and identity
// comparisons keep working. A nil Conn means "no live connection": sends and
// notifies are skipped, state still advances (defensive parity with the
// original c == nil guards).
type Conn interface {
	InstanceID() string
	PlayerName() string
}

// Store abstracts player inventory/skill state, ability grants and dirty
// tracking. Every method maps to one root helper:
//
//	InventoryLen -> len(m5StateFor(username).Inv)
//	CountItem    -> m6InvCount
//	AddItem      -> m5AddItem (returns the slot index)
//	RemoveItem   -> m6RemoveItem
//	SkillLevel   -> m5StateFor skills read (ok=false when untrained, so
//	                skill-requirement gates keep their nil-means-locked shape)
//	AddXP        -> m5AddXP
//	MarkDirty    -> markDirty
type Store interface {
	InventoryLen(username string) int
	CountItem(username, itemKey string) int
	AddItem(username, itemKey string, count int) int
	RemoveItem(username, itemKey string, count int)
	SkillLevel(username string, skill int) (int, bool)
	AddXP(c Conn, username string, skill, amount int) int
	MarkDirty(username string)
}

// Bus abstracts S->C frame delivery. SendTo maps to send() unicast for the
// instance's conn; Notify maps to m6Notify (Notification Text).
type Bus interface {
	SendTo(instance string, frames ...[]any)
	Notify(instance string, message string)
}

// Abilities abstracts the quest/achievement ability-reward call
// (abGrantAbility: abilities.add + Add/Update frame + unlock notify).
type Abilities interface {
	GrantAbility(c Conn, username, key string, level int)
}

// Deps bundles the seams for one call.
type Deps struct {
	Store     Store
	Bus       Bus
	Abilities Abilities
	DB        DB
}

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

// Opcodes.Pointer (Location0/Remove3) + Notification Popup3: the pointer and
// notification opcodes live outside internal/protocol (minigame-owned), so
// the values are pinned here (m8.go parity: Location 0, Remove 3).
const (
	PointerLocation   = 0
	PointerRemove     = 3
	NotificationPopup = 3
)

// Skill ids mirror Modules.Skills order (m5.go parity).
const (
	SkillLumberjacking = 0
	SkillAccuracy      = 1
	SkillArchery       = 2
	SkillHealth        = 3
	SkillMagic         = 4
	SkillMining        = 5
	SkillStrength      = 6
	SkillDefense       = 7
	SkillFishing       = 8
	SkillForaging      = 15
)

// StageData mirrors RawStage (impl/quest.ts) for the fields the engine acts
// on; `noc` is codersglitch's misspelled npc key (quest.ts reads stage.npc,
// so the typo stage is unreachable there — we honor both).
type StageData struct {
	Task                string        `json:"task"`
	NPC                 string        `json:"npc"`
	Noc                 string        `json:"noc"`
	Mob                 []string      `json:"mob"`
	MobCountRequirement int           `json:"mobCountRequirement"`
	ItemRequirements    []Item        `json:"itemRequirements"`
	ItemRewards         []Item        `json:"itemRewards"`
	Text                []string      `json:"text"`
	CompletedText       []string      `json:"completedText"`
	HasItemText         []string      `json:"hasItemText"`
	Pointer             *Pointer      `json:"pointer"`
	Popup               *Popup        `json:"popup"`
	Ability             string        `json:"ability"`
	AbilityLevel        int           `json:"abilityLevel"`
	Tree                string        `json:"tree"`
	TreeCount           int           `json:"treeCount"`
	Fish                string        `json:"fish"`
	FishCount           int           `json:"fishCount"`
	Rock                string        `json:"rock"`
	RockCount           int           `json:"rockCount"`
	SkillRewards        []SkillReward `json:"skillRewards"`
	SubStages           []StageData   `json:"subStages"`
	Timer               int           `json:"timer"`
}

// Item is one quest item requirement/reward entry.
type Item struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

// Pointer is one quest stage pointer.
type Pointer struct {
	Type int `json:"type"`
	X    int `json:"x"`
	Y    int `json:"y"`
}

// Popup is one quest stage popup.
type Popup struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

// SkillReward is one quest stage skill-XP reward.
type SkillReward struct {
	Key        string `json:"key"`
	Experience int    `json:"experience"`
}

// Raw mirrors RawQuest.
type Raw struct {
	Name              string               `json:"name"`
	Description       string               `json:"description"`
	Rewards           []string             `json:"rewards"`
	SkillRequirements map[string]int       `json:"skillRequirements"`
	QuestRequirements []string             `json:"questRequirements"`
	Stages            map[string]StageData `json:"stages"`
}

// Quest is one immutable quest definition shared by all players.
type Quest struct {
	Key        string
	Raw        Raw
	StageCount int
	StageOrder []int // sorted stage indices
	NoPrompts  bool  // tutorial: skip the Start prompt interface
	NPCs       map[string]bool
}

// AchievementRaw mirrors RawAchievement.
type AchievementRaw struct {
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

// AchDef is one immutable achievement definition.
type AchDef struct {
	Key        string
	Raw        AchievementRaw
	StageCount int // mobCount+1 (discovery stage) else 1
}

// ---------------------------------------------------------------------------
// Data loading (quests/ dir + achievements.json; quest_bases/ for parity).
// ---------------------------------------------------------------------------

var (
	loadOnce sync.Once
	ok       bool
	// Quests is the immutable quest registry (21 JSONs, sorted keys in
	// QuestOrder). Achievements mirror it in Achs/AchOrder. The root
	// adapter aliases these maps so external readers see live state.
	Quests     = map[string]*Quest{}
	Achs       = map[string]*AchDef{}
	QuestOrder []string // sorted keys (Node's impl/index.ts order has no client
	AchOrder   []string // observable effect — Task ids are positional only)
)

// DataDir resolves a data DIRECTORY: resourceDataPath is file-oriented
// (appends .json), so mirror its env/relative-path search for dirs.
func DataDir(name string) string {
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

// achievementsPath resolves data/achievements.json (resourceDataPath parity:
// env, relative search, then the checked-in absolute fallback).
func achievementsPath() string {
	if p := os.Getenv("RES_achievements"); p != "" {
		return p
	}
	rel := filepath.Join("..", "packages", "server", "data", "achievements.json")
	if _, err := os.Stat(rel); err == nil {
		return rel
	}
	if alt := filepath.Join("..", "..", "packages", "server", "data", "achievements.json"); true {
		if _, err := os.Stat(alt); err == nil {
			return alt
		}
	}
	return "/Users/appfuxion/repo/rpg-world-sim/packages/server/data/achievements.json"
}

// Load reads the quest/achievement registries once (m11Load parity).
func Load() {
	loadOnce.Do(func() {
		// Quests: data/quests/*.json (TS imports 21 files explicitly; we read
		// the directory and sort for determinism).
		entries, err := os.ReadDir(DataDir("quests"))
		if err != nil {
			log.Printf("m11: read quests dir: %v (quests disabled)", err)
			return
		}
		var files []string
		for _, e := range entries {
			if !e.IsDir() && len(e.Name()) > 5 && e.Name()[len(e.Name())-5:] == ".json" {
				files = append(files, e.Name())
			}
		}
		sort.Strings(files)
		questDir := DataDir("quests")
		for _, f := range files {
			key := f[:len(f)-len(".json")]
			raw, err := os.ReadFile(filepath.Join(questDir, f))
			if err != nil {
				continue
			}
			var q Raw
			if err := json.Unmarshal(raw, &q); err != nil {
				log.Printf("m11: parse %s: %v", f, err)
				continue
			}
			quest := &Quest{Key: key, Raw: q, NPCs: map[string]bool{}}
			quest.StageCount = len(q.Stages)
			// TS impl subclasses override noPrompts (impl/tutorial.ts:
			// `protected override noPrompts = true`) — the tutorial starts
			// without the accept interface. JSON carries no such flag.
			if key == "tutorial" {
				quest.NoPrompts = true
			}
			for id := range q.Stages {
				n, good := atoiOk(id)
				if !good {
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
			Quests[key] = quest
		}
		// Quest bases: authoring drafts, registry parity only.
		if entries, err := os.ReadDir(DataDir("quest_bases")); err == nil {
			n := 0
			for _, e := range entries {
				if !e.IsDir() && len(e.Name()) > 5 && e.Name()[len(e.Name())-5:] == ".json" {
					n++
				}
			}
			log.Printf("m11: quest_bases=%d (loaded, no impl — TS parity)", n)
		}
		// Achievements.
		raw, err := os.ReadFile(achievementsPath())
		if err != nil {
			log.Printf("m11: read achievements.json: %v (achievements disabled)", err)
			ok = len(Quests) > 0
			log.Printf("m11: quests=%d achievements=0", len(Quests))
			return
		}
		var achs map[string]AchievementRaw
		if err := json.Unmarshal(raw, &achs); err != nil {
			log.Printf("m11: parse achievements.json: %v", err)
			return
		}
		for k, v := range achs {
			sc := 1
			if v.MobCount > 0 {
				sc = v.MobCount + 1
			}
			Achs[k] = &AchDef{Key: k, Raw: v, StageCount: sc}
		}
		for k := range Quests {
			QuestOrder = append(QuestOrder, k)
		}
		sort.Strings(QuestOrder)
		for k := range Achs {
			AchOrder = append(AchOrder, k)
		}
		sort.Strings(AchOrder)
		ok = true
		log.Printf("m11: quests=%d achievements=%d", len(Quests), len(Achs))
	})
}

// OK reports whether the registries loaded.
func OK() bool {
	Load()
	return ok
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

func containsStr(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Per-player state.
// ---------------------------------------------------------------------------

// QuestState is one player's stage cursor for one quest.
type QuestState struct {
	Stage    int
	SubStage int
}

// PlayerState is one player's quest/achievement runtime: stage cursors,
// talk tracking (mirrors player.talkIndex + npcTalk reset) and the
// Start-prompt queue (pendingStart: key = true while the Start packet is
// outstanding; accept (C Quest) progresses the stage).
type PlayerState struct {
	Username string
	Quests   map[string]*QuestState
	Achs     map[string]int
	// TalkNPC/TalkIndex track dialogue advancement per NPC.
	TalkNPC   string
	TalkIndex int
	// PendingStart queues the Start interface per quest.
	PendingStart map[string]bool
}

var (
	mu     sync.Mutex
	states = map[string]*PlayerState{}
)

// StateFor returns the per-player state (lazily created).
func StateFor(username string) *PlayerState {
	Load()
	st, found := states[username]
	if !found {
		st = &PlayerState{
			Username:     username,
			Quests:       map[string]*QuestState{},
			Achs:         map[string]int{},
			PendingStart: map[string]bool{},
		}
		states[username] = st
	}
	return st
}

// ForgetSession drops in-memory quest state (TESTMAP harness isolation).
func ForgetSession(username string) {
	mu.Lock()
	delete(states, username)
	mu.Unlock()
}

// Quest returns the stage cursor for key (lazily created).
func (st *PlayerState) Quest(key string) *QuestState {
	q, found := st.Quests[key]
	if !found {
		q = &QuestState{}
		st.Quests[key] = q
	}
	return q
}

// IsFinished reports stage >= stageCount (Node's `>=` allows overflow
// stages to end the quest).
func (st *PlayerState) IsFinished(key string) bool {
	def := Quests[key]
	if def == nil {
		return false
	}
	return st.Quest(key).Stage >= def.StageCount
}

// IsStarted reports stage > 0.
func (st *PlayerState) IsStarted(key string) bool {
	return st.Quest(key).Stage > 0
}

// ---------------------------------------------------------------------------
// Packets.
// ---------------------------------------------------------------------------

// QuestData mirrors QuestData (impl/quest.ts): batch adds the definition
// fields the client Task needs.
type QuestData struct {
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

// AchievementData mirrors AchievementData.
type AchievementData struct {
	Key         string  `json:"key"`
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Region      *string `json:"region,omitempty"`
	Stage       int     `json:"stage"`
	StageCount  *int    `json:"stageCount,omitempty"`
	Secret      *bool   `json:"secret,omitempty"`
}

func strPtr(s string) *string { return &s }
func intPtr(v int) *int       { return &v }

// SendQuestProgress emits Quest Progress1 (quests.ts handleProgress).
func SendQuestProgress(c Conn, d Deps, key string, q *QuestState) {
	if c == nil {
		return
	}
	d.Bus.SendTo(c.InstanceID(), protocol.PktOp(protocol.PacketQuest, QuestProgress, QuestData{
		Key: key, Stage: q.Stage, SubStage: q.SubStage,
	}))
}

// SendAchievementProgress emits Achievement Progress1 (name/description
// ride along for client Task creation — setAchievement contract).
func SendAchievementProgress(c Conn, d Deps, key string, stage int) {
	if c == nil {
		return
	}
	def := Achs[key]
	name, desc := key, ""
	if def != nil {
		name, desc = def.Raw.Name, def.Raw.Description
	}
	d.Bus.SendTo(c.InstanceID(), protocol.PktOp(protocol.PacketAchievement, AchievementProgress, AchievementData{
		Key: key, Stage: stage, Name: strPtr(name), Description: strPtr(desc),
	}))
}

// SendPointer mirrors player.pointer: Remove first, then Location.
func SendPointer(c Conn, d Deps, p *Pointer) {
	if c == nil {
		return
	}
	d.Bus.SendTo(c.InstanceID(), protocol.PktOp(protocol.PacketPointer, PointerRemove, map[string]any{}))
	if p == nil || p.Type != PointerLocation {
		return
	}
	d.Bus.SendTo(c.InstanceID(), protocol.PktOp(protocol.PacketPointer, PointerLocation, map[string]any{
		"type": p.Type, "x": p.X, "y": p.Y,
	}))
}

// SendPopup mirrors player.popup: Notification Popup3 {title,message,colour}.
func SendPopup(c Conn, d Deps, title, message, colour string) {
	if c == nil {
		return
	}
	d.Bus.SendTo(c.InstanceID(), protocol.PktOp(protocol.PacketNotification, NotificationPopup, protocol.NotificationPacketData{
		Title: strPtr(title), Message: message, Colour: strPtr(colour),
	}))
}

// ---------------------------------------------------------------------------
// Login: batches + initial pointer (quests.ts load + tutorial.loaded).
// ---------------------------------------------------------------------------

// LoginBatches builds the Quest Batch + Achievement Batch frames queued
// after the Welcome extras (handler.handleQuests/handleAchievements on
// quests.onLoaded). Batched QuestData carries the definition fields.
func LoginBatches(username string) [][]any {
	Load()
	if !ok {
		return nil
	}
	st := StateFor(username)
	qs := make([]QuestData, 0, len(QuestOrder))
	for _, key := range QuestOrder {
		def := Quests[key]
		q := st.Quest(key)
		qs = append(qs, QuestData{
			Key: key, Stage: q.Stage, SubStage: q.SubStage,
			Name: strPtr(def.Raw.Name), Description: strPtr(def.Raw.Description),
			Rewards: def.Raw.Rewards, SkillRequirements: def.Raw.SkillRequirements,
			QuestRequirements: def.Raw.QuestRequirements, StageCount: intPtr(def.StageCount),
		})
	}
	achs := make([]AchievementData, 0, len(AchOrder))
	for _, key := range AchOrder {
		def := Achs[key]
		secret := def.Raw.Secret
		ad := AchievementData{Key: key, Stage: st.Achs[key], StageCount: intPtr(def.StageCount)}
		if secret {
			ad.Secret = &secret
		} else {
			ad.Name = strPtr(def.Raw.Name)
			ad.Description = strPtr(def.Raw.Description)
			if def.Raw.Region != "" {
				ad.Region = strPtr(def.Raw.Region)
			}
		}
		achs = append(achs, ad)
	}
	return [][]any{
		protocol.PktOp(protocol.PacketQuest, QuestBatch, map[string]any{"quests": qs}),
		protocol.PktOp(protocol.PacketAchievement, AchievementBatch, map[string]any{"achievements": achs}),
	}
}

// LoginPointer sends the current stage's quest pointer after Ready
// (Tutorial.loaded → setStage(0,0,false) → pointerCallback; for other quests
// Node only re-points on stage changes, we mirror that by pointing only when
// the tutorial is unfinished).
func LoginPointer(c Conn, d Deps) {
	Load()
	if !ok || c == nil {
		return
	}
	st := StateFor(c.PlayerName())
	q := st.Quest("tutorial")
	def := Quests["tutorial"]
	if def == nil || q.Stage >= def.StageCount {
		return
	}
	if p := StageDef(def, q.Stage).Pointer; p != nil {
		SendPointer(c, d, p)
	}
}

// ---------------------------------------------------------------------------
// Stage helpers.
// ---------------------------------------------------------------------------

// StageDef returns the stage definition by index. Stages are serialized
// with numeric-string keys; the lookup finds by index.
func StageDef(q *Quest, stage int) StageData {
	if stage < 0 || stage >= q.StageCount {
		return StageData{}
	}
	for id, st := range q.Raw.Stages {
		if n, good := atoiOk(id); good && n == stage {
			return st
		}
	}
	return StageData{}
}

// NpcOf returns the stage's npc key honoring the `noc` typo.
func NpcOf(st StageData) string {
	if st.NPC != "" {
		return st.NPC
	}
	return st.Noc
}
