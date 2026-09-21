package guilds

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// mustCreate creates a guild or fails the test.
func mustCreate(t *testing.T, r *Registry, owner, name string) *Guild {
	t.Helper()
	g, err := r.Create(owner, name)
	if err != nil {
		t.Fatalf("Create(%q,%q) = %v, want nil", owner, name, err)
	}
	return g
}

// inviteAccept runs the owner-invite + target-accept join path.
func inviteAccept(t *testing.T, r *Registry, owner, target string) {
	t.Helper()
	g, err := r.GuildOf(owner)
	if err != nil {
		t.Fatalf("GuildOf(%q) = %v", owner, err)
	}
	if err := r.Invite(owner, target); err != nil {
		t.Fatalf("Invite(%q,%q) = %v, want nil", owner, target, err)
	}
	if err := r.AcceptInvite(target, g.ID); err != nil {
		t.Fatalf("AcceptInvite(%q,%q) = %v, want nil", target, g.ID, err)
	}
}

func TestRankOrderMatchesTS(t *testing.T) {
	want := []Rank{RankFledgling, RankEmergent, RankEstablished, RankAdept, RankVeteran, RankElite, RankMaster, RankLandlord}
	for i, rank := range want {
		if int(rank) != i {
			t.Fatalf("rank %d = %d, want %d (TS GuildRank order)", i, int(rank), i)
		}
		if i > 0 && want[i] <= want[i-1] {
			t.Fatalf("rank order not strictly ascending at %d", i)
		}
	}
}

func TestCreate(t *testing.T) {
	r := NewRegistry()
	g := mustCreate(t, r, "alice", "Knights")
	if g.ID != "knights" {
		t.Fatalf("ID = %q, want %q (lowercase-name identifier)", g.ID, "knights")
	}
	if g.Owner != "alice" || g.Members["alice"] != RankLandlord {
		t.Fatalf("owner not Landlord: %+v", g)
	}

	if _, err := r.Create("alice", "Mages"); !errors.Is(err, ErrAlreadyInGuild) {
		t.Fatalf("second Create by guilded owner = %v, want ErrAlreadyInGuild", err)
	}
	if _, err := r.Create("bob", "KNIGHTS"); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate name Create = %v, want ErrExists", err)
	}
	if _, err := r.Create("", "Mages"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty owner Create = %v, want ErrInvalid", err)
	}
	if _, err := r.Create("bob", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty name Create = %v, want ErrInvalid", err)
	}
}

func TestInviteAcceptJoin(t *testing.T) {
	r := NewRegistry()
	mustCreate(t, r, "alice", "Knights")
	inviteAccept(t, r, "alice", "bob")

	g, err := r.GuildOf("bob")
	if err != nil {
		t.Fatalf("GuildOf(bob) = %v", err)
	}
	if g.Members["bob"] != RankFledgling {
		t.Fatalf("new member rank = %d, want Fledgling", g.Members["bob"])
	}

	// No invite pending -> reject.
	if err := r.AcceptInvite("carol", g.ID); !errors.Is(err, ErrNoInvite) {
		t.Fatalf("AcceptInvite without invite = %v, want ErrNoInvite", err)
	}
	// Unknown guild -> not found.
	if err := r.AcceptInvite("carol", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AcceptInvite unknown guild = %v, want ErrNotFound", err)
	}
}

func TestDoubleGuildJoinRejected(t *testing.T) {
	r := NewRegistry()
	mustCreate(t, r, "alice", "Knights")
	mustCreate(t, r, "carol", "Mages")

	// bob joins Knights, then must be rejected from Mages.
	inviteAccept(t, r, "alice", "bob")
	if err := r.Invite("carol", "bob"); !errors.Is(err, ErrAlreadyInGuild) {
		t.Fatalf("Invite guilded player = %v, want ErrAlreadyInGuild", err)
	}
	mages, _ := r.GuildOf("carol")
	if err := r.AcceptInvite("bob", mages.ID); !errors.Is(err, ErrAlreadyInGuild) {
		t.Fatalf("AcceptInvite while guilded = %v, want ErrAlreadyInGuild", err)
	}
	// And a guilded player cannot found a second guild.
	if _, err := r.Create("bob", "Rogues"); !errors.Is(err, ErrAlreadyInGuild) {
		t.Fatalf("Create while guilded = %v, want ErrAlreadyInGuild", err)
	}
}

func TestKickPermissionMatrix(t *testing.T) {
	r := NewRegistry()
	mustCreate(t, r, "alice", "Knights")
	inviteAccept(t, r, "alice", "bob")
	inviteAccept(t, r, "alice", "carol")

	// Plain member cannot kick (TS: only the owner may kick).
	if err := r.Kick("bob", "carol"); !errors.Is(err, ErrNoPermission) {
		t.Fatalf("member Kick = %v, want ErrNoPermission", err)
	}
	// Owner cannot kick themselves.
	if err := r.Kick("alice", "alice"); !errors.Is(err, ErrNoPermission) {
		t.Fatalf("self Kick = %v, want ErrNoPermission", err)
	}
	// Kicking a non-member fails.
	if err := r.Kick("alice", "dave"); !errors.Is(err, ErrNotMember) {
		t.Fatalf("Kick non-member = %v, want ErrNotMember", err)
	}
	// Outsider (no guild) cannot kick.
	if err := r.Kick("dave", "bob"); !errors.Is(err, ErrNotMember) {
		t.Fatalf("outsider Kick = %v, want ErrNotMember", err)
	}
	// Owner can kick.
	if err := r.Kick("alice", "bob"); err != nil {
		t.Fatalf("owner Kick = %v, want nil", err)
	}
	if _, err := r.GuildOf("bob"); !errors.Is(err, ErrNotMember) {
		t.Fatalf("GuildOf kicked = %v, want ErrNotMember", err)
	}
}

func TestLeave(t *testing.T) {
	r := NewRegistry()
	mustCreate(t, r, "alice", "Knights")
	inviteAccept(t, r, "alice", "bob")

	if err := r.Leave("bob"); err != nil {
		t.Fatalf("Leave = %v, want nil", err)
	}
	if _, err := r.GuildOf("bob"); !errors.Is(err, ErrNotMember) {
		t.Fatalf("GuildOf after Leave = %v, want ErrNotMember", err)
	}
	// Leaving with no guild fails.
	if err := r.Leave("bob"); !errors.Is(err, ErrNotMember) {
		t.Fatalf("second Leave = %v, want ErrNotMember", err)
	}
	// Owner leaving disbands (TS leave() parity).
	if err := r.Leave("alice"); err != nil {
		t.Fatalf("owner Leave = %v, want nil", err)
	}
	if _, err := r.Get("knights"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after owner Leave = %v, want ErrNotFound", err)
	}
}

func TestSetRankPermissionMatrix(t *testing.T) {
	r := NewRegistry()
	mustCreate(t, r, "alice", "Knights")
	inviteAccept(t, r, "alice", "bob")
	inviteAccept(t, r, "alice", "carol")

	// Plain member cannot rank anyone (must strictly outrank the new rank).
	if err := r.SetRank("bob", "carol", RankEmergent); !errors.Is(err, ErrNoPermission) {
		t.Fatalf("member SetRank = %v, want ErrNoPermission", err)
	}
	// No self-rank.
	if err := r.SetRank("alice", "alice", RankMaster); !errors.Is(err, ErrNoPermission) {
		t.Fatalf("self SetRank = %v, want ErrNoPermission", err)
	}
	// Out-of-range ranks rejected.
	if err := r.SetRank("alice", "bob", Rank(99)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("high SetRank = %v, want ErrInvalid", err)
	}
	if err := r.SetRank("alice", "bob", Rank(-1)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("low SetRank = %v, want ErrInvalid", err)
	}
	// Unknown target rejected.
	if err := r.SetRank("alice", "dave", RankAdept); !errors.Is(err, ErrNotMember) {
		t.Fatalf("SetRank non-member = %v, want ErrNotMember", err)
	}
	// Owner promotes to Master (7-6>=1 ok).
	if err := r.SetRank("alice", "bob", RankMaster); err != nil {
		t.Fatalf("owner promote = %v, want nil", err)
	}
	// Landlord itself is ungrantable (needs rank 8 actor).
	if err := r.SetRank("alice", "bob", RankLandlord); !errors.Is(err, ErrNoPermission) {
		t.Fatalf("grant Landlord = %v, want ErrNoPermission", err)
	}
	// A Master can grant up to Elite but not Master (TS rank-rank<1 rule).
	if err := r.SetRank("bob", "carol", RankElite); err != nil {
		t.Fatalf("master grant Elite = %v, want nil", err)
	}
	if err := r.SetRank("bob", "carol", RankMaster); !errors.Is(err, ErrNoPermission) {
		t.Fatalf("master grant Master = %v, want ErrNoPermission", err)
	}
}

func TestInvitePermission(t *testing.T) {
	r := NewRegistry()
	mustCreate(t, r, "alice", "Knights")
	inviteAccept(t, r, "alice", "bob")

	// Only the owner may invite (kick-gate mirror).
	if err := r.Invite("bob", "carol"); !errors.Is(err, ErrNoPermission) {
		t.Fatalf("member Invite = %v, want ErrNoPermission", err)
	}
	if err := r.Invite("dave", "carol"); !errors.Is(err, ErrNotMember) {
		t.Fatalf("outsider Invite = %v, want ErrNotMember", err)
	}
	if err := r.Invite("alice", "bob"); !errors.Is(err, ErrAlreadyInGuild) {
		t.Fatalf("Invite existing member = %v, want ErrAlreadyInGuild", err)
	}
}

func TestDisband(t *testing.T) {
	r := NewRegistry()
	mustCreate(t, r, "alice", "Knights")
	inviteAccept(t, r, "alice", "bob")

	if err := r.Disband("bob"); !errors.Is(err, ErrNoPermission) {
		t.Fatalf("member Disband = %v, want ErrNoPermission", err)
	}
	if err := r.Disband("dave"); !errors.Is(err, ErrNotMember) {
		t.Fatalf("outsider Disband = %v, want ErrNotMember", err)
	}
	if err := r.Disband("alice"); err != nil {
		t.Fatalf("owner Disband = %v, want nil", err)
	}
	if _, err := r.GuildOf("bob"); !errors.Is(err, ErrNotMember) {
		t.Fatalf("GuildOf after Disband = %v, want ErrNotMember", err)
	}
}

func TestAddXP(t *testing.T) {
	r := NewRegistry()
	mustCreate(t, r, "alice", "Knights")
	inviteAccept(t, r, "alice", "bob")

	if err := r.AddXP("bob", 150); err != nil {
		t.Fatalf("AddXP = %v, want nil", err)
	}
	g, _ := r.GuildOf("alice")
	if g.XP != 150 {
		t.Fatalf("XP = %d, want 150", g.XP)
	}
	if err := r.AddXP("dave", 10); !errors.Is(err, ErrNotMember) {
		t.Fatalf("outsider AddXP = %v, want ErrNotMember", err)
	}
}

func TestGuildFull(t *testing.T) {
	r := NewRegistry()
	mustCreate(t, r, "alice", "Knights")
	for i := 0; i < MaxMembers-1; i++ {
		u := fmt.Sprintf("user%02d", i)
		if err := r.Invite("alice", u); err != nil {
			t.Fatalf("Invite(%q) = %v", u, err)
		}
		if err := r.AcceptInvite(u, "knights"); err != nil {
			t.Fatalf("AcceptInvite(%q) = %v", u, err)
		}
	}
	if err := r.Invite("alice", "extra"); err != nil {
		t.Fatalf("Invite at cap = %v, want nil (cap enforced on join)", err)
	}
	if err := r.AcceptInvite("extra", "knights"); !errors.Is(err, ErrFull) {
		t.Fatalf("AcceptInvite over cap = %v, want ErrFull", err)
	}
}

func TestSchemaSQL(t *testing.T) {
	ddl := SchemaSQL()
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS guilds(",
		"CREATE TABLE IF NOT EXISTS guild_members(",
		"id TEXT PRIMARY KEY",
		"PRIMARY KEY(guild, player)",
	} {
		if !strings.Contains(ddl, want) {
			t.Fatalf("SchemaSQL missing %q:\n%s", want, ddl)
		}
	}
}
