package server

// M7 — chat + commands (thin adapter over internal/player/chat).
//
// Ports the Node chat path (incoming.ts handleChat → player.chat →
// sendToRegions / world.globalMessage) and the player+moderator command
// subset of controllers/commands.ts that the current Go stub world can
// support. S→C frames follow common/network/impl/chat.ts: the packet
// carries no opcode, and {instance,...} = entity bubble chat while
// {source,...} = static chatbox line (connection.ts handleChat).
//
// All PURE logic — sanitization, display names, token-bucket math, global
// cooldowns, command tables, outcome strings — lives in
// internal/player/chat. This file keeps ONLY wiring: frame parsing,
// transport (send/broadcast/socRoute*), the per-conn session shape the rest
// of the root package addresses (chatStateFor(...).rank,
// m7PlayerByName/m7PlayerUsernames, m7Teleport, m6NotifyWithSource) and the
// m12/m13 delegation. Behavior (frames, rate limits, command outcomes) is
// frozen: main.go/ops/social call sites compile unchanged.

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	gnet "rpg-world-server/internal/net"
	"rpg-world-server/internal/player/chat"
	worldcore "rpg-world-server/internal/world"
)

// ---------------------------------------------------------------------------
// Opcodes/modules ported from common/network.
// ---------------------------------------------------------------------------

// Opcodes.Network (opcodes.ts:52): Ping0 Pong1 Sync2.
const (
	NetworkPing = 0
	NetworkPong = 1
)

// Modules.Ranks (modules.ts:327) — subset the Go stub models. Values are the
// chat package's canonical ranks, aliased here so rank comparisons across
// the root package stay in one place.
const (
	RankNone      = chat.RankNone
	RankModerator = chat.RankModerator
	RankAdmin     = chat.RankAdmin
)

// RankTitles (modules.ts:365) for chat name prefixing (shared with the chat
// package's DisplayName helper; read-only).
var rankTitles = chat.RankTitles

// Text-pattern aliases (sanitizer/Utils.formatName parity lives in chat;
// kept here so existing references keep compiling).
var whitespaceRe = chat.WhitespaceRe
var wordRe = chat.WordRe

// ---------------------------------------------------------------------------
// Per-connection chat state.
// ---------------------------------------------------------------------------

// chatState carries the per-connection M7 session fields. Rate limiting is
// a token bucket over the region chat (commands/global paths only tick the
// rank cooldown like Node's lastGlobalChat), so spamming cannot flood a
// region regardless of rank. Bucket math delegates to chat.AllowBucket.
type chatState struct {
	bucketMu   sync.Mutex
	tokens     float64   // refillable chat tokens
	lastRefill time.Time // last token refill timestamp

	lastGlobalChat int64 // ms, mirrors player.lastGlobalChat
	rank           int   // Modules.Ranks value (0 = None)
}

const chatBucketSize = chat.BucketSize         // burst capacity
const chatRefillPerSec = chat.RefillPerSec     // one message per 2 seconds
const globalChatCooldown = chat.GlobalCooldown // Ranks.None cooldown (player.ts getGlobalChatCooldown default)

// allowChat consumes one token, refilling elapsed-time first. Calls with no
// tokens left are rejected (Node has no equivalent — it trusts the client's
// input box — but a server clone needs the guard; keeps Node check order
// intact otherwise).
func (cs *chatState) allowChat() bool {
	cs.bucketMu.Lock()
	defer cs.bucketMu.Unlock()

	ok, tokens, refill := chat.AllowBucket(cs.tokens, cs.lastRefill, time.Now())
	cs.tokens, cs.lastRefill = tokens, refill
	return ok
}

// globalChatReady ports canGlobalChat(): the rank-based cooldown between
// global messages (default rank = 60s, mods/admins = 5s).
func (cs *chatState) globalChatReady() bool {
	return chat.GlobalReady(cs.rank, cs.lastGlobalChat, nowMillis())
}

// globalChatDuration ports getGlobalChatDuration(): whole minutes left on
// the cooldown, minimum 1 (player.ts).
func (cs *chatState) globalChatDuration() int {
	return chat.GlobalDuration(cs.rank, cs.lastGlobalChat, nowMillis())
}

func nowMillis() int64 {
	return chat.NowMillis()
}

// chatStateFor returns the M7 state attached to a playerConn, creating it
// lazily (chatConn state lives beside the M6 store/talk fields).
func chatStateFor(c *playerConn) *chatState {
	if c.chat == nil {
		c.chat = &chatState{rank: c.rank}
	}
	return c.chat
}

// ---------------------------------------------------------------------------
// S→C payload shapes (common/network/impl/chat.ts + client handleChat).
// ---------------------------------------------------------------------------

// chatPacketData mirrors ChatPacketData. Instance-based frames carry a
// bubble over the entity; source-based frames are static chatbox lines.
type chatPacketData struct {
	Instance   string `json:"instance,omitempty"`
	Message    string `json:"message"`
	WithBubble bool   `json:"withBubble,omitempty"`
	Colour     string `json:"colour,omitempty"`
	Source     string `json:"source,omitempty"`
}

// ---------------------------------------------------------------------------
// Transport seams (chat.Moderation/chat.Router over live root state).
// ---------------------------------------------------------------------------

// m13Moderation implements chat.Moderation over the persisted m13 mute flags.
type m13Moderation struct{}

func (m13Moderation) IsMuted(username string) bool { return m13IsMuted(username) }

// chatRouter implements chat.Router over the live transports: region bubble
// broadcast, global Router fan-out, and sourced unicasts.
type chatRouter struct{}

var _ chat.Router = chatRouter{}

func (chatRouter) SendBubble(instance, message string, withBubble bool, colour string) {
	worldcore.Broadcast(pkt(PacketChat, chatPacketData{
		Instance:   instance,
		Message:    message,
		WithBubble: withBubble,
		Colour:     colour,
	}))
}

func (chatRouter) SendGlobal(source, message, colour string) {
	socRouteGlobal(pkt(PacketChat, chatPacketData{
		Source:  source,
		Message: message,
		Colour:  colour,
	}))
}

func (chatRouter) SendSourced(username, message, colour, source string) {
	if t := m7PlayerByName(username); t != nil {
		m6NotifyWithSource(t, message, colour, source)
	}
}

var defaultRouter = chatRouter{}

// ---------------------------------------------------------------------------
// Chat entry point (incoming.ts handleChat + player.chat).
// ---------------------------------------------------------------------------

// m7HandleChat is the PacketChat dispatcher (C→S Chat frame = [text]).
// Gate order is frozen: sanitize → visible-text → command bypass → region
// bucket → ops limiter → mute check → region chat.
func m7HandleChat(c *playerConn, frame clientFrame) {
	if len(frame) < 2 {
		return
	}
	var raw []string
	if err := json.Unmarshal(frame[1], &raw); err != nil || len(raw) == 0 {
		return
	}

	text := m7Sanitize(raw[0])
	if !chat.HasVisibleText(text) {
		return
	}

	// Commands (/ or ; prefix) bypass chat entirely (incoming.ts:476).
	if chat.IsCommand(text) {
		m7ParseCommand(c, text)
		return
	}

	cs := chatStateFor(c)

	// Rate limit before the mute check so floods cannot burn CPU on the
	// filter path (Node has no bucket; this is the Go hardening slice).
	if !cs.allowChat() {
		return
	}

	// Ops limiter: shared per-conn chat bucket (same silent drop as the
	// bucket-exhaust above — no notify).
	if !gnet.AllowChat(c.Conn) {
		return
	}

	// Mute gate (incoming.ts:479): the m13 slice persists user.mute in the
	// players.data blob and rejects chat while the deadline is in the future.
	if (m13Moderation{}).IsMuted(c.Username) {
		m6Notify(c, chat.MutedText())
		return
	}

	m7Chat(c, text, false, true, "")
}

// m7Sanitize ports sanitizer.escape + sanitize (delegates to chat.Sanitize).
func m7Sanitize(s string) string {
	return chat.Sanitize(s)
}

// m7FormatName ports Utils.formatName (delegates to chat.FormatName).
func m7FormatName(name string) string {
	return chat.FormatName(name)
}

// m7Chat ports player.chat(message, global, withBubble, colour): rank
// prefix + global cooldown → region bubble or world broadcast.
func m7Chat(c *playerConn, message string, global bool, withBubble bool, colour string) {
	cs := chatStateFor(c)

	if global {
		if !cs.globalChatReady() {
			m6Notify(c, chat.GlobalCooldownNotice(cs.globalChatDuration()))
			return
		}
		cs.lastGlobalChat = nowMillis()
	}

	name := chat.DisplayName(c.Username, cs.rank)
	colour = chat.ResolveColour(cs.rank, colour)

	if global {
		// world.globalMessage: [Global] prefix source frame, no bubble.
		// All-in-one hub routing: resolve the online set via the Router and
		// unicast; fall back to the existing broadcast when nobody resolves.
		defaultRouter.SendGlobal(chat.GlobalSource(name), message, colour)
		return
	}

	// Region-scoped bubble (player.chat → sendToRegions): the broadcast
	// helper resolves c.Instance's tile and fans out to the 9-region
	// interest sets, matching world.push(Regions).
	defaultRouter.SendBubble(c.Instance, message, withBubble, colour)
}

// ---------------------------------------------------------------------------
// Commands (controllers/commands.ts).
// ---------------------------------------------------------------------------

// m7ParseCommand ports Commands.parse (delegates prefix/split to
// chat.SplitCommand), then runs the player/mod command tables in order plus
// the M12 crafting and M13 guild/mod/admin tables.
func m7ParseCommand(c *playerConn, rawText string) {
	command, args, ok := chat.SplitCommand(rawText)
	if !ok {
		return
	}

	m7PlayerCommands(c, command, args)
	m7ModeratorCommands(c, command, args)
	m12PlayerCommands(c, command)     // M12: crafting interface opens (/crafting etc.)
	m13ParseCommand(c, command, args) // M13: guild + full mod/admin tables
}

// m7PlayerCommands ports handlePlayerCommands (the subset meaningful in the
// Go stub world): players, coords, g/gc/global, pm/msg.
func m7PlayerCommands(c *playerConn, command string, blocks []string) {
	cmd, ok := chat.ClassifyPlayer(command)
	if !ok {
		return
	}
	switch cmd {
	case chat.CmdPlayers:
		names := m7PlayerUsernames()
		m6Notify(c, chat.PlayersSummary(len(names)))
		if chatStateFor(c).rank == RankAdmin {
			m6Notify(c, strings.Join(names, ", "))
		}

	case chat.CmdCoords:
		m6Notify(c, chat.CoordsText(c.Sess.PlayerX, c.Sess.PlayerY))

	case chat.CmdPing:
		// player.ping(): Network Ping frame, bypassing the outbox queue.
		_ = gnet.Send(c.Conn, pktOp(PacketNetwork, NetworkPing, nil))

	case chat.CmdGlobal:
		m7Chat(c, strings.Join(blocks, " "), true, false, chat.GlobalColour)

	case chat.CmdPM:
		username, message, ok := chat.ParsePrivateMessage(blocks)
		if !ok {
			return
		}
		m7SendPrivateMessage(c, username, message)
	}
}

// m7ModeratorCommands ports handleModeratorCommands (subset): /teleport.
// Rank gate mirrors the isMod/isAdmin/isHollowAdmin early return.
func m7ModeratorCommands(c *playerConn, command string, blocks []string) {
	if !chat.ModAllowed(chatStateFor(c).rank) {
		return
	}
	if cmd, ok := chat.ClassifyMod(command); ok && cmd == chat.ModTeleport {
		if x, y, ok := chat.ParseTeleportArgs(blocks); ok {
			m7Teleport(c, x, y)
		}
	}
}

// m7SendPrivateMessage ports player.sendPrivateMessage + sendMessage: an
// offline target notifies misc:NOT_ONLINE; delivery is an aquamarine
// Notification with a [From <name>] source (both sides for the sender).
func m7SendPrivateMessage(c *playerConn, playerName string, message string) {
	// All-in-one hub routing: resolve the direct target via the Router first;
	// an offline target falls back to the existing misc:NOT_ONLINE notify.
	target := socRouteChat(playerName)
	if target == nil {
		m6Notify(c, chat.PMOffline(playerName))
		return
	}
	formatted := m7FormatName(c.Username)
	fromSource, toSource := chat.PMSources(formatted, m7FormatName(target.Username))
	defaultRouter.SendSourced(target.Username, message, chat.PMColour, fromSource)
	defaultRouter.SendSourced(c.Username, message, chat.PMColour, toSource)
}

// m7Teleport ports character.teleport: set position, Teleport frame to the
// surrounding regions (which the Go broadcast scopes by the entity tile).
// The tracked plateauLevel refreshes on the landing tile (door/teleport
// destinations can sit on a different plateau than the origin).
func m7Teleport(c *playerConn, x, y int) {
	c.Sess.PlayerX = x
	c.Sess.PlayerY = y
	worldcore.SetEntityPos(c.Instance, x, y)
	worldcore.UpdateRegion(c, x, y)
	worldcore.Broadcast(pkt(PacketTeleport, teleportData{Instance: c.Instance, X: x, Y: y}))
	plateauTrack(c)
}

// ---------------------------------------------------------------------------
// Player registry helpers (world/entities equivalents over the Go stub).
// ---------------------------------------------------------------------------

// m7PlayerUsernames snapshots online usernames.
func m7PlayerUsernames() []string {
	var names []string
	for _, c := range worldcore.AllOf[*playerConn]() {
		if c.Username != "" {
			names = append(names, c.Username)
		}
	}
	return names
}

// m7PlayerByName finds an online conn by username (case-insensitive, like
// world.getPlayerByName's lowercase compare).
func m7PlayerByName(name string) *playerConn {
	lower := strings.ToLower(name)
	for _, c := range worldcore.AllOf[*playerConn]() {
		if strings.ToLower(c.Username) == lower {
			return c
		}
	}
	return nil
}

// m6NotifyWithSource is the notify() variant with a source header
// (player.notify(message, colour, message.title) → Notification Text). The
// payload shape lives in the chat package (chat.SourceNotice); this wrapper
// keeps the established call sites (m7 PM path, m13 jail path) compiling.
func m6NotifyWithSource(c *playerConn, message string, colour string, source string) {
	n := chat.Notice(message, colour, source)
	col := n.Colour
	src := n.Source
	_ = gnet.Send(c.Conn, pktOp(PacketNotification, NotificationText, notificationPacketData{
		Message: n.Message,
		Colour:  &col,
		Source:  &src,
	}))
}
