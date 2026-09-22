package world

import (
	"testing"

	"github.com/gorilla/websocket"

	transport "rpg-world-server/internal/net"
	"rpg-world-server/internal/protocol"
)

// testRegistry returns an isolated registry with test geometry: sideLen 24,
// div 48 (tile 100,96 -> region 50, tile 0,0 -> region 0) and identity
// surrounding sets.
func testRegistry() *Registry {
	r := NewRegistry()
	r.Configure(24, 48, func(rid int) []int { return []int{rid} }, nil)
	return r
}

// TestRegistryEntities ensures the position index upserts, reads and drops.
func TestRegistryEntities(t *testing.T) {
	r := testRegistry()
	r.SetPos("m-1", 100, 96)
	r.SetPos("m-1", 101, 96)
	if x, y, ok := r.Pos("m-1"); !ok || x != 101 || y != 96 {
		t.Fatalf("Pos = %d,%d,%v, want 101,96,true", x, y, ok)
	}
	if _, _, ok := r.Pos("nope"); ok {
		t.Fatal("unknown instance unexpectedly found")
	}
	if n := r.Count(); n != 1 {
		t.Fatalf("Count = %d, want 1", n)
	}
	r.Remove("m-1")
	if _, _, ok := r.Pos("m-1"); ok {
		t.Fatal("removed instance unexpectedly found")
	}
}

// TestRegistryPlayers ensures the connection table lookup by socket and by
// instance, plus the typed accessors.
func TestRegistryPlayers(t *testing.T) {
	r := testRegistry()
	c := transport.NewConn(nil, "p-1")
	r.Add(nil, c)
	if v, ok := r.ByWS(nil); !ok || v != any(c) {
		t.Fatalf("ByWS = %v,%v, want conn,true", v, ok)
	}
	if v, ok := r.ByInstance("p-1"); !ok || v != any(c) {
		t.Fatalf("ByInstance = %v,%v, want conn,true", v, ok)
	}
	found, ok := r.ByInstance("p-1")
	if !ok {
		t.Fatal("ByInstance unexpectedly missed")
	}
	if tc, ok := found.(*transport.Conn); !ok || tc != c {
		t.Fatal("stored value does not round-trip")
	}
	if n := r.CountPlayers(); n != 1 {
		t.Fatalf("CountPlayers = %d, want 1", n)
	}
	if conns := r.Conns(); len(conns) != 1 || conns[0] != c {
		t.Fatalf("Conns = %v, want [conn]", conns)
	}
	if v := r.RemoveWS(nil); v != any(c) {
		t.Fatalf("RemoveWS returned %v, want conn", v)
	}
	if n := r.CountPlayers(); n != 0 {
		t.Fatalf("CountPlayers after remove = %d, want 0", n)
	}
}

// TestRegistryRegionInterest ensures tile->region math, interest updates and
// the region-enter hook.
func TestRegistryRegionInterest(t *testing.T) {
	var hooked []any
	r := testRegistry()
	r.Configure(24, 48, func(rid int) []int { return []int{rid} }, func(v any) {
		hooked = append(hooked, v)
	})
	if got := r.RegionOf(100, 96); got != 50 {
		t.Fatalf("RegionOf(100,96) = %d, want 50", got)
	}
	c := transport.NewConn(nil, "p-1")
	r.Add(nil, c)
	r.UpdateRegion(c, 100, 96)
	if regions := c.Regions(); len(regions) != 1 || regions[0] != 50 {
		t.Fatalf("interest = %v, want [50]", regions)
	}
	if len(hooked) != 1 {
		t.Fatalf("region hook calls = %d, want 1", len(hooked))
	}
	if !r.Interested(c, 101, 96) {
		t.Fatal("same-region tile unexpectedly not interested")
	}
	if r.Interested(c, 0, 0) {
		t.Fatal("foreign-region tile unexpectedly interested")
	}
	if !r.InterestedRegion(50, 50) || r.InterestedRegion(0, 50) {
		t.Fatal("InterestedRegion mismatch")
	}
}

// TestRegistryBroadcastRouting ensures region-scoped frames reach only
// interested conns while global frames fan out (and unknown instances fan
// out, matching the old broadcast).
func TestRegistryBroadcastRouting(t *testing.T) {
	r := testRegistry()
	k1, k2 := &websocket.Conn{}, &websocket.Conn{}
	c1 := transport.NewConn(k1, "p-1")
	c2 := transport.NewConn(k2, "p-2")
	r.Add(k1, c1)
	r.Add(k2, c2)
	r.UpdateRegion(c1, 100, 96) // region 50
	r.UpdateRegion(c2, 0, 0)    // region 0
	r.SetPos("mob-1", 101, 96)  // region 50

	r.Broadcast(protocol.Pkt(protocol.PacketSpawn, map[string]any{"instance": "mob-1"}))
	if len(c1.Outbox) != 1 {
		t.Fatalf("interested outbox len = %d, want 1", len(c1.Outbox))
	}
	if len(c2.Outbox) != 0 {
		t.Fatalf("uninterested outbox len = %d, want 0", len(c2.Outbox))
	}

	r.Broadcast(protocol.Pkt(protocol.PacketTeleport, map[string]any{"instance": "mob-1"}))
	if len(c1.Outbox) != 2 || len(c2.Outbox) != 1 {
		t.Fatalf("global fan-out = %d/%d, want 2/1", len(c1.Outbox), len(c2.Outbox))
	}

	r.Broadcast(protocol.Pkt(protocol.PacketSpawn, map[string]any{"instance": "ghost"}))
	if len(c1.Outbox) != 3 || len(c2.Outbox) != 2 {
		t.Fatalf("unknown-instance fan-out = %d/%d, want 3/2", len(c1.Outbox), len(c2.Outbox))
	}
}

// TestRegistryRemoveClient ensures the disconnect path cleans the tables,
// runs hooks lock-free and fans the Despawn out.
func TestRegistryRemoveClient(t *testing.T) {
	r := testRegistry()
	var hooks []string
	r.OnDisconnect(func(v any) { hooks = append(hooks, "first") })
	r.OnDisconnect(func(v any) { hooks = append(hooks, "second") })
	c := transport.NewConn(nil, "p-9")
	c.Username = "hero9"
	r.Add(nil, c)
	r.SetPos("p-9", 100, 96)

	if v := r.RemoveClient(nil); v != any(c) {
		t.Fatalf("RemoveClient returned %v, want conn", v)
	}
	if len(hooks) != 2 || hooks[0] != "first" || hooks[1] != "second" {
		t.Fatalf("hooks = %v, want [first second]", hooks)
	}
	if _, _, ok := r.Pos("p-9"); ok {
		t.Fatal("entity unexpectedly retained after disconnect")
	}
	if n := r.CountPlayers(); n != 0 {
		t.Fatalf("players after disconnect = %d, want 0", n)
	}
	if v := r.RemoveClient(nil); v != nil {
		t.Fatalf("second RemoveClient = %v, want nil", v)
	}
}
