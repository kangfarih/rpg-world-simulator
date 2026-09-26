package app

import (
	"strings"
	"testing"

	"rpg-world-server/internal/console"
	"rpg-world-server/internal/hub"
)

// registerRouterShard is a test fake for a live shard: registers name with
// the given presence list plus routing stamps.
func registerRouterShard(t *testing.T, h *hub.Server, name, addr string, players []string) {
	t.Helper()
	if err := h.Register(hub.HubHandshake{
		Type: "hub", Name: name, Addr: addr, Players: players,
		BuildID: "testbuild", State: "RUNNING",
	}); err != nil {
		t.Fatalf("Register(%s): %v", name, err)
	}
}

// /server prints the emptiest/newest-target entry (sane text naming the
// login target); an empty table prints "undefined" (TS
// console.log(findEmptyServer()) parity).
func TestRouterConsoleServer(t *testing.T) {
	h := hub.NewServer("", nil)
	c := &routerConsole{h: h}
	if got := c.Server(); got != "undefined" {
		t.Fatalf("empty Server() = %q, want undefined", got)
	}
	registerRouterShard(t, h, "old", "127.0.0.1:9001", []string{"alice"})
	registerRouterShard(t, h, "new", "127.0.0.1:9002", []string{"bob", "carol"})
	got := c.Server()
	if !strings.Contains(got, "new") || !strings.Contains(got, "127.0.0.1:9002") {
		t.Fatalf("Server() = %q, want text naming the newest target (new @ 9002)", got)
	}
	var nilHub *routerConsole
	if out := nilHub.Server(); out != "undefined" {
		t.Fatalf("nil Server() = %q, want undefined", out)
	}
}

// /player prints the shard/presence entry hosting the name, "undefined"
// when online nowhere (TS console.log(findPlayer()) parity), and the exact
// TS malformed text on a bare /player (via HubExec, handler untouched).
func TestRouterConsolePlayer(t *testing.T) {
	h := hub.NewServer("", nil)
	registerRouterShard(t, h, "shardA", "127.0.0.1:9001", []string{"alice"})
	c := &routerConsole{h: h}

	if got := c.Player("alice"); !strings.Contains(got, "alice") || !strings.Contains(got, "shardA") {
		t.Fatalf("Player(alice) = %q, want presence text naming alice on shardA", got)
	}
	if got := c.Player("ghost"); got != "undefined" {
		t.Fatalf("Player(ghost) = %q, want undefined", got)
	}

	// End-to-end through the shared dispatch: bare /player is malformed.
	if got := console.HubExec(c, "/player"); got != "Malformed command - Format: /player [username]" {
		t.Fatalf("HubExec(/player) = %q, want exact TS malformed text", got)
	}
	if got := console.HubExec(c, "/player alice"); !strings.Contains(got, "shardA") {
		t.Fatalf("HubExec(/player alice) = %q, want presence text", got)
	}
	if got := console.HubExec(c, "/server"); !strings.Contains(got, "shardA") {
		t.Fatalf("HubExec(/server) = %q, want server-list entry", got)
	}
}
