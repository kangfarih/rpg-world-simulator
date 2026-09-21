// Social wiring (friends + guilds + hub) — behavior-additive root glue over
// internal/friends, internal/guilds and internal/hub.
//
// TS sources (all read-only recon, no invented opcodes or packet shapes):
//   - packages/common/network/impl/friends.ts — FriendInfo {online, serverId}
//     per username; FriendsPacketData {list, username, status, serverId}.
//   - packages/common/network/impl/guild.ts — Member {username, rank?,
//     joinDate?, serverId?}, GuildData, GuildPacketData {identifier?, name?,
//     username?, usernames?, serverId?, member?, members?, total?, guilds?,
//     message?, owner?, decoration?, experience?, rank?}.
//   - packages/common/network/opcodes.ts — Opcodes.Friends List0 Add1
//     Remove2 Status3 Sync4; Opcodes.Guild Create0 Login1 Logout2 Join3
//     Leave4 Rank5 Update6 Experience7 Banner8 List9 Error10 Chat11
//     Promote12 Demote13 Kick14.
//   - packages/common/network/packets.ts — Packets.Guild 35, Packets.Friends 48.
//   - packages/server friends.ts — add guards (too-long/self/duplicate/
//     database.exists/world.isOnline), remove, sync of inactive friends,
//     setStatus; handler.ts S->C emits List {list}, Add {username, status,
//     serverId}, Remove {username}, Status {username, status, serverId}.
//   - packages/server controllers/guilds.ts — create/join/leave/kick/chat/
//     promote/demote/setRank/addExperience/get/list; synchronize() unicasts to
//     online members and relays to offline ones via the hub; connect() sends
//     Login {name, owner, members, decoration} + Update {online members};
//     join syncs Join {username, serverId}; leave/kick sync Leave {username};
//     setRank syncs Rank {username, rank}; guildNotify sends Error {message}.
//   - packages/server incoming.ts handleFriends (C->S Add/Remove {opcode,
//     username}) and handleGuild (C->S Create {name, colour, outline,
//     outlineColour, crest} / Join {identifier} / Leave / List {from, to} /
//     Chat {message} / Promote|Demote|Kick {username}).
//   - packages/server world.ts syncFriendsList/syncGuildMembers — login/logout
//     presence fanout (Status / Update {members:[{username, serverId}]},
//     serverId -1 on logout).
//   - packages/server controllers/commands.ts 'guild' — not-in-a-guild gate,
//     kick|rank subcommands with the exact notify strings kept below.
//   - packages/client menu.ts handleFriendConfirm/input.ts AddFriend (C->S
//     Friends {opcode, username}) and menu/guilds.ts (C->S Guild shapes above).
//
// Frames used (all pre-existing shapes, verified against the TS tree):
//
//	S->C Friends [48, opcode, {list|username,status,serverId}] List0 Add1
//	  Remove2 Status3 (Sync4 is hub-bound, never sent to the game client).
//	C->S Friends [48, {opcode, username}] Add1 Remove2 (incoming.ts parity;
//	  other opcodes ignored).
//	S->C Guild [35, opcode, data] Login1 Join3 Leave4 Rank5 Update6
//	  Experience7 List9 Error10 Chat11 (Logout2/Banner8 S->C-only or unused,
//	  mirroring the TS synchronize/connect/updateStatus emits).
//	C->S Guild [35, {opcode, ...}] Create0 Join3 Leave4 List9 Chat11
//	  Promote12 Demote13 Kick14 (incoming.ts handleGuild parity).
//	C->S Chat [19, [text]] carries `/guild invite <user>` (the invite path —
//	  TS has no invite RPC, so no new packet shape is invented for it).
//
// Divergences from TS (documented):
//   - No economy/progression gates on create (30k gold, tutorial, guests):
//     the stub has no tutorial state and harness accounts carry no gold, so
//     Create checks membership + name uniqueness only (the registry-level
//     ErrAlreadyInGuild/ErrExists mapping the package documents as
//     caller-side).
//   - Guild join is invite-only: TS join() is open, but internal/guilds
//     requires a pending Invite (package-documented). C->S Join maps to
//     AcceptInvite; a join without an invite notifies 'guilds:NO_INVITE'
//     (new string, no TS counterpart). Invites flow over `/guild invite`.
//   - Decoration (banner/outline/crest) is kept in memory only: SchemaSQL has
//     no decoration column, so banner choices do not survive a reboot (Login
//     frames echo the in-memory choice, client fallbacks apply otherwise).
//   - Hub relay for offline members is a skip: TS synchronize() relays to
//     offline members over the hub socket; all-in-one the Router resolves the
//     online subset and offline members get nothing (no cross-server socket
//     exists). Guild chat therefore never falls back to a global broadcast —
//     that would leak guild chat to non-members.
//   - Hub usernames are lowercase-normalized at the boundary (the Router
//     itself is exact-match per its docs; the stub's PM path lowercases names
//     before lookup, so registration normalizes the same way to keep
//     Route/lookup consistent).
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"

	"rpg-world-server/internal/friends"
	"rpg-world-server/internal/guilds"
	"rpg-world-server/internal/hub"
)

// socServerID mirrors config.serverId on the wire (the stub handshake sends
// ServerID 1; offline presence is -1 per friends.ts load/add/setStatus).
const socServerID = 1

// Friend opcodes (Opcodes.Friends): List0 Add1 Remove2 Status3 Sync4.
const (
	FriendList   = 0
	FriendAdd    = 1
	FriendRemove = 2
	FriendStatus = 3
	FriendSync   = 4
)

// Guild opcodes alias the internal/guilds Op* constants (Opcodes.Guild order:
// Create0 Login1 Logout2 Join3 Leave4 Rank5 Update6 Experience7 Banner8 List9
// Error10 Chat11 Promote12 Demote13 Kick14).
const (
	GuildCreate     = guilds.OpCreate
	GuildLogin      = guilds.OpLogin
	GuildLogout     = guilds.OpLogout
	GuildJoin       = guilds.OpJoin
	GuildLeave      = guilds.OpLeave
	GuildRank       = guilds.OpRank
	GuildUpdate     = guilds.OpUpdate
	GuildExperience = guilds.OpExperience
	GuildBanner     = guilds.OpBanner
	GuildList       = guilds.OpList
	GuildError      = guilds.OpError
	GuildChat       = guilds.OpChat
	GuildPromote    = guilds.OpPromote
	GuildDemote     = guilds.OpDemote
	GuildKick       = guilds.OpKick
)

var (
	// socHub is the all-in-one Router: Register on login, Unregister on
	// logout/disconnect, Route every cross-player message through it.
	socHub = hub.NewRouter()

	socMu      sync.Mutex
	socFriends = map[string]*friends.List{} // username (exact login case) -> list
	socGuilds  = guilds.NewRegistry()

	// socGuildIDs tracks live guild identifiers (the Registry has no
	// enumerate API, so the wire layer keeps the set for persistence).
	socGuildIDs = map[string]bool{}

	// socDeco keeps per-guild banner choices in memory (no SchemaSQL column;
	// see divergences). Keyed by guild identifier.
	socDeco = map[string]socDecoration{}
)

// socDecoration mirrors Decoration (impl/guild.ts). BannerColour/Crest are
// strings ('grey', 'goldenyellow', 'none', ...); outline is numeric.
type socDecoration struct {
	Banner        string `json:"banner"`
	Outline       int    `json:"outline"`
	OutlineColour string `json:"outlineColour"`
	Crest         string `json:"crest"`
}

// socDefaultDeco mirrors the client handleConnect fallbacks
// (banner Grey, outline StyleOne, outlineColour GoldenYellow).
func socDefaultDeco() socDecoration {
	return socDecoration{Banner: "grey", Outline: 0, OutlineColour: "goldenyellow", Crest: "none"}
}

// socFriendInfo mirrors FriendInfo (impl/friends.ts).
type socFriendInfo struct {
	Online   bool `json:"online"`
	ServerID int  `json:"serverId"`
}

// socGuildMember mirrors Member (impl/guild.ts): optionals omitted when unset.
type socGuildMember struct {
	Username string `json:"username"`
	Rank     *int   `json:"rank,omitempty"`
	ServerID *int   `json:"serverId,omitempty"`
}

// ---------------------------------------------------------------------------
// Tables + persistence (m11EnsureTables/m13EnsureTables precedent; single
// writer: every Exec/Query runs under dbMu, never nested inside socMu).
// ---------------------------------------------------------------------------

// socEnsureTables executes guilds.SchemaSQL() plus the friends table (same
// TEXT/INT column style). Called at boot and on login like m11EnsureTables.
func socEnsureTables() {
	if dbConn == nil {
		return
	}
	dbMu.Lock()
	defer dbMu.Unlock()
	for _, ddl := range strings.Split(guilds.SchemaSQL(), "\n") {
		if strings.TrimSpace(ddl) == "" {
			continue
		}
		if _, err := dbConn.Exec(ddl); err != nil {
			log.Printf("social: ddl: %v", err)
		}
	}
	if _, err := dbConn.Exec(
		`CREATE TABLE IF NOT EXISTS friends(player TEXT, friend TEXT, PRIMARY KEY(player, friend))`); err != nil {
		log.Printf("social: ddl friends: %v", err)
	}
}

// socLoadGuilds rebuilds the registry from the guild tables (boot only, while
// single-threaded). Members rejoin via Invite+AcceptInvite+SetRank so the
// one-guild rule and ranks restore exactly.
func socLoadGuilds() {
	if dbConn == nil {
		return
	}
	type grow struct {
		id, name, owner string
		xp              int
	}
	dbMu.Lock()
	defer dbMu.Unlock()
	rows, err := dbConn.Query(`SELECT id,name,owner FROM guilds`)
	if err != nil {
		return
	}
	var gs []grow
	for rows.Next() {
		var g grow
		if rows.Scan(&g.id, &g.name, &g.owner) == nil {
			gs = append(gs, g)
		}
	}
	rows.Close()
	// xp column predates some DBs; tolerate its absence (zero value).
	for i := range gs {
		var xp int
		if err := dbConn.QueryRow(`SELECT xp FROM guilds WHERE id=?`, gs[i].id).Scan(&xp); err == nil {
			gs[i].xp = xp
		}
	}
	for _, g := range gs {
		created, err := socGuilds.Create(g.owner, g.name)
		if err != nil {
			continue
		}
		socGuildIDs[created.ID] = true
		socDeco[created.ID] = socDefaultDeco()
		mrows, merr := dbConn.Query(`SELECT player,rank FROM guild_members WHERE guild=?`, created.ID)
		if merr != nil {
			continue
		}
		var members []struct {
			name string
			rank int
		}
		for mrows.Next() {
			var m struct {
				name string
				rank int
			}
			if mrows.Scan(&m.name, &m.rank) == nil {
				members = append(members, m)
			}
		}
		mrows.Close()
		sort.Slice(members, func(a, b int) bool { return members[a].name < members[b].name })
		for _, m := range members {
			if m.name == g.owner {
				continue
			}
			if err := socGuilds.Invite(g.owner, m.name); err != nil {
				continue
			}
			if err := socGuilds.AcceptInvite(m.name, created.ID); err != nil {
				continue
			}
			if m.rank != int(guilds.RankFledgling) {
				_ = socGuilds.SetRank(g.owner, m.name, guilds.Rank(m.rank))
			}
		}
		if g.xp != 0 {
			_ = socGuilds.AddXP(g.owner, g.xp)
		}
	}
	if len(gs) > 0 {
		log.Printf("social: restored %d guilds", len(gs))
	}
}

// socSaveGuildRows persists one guild snapshot (caller holds no locks).
func socSaveGuildRows(g *guilds.Guild) {
	if dbConn == nil || g == nil {
		return
	}
	dbMu.Lock()
	defer dbMu.Unlock()
	if _, err := dbConn.Exec(`INSERT INTO guilds(id,name,owner,xp) VALUES(?,?,?,?) `+
		`ON CONFLICT(id) DO UPDATE SET name=?,owner=?,xp=?`,
		g.ID, g.Name, g.Owner, g.XP, g.Name, g.Owner, g.XP); err != nil {
		log.Printf("social: save guild %s: %v", g.ID, err)
		return
	}
	if _, err := dbConn.Exec(`DELETE FROM guild_members WHERE guild=?`, g.ID); err != nil {
		return
	}
	for user, rank := range g.Members {
		if _, err := dbConn.Exec(`INSERT INTO guild_members(guild,player,rank) VALUES(?,?,?)`,
			g.ID, user, int(rank)); err != nil {
			log.Printf("social: save member %s/%s: %v", g.ID, user, err)
		}
	}
}

// socDropGuildRows deletes one guild's rows (disband path).
func socDropGuildRows(id string) {
	if dbConn == nil || id == "" {
		return
	}
	dbMu.Lock()
	defer dbMu.Unlock()
	if _, err := dbConn.Exec(`DELETE FROM guild_members WHERE guild=?`, id); err != nil {
		log.Printf("social: drop members %s: %v", id, err)
	}
	if _, err := dbConn.Exec(`DELETE FROM guilds WHERE id=?`, id); err != nil {
		log.Printf("social: drop guild %s: %v", id, err)
	}
}

// socPersistGuildID saves or drops the guild by identifier depending on
// whether it still exists (leave/disband/kick paths).
func socPersistGuildID(id string) {
	if id == "" {
		return
	}
	if g, err := socGuilds.Get(id); err == nil {
		socSaveGuildRows(g)
		return
	}
	socMu.Lock()
	delete(socGuildIDs, id)
	delete(socDeco, id)
	socMu.Unlock()
	socDropGuildRows(id)
}

// socFriendsFor returns the in-memory list for username, creating it empty.
func socFriendsFor(username string) *friends.List {
	socMu.Lock()
	defer socMu.Unlock()
	if l := socFriends[username]; l != nil {
		return l
	}
	l := friends.New(username)
	socFriends[username] = l
	return l
}

// socLoadFriends restores username's rows into memory and resolves presence
// against the online map (friends.ts load() parity). Called on login.
func socLoadFriends(username string) {
	l := socFriendsFor(username)
	if dbConn == nil || username == "" {
		return
	}
	dbMu.Lock()
	rows, err := dbConn.Query(`SELECT friend FROM friends WHERE player=?`, username)
	if err != nil {
		dbMu.Unlock()
		return
	}
	var names []string
	for rows.Next() {
		var f string
		if rows.Scan(&f) == nil {
			names = append(names, f)
		}
	}
	rows.Close()
	dbMu.Unlock()
	presence := map[string]bool{}
	for _, n := range names {
		presence[n] = m7PlayerByName(n) != nil
	}
	socMu.Lock()
	defer socMu.Unlock()
	l.Load(names)
	for n, online := range presence {
		sid := friends.OfflineServerID
		if online {
			sid = socServerID
		}
		l.SetStatus(n, online, sid)
	}
}

// socPersistFriends flushes username's list (load-on-login/flush-on-change/
// disconnect; caller holds no locks).
func socPersistFriends(username string) {
	if dbConn == nil || username == "" {
		return
	}
	socMu.Lock()
	l := socFriends[username]
	var members []string
	if l != nil {
		members = l.Members()
	}
	socMu.Unlock()
	dbMu.Lock()
	defer dbMu.Unlock()
	if _, err := dbConn.Exec(`DELETE FROM friends WHERE player=?`, username); err != nil {
		return
	}
	for _, f := range members {
		if _, err := dbConn.Exec(`INSERT INTO friends(player,friend) VALUES(?,?)`, username, f); err != nil {
			log.Printf("social: save friend %s/%s: %v", username, f, err)
		}
	}
}

// socPlayerExists mirrors the TS database.exists gate: an online conn or a
// persisted players row (instance or name column; m5 keys rows by username).
func socPlayerExists(username string) bool {
	if m7PlayerByName(username) != nil {
		return true
	}
	if dbConn == nil || username == "" {
		return false
	}
	dbMu.Lock()
	defer dbMu.Unlock()
	var one int
	if err := dbConn.QueryRow(`SELECT 1 FROM players WHERE instance=? OR name=?`, username, username).Scan(&one); err != nil {
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// S->C frame builders (TS handler.ts / guilds.ts emit parity).
// ---------------------------------------------------------------------------

func socFriendsListFrame(l *friends.List) []any {
	socMu.Lock()
	members := l.Members()
	info := make(map[string]socFriendInfo, len(members))
	for _, n := range members {
		fi, _ := l.Info(n)
		info[n] = socFriendInfo{Online: fi.Online, ServerID: fi.ServerID}
	}
	socMu.Unlock()
	if info == nil {
		info = map[string]socFriendInfo{}
	}
	return pktOp(PacketFriends, FriendList, map[string]any{"list": info})
}

func socSendFriendsAdd(c *playerConn, username string, online bool, serverID int) {
	_ = send(c.conn, pktOp(PacketFriends, FriendAdd, map[string]any{
		"username": username, "status": online, "serverId": serverID,
	}))
}

func socSendFriendsRemove(c *playerConn, username string) {
	_ = send(c.conn, pktOp(PacketFriends, FriendRemove, map[string]any{
		"username": username,
	}))
}

func socSendFriendsStatus(c *playerConn, username string, online bool, serverID int) {
	_ = send(c.conn, pktOp(PacketFriends, FriendStatus, map[string]any{
		"username": username, "status": online, "serverId": serverID,
	}))
}

// socGuildLoginFrame builds the connect/Login frame (guilds.ts create/connect
// parity: {name, owner, members, decoration}).
func socGuildLoginFrame(g *guilds.Guild) []any {
	names := make([]string, 0, len(g.Members))
	for u := range g.Members {
		names = append(names, u)
	}
	sort.Strings(names)
	members := make([]socGuildMember, 0, len(names))
	for _, u := range names {
		r := int(g.Members[u])
		members = append(members, socGuildMember{Username: u, Rank: &r})
	}
	socMu.Lock()
	deco, ok := socDeco[g.ID]
	socMu.Unlock()
	if !ok {
		deco = socDefaultDeco()
	}
	return pktOp(PacketGuild, GuildLogin, map[string]any{
		"name": g.Name, "owner": g.Owner, "members": members, "decoration": deco,
	})
}

func socSendGuildError(c *playerConn, message string) {
	_ = send(c.conn, pktOp(PacketGuild, GuildError, map[string]any{"message": message}))
}

// socOnlineGuildmates snapshots the online members of g excluding skip.
func socOnlineGuildmates(g *guilds.Guild, skip string) []string {
	var out []string
	for u := range g.Members {
		if u == skip {
			continue
		}
		if m7PlayerByName(u) != nil {
			out = append(out, u)
		}
	}
	sort.Strings(out)
	return out
}

// socDeliverGuildChat fans a Chat frame out to the Router-resolved online
// subset (all-in-one: Route filters the candidate roster; offline members are
// skipped — no hub socket exists to relay further, and a global broadcast
// fallback would leak guild chat to non-members).
func socDeliverGuildChat(from, message string, members []string) {
	recips, _ := socHub.Route(hub.Message{From: from, Kind: hub.KindGuild, Payload: members})
	for _, name := range recips {
		if t := m7PlayerByName(name); t != nil {
			_ = send(t.conn, pktOp(PacketGuild, GuildChat, map[string]any{
				"username": from, "serverId": socServerID, "message": message,
			}))
		}
	}
}

// ---------------------------------------------------------------------------
// C->S dispatch.
// ---------------------------------------------------------------------------

// socHandleFriends routes C->S Friends frames [48,{opcode,username}]
// (incoming.ts handleFriends parity: Add/Remove only, the rest ignored).
func socHandleFriends(c *playerConn, data []byte) {
	if c == nil {
		return
	}
	var d struct {
		Opcode   *int   `json:"opcode"`
		Username string `json:"username"`
	}
	if err := json.Unmarshal(data, &d); err != nil || d.Opcode == nil {
		return
	}
	switch *d.Opcode {
	case FriendAdd:
		socFriendAdd(c, d.Username)
	case FriendRemove:
		socFriendRemove(c, d.Username)
	}
}

// socFriendAdd ports friends.ts add() with the misc:FRIENDS_* notify mapping
// (caller-owned per the friends package docs).
func socFriendAdd(c *playerConn, username string) {
	key := strings.ToLower(strings.TrimSpace(username))
	if len(key) > friends.MaxUsernameLen {
		m6Notify(c, "misc:FRIENDS_USERNAME_TOO_LONG")
		return
	}
	if key == strings.ToLower(c.username) {
		m6Notify(c, "misc:FRIENDS_ADD_SELF")
		return
	}
	l := socFriendsFor(c.username)
	socMu.Lock()
	dup := l.IsFriend(key)
	socMu.Unlock()
	if dup {
		m6Notify(c, "misc:FRIENDS_ALREADY_ADDED")
		return
	}
	if !socPlayerExists(key) && !socPlayerExists(username) {
		m6Notify(c, "misc:FRIENDS_USER_DOES_NOT_EXIST")
		return
	}
	socMu.Lock()
	added := l.Add(key)
	socMu.Unlock()
	if !added {
		m6Notify(c, "misc:FRIENDS_USER_DOES_NOT_EXIST")
		return
	}
	online := m7PlayerByName(key) != nil
	sid := friends.OfflineServerID
	if online {
		sid = socServerID
	}
	socMu.Lock()
	l.SetStatus(key, online, sid)
	socMu.Unlock()
	socPersistFriends(c.username)
	socSendFriendsAdd(c, key, online, sid)
	log.Printf("social: %s added friend %s online=%v", c.username, key, online)
}

// socFriendRemove ports friends.ts remove().
func socFriendRemove(c *playerConn, username string) {
	key := strings.ToLower(strings.TrimSpace(username))
	l := socFriendsFor(c.username)
	socMu.Lock()
	removed := l.Remove(key)
	socMu.Unlock()
	if !removed {
		m6Notify(c, "misc:FRIENDS_NOT_IN_LIST")
		return
	}
	socPersistFriends(c.username)
	socSendFriendsRemove(c, key)
	log.Printf("social: %s removed friend %s", c.username, key)
}

// socHandleGuild routes C->S Guild frames [35,{opcode,...}]
// (incoming.ts handleGuild parity).
func socHandleGuild(c *playerConn, data []byte) {
	if c == nil {
		return
	}
	var d struct {
		Opcode        *int   `json:"opcode"`
		Name          string `json:"name"`
		Colour        string `json:"colour"`
		Outline       *int   `json:"outline"`
		OutlineColour string `json:"outlineColour"`
		Crest         string `json:"crest"`
		Identifier    string `json:"identifier"`
		From          *int   `json:"from"`
		To            *int   `json:"to"`
		Message       string `json:"message"`
		Username      string `json:"username"`
	}
	if err := json.Unmarshal(data, &d); err != nil || d.Opcode == nil {
		return
	}
	switch *d.Opcode {
	case GuildCreate:
		socGuildCreate(c, d.Name, d.Colour, d.Outline, d.OutlineColour, d.Crest)
	case GuildJoin:
		socGuildJoin(c, d.Identifier)
	case GuildLeave:
		socGuildLeave(c)
	case GuildList:
		from, to := 0, 50
		if d.From != nil {
			from = *d.From
		}
		if d.To != nil {
			to = *d.To
		}
		socGuildList(c, from, to)
	case GuildChat:
		socGuildChat(c, d.Message)
	case GuildPromote:
		socGuildSetRankDelta(c, d.Username, +1)
	case GuildDemote:
		socGuildSetRankDelta(c, d.Username, -1)
	case GuildKick:
		socGuildKick(c, d.Username, false)
	}
}

// socGuildCreate ports guilds.ts create() minus the economy/progression gates
// (documented divergence): membership + name-uniqueness checks only.
func socGuildCreate(c *playerConn, name, colour string, outline *int, outlineColour, crest string) {
	if _, err := socGuilds.GuildOf(c.username); err == nil {
		m6Notify(c, "guilds:ALREADY_IN_GUILD")
		return
	}
	g, err := socGuilds.Create(c.username, name)
	if err != nil {
		if err == guilds.ErrAlreadyInGuild {
			m6Notify(c, "guilds:ALREADY_IN_GUILD")
		} else if err == guilds.ErrExists {
			socSendGuildError(c, "A guild with that name already exists.")
		}
		return
	}
	socMu.Lock()
	socGuildIDs[g.ID] = true
	deco := socDefaultDeco()
	if colour != "" {
		deco.Banner = colour
	}
	if outline != nil {
		deco.Outline = *outline
	}
	if outlineColour != "" {
		deco.OutlineColour = outlineColour
	}
	if crest != "" {
		deco.Crest = crest
	}
	socDeco[g.ID] = deco
	socMu.Unlock()
	socSaveGuildRows(g)
	_ = send(c.conn, socGuildLoginFrame(g))
	log.Printf("social: %s created guild %s (%s)", c.username, g.Name, g.ID)
}

// socGuildJoin ports the join flow over a pending invite (invite-only
// divergence): AcceptInvite, then connect parity (Login + Update of online
// members to the joiner) plus a Join sync to the other online members.
func socGuildJoin(c *playerConn, identifier string) {
	if _, err := socGuilds.GuildOf(c.username); err == nil {
		m6Notify(c, "guilds:ALREADY_IN_GUILD")
		return
	}
	id := strings.ToLower(identifier)
	if err := socGuilds.AcceptInvite(c.username, id); err != nil {
		switch err {
		case guilds.ErrAlreadyInGuild:
			m6Notify(c, "guilds:ALREADY_IN_GUILD")
		case guilds.ErrFull:
			m6Notify(c, "guilds:GUILD_FULL")
		case guilds.ErrNotFound:
			socGuildList(c, 0, 10) // TS join-miss resends the guild list
		default: // ErrNoInvite / ErrInvalid
			m6Notify(c, "guilds:NO_INVITE")
		}
		return
	}
	g, err := socGuilds.GuildOf(c.username)
	if err != nil {
		return
	}
	socPersistGuildID(g.ID)
	_ = send(c.conn, socGuildLoginFrame(g))
	socSendGuildUpdateOnline(c)
	for _, name := range socOnlineGuildmates(g, c.username) {
		if t := m7PlayerByName(name); t != nil {
			_ = send(t.conn, pktOp(PacketGuild, GuildJoin, map[string]any{
				"username": c.username, "serverId": socServerID,
			}))
		}
	}
	log.Printf("social: %s joined guild %s", c.username, g.ID)
}

// socSendGuildUpdateOnline sends the joiner the online roster
// (guilds.ts updateStatus parity: Update {members:[{username, serverId}]}).
func socSendGuildUpdateOnline(c *playerConn) {
	g, err := socGuilds.GuildOf(c.username)
	if err != nil {
		return
	}
	var online []socGuildMember
	for u := range g.Members {
		if m7PlayerByName(u) == nil {
			continue
		}
		sid := socServerID
		online = append(online, socGuildMember{Username: u, ServerID: &sid})
	}
	sort.Slice(online, func(a, b int) bool { return online[a].Username < online[b].Username })
	if online == nil {
		online = []socGuildMember{}
	}
	_ = send(c.conn, pktOp(PacketGuild, GuildUpdate, map[string]any{"members": online}))
}

// socGuildLeave ports guilds.ts leave() (owner leaving disbands).
func socGuildLeave(c *playerConn) {
	before, err := socGuilds.GuildOf(c.username)
	if err != nil {
		return
	}
	members := make([]string, 0, len(before.Members))
	for u := range before.Members {
		members = append(members, u)
	}
	ownerLeaving := before.Owner == c.username
	if err := socGuilds.Leave(c.username); err != nil {
		return
	}
	socPersistGuildID(before.ID)
	_ = send(c.conn, pktOp(PacketGuild, GuildLeave, nil))
	for _, name := range members {
		if name == c.username {
			continue
		}
		if t := m7PlayerByName(name); t != nil {
			_ = send(t.conn, pktOp(PacketGuild, GuildLeave, map[string]any{
				"username": c.username,
			}))
		}
	}
	log.Printf("social: %s left guild %s disband=%v", c.username, before.ID, ownerLeaving)
}

// socGuildList ports guilds.ts get() from the persisted rows (the registry
// has no enumerate API; the DB is the list source like the TS loader).
func socGuildList(c *playerConn, from, to int) {
	if dbConn == nil {
		return
	}
	if from < 0 {
		from = 0
	}
	if to <= from {
		to = from + 50
	}
	type entry struct {
		name string
		n    int
	}
	dbMu.Lock()
	rows, err := dbConn.Query(`SELECT id,name,owner FROM guilds`)
	if err != nil {
		dbMu.Unlock()
		return
	}
	var ids []string
	names := map[string]string{}
	for rows.Next() {
		var id, name, owner string
		if rows.Scan(&id, &name, &owner) == nil {
			ids = append(ids, id)
			names[id] = name
		}
	}
	rows.Close()
	sort.Strings(ids)
	var list []map[string]any
	total := 0
	for _, id := range ids {
		var n int
		if err := dbConn.QueryRow(`SELECT COUNT(*) FROM guild_members WHERE guild=?`, id).Scan(&n); err != nil {
			continue
		}
		if n >= guilds.MaxMembers {
			continue
		}
		total++
	}
	dbMu.Unlock()
	for i, id := range ids {
		if i < from || i >= to {
			continue
		}
		var n int
		dbMu.Lock()
		_ = dbConn.QueryRow(`SELECT COUNT(*) FROM guild_members WHERE guild=?`, id).Scan(&n)
		dbMu.Unlock()
		if n >= guilds.MaxMembers {
			continue
		}
		socMu.Lock()
		deco, ok := socDeco[id]
		socMu.Unlock()
		if !ok {
			deco = socDefaultDeco()
		}
		list = append(list, map[string]any{
			"name": names[id], "members": n, "decoration": deco,
		})
	}
	if list == nil {
		list = []map[string]any{}
	}
	_ = send(c.conn, pktOp(PacketGuild, GuildList, map[string]any{
		"guilds": list, "total": total,
	}))
}

// socGuildChat ports guilds.ts chat() through the Router fanout.
func socGuildChat(c *playerConn, message string) {
	g, err := socGuilds.GuildOf(c.username)
	if err != nil {
		return
	}
	text := m7Sanitize(message)
	if !whitespaceRe.MatchString(text) {
		return
	}
	members := make([]string, 0, len(g.Members))
	for u := range g.Members {
		members = append(members, u)
	}
	sort.Strings(members)
	socDeliverGuildChat(c.username, text, members)
	log.Printf("social: guild %s chat from %s", g.ID, c.username)
}

// socGuildSetRankDelta ports promote/demote via SetRank(rank +/- 1).
func socGuildSetRankDelta(c *playerConn, username string, delta int) {
	g, err := socGuilds.GuildOf(c.username)
	if err != nil {
		return
	}
	cur, ok := g.Members[username]
	if !ok {
		return
	}
	if err := socGuilds.SetRank(c.username, username, cur+guilds.Rank(delta)); err != nil {
		if err == guilds.ErrNoPermission {
			m6Notify(c, "guilds:NO_PERMISSION_RANK")
		}
		return
	}
	socPersistGuildID(g.ID)
	rank := int(cur + guilds.Rank(delta))
	for _, name := range socOnlineGuildmates(g, "") {
		if t := m7PlayerByName(name); t != nil {
			_ = send(t.conn, pktOp(PacketGuild, GuildRank, map[string]any{
				"username": username, "rank": rank,
			}))
		}
	}
	log.Printf("social: %s set %s rank to %d in %s", c.username, username, rank, g.ID)
}

// socGuildKick ports guilds.ts kick(). Packet path is silent like TS
// incoming (the /guild command path notifies separately).
func socGuildKick(c *playerConn, username string, viaCommand bool) {
	before, err := socGuilds.GuildOf(c.username)
	if err != nil {
		if viaCommand {
			m6Notify(c, "You are not in a guild.")
		}
		return
	}
	if err := socGuilds.Kick(c.username, username); err != nil {
		if viaCommand {
			if err == guilds.ErrNoPermission {
				m6Notify(c, "guilds:NO_PERMISSION")
			} else {
				m6Notify(c, "guilds:NO_PERMISSION")
			}
		}
		return
	}
	socPersistGuildID(before.ID)
	if t := m7PlayerByName(username); t != nil {
		_ = send(t.conn, pktOp(PacketGuild, GuildLeave, nil))
	}
	for _, name := range socOnlineGuildmates(before, username) {
		if name == c.username {
			continue
		}
		if t := m7PlayerByName(name); t != nil {
			_ = send(t.conn, pktOp(PacketGuild, GuildLeave, map[string]any{
				"username": username,
			}))
		}
	}
	if viaCommand {
		m6Notify(c, "You have kicked "+username+" from your guild.")
	}
	log.Printf("social: %s kicked %s from %s", c.username, username, before.ID)
}

// socBroadcastGuildRank fans a Rank frame to every online member of g.
func socBroadcastGuildRank(g *guilds.Guild, username string, rank int) {
	for _, name := range socOnlineGuildmates(g, "") {
		if t := m7PlayerByName(name); t != nil {
			_ = send(t.conn, pktOp(PacketGuild, GuildRank, map[string]any{
				"username": username, "rank": rank,
			}))
		}
	}
}

// socGuildRankCommand ports the /guild rank subcommand strings
// (commands.ts:129-158): landlord guard, member lookup, rank set + ack.
func socGuildRankCommand(c *playerConn, rankStr, username string) {
	if rankStr == "7" || rankStr == "landlord" {
		m6Notify(c, "You cannot set a rank to landlord.")
		return
	}
	var n int
	if _, err := fmt.Sscanf(rankStr, "%d", &n); err != nil {
		m6Notify(c, "Malformed command, expected /guild rank [rank 0-6] [username]")
		return
	}
	g, err := socGuilds.GuildOf(c.username)
	if err != nil {
		m6Notify(c, "You are not in a guild.")
		return
	}
	if _, ok := g.Members[username]; !ok {
		m6Notify(c, "Could not find a member with the name: "+username+".")
		return
	}
	if serr := socGuilds.SetRank(c.username, username, guilds.Rank(n)); serr != nil {
		if serr == guilds.ErrNoPermission {
			m6Notify(c, "guilds:NO_PERMISSION_RANK")
		}
		return
	}
	socPersistGuildID(g.ID)
	socBroadcastGuildRank(g, username, n)
	m6Notify(c, "You have set "+username+"'s rank to "+rankStr+".")
}

// socGuildInvite records a pending invite (owner-only gate mirrors kick's).
// There is no TS invite RPC, so invites ride `/guild invite` (no new packet
// shape) with plain-text notifies on both ends.
func socGuildInvite(c *playerConn, target string) {
	name := strings.TrimSpace(target)
	if name == "" {
		m6Notify(c, "Malformed command, expected /guild invite [username]")
		return
	}
	g, err := socGuilds.GuildOf(c.username)
	if err != nil {
		m6Notify(c, "You are not in a guild.")
		return
	}
	if err := socGuilds.Invite(c.username, name); err != nil {
		switch err {
		case guilds.ErrNoPermission:
			m6Notify(c, "guilds:NO_PERMISSION")
		case guilds.ErrAlreadyInGuild:
			m6Notify(c, "guilds:ALREADY_IN_GUILD")
		default:
			m6Notify(c, "guilds:NO_PERMISSION")
		}
		return
	}
	m6Notify(c, "You have invited "+name+" to your guild.")
	if t := m7PlayerByName(name); t != nil {
		m6Notify(t, "You have been invited to guild "+g.Name+".")
	}
	log.Printf("social: %s invited %s to %s", c.username, name, g.ID)
}

// ---------------------------------------------------------------------------
// Login / logout (world.ts syncFriendsList/syncGuildMembers parity) + hub
// presence + disconnect cleanup for all three subsystems.
// ---------------------------------------------------------------------------

// socOnLogin registers hub presence, restores friends, and builds the login
// frames (Friends List + guild Login/Update when guilded). Presence fanout to
// other conns goes out directly; the returned frames join the Welcome bulk.
func socOnLogin(c *playerConn) [][]any {
	if c == nil || c.username == "" {
		return nil
	}
	socHub.Register(strings.ToLower(c.username))
	socLoadFriends(c.username)
	l := socFriendsFor(c.username)
	var frames [][]any
	frames = append(frames, socFriendsListFrame(l))
	if g, err := socGuilds.GuildOf(c.username); err == nil {
		frames = append(frames, socGuildLoginFrame(g))
		socSendGuildUpdateOnline(c)
		for _, name := range socOnlineGuildmates(g, c.username) {
			if t := m7PlayerByName(name); t != nil {
				_ = send(t.conn, pktOp(PacketGuild, GuildUpdate, map[string]any{
					"members": []socGuildMember{{Username: c.username, ServerID: intp(socServerID)}},
				}))
			}
		}
	}
	// Friends online fanout: every online owner listing this user gets a
	// Status update (world.ts syncFriendsList parity).
	for _, name := range m7PlayerUsernames() {
		if name == c.username {
			continue
		}
		o := m7PlayerByName(name)
		if o == nil {
			continue
		}
		ol := socFriendsFor(name)
		socMu.Lock()
		has := ol.IsFriend(c.username)
		if has {
			ol.SetStatus(c.username, true, socServerID)
		}
		socMu.Unlock()
		if has {
			socSendFriendsStatus(o, c.username, true, socServerID)
		}
	}
	return frames
}

// socOnDisconnect unregisters hub presence, flushes friends, and fans logout
// presence out (Status offline + guild Update serverId -1). Called from
// removeClient after the conn leaves the players map.
func socOnDisconnect(c *playerConn) {
	if c == nil || c.username == "" {
		return
	}
	socHub.Unregister(strings.ToLower(c.username))
	socPersistFriends(c.username)
	for _, name := range m7PlayerUsernames() {
		o := m7PlayerByName(name)
		if o == nil {
			continue
		}
		ol := socFriendsFor(name)
		socMu.Lock()
		has := ol.IsFriend(c.username)
		if has {
			ol.SetStatus(c.username, false, friends.OfflineServerID)
		}
		socMu.Unlock()
		if has {
			socSendFriendsStatus(o, c.username, false, friends.OfflineServerID)
		}
	}
	if g, err := socGuilds.GuildOf(c.username); err == nil {
		for _, name := range socOnlineGuildmates(g, c.username) {
			if t := m7PlayerByName(name); t != nil {
				_ = send(t.conn, pktOp(PacketGuild, GuildUpdate, map[string]any{
					"members": []socGuildMember{{Username: c.username, ServerID: intp(friends.OfflineServerID)}},
				}))
			}
		}
	}
	log.Printf("social: %s disconnected (presence flushed)", c.username)
}

// socRouteChat resolves a PM target through the Router (all-in-one: the
// single process owns every player, so ErrOffline falls back to the existing
// local lookup + misc:NOT_ONLINE notify — behavior unchanged).
func socRouteChat(target string) *playerConn {
	if recips, err := socHub.Route(hub.Message{Kind: hub.KindChat, To: strings.ToLower(target)}); err == nil && len(recips) > 0 {
		if t := m7PlayerByName(target); t != nil {
			return t
		}
	}
	return m7PlayerByName(target)
}

// socRouteGlobal delivers a global chat line to the Router-resolved online
// set, falling back to the existing broadcast when the target set is offline
// (empty router — same recipients either way in all-in-one mode).
func socRouteGlobal(frame []any) {
	recips, _ := socHub.Route(hub.Message{Kind: hub.KindGlobal})
	if len(recips) == 0 {
		broadcast(frame)
		return
	}
	anySent := false
	for _, name := range recips {
		if t := m7PlayerByName(name); t != nil {
			_ = send(t.conn, frame)
			anySent = true
		}
	}
	if !anySent {
		broadcast(frame)
	}
}

// ---------------------------------------------------------------------------
// TESTMAP debug dispatcher ([46 {socialtest:...}], abtest/pettest precedent).
// ---------------------------------------------------------------------------

// socTestHandler echoes friends/guild/hub state through m6Notify (the e2e
// greps these): ops "friends", "guild", "hub".
func socTestHandler(c *playerConn, data []byte) {
	if !testMode || c == nil {
		return
	}
	var d struct {
		SocialTest string `json:"socialtest"`
	}
	if err := json.Unmarshal(data, &d); err != nil || d.SocialTest == "" {
		return
	}
	switch d.SocialTest {
	case "friends":
		l := socFriendsFor(c.username)
		socMu.Lock()
		members := l.Members()
		parts := make([]string, 0, len(members))
		for _, n := range members {
			fi, _ := l.Info(n)
			parts = append(parts, n)
			_ = fi
		}
		socMu.Unlock()
		sort.Strings(parts)
		m6Notify(c, "social:friends "+c.username+"=["+strings.Join(parts, ",")+"]")
	case "guild":
		g, err := socGuilds.GuildOf(c.username)
		if err != nil {
			m6Notify(c, "social:guild "+c.username+"=none")
			return
		}
		names := make([]string, 0, len(g.Members))
		for u, r := range g.Members {
			names = append(names, u+"="+itoa(int64(int(r))))
		}
		sort.Strings(names)
		m6Notify(c, "social:guild "+g.ID+" ["+strings.Join(names, ",")+"]")
	case "hub":
		recips, _ := socHub.Route(hub.Message{Kind: hub.KindGlobal})
		m6Notify(c, "social:hub ["+strings.Join(recips, ",")+"]")
	}
}
