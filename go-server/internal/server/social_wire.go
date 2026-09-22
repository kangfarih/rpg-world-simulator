// Social wiring (friends + guilds + hub) — thin root adapter over
// internal/social.
//
// Canonical owner: internal/social (friends/guild/hub orchestration; the
// friends, guilds and hub packages are re-exported from there). This file
// only wires the package seams to the root globals (players map, dbConn/
// dbMu, m7 chat helpers, gnet + worldcore transport) and keeps the entry
// points main.go/m7.go/m13.go/ops_wire.go/world_wire.go call — with
// UNCHANGED signatures — delegating to the package. Packet shapes, log
// strings and debug ops are identical (owned by the package).
package server

import (
	"rpg-world-server/internal/guilds"
	gnet "rpg-world-server/internal/net"
	"rpg-world-server/internal/social"
	worldcore "rpg-world-server/internal/world"
)

// socServerID mirrors config.serverId on the wire (the stub handshake sends
// ServerID 1; offline presence is -1 per friends.ts load/add/setStatus).
const socServerID = 1

// Friend opcodes (Opcodes.Friends): List0 Add1 Remove2 Status3 Sync4.
const (
	FriendList   = social.FriendList
	FriendAdd    = social.FriendAdd
	FriendRemove = social.FriendRemove
	FriendStatus = social.FriendStatus
	FriendSync   = social.FriendSync
)

// Guild opcodes alias the internal/guilds Op* constants (Opcodes.Guild order:
// Create0 Login1 Logout2 Join3 Leave4 Rank5 Update6 Experience7 Banner8 List9
// Error10 Chat11 Promote12 Demote13 Kick14).
const (
	GuildCreate     = social.GuildCreate
	GuildLogin      = social.GuildLogin
	GuildLogout     = social.GuildLogout
	GuildJoin       = social.GuildJoin
	GuildLeave      = social.GuildLeave
	GuildRank       = social.GuildRank
	GuildUpdate     = social.GuildUpdate
	GuildExperience = social.GuildExperience
	GuildBanner     = social.GuildBanner
	GuildList       = social.GuildList
	GuildError      = social.GuildError
	GuildChat       = social.GuildChat
	GuildPromote    = social.GuildPromote
	GuildDemote     = social.GuildDemote
	GuildKick       = social.GuildKick
)

// socConn converts a root conn to the package view (nil-safe). Delivery
// stays direct (gnet.Send on the live socket, m6Notify on the same conn).
func socConn(c *playerConn) *social.Conn {
	if c == nil {
		return nil
	}
	return &social.Conn{
		Instance: c.Instance,
		Username: c.Username,
		Send: func(frames ...[]any) {
			_ = gnet.Send(c.Conn, frames...)
		},
		Notify: func(message string) {
			m6Notify(c, message)
		},
	}
}

// socPlayerConn resolves an online player to the package view (nil-safe).
func socPlayerConn(name string) (*social.Conn, bool) {
	c := m7PlayerByName(name)
	if c == nil {
		return nil, false
	}
	return socConn(c), true
}

// socConfigure wires the social seams (called once from m5Init, before
// tables, logins or ticks run).
func socConfigure() {
	social.Configure(social.Deps{
		DB:       dbConn,
		LockDB:   func() { dbMu.Lock() },
		UnlockDB: func() { dbMu.Unlock() },
		ServerID: socServerID,
		Test:     testMode,
		Broadcast: func(frame []any) {
			worldcore.Broadcast(frame)
		},
		PlayerConn:  socPlayerConn,
		PlayerNames: m7PlayerUsernames,
		Sanitize:    m7Sanitize,
		IsNonBlank:  whitespaceRe.MatchString,
		FormatName:  m7FormatName,
	})
}

func socEnsureTables() { social.EnsureTables() }

func socLoadGuilds() { social.LoadGuilds() }

func socOnLogin(c *playerConn) [][]any { return social.OnLogin(socConn(c)) }

func socOnDisconnect(c *playerConn) { social.OnDisconnect(socConn(c)) }

func socHandleFriends(c *playerConn, data []byte) { social.HandleFriends(socConn(c), data) }

func socHandleGuild(c *playerConn, data []byte) { social.HandleGuild(socConn(c), data) }

func socGuildCreate(c *playerConn, name, colour string, outline *int, outlineColour, crest string) {
	social.CreateGuild(socConn(c), name, colour, outline, outlineColour, crest)
}

func socGuildJoin(c *playerConn, identifier string) { social.JoinGuild(socConn(c), identifier) }

func socGuildLeave(c *playerConn) { social.LeaveGuild(socConn(c)) }

func socGuildChat(c *playerConn, message string) { social.ChatGuild(socConn(c), message) }

func socGuildInvite(c *playerConn, target string) { social.GuildInvite(socConn(c), target) }

func socGuildKick(c *playerConn, username string, viaCommand bool) {
	if viaCommand {
		social.GuildKickCommand(socConn(c), username)
		return
	}
	social.KickGuild(socConn(c), username, false)
}

func socGuildRankCommand(c *playerConn, rankStr, username string) {
	social.GuildRankCommand(socConn(c), rankStr, username)
}

// socGuildOf reports the caller's guild (m13 command-gate parity).
func socGuildOf(username string) (*guilds.Guild, error) { return social.GuildOf(username) }

func socRouteChat(target string) *playerConn {
	social.RouteChat(target)
	return m7PlayerByName(target)
}

func socRouteGlobal(frame []any) { social.RouteGlobal(frame) }

func socTestHandler(c *playerConn, data []byte) { social.TestHandler(socConn(c), data) }
