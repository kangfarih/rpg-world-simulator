// Hub console command dispatcher (ADDITIVE-ONLY): the router-role twin of
// the game console in console.go. Existing Handler/Exec are untouched; hub
// commands live behind the separate HubHandler/HubExec pair so game-console
// implementers (internal/app opsConsole, tests) need no changes.
//
// TS sources mirrored here (read-only, DO NOT import):
//   - packages/hub/src/console.ts — hub Console stdin listener: lines not
//     starting with '/' are ignored; the text after '/' is split on ' ',
//     the first block is the command. Commands: server (console.log of
//     findEmptyServer()) and player (username = blocks.join(' '); an empty
//     username logs the warning `Malformed command - Format:
//     /player [username]` via log.warning, otherwise console.log of
//     findPlayer(username)).
//   - packages/hub/src/controllers/models.ts — findEmptyServer (the first
//     server with players.length < maxPlayers - 1, else undefined) and
//     findPlayer (the first server whose players include username, else
//     undefined).
//
// Like console.go, the caller owns the stdin loop (see StartRouterConsole
// in internal/app/router.go): it reads lines and passes each one to
// HubExec with a HubHandler over the hub Server's server-list + presence
// roster. This file opens no stdin, spawns no goroutines, and performs no
// I/O itself.
package console

import (
	"fmt"
	"strings"
)

// HubCommands lists the hub slash commands dispatched by HubExec.
var HubCommands = []string{
	"server",
	"player",
}

// HubHandler applies hub console commands to the hub server-list and
// presence roster and returns the output text the caller prints
// (mirroring the TS console.log lines). "undefined" mirrors the TS
// console.log(undefined) when no server or player matches.
type HubHandler interface {
	// Server prints the emptiest/newest-target server-list entry (TS
	// findEmptyServer parity: the login target with room; "undefined"
	// when no healthy shard is registered).
	Server() string
	// Player prints the shard/presence entry hosting username (TS
	// findPlayer parity: the server containing the player; "undefined"
	// when the name is online nowhere).
	Player(username string) string
}

// HubExec parses line and dispatches it to h, returning the output text
// the caller prints. Empty output ("") means "nothing to print",
// mirroring the TS early return on non-'/' input. Unknown commands return
// an "Unknown command: <name>" error text. A bare /player returns the
// exact TS warning text and does not call the handler.
func HubExec(h HubHandler, line string) string {
	name, args := Parse(line)
	if name == "" {
		return ""
	}
	if h == nil {
		return ""
	}
	switch name {
	case "server":
		return h.Server()
	case "player":
		username := strings.Join(args, " ")
		if username == "" {
			return "Malformed command - Format: /player [username]"
		}
		return h.Player(username)
	default:
		return fmt.Sprintf("Unknown command: %s", name)
	}
}

// Divergences from TS console.ts (deliberate, shared-dispatch scope):
//   - Parsing is shared with the game console (Parse): slash prefix,
//     whitespace trimming, and lower-cased command names. TS matches the
//     hub commands case-sensitively; username args rejoin with single
//     spaces (TS blocks.join(' ')).
//   - Output is returned as a string for the caller to print; TS logs the
//     server object via console.log and the malformed-/player text via
//     log.warning. Handler implementations own the hub reads (server-list
//     pick, presence lookup) and render entries as text — TS prints the
//     live Server object reference, which has no text form to mirror.
//   - The TS findEmptyServer first-with-space pick maps to the hub
//     Server's newest healthy RUNNING target (lowest load within the
//     newest version): the emptiest login target, which is the entry the
//     router actually routes to.
