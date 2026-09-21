package controller

import (
	"fmt"
	"strings"
)

// ModeratorCommands ports handleModeratorCommands. Gate: rank >= Moderator.
func ModeratorCommands(c CommandConn, command string, blocks []string, d CommandDeps) {
	if c.Rank() < CmdRankModerator {
		return
	}

	// mute/ban share one case: duration + username parse, self-ban guard,
	// mod caps, then set user.mute (save) or user.ban (sendUTF8('ban') +
	// close).
	if command == "mute" || command == "ban" {
		var duration int
		if len(blocks) > 0 {
			fmt.Sscanf(blocks[0], "%d", &duration)
			blocks = blocks[1:]
		}
		targetName := strings.ToLower(strings.Join(blocks, " "))
		if duration <= 0 || targetName == "" {
			d.Bus.Notify(c.InstanceID(), "Malformed command, expected /ban(mute) [duration] [username]")
			return
		}
		if targetName == strings.ToLower(c.PlayerName()) {
			d.Bus.Notify(c.InstanceID(), "You cannot ban yourself you silly. Thanks James.")
			return
		}
		target, ok := d.Peers.ByUsername(targetName)
		if !ok {
			d.Bus.Notify(c.InstanceID(), fmt.Sprintf("Could not find player with name: %s.", targetName))
			return
		}
		duration = ClampMuteBan(command, c.Rank(), duration)
		hours := duration
		deadline := nowMs().Add(hoursDuration(hours)).UnixMilli()
		f := d.Flags.Load(target.PlayerName())
		if command == "mute" {
			f.Mute = deadline
			d.Flags.Save(target.PlayerName(), f)
			d.Bus.Notify(c.InstanceID(), fmt.Sprintf("%s has been muted for %d hours.", target.PlayerName(), hours))
		} else {
			f.Ban = deadline
			d.Flags.Save(target.PlayerName(), f)
			// user.connection.sendUTF8('ban') + close: a raw text frame then
			// the socket close (Node close(reason, true) is a forced kick).
			d.Bus.SendBan(target.InstanceID())
			d.Bus.Close(target.InstanceID())
			d.Bus.Notify(c.InstanceID(), fmt.Sprintf("%s has been banned for %d hours.", target.PlayerName(), hours))
		}
		return
	}

	switch command {
	case "unmute": // user.mute = Date.now() - 3600 (in the past -> clear)
		username := strings.Join(blocks, " ")
		target, ok := d.Peers.ByUsername(username)
		if !ok {
			// TS notifies `Player ${uTargetName} not found.` only when the
			// name is empty; an offline unknown name just no-ops (the
			// getPlayerByName miss returns undefined and .mute throws —
			// the Go stub notifies instead, divergence noted).
			if username == "" {
				d.Bus.Notify(c.InstanceID(), "Player  not found.")
			}
			return
		}
		f := d.Flags.Load(target.PlayerName())
		f.Mute = 0
		d.Flags.Save(target.PlayerName(), f)
		d.Bus.Notify(c.InstanceID(), fmt.Sprintf("%s has been unmuted.", target.PlayerName()))

	case "kick", "forcekick":
		username := strings.Join(blocks, " ")
		if username == "" {
			d.Bus.Notify(c.InstanceID(), "Malformed command, expected /kick username")
			return
		}
		target, ok := d.Peers.ByUsername(username)
		if !ok {
			d.Bus.Notify(c.InstanceID(), fmt.Sprintf("Could not find player with name: %s", username))
			return
		}
		// connection.close(reason, force): both variants drop the socket.
		d.Bus.Close(target.InstanceID())

	case "jail":
		var duration int
		if len(blocks) > 0 {
			fmt.Sscanf(blocks[0], "%d", &duration)
			blocks = blocks[1:]
		}
		username := strings.Join(blocks, " ")
		if duration <= 0 || username == "" {
			d.Bus.Notify(c.InstanceID(), "Malformed command, expected /jail [duration] [username]")
			return
		}
		target, ok := d.Peers.ByUsername(username)
		if !ok {
			d.Bus.Notify(c.InstanceID(), fmt.Sprintf("Could not find player with name: %s.", username))
			return
		}
		duration = ClampJail(c.Rank(), duration)
		hours := duration
		f := d.Flags.Load(target.PlayerName())
		f.Jail = nowMs().Add(hoursDuration(hours)).UnixMilli()
		d.Flags.Save(target.PlayerName(), f)
		// player.sendToSpawn(): teleport to the spawn point (100,96 — the
		// m9 respawn tile) since the Go stub has no home-point registry.
		d.World.Teleport(target, 100, 96)
		d.Bus.NotifySource(target.InstanceID(), fmt.Sprintf("You have been jailed for %d hours.", hours), "crimsonred", "")
		d.Bus.Notify(c.InstanceID(), fmt.Sprintf("%s has been jailed for %d hours.", target.PlayerName(), hours))

	case "unjail":
		username := strings.Join(blocks, " ")
		if username == "" {
			d.Bus.Notify(c.InstanceID(), "Malformed command, expected /unjail [username]")
			return
		}
		target, ok := d.Peers.ByUsername(username)
		if !ok {
			d.Bus.Notify(c.InstanceID(), fmt.Sprintf("Could not find player with name: %s.", username))
			return
		}
		f := d.Flags.Load(target.PlayerName())
		f.Jail = 0
		d.Flags.Save(target.PlayerName(), f)
		d.World.Teleport(target, 100, 96)
		d.Bus.Notify(target.InstanceID(), "You have been unjailed.")
		d.Bus.Notify(c.InstanceID(), fmt.Sprintf("%s has been unjailed.", target.PlayerName()))
	}
}
