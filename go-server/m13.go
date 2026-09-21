// M13 slice — the full commands.ts port (finishes the M7 command half).
//
// Port of controllers/commands.ts: handlePlayerCommands guild subcommand +
// the complete handleModeratorCommands (mute/unmute/ban/kick/jail/un jail)
// and handleAdminCommands tables. Commands needing subsystems the Go stub
// does not model (abilities, pets, poison, bosses, hub regions) are skipped
// the way the original m7 subset skipped them — no invented behavior.
//
// Admin/mod state (mute/ban/jail deadlines, noclip, mspeed) persists in a
// dedicated m13flags SQLite table (the m11 tables precedent — the players.data
// blob is owned by the m5 equip writer, so a separate table avoids the
// write-vs-flush fight).
//
// Debug dispatcher: [46 {"m13test":...}] (TESTMAP only) seeds state + echoes
// it back, mirroring m9test/m11test.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// TS parity constants.
// ---------------------------------------------------------------------------

// Opcodes.Pointer (opcodes.ts): Location0 Entity1 Remove3 (m8.go ports the
// enum; re-declared here for locality is pointless — use the m8 constants).
//
// Modules.Effects values used by /toggle (modules.ts:271 enum order).
const (
	EffectTerrorStatus = 3
	EffectStun         = 4
	EffectFreezingM13  = 17 // duplicate of m10's effectFreezing (same enum)
	EffectInvincibleM  = 18
	EffectBurningM13   = 16
)

// Modules.Ranks (modules.ts:327): None0 Moderator1 Admin2 Veteran3 Patron4
// Artist5 Cheater6 TierOne7 TierTwo8 TierThree9 Landlord10.
const (
	RankLandlordM13 = 10
)

// Mod caps from commands.ts (hours).
const (
	m13ModMuteCap = 168
	m13ModBanCap  = 72
	m13ModJailCap = 12
)

// m13TPSpots mirrors the /tp [key] switch (commands.ts 'tp'): hardcoded
// map destinations, teleporting with animation.
var m13TPSpots = map[string][2]int{
	"home":              {191, 166},
	"underwater":        {119, 290},
	"underwatervolcano": {194, 427},
	"kok":               {290, 357},
	"mountain":          {440, 312},
	"santa":             {526, 256},
	"ice":               {608, 332},
	"pink":              {685, 433},
	"hell":              {1112, 787},
	"sewer":             {1117, 709},
	"shroom":            {992, 631},
	"skeletonking":      {120, 794},
	"ogrelord":          {342, 166},
	"queenant":          {591, 820},
	"forestdragon":      {457, 807},
	"crystalcave":       {886, 622},
	"piratecaptain":     {930, 752},
}

// ---------------------------------------------------------------------------
// Persisted player state (m13flags table): mute/ban/jail ms deadlines
// (epoch ms; 0/absent = clear), noclip flag, movement speed override.
// ---------------------------------------------------------------------------

type m13Flags struct {
	Mute   int64 // epoch ms
	Ban    int64 // epoch ms
	Jail   int64 // epoch ms
	Noclip bool
	MSpeed int
}

// m13EnsureTables creates the flags table up-front (called from main()).
func m13EnsureTables() {
	if dbConn == nil {
		return
	}
	dbMu.Lock()
	defer dbMu.Unlock()
	if _, err := dbConn.Exec(`CREATE TABLE IF NOT EXISTS m13flags(` +
		`player TEXT PRIMARY KEY, mute INT, ban INT, jail INT, noclip INT, mspeed INT)`); err != nil {
		log.Fatalf("m13: ddl: %v", err)
	}
}

// m13FlagsFor loads the persisted flags for a username. Missing row = zero
// flags (mute/ban/jail 0, noclip false, mspeed 0 = server default).
func m13FlagsFor(username string) m13Flags {
	f := m13Flags{}
	if dbConn == nil || username == "" {
		return f
	}
	dbMu.Lock()
	defer dbMu.Unlock()
	var noclip int
	if err := dbConn.QueryRow(
		`SELECT mute,ban,jail,noclip,mspeed FROM m13flags WHERE player=?`, username,
	).Scan(&f.Mute, &f.Ban, &f.Jail, &noclip, &f.MSpeed); err != nil {
		return m13Flags{}
	}
	f.Noclip = noclip != 0
	return f
}

// m13SaveFlags upserts the flags row.
func m13SaveFlags(username string, f m13Flags) {
	if dbConn == nil || username == "" {
		return
	}
	dbMu.Lock()
	defer dbMu.Unlock()
	noclip := 0
	if f.Noclip {
		noclip = 1
	}
	if _, err := dbConn.Exec(
		`INSERT INTO m13flags(player,mute,ban,jail,noclip,mspeed) VALUES(?,?,?,?,?,?) `+
			`ON CONFLICT(player) DO UPDATE SET mute=?,ban=?,jail=?,noclip=?,mspeed=?`,
		username, f.Mute, f.Ban, f.Jail, noclip, f.MSpeed,
		f.Mute, f.Ban, f.Jail, noclip, f.MSpeed); err != nil {
		log.Printf("m13: save flags %s: %v", username, err)
	}
}

// m13IsMuted reports whether the user's mute deadline is in the future.
func m13IsMuted(username string) bool {
	f := m13FlagsFor(username)
	return f.Mute > time.Now().UnixMilli()
}

// m13IsBanned reports whether the user's ban deadline is in the future.
func m13IsBanned(username string) bool {
	f := m13FlagsFor(username)
	return f.Ban > time.Now().UnixMilli()
}

// m13IsJailed reports whether the user's jail deadline is in the future.
func m13IsJailed(username string) bool {
	f := m13FlagsFor(username)
	return f.Jail > time.Now().UnixMilli()
}

// m13NoclipAllowed reports whether the user may pass blocked tiles
// (player.noclip). Called from the movement anti-cheat path.
func m13NoclipAllowed(username string) bool {
	return m13FlagsFor(username).Noclip
}

// m13MovementSpeed returns the user's overrideMovementSpeed (0 = default).
func m13MovementSpeed(username string) int {
	return m13FlagsFor(username).MSpeed
}

// ---------------------------------------------------------------------------
// Parse entry (extends the m7 dispatcher).
// ---------------------------------------------------------------------------

// m13ParseCommand runs the M13 command tables after the m7/m12 ones. The
// m7 player table handles players/coords/ping/g/pm; m13 adds guild.
func m13ParseCommand(c *playerConn, command string, blocks []string) {
	m13GuildCommand(c, command, blocks)
	m13ModeratorCommands(c, command, blocks)
	m13AdminCommands(c, command, blocks)
}

// ---------------------------------------------------------------------------
// Player commands: /guild kick|rank (commands.ts 'guild').
// ---------------------------------------------------------------------------

// m13GuildCommand ports the 'guild' case (commands.ts:108-162) over the real
// guild registry (social_wire.go): the not-in-a-guild gate keeps the exact TS
// string, then kick|rank|invite subcommands run. Unknown subcommands stay
// silent like the TS switch default.
func m13GuildCommand(c *playerConn, command string, blocks []string) {
	if command != "guild" {
		return
	}
	if _, err := socGuilds.GuildOf(c.username); err != nil {
		m6Notify(c, "You are not in a guild.")
		return
	}
	if len(blocks) == 0 {
		return
	}
	sub := blocks[0]
	args := blocks[1:]
	switch sub {
	case "invite":
		socGuildInvite(c, strings.Join(args, " "))
	case "kick":
		username := strings.Join(args, " ")
		if username == "" {
			m6Notify(c, "Malformed command, expected /guild kick [username]")
			return
		}
		socGuildKick(c, username, true)
	case "rank":
		if len(args) < 2 {
			m6Notify(c, "Malformed command, expected /guild rank [rank 0-6] [username]")
			return
		}
		socGuildRankCommand(c, args[0], strings.Join(args[1:], " "))
	}
}

// ---------------------------------------------------------------------------
// Moderator commands (handleModeratorCommands). Gate: rank >= Moderator.
// ---------------------------------------------------------------------------

func m13ModeratorCommands(c *playerConn, command string, blocks []string) {
	if chatStateFor(c).rank < RankModerator {
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
			m6Notify(c, "Malformed command, expected /ban(mute) [duration] [username]")
			return
		}
		if targetName == strings.ToLower(c.username) {
			m6Notify(c, "You cannot ban yourself you silly. Thanks James.")
			return
		}
		target := m7PlayerByName(targetName)
		if target == nil {
			m6Notify(c, fmt.Sprintf("Could not find player with name: %s.", targetName))
			return
		}
		if chatStateFor(c).rank == RankModerator {
			if command == "mute" && duration > m13ModMuteCap {
				duration = m13ModMuteCap
			}
			if command == "ban" && duration > m13ModBanCap {
				duration = m13ModBanCap
			}
		}
		hours := duration
		deadline := time.Now().Add(time.Duration(duration) * time.Hour).UnixMilli()
		f := m13FlagsFor(target.username)
		if command == "mute" {
			f.Mute = deadline
			m13SaveFlags(target.username, f)
			m6Notify(c, fmt.Sprintf("%s has been muted for %d hours.", target.username, hours))
		} else {
			f.Ban = deadline
			m13SaveFlags(target.username, f)
			// user.connection.sendUTF8('ban') + close: a raw text frame then
			// the socket close (Node close(reason, true) is a forced kick).
			writeMu.Lock()
			_ = target.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			_ = target.conn.WriteMessage(websocket.TextMessage, []byte("ban"))
			writeMu.Unlock()
			removeClient(target.conn)
			m6Notify(c, fmt.Sprintf("%s has been banned for %d hours.", target.username, hours))
		}
		return
	}

	switch command {
	case "unmute": // user.mute = Date.now() - 3600 (in the past -> clear)
		username := strings.Join(blocks, " ")
		target := m7PlayerByName(username)
		if target == nil {
			// TS notifies `Player ${uTargetName} not found.` only when the
			// name is empty; an offline unknown name just no-ops (the
			// getPlayerByName miss returns undefined and .mute throws —
			// the Go stub notifies instead, divergence noted).
			if username == "" {
				m6Notify(c, "Player  not found.")
			}
			return
		}
		f := m13FlagsFor(target.username)
		f.Mute = 0
		m13SaveFlags(target.username, f)
		m6Notify(c, fmt.Sprintf("%s has been unmuted.", target.username))

	case "kick", "forcekick":
		username := strings.Join(blocks, " ")
		if username == "" {
			m6Notify(c, "Malformed command, expected /kick username")
			return
		}
		target := m7PlayerByName(username)
		if target == nil {
			m6Notify(c, fmt.Sprintf("Could not find player with name: %s", username))
			return
		}
		// connection.close(reason, force): both variants drop the socket.
		removeClient(target.conn)

	case "jail":
		var duration int
		if len(blocks) > 0 {
			fmt.Sscanf(blocks[0], "%d", &duration)
			blocks = blocks[1:]
		}
		username := strings.Join(blocks, " ")
		if duration <= 0 || username == "" {
			m6Notify(c, "Malformed command, expected /jail [duration] [username]")
			return
		}
		target := m7PlayerByName(username)
		if target == nil {
			m6Notify(c, fmt.Sprintf("Could not find player with name: %s.", username))
			return
		}
		if chatStateFor(c).rank == RankModerator && duration > m13ModJailCap {
			duration = m13ModJailCap
		}
		hours := duration
		f := m13FlagsFor(target.username)
		f.Jail = time.Now().Add(time.Duration(duration) * time.Hour).UnixMilli()
		m13SaveFlags(target.username, f)
		// player.sendToSpawn(): teleport to the spawn point (100,96 — the
		// m9 respawn tile) since the Go stub has no home-point registry.
		m7Teleport(target, 100, 96)
		m6NotifyWithSource(target, fmt.Sprintf("You have been jailed for %d hours.", hours), "crimsonred", "")
		m6Notify(c, fmt.Sprintf("%s has been jailed for %d hours.", target.username, hours))

	case "unjail":
		username := strings.Join(blocks, " ")
		if username == "" {
			m6Notify(c, "Malformed command, expected /unjail [username]")
			return
		}
		target := m7PlayerByName(username)
		if target == nil {
			m6Notify(c, fmt.Sprintf("Could not find player with name: %s.", username))
			return
		}
		f := m13FlagsFor(target.username)
		f.Jail = 0
		m13SaveFlags(target.username, f)
		m7Teleport(target, 100, 96)
		m6Notify(target, "You have been unjailed.")
		m6Notify(c, fmt.Sprintf("%s has been unjailed.", target.username))
	}
}

// ---------------------------------------------------------------------------
// Admin commands (handleAdminCommands). Gate: rank >= Admin.
// ---------------------------------------------------------------------------

func m13AdminCommands(c *playerConn, command string, blocks []string) {
	if chatStateFor(c).rank < RankAdmin {
		return
	}

	switch command {
	// --- item/inventory -----------------------------------------------------
	case "spawn": // /spawn [key] [count]
		if len(blocks) == 0 {
			return
		}
		key := blocks[0]
		count := 1
		if len(blocks) > 1 {
			fmt.Sscanf(blocks[1], "%d", &count)
		}
		if count < 1 {
			count = 1
		}
		if m6ItemInfoFor(key) == nil {
			m6Notify(c, fmt.Sprintf("No item with key %s exists.", key))
			return
		}
		idx := m5AddItem(c.username, key, count)
		m6Notify(c, fmt.Sprintf("Spawned %dx %s (slot %d).", count, key, idx))

	case "take": // /take [index] [container=bank/inventory] [username]
		var index int
		container, username := "", ""
		if len(blocks) > 0 {
			fmt.Sscanf(blocks[0], "%d", &index)
		}
		if len(blocks) > 1 {
			container = blocks[1]
		}
		if len(blocks) > 2 {
			username = strings.Join(blocks[2:], " ")
		}
		if index < 1 || username == "" {
			m6Notify(c, "Invalid command, usage /take [index] [container=bank/inventory] [username]")
			return
		}
		target := m7PlayerByName(username)
		if target == nil {
			m6Notify(c, fmt.Sprintf("Player %s not found.", username))
			return
		}
		slot, ok := m13ContainerSlot(target.username, container, index)
		if !ok || slot.Key == "" {
			m6Notify(c, fmt.Sprintf("Player %s has no item at index %d.", username, index))
			return
		}
		m13ContainerRemove(target.username, container, index, slot.Count)
		m6Notify(c, fmt.Sprintf("Took %dx %s from %s.", slot.Count, slot.Key, username))

	case "takeitem": // /takeitem [key] [count] [container] [username]
		key, container, username := "", "", ""
		count := 0
		if len(blocks) > 0 {
			key = blocks[0]
		}
		if len(blocks) > 1 {
			fmt.Sscanf(blocks[1], "%d", &count)
		}
		if len(blocks) > 2 {
			container = blocks[2]
		}
		if len(blocks) > 3 {
			username = strings.Join(blocks[3:], " ")
		}
		if key == "" || username == "" || (container != "inventory" && container != "bank") {
			m6Notify(c, "Invalid command, usage /takeitem [key] [count] [container=bank/inventory] [username]")
			return
		}
		target := m7PlayerByName(username)
		if target == nil {
			m6Notify(c, fmt.Sprintf("Player %s not found.", username))
			return
		}
		m13ContainerRemoveKey(target.username, container, key, count)
		m6Notify(c, fmt.Sprintf("Took %dx %s from %s.", count, key, username))

	case "copybank", "copyinventory": // clone the target container
		username := strings.Join(blocks, " ")
		if username == "" {
			m6Notify(c, fmt.Sprintf("Invalid command, usage /%s [username]", command))
			return
		}
		target := m7PlayerByName(username)
		if target == nil {
			m6Notify(c, fmt.Sprintf("Player %s is not online.", username))
			return
		}
		m13CopyContainer(target.username, c.username, command == "copybank")
		m6Notify(c, fmt.Sprintf("Copied %s's %s to your %s.", username, command, command))

	case "drop": // spawn a ground item at the player's feet
		if len(blocks) == 0 {
			return
		}
		key := blocks[0]
		count := 1
		if len(blocks) > 1 {
			fmt.Sscanf(blocks[1], "%d", &count)
		}
		if count < 1 {
			count = 1
		}
		m5SpawnLootAt(c.username, key, count, c.sess.playerX, c.sess.playerY)

	case "remove": // /remove [key] [count] from own inventory
		if len(blocks) < 2 {
			return
		}
		count := 0
		fmt.Sscanf(blocks[1], "%d", &count)
		if count < 1 {
			return
		}
		m13ContainerRemoveKey(c.username, "inventory", blocks[0], count)

	case "empty":
		m13EmptyContainer(c.username, "inventory")

	case "clear": // forEachSlot remove — same end state for the inventory
		m13EmptyContainer(c.username, "inventory")

	// --- teleport -----------------------------------------------------------
	case "teleport": // admin version accepts the withAnimation flag
		if len(blocks) < 2 {
			return
		}
		var x, y, anim int
		fmt.Sscanf(blocks[0], "%d", &x)
		fmt.Sscanf(blocks[1], "%d", &y)
		if x != 0 && y != 0 {
			_ = anim // animation flag is client-rendered; Go teleport is instant
			m7Teleport(c, x, y)
		}

	case "teletome":
		target := m7PlayerByName(strings.Join(blocks, " "))
		if target != nil {
			m7Teleport(target, c.sess.playerX, c.sess.playerY)
		}

	case "teleto":
		target := m7PlayerByName(strings.Join(blocks, " "))
		if target != nil {
			m7Teleport(c, target.sess.playerX, target.sess.playerY)
		}

	case "teleall": // every online player -> the admin's tile
		for _, name := range m7PlayerUsernames() {
			if p := m7PlayerByName(name); p != nil && p.instance != c.instance {
				m7Teleport(p, c.sess.playerX, c.sess.playerY)
			}
		}

	case "tp":
		if len(blocks) == 0 {
			return
		}
		if spot, ok := m13TPSpots[blocks[0]]; ok {
			m7Teleport(c, spot[0], spot[1])
		}

	// --- character ----------------------------------------------------------
	case "immortal", "nohit", "invincible": // Invincible effect toggle
		m13ToggleEffect(c, EffectInvincibleM)

	case "toggle":
		m13ToggleCommand(c, blocks)

	case "hide": // visibility flip: Despawn/Spawn to the surrounding regions
		m13ToggleHide(c)

	case "ms": // /ms [speed] -> overrideMovementSpeed (75 floor, 2000 ceiling)
		if len(blocks) == 0 {
			m6Notify(c, "No movement speed specified.")
			return
		}
		speed := 0
		fmt.Sscanf(blocks[0], "%d", &speed)
		if speed <= 0 {
			m6Notify(c, "Invalid movement speed specified.")
			return
		}
		if speed > 10000 {
			speed = 2000
		}
		if speed < 75 {
			speed = 75
		}
		f := m13FlagsFor(c.username)
		f.MSpeed = speed
		m13SaveFlags(c.username, f)
		c.sess.movementSpeed = speed // live too (checkSpeed reads the session)

	case "noclip":
		f := m13FlagsFor(c.username)
		f.Noclip = !f.Noclip
		m13SaveFlags(c.username, f)
		m6Notify(c, fmt.Sprintf("Noclip: %v", f.Noclip))

	case "kill": // /kill username|instance — full-damage hit
		username := strings.Join(blocks, " ")
		if username == "" {
			m6Notify(c, "Malformed command, expected /kill username/instance")
			return
		}
		if target := m7PlayerByName(username); target != nil {
			m9DamagePlayer(target, m9PlayerHP(target), nil)
			return
		}
		if m := m9MobFor(username); m != nil {
			m9PlayerHit(m, nil, m9MobHP(m))
		}

	case "nuke": // kill every character in the region (players only with `all`)
		all := len(blocks) > 0 && blocks[0] != ""
		px, py := c.sess.playerX, c.sess.playerY
		for _, name := range m7PlayerUsernames() {
			p := m7PlayerByName(name)
			if p == nil || p.instance == c.instance {
				continue
			}
			if !all {
				continue // region scan: the Go stub region == the 9-region set;
				// players on the admin's tile region only — keep it simple and
				// kill same-tile-region players (documented divergence).
			}
			if m10SameRegion(px, py, p.sess.playerX, p.sess.playerY) {
				m9DamagePlayer(p, m9PlayerHP(p), nil)
			}
		}
		m6Notify(c, "Congratulations, you killed everyone, are you happy with yourself?")

	case "aoe": // self-hit for 600 (commands.ts 'aoe')
		m9DamagePlayer(c, 600, nil)

	// --- mobs ---------------------------------------------------------------
	case "mob": // /mob [key] -> spawnMob at the player's tile
		if len(blocks) == 0 {
			m6Notify(c, "No mob specified.")
			return
		}
		inst := fmt.Sprintf("m13-%d", time.Now().UnixNano()%1000000)
		if !m9SpawnMob(inst, blocks[0], c.sess.playerX, c.sess.playerY, m9Overrides{Chase: true}) {
			m6Notify(c, fmt.Sprintf("No mob with key %s exists.", blocks[0]))
		}

	case "movenpc": // /movenpc [instance] [x] [y] — mobs only (isMob gate)
		if len(blocks) < 3 {
			m6Notify(c, "Malformed command, expected /movenpc instance x y")
			return
		}
		var x, y int
		fmt.Sscanf(blocks[1], "%d", &x)
		fmt.Sscanf(blocks[2], "%d", &y)
		if m := m9MobFor(blocks[0]); m != nil {
			m13SetMobPos(m, x, y)
		} else {
			m6Notify(c, "Entity not found.")
		}

	case "nvn": // NPC vs NPC
		if len(blocks) < 2 {
			m6Notify(c, "Malformed command, expected /nvn instance target")
			return
		}
		a, b := m9MobFor(blocks[0]), m9MobFor(blocks[1])
		if a == nil || b == nil {
			m6Notify(c, "Could not find entity instances specified.")
			return
		}
		m13MobAttack(a, b)
		m6Notify(c, fmt.Sprintf("%s is attacking %s", a.key, b.key))

	case "allattack": // every mob in the region attacks the target instance
		if len(blocks) == 0 {
			m6Notify(c, "Invalid command. Usage: /allattack [target_instance]")
			return
		}
		if m9MobFor(blocks[0]) == nil {
			return
		}
		px, py := c.sess.playerX, c.sess.playerY
		for inst := range m9MobsSnapshot() {
			m := m9MobFor(inst)
			if m == nil || inst == blocks[0] {
				continue
			}
			if m10SameRegion(px, py, m.x, m.y) {
				m13MobAttackTarget(m, blocks[0])
			}
		}

	case "roam":
		m6Notify(c, "All mobs in the region will now roam!")
		px, py := c.sess.playerX, c.sess.playerY
		for inst := range m9MobsSnapshot() {
			m := m9MobFor(inst)
			if m == nil {
				continue
			}
			if m10SameRegion(px, py, m.x, m.y) {
				m13MobRoam(m)
			}
		}

	case "find": // /find [npcKey]
		m13FindNPC(c, strings.Join(blocks, " "))

	// --- world/info ---------------------------------------------------------
	case "getregion":
		m6Notify(c, fmt.Sprintf("Current Region: %d", regionOf(c.sess.playerX, c.sess.playerY)))

	case "collision": // /collision [x] [y]
		x, y := c.sess.playerX, c.sess.playerY
		if len(blocks) > 0 {
			fmt.Sscanf(blocks[0], "%d", &x)
		}
		if len(blocks) > 1 {
			fmt.Sscanf(blocks[1], "%d", &y)
		}
		m6Notify(c, fmt.Sprintf("%v - index: %d", tileBlocked(x, y), y*worldWidth()+x))

	case "distance": // Utils.getDistance is the Manhattan distance
		if len(blocks) < 2 {
			m6Notify(c, "Malformed command, expected /distance x y")
			return
		}
		var x, y int
		fmt.Sscanf(blocks[0], "%d", &x)
		fmt.Sscanf(blocks[1], "%d", &y)
		dx, dy := c.sess.playerX-x, c.sess.playerY-y
		if dx < 0 {
			dx = -dx
		}
		if dy < 0 {
			dy = -dy
		}
		m6Notify(c, fmt.Sprintf("Distance: %d", dx+dy))

	case "togglepvp": // force every player into PVP mode
		for _, name := range m7PlayerUsernames() {
			if p := m7PlayerByName(name); p != nil {
				m10UpdatePVP(p, true)
			}
		}

	case "countdown": // CountdownPacket broadcast to nearby regions
		if len(blocks) == 0 {
			m6Notify(c, "Malformed command, expected /countdown time")
			return
		}
		var t int
		fmt.Sscanf(blocks[0], "%d", &t)
		if t > 0 {
			broadcast(pkt(PacketCountdown, map[string]any{"instance": c.instance, "time": t}))
		}

	case "popup": // hardcoded quest popup from commands.ts
		m11SendPopup(c, "New Quest Found!", "@blue@New @darkblue@quest @green@has@red@ been discovered!", "#00000")

	// --- quest/achievement --------------------------------------------------
	case "undostage":
		m13QuestStageShift(c, firstBlock(blocks), -1)

	case "resetquest":
		m13QuestReset(c, firstBlock(blocks))

	case "resetquests":
		m13QuestResetAll(c)

	case "finishquest":
		m13QuestFinish(c, firstBlock(blocks))

	case "resetachievements":
		m13AchievementsReset(c)

	case "finishachievement":
		m13AchievementFinish(c, firstBlock(blocks))

	case "finishachievements":
		m13AchievementsFinishAll(c)

	// --- misc ---------------------------------------------------------------
	case "timeout": // reject the commander's own connection
		removeClient(c.conn)

	case "testitems": // 100x every items.json key into the bank
		m13TestItems(c)

	case "lootbag": // hardcoded loot bag (oldonesblade/froghelm/1500 gold)
		m5SpawnLootBag(c.username, c.sess.playerX, c.sess.playerY,
			[]m5Drop{{Key: "oldonesblade", Count: 1}, {Key: "froghelm", Count: 1}, {Key: "gold", Count: 1500}})
	}
}

// ---------------------------------------------------------------------------
// Admin helpers (thin wrappers so the switch above stays readable).
// ---------------------------------------------------------------------------

func firstBlock(blocks []string) string {
	if len(blocks) == 0 {
		return ""
	}
	return blocks[0]
}

// m13ContainerSlot reads one slot by the TS 0-based array index (container.get
// returns slots[index] directly; the /take parse only rejects 0 via !index).
func m13ContainerSlot(username, container string, index int) (m5Slot, bool) {
	st := m5StateFor(username)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	var slots []m5Slot
	if container == "inventory" {
		slots = st.Inv
	} else {
		slots = st.Bank
	}
	if index < 0 || index >= len(slots) {
		return m5Slot{}, false
	}
	return slots[index], true
}

// m13ContainerRemove removes count from the slot at the TS 0-based index
// (container.remove(index, count)) and syncs the client if online.
func m13ContainerRemove(username, container string, index, count int) {
	st := m5StateFor(username)
	pstateMu.Lock()
	var slots *[]m5Slot
	if container == "inventory" {
		slots = &st.Inv
	} else {
		slots = &st.Bank
	}
	if index < 0 || index >= len(*slots) {
		pstateMu.Unlock()
		return
	}
	s := (*slots)[index]
	s.Count -= count
	result := s
	if s.Count > 0 {
		(*slots)[index] = s
	} else {
		*slots = append((*slots)[:index], (*slots)[index+1:]...)
		result = m5Slot{}
	}
	pstateMu.Unlock()
	markDirty(username)
	// container.remove fires the removeCallback -> the victim's client gets
	// a Container Remove sync (the admin sees nothing).
	if c := m7ConnByUsername(username); c != nil {
		ctype := ContainerTypeBank
		if container == "inventory" {
			ctype = ContainerTypeInventory
		}
		_ = send(c.conn, pktOp(PacketContainer, ContainerRemove, containerData{
			Type: ctype,
			Slot: &slotData{Index: index, Key: result.Key, Count: result.Count},
		}))
	}
}

// m13ContainerRemoveKey removes count of key from the container (like
// container.removeItem: first-match key scan).
func m13ContainerRemoveKey(username, container, key string, count int) {
	remaining := count
	st := m5StateFor(username)
	pstateMu.Lock()
	var slots *[]m5Slot
	if container == "inventory" {
		slots = &st.Inv
	} else {
		slots = &st.Bank
	}
	for i := len(*slots) - 1; i >= 0 && remaining > 0; i-- {
		if (*slots)[i].Key != key {
			continue
		}
		take := (*slots)[i].Count
		if take > remaining {
			take = remaining
		}
		(*slots)[i].Count -= take
		remaining -= take
		if (*slots)[i].Count <= 0 {
			*slots = append((*slots)[:i], (*slots)[i+1:]...)
		}
	}
	pstateMu.Unlock()
	if remaining < count {
		markDirty(username)
		// The victim's client syncs via Container Remove (removeItem ->
		// removeCallback, same path as m13ContainerRemove).
		if c := m7ConnByUsername(username); c != nil {
			ctype := ContainerTypeBank
			if container == "inventory" {
				ctype = ContainerTypeInventory
			}
			_ = send(c.conn, pktOp(PacketContainer, ContainerRemove, containerData{
				Type: ctype,
				Slot: &slotData{Index: 0, Key: key, Count: remaining2(remaining, count)},
			}))
		}
	}
}

// remaining2 reports 0 when every requested count was removed (full-stack
// clear serializes Count 0 = empty slot).
func remaining2(remaining, count int) int {
	if remaining == 0 {
		return 0
	}
	return count - remaining
}

// m13BankCount sums the bank stacks of one key (bank echo helper).
func m13BankCount(username, itemKey string) int {
	st := m5StateFor(username)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	n := 0
	for _, s := range st.Bank {
		if s.Key == itemKey {
			n += s.Count
		}
	}
	return n
}

// m13EmptyContainer drops every slot (container.empty()).
func m13EmptyContainer(username, container string) {
	st := m5StateFor(username)
	pstateMu.Lock()
	if container == "inventory" {
		st.Inv = nil
	} else {
		st.Bank = nil
	}
	pstateMu.Unlock()
	markDirty(username)
}

// m13CopyContainer clones src's container into dst (copybank/copyinventory).
func m13CopyContainer(src, dst string, bank bool) {
	stSrc := m5StateFor(src)
	stDst := m5StateFor(dst)
	pstateMu.Lock()
	if bank {
		clone := append([]m5Slot(nil), stSrc.Bank...)
		stDst.Bank = clone
	} else {
		clone := append([]m5Slot(nil), stSrc.Inv...)
		stDst.Inv = clone
	}
	pstateMu.Unlock()
	markDirty(dst)
}

// m5SpawnLootAt wraps m5SpawnLoot with an explicit tile (the /drop path
// spawns without a killer gate — no drop-table roll, the exact key).
func m5SpawnLootAt(owner, key string, count, x, y int) {
	m5SpawnLootBag(owner, x, y, []m5Drop{{Key: key, Count: count}})
}

// m5SpawnLootBag creates a loot entity with the given exact items (used by
// /drop and /lootbag; TS spawns Item/LootBag entities directly).
func m5SpawnLootBag(owner string, cx, cy int, items []m5Drop) {
	if len(items) == 0 {
		return
	}
	lx, ly := m5NearWalkable(cx, cy)
	lootMu.Lock()
	lootSeq++
	inst := fmt.Sprintf("loot-%d", lootSeq)
	l := &m5Loot{Instance: inst, Bag: len(items) > 1, Items: items, X: lx, Y: ly, Owner: owner}
	loots[inst] = l
	lootMu.Unlock()
	setEntityPos(inst, lx, ly)
	var payload EntityData
	if l.Bag {
		payload = EntityData{Instance: inst, Type: EntityLootBag, Key: "lootbag", Name: "Loot Bag", X: lx, Y: ly}
	} else {
		payload = EntityData{Instance: inst, Type: EntityItem, Key: items[0].Key, Name: items[0].Key, X: lx, Y: ly, Count: intp(items[0].Count)}
	}
	broadcast(pkt(PacketSpawn, payload))
	log.Printf("m13: loot %s spawned (%s x%d) at %d,%d owner=%s bag=%v", inst, items[0].Key, items[0].Count, lx, ly, owner, l.Bag)
}

// m7ConnByUsername finds the conn for a persisted-state mutation target.
func m7ConnByUsername(username string) *playerConn {
	return m7PlayerByName(username)
}

// m9MobHP snapshots a mob's current HP.
func m9MobHP(m *m9Mob) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hp
}

// m13SetMobPos ports mob.setPosition: registry + roam origin move.
func m13SetMobPos(m *m9Mob, x, y int) {
	m.mu.Lock()
	m.x, m.y = x, y
	m.spawnX, m.spawnY = x, y
	m.mu.Unlock()
	setEntityPos(m.instance, x, y)
	broadcast(pkt(PacketTeleport, teleportData{Instance: m.instance, X: x, Y: y}))
}

// m13MobAttack makes mob a attack mob b (combat.attack over Character refs).
func m13MobAttack(a, b *m9Mob) {
	a.mu.Lock()
	a.target = b.instance
	a.lastTgt = time.Now()
	a.mu.Unlock()
}

// m13MobAttackTarget points a mob at an arbitrary instance (player or mob).
func m13MobAttackTarget(m *m9Mob, target string) {
	m.mu.Lock()
	m.target = target
	m.lastTgt = time.Now()
	m.mu.Unlock()
}

// m13MobRoam clears the target so the tick loop resumes roaming
// (entity.roamingCallback).
func m13MobRoam(m *m9Mob) {
	m.mu.Lock()
	m.target = ""
	m.mu.Unlock()
}

// m9MobsSnapshot copies the live mob instance ids (region scans iterate it
// without holding m9Mu during the per-mob locks).
func m9MobsSnapshot() map[string]bool {
	m9Mu.Lock()
	defer m9Mu.Unlock()
	out := make(map[string]bool, len(m9Mobs))
	for inst := range m9Mobs {
		out[inst] = true
	}
	return out
}

// m13FindNPC scans the npcs.json registry + the live entity tiles.
func m13FindNPC(c *playerConn, npcKey string) {
	m6LoadNPCs()
	if info, ok := m6NPCs[npcKey]; ok && info != nil {
		// Find the first live entity whose key resolves to npcKey.
		entitiesMu.Lock()
		for _, e := range entities {
			if key := m6ResolveNPCKey(nil, e.Instance); key == npcKey {
				entitiesMu.Unlock()
				m6Notify(c, fmt.Sprintf("Found NPC: %s at x: %d, y: %d", info.Name, e.X, e.Y))
				return
			}
		}
		entitiesMu.Unlock()
		m6Notify(c, "Could not find NPC.")
		return
	}
	m6Notify(c, "Could not find NPC.")
}

// worldWidth snapshots world.Width (loadWorld cached).
func worldWidth() int {
	loadWorld()
	return world.Width
}

// m13ToggleEffect flips a status effect with the TS notify strings.
func m13ToggleEffect(c *playerConn, effect int) {
	if m13HasEffect(c, effect) {
		m13RemoveEffect(c, effect)
		m6Notify(c, "You are no longer invincible.")
		return
	}
	m13AddEffect(c, effect)
	m6Notify(c, "You are now invincible.")
}

// m13ToggleCommand ports the /toggle switch: freeze/fire/terror/stun with
// addWithTimeout(60s), hide routes to the client-side command, and the
// default clears all effects.
func m13ToggleCommand(c *playerConn, blocks []string) {
	if len(blocks) == 0 {
		// TS: key = blocks.shift()! -> undefined -> switch default ->
		// status.clear().
		m13ClearEffects(c)
		return
	}
	effect := -1
	switch blocks[0] {
	case "cold", "freeze", "freezing":
		effect = EffectFreezingM13
	case "fire", "burn", "burning":
		effect = EffectBurningM13
	case "terror":
		effect = EffectTerrorStatus
	case "stun":
		effect = EffectStun
	case "hide":
		// client-side hide command (CommandPacket) — Go stub has no client
		// command channel; notify instead (divergence note).
		m6Notify(c, "hide")
		return
	}
	if effect < 0 {
		m13ClearEffects(c)
		return
	}
	if m13HasEffect(c, effect) {
		m13RemoveEffect(c, effect)
		return
	}
	m13AddEffect(c, effect)
	// addWithTimeout(60_000): auto-remove after a minute.
	time.AfterFunc(time.Minute, func() {
		if m13HasEffect(c, effect) {
			m13RemoveEffect(c, effect)
		}
	})
}

// Effect helpers over the per-instance effect map (PacketEffect Add/Remove).
var (
	m13EffMu sync.Mutex
	m13Effs  = map[string]map[int]bool{}
)

func m13HasEffect(c *playerConn, effect int) bool {
	m13EffMu.Lock()
	defer m13EffMu.Unlock()
	return m13Effs[c.instance][effect]
}

func m13AddEffect(c *playerConn, effect int) {
	m13EffMu.Lock()
	if m13Effs[c.instance] == nil {
		m13Effs[c.instance] = map[int]bool{}
	}
	m13Effs[c.instance][effect] = true
	m13EffMu.Unlock()
	broadcast(pktOp(PacketEffect, EffectAdd, effectData{Instance: c.instance, Effect: effect}))
}

func m13RemoveEffect(c *playerConn, effect int) {
	m13EffMu.Lock()
	delete(m13Effs[c.instance], effect)
	m13EffMu.Unlock()
	broadcast(pktOp(PacketEffect, EffectRemove, effectData{Instance: c.instance, Effect: effect}))
}

func m13ClearEffects(c *playerConn) {
	m13EffMu.Lock()
	effs := m13Effs[c.instance]
	m13Effs[c.instance] = nil
	m13EffMu.Unlock()
	for e := range effs {
		broadcast(pktOp(PacketEffect, EffectRemove, effectData{Instance: c.instance, Effect: e}))
	}
}

// m13ForgetPlayer drops the per-conn effect state on disconnect.
func m13ForgetPlayer(instance string) {
	m13EffMu.Lock()
	delete(m13Effs, instance)
	m13EffMu.Unlock()
}

// m13ToggleHide ports the 'hide' command: flip player.visible and
// Despawn/Spawn to the surrounding regions.
func m13ToggleHide(c *playerConn) {
	m13VisMu.Lock()
	vis := !m13Hidden[c.instance]
	m13Hidden[c.instance] = vis
	m13VisMu.Unlock()
	if vis {
		// hidden = true -> notify "You are now invisible." + Despawn.
		m6Notify(c, "You are now invisible.")
		broadcast(pkt(PacketDespawn, despawnData{Instance: c.instance}))
		return
	}
	m6Notify(c, "You are now visible.")
	broadcast(pkt(PacketSpawn, welcomePlayer(c.instance)))
}

var (
	m13VisMu  sync.Mutex
	m13Hidden = map[string]bool{}
)

// m13IsHidden reports whether the instance is currently hidden (movement
// visibility paths could consult this; today only the toggle uses it).
func m13IsHidden(instance string) bool {
	m13VisMu.Lock()
	defer m13VisMu.Unlock()
	return m13Hidden[instance]
}

// m10SameRegion compares regionOf for two tiles (the region scan helper).
func m10SameRegion(ax, ay, bx, by int) bool {
	return regionOf(ax, ay) == regionOf(bx, by)
}

// ---------------------------------------------------------------------------
// Quest/achievement admin commands (m11 state bridging).
// ---------------------------------------------------------------------------

// m13QuestStageShift shifts the quest stage by delta (undostage).
func m13QuestStageShift(c *playerConn, key string, delta int) {
	if key == "" {
		m6Notify(c, "No quest specified.")
		return
	}
	st := m11StateFor(c.username)
	def := m11Q[key]
	if def == nil {
		m6Notify(c, "Could not find quest.")
		return
	}
	q := st.quest(key)
	if q.Stage+delta < 0 {
		return
	}
	m11SetStage(c, st, key, q.Stage+delta, q.SubStage, true)
}

// m13QuestReset sets the quest to stage 0 (resetquest/resetquests).
func m13QuestReset(c *playerConn, key string) {
	if key == "" {
		m6Notify(c, "No quest specified.")
		return
	}
	st := m11StateFor(c.username)
	if m11Q[key] == nil {
		m6Notify(c, "No quest specified.")
		return
	}
	m11SetStage(c, st, key, 0, 0, true)
}

// m13QuestResetAll resets every quest (resetquests: setStage(0, ..., true)).
func m13QuestResetAll(c *playerConn) {
	st := m11StateFor(c.username)
	for key := range m11Q {
		m11SetStage(c, st, key, 0, 0, true)
	}
}

// m13QuestFinish sets the stage past the last (setStage(9999) — clamped to
// StageCount so the completion path fires exactly once).
func m13QuestFinish(c *playerConn, key string) {
	if key == "" {
		m6Notify(c, "Malformed command, expected /finishquest questKey")
		return
	}
	st := m11StateFor(c.username)
	def := m11Q[key]
	if def == nil {
		m6Notify(c, fmt.Sprintf("Could not find quest with key: %s", key))
		return
	}
	m11SetStage(c, st, key, def.StageCount, 0, true)
}

// m13AchievementsReset zeroes every achievement stage.
func m13AchievementsReset(c *playerConn) {
	st := m11StateFor(c.username)
	for key := range st.Achs {
		st.Achs[key] = 0
	}
	markDirty(c.username)
	for key, stage := range st.Achs {
		m11SendAchievementProgress(c, key, stage)
	}
}

// m13AchievementFinish completes one achievement (achievement.finish()).
func m13AchievementFinish(c *playerConn, key string) {
	if key == "" {
		m6Notify(c, "Malformed command, expected /finishachievement achievementKey")
		return
	}
	def := m11A[key]
	if def == nil {
		m6Notify(c, fmt.Sprintf("Could not find achievement with key: %s", key))
		return
	}
	st := m11StateFor(c.username)
	for st.Achs[key] < def.StageCount {
		m11AchProgress(c, st, key)
	}
}

// m13AchievementsFinishAll completes every achievement.
func m13AchievementsFinishAll(c *playerConn) {
	st := m11StateFor(c.username)
	for key, def := range m11A {
		for st.Achs[key] < def.StageCount {
			m11AchProgress(c, st, key)
		}
	}
}

// m13TestItems empties the bank and adds 100x of every items.json key.
func m13TestItems(c *playerConn) {
	m13EmptyContainer(c.username, "bank")
	m6LoadItems()
	keys := make([]string, 0, len(m6Items))
	for k := range m6Items {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	st := m5StateFor(c.username)
	pstateMu.Lock()
	for _, k := range keys {
		st.Bank = append(st.Bank, m5Slot{Key: k, Count: 100})
	}
	pstateMu.Unlock()
	markDirty(c.username)
	m6Notify(c, fmt.Sprintf("Bank filled with %d items.", len(keys)))
}

// ---------------------------------------------------------------------------
// Login gates: ban check + mute wiring (m5LoginWelcome side hooks).
// ---------------------------------------------------------------------------

// m13CheckBan is the login ban gate: returns true when the conn must be
// rejected (user.ban future deadline). Node sends 'ban' as a UTF8 text
// frame then closes.
func m13CheckBan(username string) bool {
	if m13IsBanned(username) {
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// TESTMAP debug dispatcher.
// ---------------------------------------------------------------------------

// m13TestHandler ports the m9test/m11test dispatcher pattern: TESTMAP-only
// seeding + echo back through m6Notify (the e2e greps these).
func m13TestHandler(c *playerConn, data []byte) {
	if !testMode {
		return
	}
	var d struct {
		M13Test  string `json:"m13test"`
		Username string `json:"username"`
		Op       string `json:"op"`
		Key      string `json:"key"`
		Value    int    `json:"value"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return
	}
	target := c
	if d.Username != "" {
		if t := m7PlayerByName(d.Username); t != nil {
			target = t
		} else {
			// Offline target: a synthetic conn carrying just the username —
			// flag/echo reads hit the DB table, notifies go to the requester.
			target = &playerConn{username: d.Username}
		}
	}
	switch d.Op {
	case "seed":
		// (target: online conn, else a synthetic username carrier so offline
		// flags still resolve from the table — m6Notify goes to the requester).
		// Seed the flag set wholesale (mute/ban/jail epoch ms + noclip).
		f := m13FlagsFor(target.username)
		if d.Key == "mute" {
			f.Mute = int64(d.Value)
		}
		if d.Key == "ban" {
			f.Ban = int64(d.Value)
		}
		if d.Key == "jail" {
			f.Jail = int64(d.Value)
		}
		m13SaveFlags(target.username, f)
	case "echo":
		// State echo for the e2e: mute/ban/jail/noclip/mspeed.
		f := m13FlagsFor(target.username)
		verb := "cleared"
		if f.Mute > 0 {
			verb = fmt.Sprintf("until %d", f.Mute)
		}
		m6Notify(c, fmt.Sprintf("m13:flags user=%s mute=%s ban=%d jail=%d noclip=%v mspeed=%d",
			target.username, verb, f.Ban, f.Jail, f.Noclip, f.MSpeed))
	case "seedinv":
		// Seed an inventory stack for the target (TESTMAP container legs).
		m5AddItem(target.username, d.Key, d.Value)
		markDirty(target.username)
		m6Notify(c, fmt.Sprintf("m13:inv %s=%d", d.Key, m6InvCount(target.username, d.Key)))
	case "inv":
		// Inventory count echo (m13:inv <key>=<n>) for the container legs.
		m6Notify(c, fmt.Sprintf("m13:inv %s=%d", d.Key, m6InvCount(target.username, d.Key)))
	case "bank":
		// Bank count echo (m13:bank <key>=<n>).
		m6Notify(c, fmt.Sprintf("m13:bank %s=%d", d.Key, m13BankCount(target.username, d.Key)))
	case "quest":
		// Echo quest state: m11:quest:<key>=<stage>/<stageCount>.
		st := m11StateFor(target.username)
		def := m11Q[d.Key]
		if def == nil {
			m6Notify(c, fmt.Sprintf("m13:noquest %s", d.Key))
			return
		}
		q := st.quest(d.Key)
		m6Notify(c, fmt.Sprintf("m11:quest:%s=%d/%d", d.Key, q.Stage, def.StageCount))
	}
}
