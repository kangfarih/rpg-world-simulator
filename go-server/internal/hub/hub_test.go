package hub

import (
	"errors"
	"reflect"
	"testing"
)

// Register makes a username routable; Unregister revokes it; Route delivers
// a direct chat message while registered and ErrOffline after.
func TestRegisterRouteUnregister(t *testing.T) {
	r := NewRouter()
	r.Register("alice")
	r.Register("bob")

	got, err := r.Route(Message{From: "alice", To: "bob", Kind: KindChat})
	if err != nil {
		t.Fatalf("Route(chat alice->bob) err = %v, want nil", err)
	}
	if !reflect.DeepEqual(got, []string{"bob"}) {
		t.Fatalf("Route(chat alice->bob) = %v, want [bob]", got)
	}

	r.Unregister("bob")

	if _, err := r.Route(Message{From: "alice", To: "bob", Kind: KindChat}); !errors.Is(err, ErrOffline) {
		t.Fatalf("Route(chat alice->bob) after Unregister err = %v, want ErrOffline", err)
	}
}

// Routing a direct message to a never-registered name fails with ErrOffline
// so the caller can fall back to local broadcast.
func TestRouteOfflineError(t *testing.T) {
	r := NewRouter()
	r.Register("alice")

	if _, err := r.Route(Message{From: "alice", To: "ghost", Kind: KindChat}); !errors.Is(err, ErrOffline) {
		t.Fatalf("Route(chat alice->ghost) err = %v, want ErrOffline", err)
	}
}

// Guild messages fan out to the online subset of the caller-supplied
// candidate member list (TS handleGuild Update semantics, resolved
// transport-free): offline members are filtered, the result is sorted.
func TestGuildFanout(t *testing.T) {
	r := NewRouter()
	r.Register("bob")
	r.Register("alice")
	// "carol" stays offline; "ghost" was never registered.

	got, err := r.Route(Message{
		From:    "alice",
		Kind:    KindGuild,
		Payload: []string{"carol", "ghost", "bob", "alice"},
	})
	if err != nil {
		t.Fatalf("Route(guild) err = %v, want nil", err)
	}
	if want := []string{"alice", "bob"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Route(guild) = %v, want %v", got, want)
	}
}

// Global messages address every online username (TS handleChat no-target
// globalMessage path, resolved transport-free).
func TestGlobalBroadcast(t *testing.T) {
	r := NewRouter()
	r.Register("bob")
	r.Register("alice")

	got, err := r.Route(Message{From: "alice", Kind: KindGlobal})
	if err != nil {
		t.Fatalf("Route(global) err = %v, want nil", err)
	}
	if want := []string{"alice", "bob"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Route(global) = %v, want %v", got, want)
	}
}
