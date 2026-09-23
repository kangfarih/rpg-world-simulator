// Package console is an additive, transport-free admin console command
// dispatcher for the Go server stub. The caller owns the bufio/stdin loop:
// it reads lines and passes each one to Exec with a Handler that applies
// the command to the world. This package opens no stdin, spawns no
// goroutines, and performs no I/O itself.
//
// TS source mirrored here (read-only, DO NOT import):
//   - packages/server/src/console.ts — Console constructor stdin listener:
//     lines not starting with '/' are ignored; the text after '/' is split
//     on ' ', the first block is the command, the rest are arguments.
//     Commands: players (online count), total (registered count), update
//     (reject all + allowConnections=false), kill <username> (lethal hit),
//     kick/timeout <username> (connection.close), setadmin/setmod <username>
//     (setRank + sync), removeadmin/removemod <username> (strip rank),
//     ipban/unbanip <ip> (setIpBan + reject same-IP players), save
//     (world.save). Malformed ipban logs
//     `Malformed command, expected /<command> <ip>`.
package console

import (
	"fmt"
	"strings"
)

// Commands lists the slash commands dispatched by Exec, in the order they
// appear in the TS switch.
var Commands = []string{
	"players",
	"total",
	"update",
	"kill",
	"kick",
	"timeout",
	"setadmin",
	"setmod",
	"removeadmin",
	"removemod",
	"ipban",
	"unbanip",
	"save",
}

// Command is one parsed console line: the lower-cased slash command name
// plus its raw argument tokens (case preserved for usernames/IPs).
type Command struct {
	Name string
	Args []string
}

// Handler applies console commands to the world and returns the output
// text the caller prints (mirroring the TS log.info lines). Username
// arguments arrive with interior spaces preserved (TS blocks.join(' '));
// IP arrives as the first token only (TS blocks.shift()).
type Handler interface {
	Players() string
	Total() string
	Update() string
	Kill(username string) string
	Kick(username string) string
	Timeout(username string) string
	SetAdmin(username string) string
	SetMod(username string) string
	// RemoveAdmin strips the admin rank (TS removeadmin: setRank() default
	// None + target notify + sync; offline yields 'Player is not logged in.').
	RemoveAdmin(username string) string
	// RemoveMod strips the moderator rank (TS removemod, same shape as
	// RemoveAdmin).
	RemoveMod(username string) string
	IPBan(ip string) string
	// UnbanIP clears an IP ban (TS unbanip: database.setIpBan(ip, false)).
	// Same-IP conns stay connected (TS shares the kick loop with ipban; the
	// Go console deliberately does not re-kick on unban).
	UnbanIP(ip string) string
	Save() string
}

// Parse splits line into its slash command name and argument tokens.
// Leading/trailing whitespace (including CR/LF) is ignored; the line must
// start with '/'. The name is lower-cased; args keep their original case.
// Lines without a '/' prefix, a bare "/", or blank input return ("", nil),
// mirroring the TS early return on non-'/' input.
func Parse(line string) (name string, args []string) {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) < 2 || trimmed[0] != '/' {
		return "", nil
	}
	rest := strings.TrimSpace(trimmed[1:])
	if rest == "" {
		return "", nil
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", nil
	}
	name = strings.ToLower(fields[0])
	if len(fields) > 1 {
		args = fields[1:]
	}
	return name, args
}

// Exec parses line and dispatches it to h, returning the output text the
// caller prints. Empty output ("") means "nothing to print", mirroring the
// TS early return on non-'/' input. Unknown commands return an
// "Unknown command: <name>" error text. Commands missing a required
// argument return a usage text and do not call the handler.
func Exec(h Handler, line string) string {
	name, args := Parse(line)
	if name == "" {
		return ""
	}
	if h == nil {
		return ""
	}
	switch name {
	case "players":
		return h.Players()
	case "total":
		return h.Total()
	case "update":
		return h.Update()
	case "save":
		return h.Save()
	case "kill":
		username := strings.Join(args, " ")
		if username == "" {
			return "Usage: /kill <username>"
		}
		return h.Kill(username)
	case "kick":
		username := strings.Join(args, " ")
		if username == "" {
			return "Usage: /kick <username>"
		}
		return h.Kick(username)
	case "timeout":
		username := strings.Join(args, " ")
		if username == "" {
			return "Usage: /timeout <username>"
		}
		return h.Timeout(username)
	case "setadmin":
		username := strings.Join(args, " ")
		if username == "" {
			return "Usage: /setadmin <username>"
		}
		return h.SetAdmin(username)
	case "setmod":
		username := strings.Join(args, " ")
		if username == "" {
			return "Usage: /setmod <username>"
		}
		return h.SetMod(username)
	case "removeadmin":
		username := strings.Join(args, " ")
		if username == "" {
			return "Usage: /removeadmin <username>"
		}
		return h.RemoveAdmin(username)
	case "removemod":
		username := strings.Join(args, " ")
		if username == "" {
			return "Usage: /removemod <username>"
		}
		return h.RemoveMod(username)
	case "ipban":
		if len(args) == 0 {
			return "Malformed command, expected /ipban <ip>"
		}
		return h.IPBan(args[0])
	case "unbanip":
		if len(args) == 0 {
			return "Malformed command, expected /unbanip <ip>"
		}
		return h.UnbanIP(args[0])
	default:
		return fmt.Sprintf("Unknown command: %s", name)
	}
}

// Divergences from TS console.ts (deliberate, transport-free scope):
//   - No stdin ownership: TS calls process.openStdin in the constructor;
//     here the caller owns the bufio loop and calls Exec per line, so this
//     package imports neither os nor bufio.
//   - Tokenising uses strings.Fields (collapses runs of spaces) instead of
//     TS split(' ') (which yields empty blocks); outer CR/LF/space is
//     trimmed instead of stripping only \r\n. Command names are
//     lower-cased; TS matches case-sensitively.
//   - Username args rejoin with single spaces (TS blocks.join(' ')); exact
//     interior spacing is not preserved.
//   - Missing username args return "Usage: /<command> <username>" without
//     calling the handler; TS would look up the empty name and log
//     "Player is not logged in." The ipban/unbanip messages keep the exact TS
//     "Malformed command, expected /<command> <ip>" text (TS builds it from
//     the command name, so unbanip carries its own name).
//   - TS unbanip shares the ipban kick loop (same-IP conns are rejected with
//     'banned' even on unban) and falls through to save (missing break).
//     Neither is mirrored: UnbanIP only clears the ban (same-IP stays
//     connected) and every command returns after its own handler call.
//   - Output is returned as a string for the caller to print; TS logs via
//     log.info inside each case. Handler implementations own the world
//     effects (online lookups, hits, closes, rank sets, bans, saves).
