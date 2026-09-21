// Package player holds the PURE player-domain data model: owned structs
// mirroring the TS player aggregate, extracted without transport or
// persistence. Everything in here is transport-free: no mutexes, no
// websocket.Conn pointers, no send/broadcast/DB calls. The root adapter
// (main.go playerConn, m5.go m5State, m7.go chatState) keeps ALL wiring and
// joins this model to live conns by instance ID.
//
// Field-mapping table (root field -> player field, or exclusion reason):
//
//	playerConn.instance            -> Player.Instance (identity)
//	playerConn.username            -> Player.Username (identity, M5 DB key)
//	playerConn.sess                -> EXCLUDED (movement-owned: playerX/playerY,
//	                             target, lastStep, movementSpeed, cheatScore
//	                             stay with the session/world tick)
//	playerConn.conn                -> EXCLUDED (transport: *websocket.Conn)
//	playerConn.outbox              -> EXCLUDED (transport: S->C frame queue)
//	playerConn.regions / dropped   -> EXCLUDED (transport: interest set / stats)
//	playerConn.storeOpen           -> BankState.StoreOpen
//	playerConn.canAccessContainer  -> BankState.CanAccessContainer
//	playerConn.talkNPC             -> SocialState.TalkNPC
//	playerConn.talkIndex           -> SocialState.TalkIndex
//	playerConn.rank                -> ChatState.Rank
//	playerConn.chat.tokens         -> ChatState.Tokens (snapshot value only)
//	playerConn.chat.lastGlobalChat -> ChatState.LastGlobalChat
//	playerConn.chat.bucketMu       -> EXCLUDED (mutex)
//	playerConn.chat.lastRefill     -> EXCLUDED (rate-limiter clock, transport)
//	playerConn.chat.rank           -> ChatState.Rank (same source as .rank)
//	playerConn.m8Game              -> MinigameState.Game
//	playerConn.m8Team              -> MinigameState.Team
//	playerConn.m8Score             -> MinigameState.Score
//	playerConn.m8Target            -> MinigameState.Target
//	m5State.Inv                    -> State.Inventory (slot Ench excluded:
//	                             root Enchantments type is M12-owned)
//	m5State.Bank                   -> State.Bank (slots; access flags live in
//	                             BankState)
//	m5State.Equip                  -> State.Equipment
//	m5State.Skills[.Level/.XP]     -> State.Skills / Skill.Level / Skill.XP
//	m5State.X/Y/Level/HP           -> State.X/Y/Level/HP (persist snapshot)
package player

// Slot is one inventory/bank/equipment entry (m5Slot parity without the
// root Enchantments field, which stays M12-owned).
type Slot struct {
	Key   string
	Count int
}

// Skill is one skill snapshot (m5Skill parity: Level + XP).
type Skill struct {
	Level int
	XP    int
}

// State is the m5State persist snapshot: position/level/vitals plus the
// inventory, bank and equipment slot lists and the skills map.
// Transport-free: plain slices/maps, usable at the zero value.
type State struct {
	X         int
	Y         int
	Level     int
	HP        int
	Inventory []Slot
	Bank      []Slot
	Equipment []Slot
	Skills    map[int]Skill
}

// BankState is the M6 store/bank session cursor (stores.ts/handler.ts
// parity): which store is open and whether banker-granted bank access is
// currently held (cleared on move).
type BankState struct {
	StoreOpen          string // key of the open store ("" = none)
	CanAccessContainer bool   // banker-granted bank access
}

// ChatState is the M7 chat snapshot (player.chat parity): the Modules.Ranks
// value plus the rate-limiter token balance and last-global-chat timestamp.
// No mutex: the root adapter owns synchronization.
type ChatState struct {
	Rank           int     // Modules.Ranks value (0 = None)
	Tokens         float64 // refillable chat token balance snapshot
	LastGlobalChat int64   // ms, mirrors player.lastGlobalChat
}

// MinigameState is the M8 minigame session cursor
// (player.minigame/team/coursingScore/coursingTarget parity).
type MinigameState struct {
	Game   string // "coursing"|"teamwar" when playing ("" = none)
	Team   int    // Team enum value for the active game
	Score  int    // coursingScore mirror
	Target string // coursingTarget (pointer entity)
}

// SocialState is the NPC-talk dialog cursor (plain-NPC talk parity).
type SocialState struct {
	TalkNPC   string // last plain-NPC key talked to (talkIndex reset on change)
	TalkIndex int    // current npc.talk() index for TalkNPC
}

// Player aggregates the per-player domain state plus identity. It embeds
// State, BankState, ChatState, MinigameState and SocialState; the zero value
// is usable (no required constructor, no locks, no conn pointers).
type Player struct {
	Instance string // entity instance ID
	Username string // login name, DB key for the M5 persist slice

	State
	BankState
	ChatState
	MinigameState
	SocialState
}

// InMinigame reports whether the player is currently in a minigame
// (player.minigame non-empty parity). Nil-safe.
func (p *Player) InMinigame() bool {
	return p != nil && p.MinigameState.Game != ""
}

// CanAccessBank reports whether banker-granted bank access is held.
func (p *Player) CanAccessBank() bool {
	return p != nil && p.BankState.CanAccessContainer
}

// StoreKey returns the key of the currently open store ("" = none).
// Nil-safe.
func (p *Player) StoreKey() string {
	if p == nil {
		return ""
	}
	return p.BankState.StoreOpen
}
