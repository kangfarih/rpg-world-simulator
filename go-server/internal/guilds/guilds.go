// Package guilds is an additive, transport-free guild domain engine for the
// Go server stub. The caller owns all packet I/O, ticking, and persistence:
// this package only computes guild membership transitions. The caller applies
// the results (Guild packets) and executes SchemaSQL() itself. There are no
// timers, goroutines, database calls, or network sends here.
//
// TS sources mirrored here:
//   - packages/server/src/controllers/guilds.ts — create/join/leave/kick/
//     chat/promote/demote/addExperience/setRank/getGuild/getMember/get.
//   - packages/common/network/impl/guild.ts — GuildData/Member/Decoration/
//     GuildPacketData shapes (GuildPacket opcode + data envelope; the packet
//     itself is emitted by the caller, not here).
//   - packages/common/network/opcodes.ts — Opcodes.Guild enum order
//     (Create0 Login1 Logout2 Join3 Leave4 Rank5 Update6 Experience7 Banner8
//     List9 Error10 Chat11 Promote12 Demote13 Kick14 — see the Op*
//     constants below for the exact order).
//   - packages/common/network/modules.ts — GuildRank enum order
//     (Fledgling0 Emergent1 Established2 Adept3 Veteran4 Elite5 Master6
//     Landlord7) and Constants.MAX_GUILD_MEMBERS (50).
//   - packages/common/network/packets.ts — Packets.Guild (35) for caller
//     wiring (S->C Guild frames are [35, opcode, data]).
//   - go-server/m13.go lines ~180-200 — the current not-in-a-guild stub
//     (m13GuildCommand notifies "You are not in a guild."); conventions only,
//     untouched by this package (ADDITIVE ONLY — no root edits).
//
// Packet ID conventions (for the caller, not emitted here): S->C Guild is
// [35, ...] (Packets.Guild); opcodes are the Op* constants below mirroring
// Opcodes.Guild.
package guilds

import (
	"errors"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// Rank model (modules.ts GuildRank order — numeric order matters: higher
// outranks lower and SetRank compares ranks arithmetically, as TS does).
// ---------------------------------------------------------------------------

// Rank is a guild member's ladder position. Values match TS GuildRank:
// Fledgling0 Emergent1 Established2 Adept3 Veteran4 Elite5 Master6 Landlord7.
type Rank int

const (
	RankFledgling   Rank = 0 // Modules.GuildRank.Fledgling
	RankEmergent    Rank = 1 // Modules.GuildRank.Emergent
	RankEstablished Rank = 2 // Modules.GuildRank.Established
	RankAdept       Rank = 3 // Modules.GuildRank.Adept
	RankVeteran     Rank = 4 // Modules.GuildRank.Veteran
	RankElite       Rank = 5 // Modules.GuildRank.Elite
	RankMaster      Rank = 6 // Modules.GuildRank.Master
	RankLandlord    Rank = 7 // Modules.GuildRank.Landlord (guild creator)
)

// MaxMembers caps guild size (Modules.Constants.MAX_GUILD_MEMBERS = 50).
// TS join() rejects with 'guilds:GUILD_FULL' at/above this count.
const MaxMembers = 50

// ---------------------------------------------------------------------------
// Guild opcodes (opcodes.ts Opcodes.Guild order) + packet id for the caller.
// ---------------------------------------------------------------------------

// PacketGuild is the S->C packet id carrying Guild frames (packets.ts:
// Packets.Guild = 35). This package never emits it; the caller does.
const PacketGuild = 35

// Opcodes.Guild (opcodes.ts:121) — Create0 Login1 Logout2 Join3 Leave4 Rank5
// Update6 Experience7 Banner8 List9 Error10 Chat11 Promote12 Demote13 Kick14.
const (
	OpCreate     = 0  // Opcodes.Guild.Create
	OpLogin      = 1  // Opcodes.Guild.Login
	OpLogout     = 2  // Opcodes.Guild.Logout
	OpJoin       = 3  // Opcodes.Guild.Join
	OpLeave      = 4  // Opcodes.Guild.Leave
	OpRank       = 5  // Opcodes.Guild.Rank
	OpUpdate     = 6  // Opcodes.Guild.Update
	OpExperience = 7  // Opcodes.Guild.Experience
	OpBanner     = 8  // Opcodes.Guild.Banner
	OpList       = 9  // Opcodes.Guild.List
	OpError      = 10 // Opcodes.Guild.Error
	OpChat       = 11 // Opcodes.Guild.Chat
	OpPromote    = 12 // Opcodes.Guild.Promote
	OpDemote     = 13 // Opcodes.Guild.Demote
	OpKick       = 14 // Opcodes.Guild.Kick
)

// ---------------------------------------------------------------------------
// Errors (explicit sentinels so the caller can map them to notifies).
// ---------------------------------------------------------------------------

var (
	// ErrNotFound is returned when the named guild does not exist.
	ErrNotFound = errors.New("guilds: guild not found")
	// ErrNotMember is returned when the actor (or target) is not in the
	// guild the operation requires.
	ErrNotMember = errors.New("guilds: not a member")
	// ErrNoPermission is returned when the actor lacks the rank/role the
	// operation requires (TS 'guilds:NO_PERMISSION' / NO_PERMISSION_RANK,
	// CANNOT_KICK_YOURSELF, and self-rank attempts).
	ErrNoPermission = errors.New("guilds: no permission")
	// ErrAlreadyInGuild is returned when a player who already belongs to a
	// guild tries to create or join another (TS 'guilds:ALREADY_IN_GUILD').
	ErrAlreadyInGuild = errors.New("guilds: already in a guild")
	// ErrExists is returned when a guild name (identifier) is already taken
	// (TS 'A guild with that name already exists.').
	ErrExists = errors.New("guilds: guild already exists")
	// ErrFull is returned when a join would exceed MaxMembers
	// (TS 'guilds:GUILD_FULL').
	ErrFull = errors.New("guilds: guild is full")
	// ErrInvalid is returned for empty names/usernames or out-of-range ranks
	// (TS logs 'invalid rank' warnings; empties are unvalidated in TS).
	ErrInvalid = errors.New("guilds: invalid argument")
	// ErrNoInvite is returned when accepting without a pending invite (no TS
	// counterpart — TS join() is open; see divergences).
	ErrNoInvite = errors.New("guilds: no pending invite")
)

// ---------------------------------------------------------------------------
// Guild + Registry (transport-free; all state behind a mutex).
// ---------------------------------------------------------------------------

// Guild is one guild. ID is the lowercase-name identifier (TS create():
// identifier = name.toLowerCase()). Owner is the creator's username (sole
// RankLandlord under TS rules — SetRank can never grant Landlord since it
// requires the actor to strictly outrank the new rank). Members maps username
// (exact case, as TS compares member.username) to rank. XP accumulates via
// AddXP (TS guild.experience).
type Guild struct {
	ID      string
	Name    string
	Owner   string
	Members map[string]Rank
	XP      int
}

// Registry holds all guilds plus the reverse (player -> guild) and pending
// invite indexes. Zero value is unusable; construct with NewRegistry.
type Registry struct {
	mu      sync.Mutex
	guilds  map[string]*Guild
	member  map[string]string
	invites map[string]map[string]bool // guild ID -> invited usernames
}

// NewRegistry returns an empty guild registry.
func NewRegistry() *Registry {
	return &Registry{
		guilds:  map[string]*Guild{},
		member:  map[string]string{},
		invites: map[string]map[string]bool{},
	}
}

// copyGuild snapshots a guild for safe hand-out (callers must not mutate
// registry state).
func copyGuild(g *Guild) *Guild {
	out := &Guild{ID: g.ID, Name: g.Name, Owner: g.Owner, XP: g.XP, Members: make(map[string]Rank, len(g.Members))}
	for u, r := range g.Members {
		out.Members[u] = r
	}
	return out
}

// Create founds a guild owned by owner (TS create(): owner joins as Landlord,
// 30k gold/tutorial/guest gates are caller-side — see divergences). The name
// must be unused (case-insensitive) and the owner guild-free.
func (r *Registry) Create(owner, name string) (*Guild, error) {
	if owner == "" || name == "" {
		return nil, ErrInvalid
	}
	id := strings.ToLower(name)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.member[owner]; ok {
		return nil, ErrAlreadyInGuild
	}
	if _, ok := r.guilds[id]; ok {
		return nil, ErrExists
	}
	g := &Guild{ID: id, Name: name, Owner: owner, Members: map[string]Rank{owner: RankLandlord}}
	r.guilds[id] = g
	r.member[owner] = id
	return copyGuild(g), nil
}

// Disband deletes the actor's guild (TS leave() by the owner: every member is
// removed and the row deleted). Only the owner may disband.
func (r *Registry) Disband(actor string) error {
	if actor == "" {
		return ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.member[actor]
	if !ok {
		return ErrNotMember
	}
	g, ok := r.guilds[id]
	if !ok {
		return ErrNotFound
	}
	if actor != g.Owner {
		return ErrNoPermission
	}
	for u := range g.Members {
		delete(r.member, u)
	}
	delete(r.guilds, id)
	delete(r.invites, id)
	return nil
}

// Invite records a pending invite for target into the actor's guild (no TS
// counterpart — TS has no invite RPC; owner-only gate mirrors kick's owner
// gate — see divergences). Invites are idempotent.
func (r *Registry) Invite(actor, target string) error {
	if actor == "" || target == "" {
		return ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.member[actor]
	if !ok {
		return ErrNotMember
	}
	g, ok := r.guilds[id]
	if !ok {
		return ErrNotFound
	}
	if actor != g.Owner {
		return ErrNoPermission
	}
	if _, ok := g.Members[target]; ok {
		return ErrAlreadyInGuild
	}
	if _, ok := r.member[target]; ok {
		return ErrAlreadyInGuild
	}
	if r.invites[id] == nil {
		r.invites[id] = map[string]bool{}
	}
	r.invites[id][target] = true
	return nil
}

// AcceptInvite joins username to guildID via a pending invite at Fledgling
// rank (TS join() starts members at Fledgling). Already-guilded players are
// rejected (TS 'guilds:ALREADY_IN_GUILD'), keeping one-guild membership.
func (r *Registry) AcceptInvite(username, guildID string) error {
	if username == "" || guildID == "" {
		return ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.member[username]; ok {
		return ErrAlreadyInGuild
	}
	g, ok := r.guilds[guildID]
	if !ok {
		return ErrNotFound
	}
	if !r.invites[guildID][username] {
		return ErrNoInvite
	}
	if len(g.Members) >= MaxMembers {
		return ErrFull
	}
	g.Members[username] = RankFledgling
	r.member[username] = guildID
	delete(r.invites[guildID], username)
	// Drop this player's other pending invites so no stale invite outlives
	// the one-guild rule (new-in-Go bookkeeping, no TS counterpart).
	for id, set := range r.invites {
		if id != guildID {
			delete(set, username)
		}
	}
	return nil
}

// Kick removes target from the actor's guild (TS kick(): owner-only,
// self-kick rejected). The victim's reverse mapping is cleared.
func (r *Registry) Kick(actor, target string) error {
	if actor == "" || target == "" {
		return ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.member[actor]
	if !ok {
		return ErrNotMember
	}
	g, ok := r.guilds[id]
	if !ok {
		return ErrNotFound
	}
	if actor != g.Owner {
		return ErrNoPermission
	}
	if actor == target {
		return ErrNoPermission
	}
	if _, ok := g.Members[target]; !ok {
		return ErrNotMember
	}
	delete(g.Members, target)
	delete(r.member, target)
	return nil
}

// Leave removes username from their guild (TS leave(): a plain member is
// filtered out and synced; the OWNER leaving disbands the whole guild —
// every member mapping cleared and the guild deleted).
func (r *Registry) Leave(username string) error {
	if username == "" {
		return ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.member[username]
	if !ok {
		return ErrNotMember
	}
	g, ok := r.guilds[id]
	if !ok {
		return ErrNotFound
	}
	if username == g.Owner {
		for u := range g.Members {
			delete(r.member, u)
		}
		delete(r.guilds, id)
		delete(r.invites, id)
		return nil
	}
	delete(g.Members, username)
	delete(r.member, username)
	return nil
}

// SetRank sets target's rank (TS setRank(), shared by promote/demote which
// pass member.rank +/- 1): self-rank is rejected, the new rank must stay in
// [Fledgling, Landlord], and the actor must strictly outrank the NEW rank
// (TS `playerMember.rank - rank < 1` -> 'guilds:NO_PERMISSION_RANK'). Hence a
// Landlord (7) can grant at most Master (6): Landlord itself is ungrantable
// and the owner un-demotable, exactly as in TS.
func (r *Registry) SetRank(actor, target string, rank Rank) error {
	if actor == "" || target == "" {
		return ErrInvalid
	}
	if rank < RankFledgling || rank > RankLandlord {
		return ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	aid, ok := r.member[actor]
	if !ok {
		return ErrNotMember
	}
	g, ok := r.guilds[aid]
	if !ok {
		return ErrNotFound
	}
	if actor == target {
		return ErrNoPermission
	}
	tr, ok := g.Members[target]
	_ = tr
	if !ok {
		return ErrNotMember
	}
	ar, ok := g.Members[actor]
	if !ok {
		return ErrNotMember
	}
	if ar-rank < 1 {
		return ErrNoPermission
	}
	g.Members[target] = rank
	return nil
}

// AddXP adds experience to the caller's guild (TS addExperience(): any member
// may contribute; the sum is unguarded, negatives included).
func (r *Registry) AddXP(username string, xp int) error {
	if username == "" {
		return ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.member[username]
	if !ok {
		return ErrNotMember
	}
	g, ok := r.guilds[id]
	if !ok {
		return ErrNotFound
	}
	g.XP += xp
	return nil
}

// Get returns a snapshot of the guild by ID (lowercase-name identifier).
func (r *Registry) Get(id string) (*Guild, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	g, ok := r.guilds[id]
	if !ok {
		return nil, ErrNotFound
	}
	return copyGuild(g), nil
}

// GuildOf returns a snapshot of username's guild.
func (r *Registry) GuildOf(username string) (*Guild, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.member[username]
	if !ok {
		return nil, ErrNotMember
	}
	g, ok := r.guilds[id]
	if !ok {
		return nil, ErrNotFound
	}
	return copyGuild(g), nil
}

// SchemaSQL returns the CREATE TABLE statements for guilds + guild_members.
// The caller executes them (one statement per Exec, mirroring the
// m11EnsureTables loop and the m11/m13 `CREATE TABLE IF NOT EXISTS ... TEXT /
// INT` column style). No DB work happens inside this package.
func SchemaSQL() string {
	return `CREATE TABLE IF NOT EXISTS guilds(id TEXT PRIMARY KEY, name TEXT, owner TEXT, xp INT);` +
		"\n" +
		`CREATE TABLE IF NOT EXISTS guild_members(guild TEXT, player TEXT, rank INT, PRIMARY KEY(guild, player));`
}

// Divergences from TS (documented; spec-mandated shape or new-in-Go logic):
//   - Guild struct shape is spec-fixed ({ID, Name, Owner, Members, XP}): it
//     drops GuildData fields the stub has no systems for — creationDate,
//     inviteOnly, decoration (banner/outline/crest), joinDate per member, and
//     serverId/online presence. The caller re-adds them at the packet layer.
//   - Invite/AcceptInvite are new: TS guilds.ts has NO invite RPC — join() is
//     open (any guildless player may join any known, non-full guild; the
//     inviteOnly flag only hides guilds from the list response). Here every
//     join requires a prior Invite, i.e. invite-only semantics. Callers
//     wanting TS open-join emulate it with Invite immediately followed by
//     AcceptInvite.
//   - Invite gate is owner-only (mirrors kick's `player.username !==
//     guild.owner` gate). TS constrains nothing because the RPC does not
//     exist; a looser (officer+) rule would also be defensible.
//   - create() economy/progression gates (30_000 gold, tutorial finished, no
//     guests) and duplicate-name/guest/already-in-guild notifies are
//     caller-side: this package checks membership + name uniqueness only and
//     reports them as ErrAlreadyInGuild/ErrExists/ErrInvalid.
//   - Kick maps TS's 'CANNOT_KICK_YOURSELF' notify to ErrNoPermission (same
//     sentinel as the non-owner path); TS distinguishes the strings.
//   - Leave by the owner disbands (TS leave() parity: kick-everyone +
//     deleteGuild). Disband() is the explicit owner-only equivalent.
//   - SetRank maps TS's self-rank and invalid-rank warning logs to
//     ErrNoPermission/ErrInvalid so callers can notify instead of dropping.
//   - AddXP accepts negatives (TS `guild.experience += experience` is
//     unguarded) and any member may contribute (TS checks membership only).
//   - Usernames are exact-case keys (TS `member.username ===` compares); only
//     guild IDs (names) are case-folded. No guest/tutorial/max-gold logic.
//   - AcceptInvite clears the joiner's OTHER pending invites (no TS
//     counterpart; TS has no invite state to go stale).
//   - Rank is a plain int enum: TS GuildRank has no methods either, but TS
//     member.rank is optional (undefined for presence-only Update entries);
//     here every member always carries a concrete rank.
