// Pipeline ports the M7 command tables and chat outcome strings as pure,
// transport-free decisions (controllers/commands.ts parity). The root
// adapter (m7.go) keeps ALL wiring — frame parsing, send/broadcast/socRoute*
// delivery, registry lookups and the m12/m13 delegation — and drives these
// helpers through the Moderation/Router seams below. Behavior (frames, rate
// limits, command outcomes) is frozen.
package chat

import "fmt"

// ---------------------------------------------------------------------------
// Transport seams (implemented by the root adapter).
// ---------------------------------------------------------------------------

// Moderation gates chat on persisted mute state (m13IsMuted at the root:
// the m13 slice persists user.mute in the players.data blob and rejects
// chat while the deadline is in the future, incoming.ts:479 parity).
type Moderation interface {
	IsMuted(username string) bool
}

// Router abstracts the three S→C chat deliveries (the m7Chat region/global
// legs plus the m7SendPrivateMessage unicasts). String-keyed so this package
// stays transport-free: no *websocket.Conn, no send/broadcast calls. The
// root adapter resolves usernames/instances to live conns.
type Router interface {
	// SendBubble delivers a region-scoped entity bubble frame
	// (player.chat → sendToRegions parity).
	SendBubble(instance, message string, withBubble bool, colour string)
	// SendGlobal delivers a [Global]-prefixed static line
	// (world.globalMessage parity).
	SendGlobal(source, message, colour string)
	// SendSourced delivers a static line with a source header
	// (player.notify(message, colour, title) parity — the re-homed
	// m6NotifyWithSource shape, see SourceNotice).
	SendSourced(username, message, colour, source string)
}

// ---------------------------------------------------------------------------
// Source notices (m6NotifyWithSource re-homed: the notify() variant with a
// source header lived in m7.go under an m6* name; its payload shape now
// lives here and the root keeps only the send wrapper).
// ---------------------------------------------------------------------------

// SourceNotice is the transport-free form of the sourced Notification Text
// frame (player.notify(message, colour, message.title) parity).
type SourceNotice struct {
	Message string
	Colour  string
	Source  string
}

// Notice builds a SourceNotice.
func Notice(message, colour, source string) SourceNotice {
	return SourceNotice{Message: message, Colour: colour, Source: source}
}

// PMSources ports m7SendPrivateMessage's dual-source outcome: the recipient
// sees [From <sender>], the sender sees [To <target>], both aquamarine
// (player.sendPrivateMessage + sendMessage parity).
func PMSources(fromDisplay, toDisplay string) (recipientSource, senderSource string) {
	return "[From " + fromDisplay + "]", "[To " + toDisplay + "]"
}

// ---------------------------------------------------------------------------
// Command tables (controllers/commands.ts).
// ---------------------------------------------------------------------------

// PlayerCommand enumerates the m7 player-table commands
// (handlePlayerCommands subset meaningful in the stub world: players,
// coords, ping, g/gc/global, pm/msg).
type PlayerCommand int

const (
	CmdPlayers PlayerCommand = iota + 1
	CmdCoords
	CmdPing
	CmdGlobal
	CmdPM
)

// ClassifyPlayer maps a parsed command word to the m7 player table. ok is
// false for words the m7 table does not handle (the m12/m13 tables still run
// for every command — m7ParseCommand delegation order is unchanged).
func ClassifyPlayer(command string) (PlayerCommand, bool) {
	switch command {
	case "players":
		return CmdPlayers, true
	case "coords":
		return CmdCoords, true
	case "ping":
		return CmdPing, true
	case "g", "gc", "global":
		return CmdGlobal, true
	case "pm", "msg":
		return CmdPM, true
	}
	return 0, false
}

// ModCommand enumerates the m7 moderator-table commands
// (handleModeratorCommands subset: /teleport).
type ModCommand int

const (
	ModTeleport ModCommand = iota + 1
)

// ClassifyMod maps a parsed command word to the m7 moderator table.
func ClassifyMod(command string) (ModCommand, bool) {
	if command == "teleport" {
		return ModTeleport, true
	}
	return 0, false
}

// ModAllowed ports the rank gate mirroring the isMod/isAdmin/isHollowAdmin
// early return: moderator commands run at Modules.Ranks Moderator and above.
func ModAllowed(rank int) bool {
	return rank >= RankModerator
}

// ---------------------------------------------------------------------------
// Outcome texts (exact user-visible strings, frozen).
// ---------------------------------------------------------------------------

// PlayersSummary ports the /players population line.
func PlayersSummary(population int) string {
	if population == 1 {
		return "There is currently 1 person online."
	}
	return fmt.Sprintf("There are currently %d people online.", population)
}

// CoordsText ports the /coords reply.
func CoordsText(x, y int) string {
	return fmt.Sprintf("x: %d y: %d", x, y)
}

// GlobalCooldownNotice ports the global-chat cooldown rejection
// (misc:CANNOT_GLOBAL_CHAT_MINUTES with the whole-minutes duration).
func GlobalCooldownNotice(duration int) string {
	return fmt.Sprintf("misc:CANNOT_GLOBAL_CHAT_MINUTES;duration=%d", duration)
}

// PMOffline ports the offline-target reply (misc:NOT_ONLINE).
func PMOffline(playerName string) string {
	return fmt.Sprintf("misc:NOT_ONLINE;username=%s", playerName)
}

// MutedText ports the mute-gate rejection.
func MutedText() string {
	return "You have been muted."
}
