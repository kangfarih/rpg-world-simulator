package controller

import (
	"strings"
)

// GuildCommand ports the 'guild' case (commands.ts:108-162) over the guild
// registry: the not-in-a-guild gate keeps the exact TS string, then
// kick|rank|invite subcommands run. Unknown subcommands stay silent like the
// TS switch default.
func GuildCommand(c CommandConn, command string, blocks []string, d CommandDeps) {
	if command != "guild" {
		return
	}
	if !d.Guilds.InGuild(c.PlayerName()) {
		d.Bus.Notify(c.InstanceID(), "You are not in a guild.")
		return
	}
	if len(blocks) == 0 {
		return
	}
	sub := blocks[0]
	args := blocks[1:]
	switch sub {
	case "invite":
		d.Guilds.Invite(c, strings.Join(args, " "))
	case "kick":
		username := strings.Join(args, " ")
		if username == "" {
			d.Bus.Notify(c.InstanceID(), "Malformed command, expected /guild kick [username]")
			return
		}
		d.Guilds.Kick(c, username, true)
	case "rank":
		if len(args) < 2 {
			d.Bus.Notify(c.InstanceID(), "Malformed command, expected /guild rank [rank 0-6] [username]")
			return
		}
		d.Guilds.RankCommand(c, args[0], strings.Join(args[1:], " "))
	}
}
