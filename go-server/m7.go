package main

// M7 — chat + commands (chat/commands slice of the Node clone).
//
// Ports the Node chat path (incoming.ts handleChat → player.chat →
// sendToRegions / world.globalMessage) and the player+moderator command
// subset of controllers/commands.ts that the current Go stub world can
// support. S→C frames follow common/network/impl/chat.ts: the packet
// carries no opcode, and {instance,...} = entity bubble chat while
// {source,...} = static chatbox line (connection.ts handleChat).

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Opcodes/modules ported from common/network.
// ---------------------------------------------------------------------------

// Opcodes.Network (opcodes.ts:52): Ping0 Pong1 Sync2.
const (
	NetworkPing = 0
	NetworkPong = 1
)

// Modules.Ranks (modules.ts:327) — subset the Go stub models.
const (
	RankNone      = 0
	RankModerator = 1
	RankAdmin     = 2
)

// RankTitles (modules.ts:365) for chat name prefixing.
var rankTitles = map[int]string{
	RankModerator: "Mod",
	RankAdmin:     "Admin",
}

// ---------------------------------------------------------------------------
// Per-connection chat state.
// ---------------------------------------------------------------------------

// chatState carries the per-connection M7 session fields. Rate limiting is
// a token bucket over the region chat (commands/global paths only tick the
// rank cooldown like Node's lastGlobalChat), so spamming cannot flood a
// region regardless of rank.
type chatState struct {
	bucketMu   sync.Mutex
	tokens     float64   // refillable chat tokens
	lastRefill time.Time // last token refill timestamp

	lastGlobalChat int64 // ms, mirrors player.lastGlobalChat
	rank           int   // Modules.Ranks value (0 = None)
}

const chatBucketSize = 3.0               // burst capacity
const chatRefillPerSec = 1.0 / 2.0       // one message per 2 seconds
const globalChatCooldown = int64(60_000) // Ranks.None cooldown (player.ts getGlobalChatCooldown default)

// allowChat consumes one token, refilling elapsed-time first. Calls with no
// tokens left are rejected (Node has no equivalent — it trusts the client's
// input box — but a server clone needs the guard; keeps Node check order
// intact otherwise).
func (cs *chatState) allowChat() bool {
	cs.bucketMu.Lock()
	defer cs.bucketMu.Unlock()

	now := time.Now()
	if cs.lastRefill.IsZero() {
		cs.lastRefill = now
		cs.tokens = chatBucketSize
	}
	cs.tokens += now.Sub(cs.lastRefill).Seconds() * chatRefillPerSec
	if cs.tokens > chatBucketSize {
		cs.tokens = chatBucketSize
	}
	cs.lastRefill = now

	if cs.tokens < 1 {
		return false
	}
	cs.tokens--
	return true
}

// globalChatReady ports canGlobalChat(): the rank-based cooldown between
// global messages (default rank = 60s, mods/admins = 5s).
func (cs *chatState) globalChatReady() bool {
	cooldown := globalChatCooldown
	if cs.rank >= RankModerator {
		cooldown = 5000
	}
	return nowMillis()-cs.lastGlobalChat > cooldown
}

// globalChatDuration ports getGlobalChatDuration(): whole minutes left on
// the cooldown, minimum 1 (player.ts).
func (cs *chatState) globalChatDuration() int {
	cooldown := globalChatCooldown
	if cs.rank >= RankModerator {
		cooldown = 5000
	}
	d := (cooldown - (nowMillis() - cs.lastGlobalChat)) / 1000 / 60
	if d < 1 {
		d = 1
	}
	return int(d)
}

func nowMillis() int64 {
	return time.Now().UnixMilli()
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
// Chat entry point (incoming.ts handleChat + player.chat).
// ---------------------------------------------------------------------------

var whitespaceRe = regexp.MustCompile(`\S`)
var wordRe = regexp.MustCompile(`\w\S*`)

// m7HandleChat is the PacketChat dispatcher (C→S Chat frame = [text]).
func m7HandleChat(c *playerConn, frame clientFrame) {
	if len(frame) < 2 {
		return
	}
	var raw []string
	if err := json.Unmarshal(frame[1], &raw); err != nil || len(raw) == 0 {
		return
	}

	// Sanitization: strip tags then collapse control characters. The Node
	// sanitizer.escape/sanitize combo HTML-escapes < > & and quotes.
	text := m7Sanitize(raw[0])
	if !whitespaceRe.MatchString(text) {
		return
	}

	// Commands (/ or ; prefix) bypass chat entirely (incoming.ts:476).
	if strings.HasPrefix(text, "/") || strings.HasPrefix(text, ";") {
		m7ParseCommand(c, text)
		return
	}

	cs := chatStateFor(c)

	// Rate limit before the mute check so floods cannot burn CPU on the
	// filter path (Node has no bucket; this is the Go hardening slice).
	if !cs.allowChat() {
		return
	}

	// Mute gate (incoming.ts:479): the m13 slice persists user.mute in the
	// players.data blob and rejects chat while the deadline is in the future.
	if m13IsMuted(c.username) {
		m6Notify(c, "You have been muted.")
		return
	}

	m7Chat(c, text, false, true, "")
}

// m7Sanitize ports sanitizer.escape + sanitize: HTML-escape the five XML
// entities and drop NULs. Node's `bo-wie/sanitize-html` escape pass.
func m7Sanitize(s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#x27;",
	)
	return r.Replace(s)
}

// m7FormatName ports Utils.formatName: capitalize every word.
func m7FormatName(name string) string {
	return wordRe.ReplaceAllStringFunc(name, func(w string) string {
		if w == "" {
			return w
		}
		return strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
	})
}

// m7Chat ports player.chat(message, global, withBubble, colour): rank
// prefix + global cooldown → region bubble or world broadcast.
func m7Chat(c *playerConn, message string, global bool, withBubble bool, colour string) {
	cs := chatStateFor(c)

	if global {
		if !cs.globalChatReady() {
			m6Notify(c, fmt.Sprintf("misc:CANNOT_GLOBAL_CHAT_MINUTES;duration=%d", cs.globalChatDuration()))
			return
		}
		cs.lastGlobalChat = nowMillis()
	}

	name := m7FormatName(c.username)
	if cs.rank != RankNone {
		if title, ok := rankTitles[cs.rank]; ok {
			name = "[" + title + "] " + name
		}
		if colour == "" {
			colour = "rgba(191, 161, 63, 1.0)"
		}
	}

	if global {
		// world.globalMessage: [Global] prefix source frame, no bubble.
		frame := pkt(PacketChat, chatPacketData{
			Source:  "[Global] " + name,
			Message: message,
			Colour:  colour,
		})
		broadcast(frame)
		return
	}

	// Region-scoped bubble (player.chat → sendToRegions): the broadcast
	// helper resolves c.instance's tile and fans out to the 9-region
	// interest sets, matching world.push(Regions).
	frame := pkt(PacketChat, chatPacketData{
		Instance:   c.instance,
		Message:    message,
		WithBubble: withBubble,
		Colour:     colour,
	})
	broadcast(frame)
}

// ---------------------------------------------------------------------------
// Commands (controllers/commands.ts).
// ---------------------------------------------------------------------------

// m7ParseCommand ports Commands.parse: strip the prefix, split on spaces,
// then run the player/mod command tables.
func m7ParseCommand(c *playerConn, rawText string) {
	blocks := strings.Split(strings.TrimPrefix(strings.TrimPrefix(rawText, "/"), ";"), " ")
	if len(blocks) == 0 || blocks[0] == "" {
		return
	}
	command := blocks[0]
	args := blocks[1:]

	m7PlayerCommands(c, command, args)
	m7ModeratorCommands(c, command, args)
	m12PlayerCommands(c, command)     // M12: crafting interface opens (/crafting etc.)
	m13ParseCommand(c, command, args) // M13: guild + full mod/admin tables
}

// m7PlayerCommands ports handlePlayerCommands (the subset meaningful in the
// Go stub world): players, coords, g/gc/global, pm/msg.
func m7PlayerCommands(c *playerConn, command string, blocks []string) {
	switch command {
	case "players":
		names := m7PlayerUsernames()
		population := len(names)
		if population == 1 {
			m6Notify(c, "There is currently 1 person online.")
		} else {
			m6Notify(c, fmt.Sprintf("There are currently %d people online.", population))
		}
		if chatStateFor(c).rank == RankAdmin {
			m6Notify(c, strings.Join(names, ", "))
		}

	case "coords":
		m6Notify(c, fmt.Sprintf("x: %d y: %d", c.sess.playerX, c.sess.playerY))

	case "ping":
		// player.ping(): Network Ping frame, bypassing the outbox queue.
		_ = send(c.conn, pktOp(PacketNetwork, NetworkPing, nil))

	case "g", "gc", "global":
		m7Chat(c, strings.Join(blocks, " "), true, false, "rgba(191, 161, 63, 1.0)")

	case "pm", "msg":
		// commands.ts: username = the text between the two `*` markers, and
		// the message is every block after the username's blocks (which keeps
		// the `*username*` wrapper in the delivered text — a Node quirk,
		// cloned verbatim).
		joined := strings.Join(blocks, " ")
		parts := strings.Split(joined, "*")
		if len(parts) < 2 || parts[1] == "" {
			return
		}
		username := parts[1]
		usernameBlocks := len(strings.Fields(username))
		message := strings.Join(blocks[usernameBlocks:], " ")
		m7SendPrivateMessage(c, strings.ToLower(username), message)
	}
}

// m7ModeratorCommands ports handleModeratorCommands (subset): /teleport.
// Rank gate mirrors the isMod/isAdmin/isHollowAdmin early return.
func m7ModeratorCommands(c *playerConn, command string, blocks []string) {
	if chatStateFor(c).rank < RankModerator {
		return
	}
	switch command {
	case "teleport":
		if len(blocks) < 2 {
			return
		}
		var x, y int
		_, errX := fmt.Sscanf(blocks[0], "%d", &x)
		_, errY := fmt.Sscanf(blocks[1], "%d", &y)
		if errX == nil && errY == nil {
			m7Teleport(c, x, y)
		}
	}
}

// m7SendPrivateMessage ports player.sendPrivateMessage + sendMessage: an
// offline target notifies misc:NOT_ONLINE; delivery is an aquamarine
// Notification with a [From <name>] source (both sides for the sender).
func m7SendPrivateMessage(c *playerConn, playerName string, message string) {
	target := m7PlayerByName(playerName)
	if target == nil {
		m6Notify(c, fmt.Sprintf("misc:NOT_ONLINE;username=%s", playerName))
		return
	}
	formatted := m7FormatName(c.username)
	m6NotifyWithSource(target, message, "aquamarine", "[From "+formatted+"]")
	m6NotifyWithSource(c, message, "aquamarine", "[To "+m7FormatName(target.username)+"]")
}

// m7Teleport ports character.teleport: set position, Teleport frame to the
// surrounding regions (which the Go broadcast scopes by the entity tile).
func m7Teleport(c *playerConn, x, y int) {
	c.sess.playerX = x
	c.sess.playerY = y
	setEntityPos(c.instance, x, y)
	updateClientRegion(c)
	broadcast(pkt(PacketTeleport, teleportData{Instance: c.instance, X: x, Y: y}))
}

// ---------------------------------------------------------------------------
// Player registry helpers (world/entities equivalents over the Go stub).
// ---------------------------------------------------------------------------

// m7PlayerUsernames snapshots online usernames.
func m7PlayerUsernames() []string {
	playersMu.Lock()
	defer playersMu.Unlock()
	names := make([]string, 0, len(players))
	for _, c := range players {
		if c.username != "" {
			names = append(names, c.username)
		}
	}
	return names
}

// m7PlayerByName finds an online conn by username (case-insensitive, like
// world.getPlayerByName's lowercase compare).
func m7PlayerByName(name string) *playerConn {
	lower := strings.ToLower(name)
	playersMu.Lock()
	defer playersMu.Unlock()
	for _, c := range players {
		if strings.ToLower(c.username) == lower {
			return c
		}
	}
	return nil
}

// m6NotifyWithSource is the notify() variant with a source header
// (player.notify(message, colour, message.title) → Notification Text).
func m6NotifyWithSource(c *playerConn, message string, colour string, source string) {
	col := colour
	src := source
	_ = send(c.conn, pktOp(PacketNotification, NotificationText, notificationPacketData{
		Message: message,
		Colour:  &col,
		Source:  &src,
	}))
}
