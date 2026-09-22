package net

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"rpg-world-server/internal/protocol"
)

// TestConnRegionsCopy ensures Regions snapshots (mutating the result must
// not affect routing state) and BumpDropped counts.
func TestConnRegionsCopy(t *testing.T) {
	c := NewConn(nil, "p-1")
	if c.Sess.PlayerX != 100 || c.Sess.PlayerY != 96 || c.Sess.MovementSpeed != 220 {
		t.Fatalf("default session = %+v, want spawn 100,96 speed 220", c.Sess)
	}
	if len(c.Outbox) != 0 || cap(c.Outbox) != OutboxSize {
		t.Fatalf("outbox cap = %d, want %d", cap(c.Outbox), OutboxSize)
	}
	c.SetRegions([]int{50, 25})
	got := c.Regions()
	got[0] = -1
	if again := c.Regions(); again[0] != 50 {
		t.Fatalf("Regions aliases internal state: %v", again)
	}
	if n := c.BumpDropped(); n != 1 {
		t.Fatalf("BumpDropped = %d, want 1", n)
	}
}

// TestTryEnqueueOverflow ensures the non-blocking enqueue reports overflow
// at capacity (the caller owns drop accounting).
func TestTryEnqueueOverflow(t *testing.T) {
	c := NewConn(nil, "p-1")
	for i := 0; i < OutboxSize; i++ {
		if !TryEnqueue(c, Frame{protocol.PacketTeleport, nil}) {
			t.Fatalf("enqueue %d unexpectedly dropped", i)
		}
	}
	if TryEnqueue(c, Frame{protocol.PacketTeleport, nil}) {
		t.Fatal("enqueue over capacity unexpectedly accepted")
	}
}

// TestRegionScopedFrozen ensures the region-routed packet set is unchanged.
func TestRegionScopedFrozen(t *testing.T) {
	scoped := []int{
		protocol.PacketSpawn, protocol.PacketMovement, protocol.PacketAnimation,
		protocol.PacketCombat, protocol.PacketResource, protocol.PacketEffect,
		protocol.PacketChat, protocol.PacketDeath, protocol.PacketRespawn,
	}
	for _, id := range scoped {
		if !RegionScoped(id) {
			t.Fatalf("packet %d should be region-scoped", id)
		}
	}
	for _, id := range []int{
		protocol.PacketWelcome, protocol.PacketTeleport, protocol.PacketPoints,
		protocol.PacketDespawn, protocol.PacketList, protocol.PacketSync,
		protocol.PacketEquipment, protocol.PacketHeal,
	} {
		if RegionScoped(id) {
			t.Fatalf("packet %d should fan out globally", id)
		}
	}
}

// TestFrameInstanceProbe ensures instance extraction from S->C frames.
func TestFrameInstanceProbe(t *testing.T) {
	f := Frame{protocol.PacketSpawn, map[string]any{"instance": "m-1"}}
	if got := FrameInstance(f); got != "m-1" {
		t.Fatalf("FrameInstance = %q, want m-1", got)
	}
	if got := FrameInstance(Frame{protocol.PacketConnected}); got != "" {
		t.Fatalf("short frame instance = %q, want empty", got)
	}
}

// TestHubSendEnqueues ensures Send queues (never direct-writes) and drops
// with accounting at capacity.
func TestHubSendEnqueues(t *testing.T) {
	h := NewHub()
	c := NewConn(nil, "p-1")
	if err := h.Send(c, Frame{protocol.PacketTeleport, nil}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(c.Outbox) != 1 {
		t.Fatalf("outbox len = %d, want 1", len(c.Outbox))
	}
	for len(c.Outbox) < OutboxSize {
		if !TryEnqueue(c, Frame{protocol.PacketTeleport, nil}) {
			t.Fatal("fill unexpectedly dropped")
		}
	}
	if err := h.Send(c, Frame{protocol.PacketTeleport, nil}); err != nil {
		t.Fatalf("Send over capacity: %v", err)
	}
	if got := len(c.Outbox); got != OutboxSize {
		t.Fatalf("outbox len = %d, want cap %d (overflow must drop)", got, OutboxSize)
	}
}

// TestHubAcceptGate ensures update-mode, IP bans and the per-IP cap reject
// with the frozen log/status behavior, and a failed upgrade releases its
// slot (a plain non-WS request never upgrades).
func TestHubAcceptGate(t *testing.T) {
	h := NewHub()

	h.SetAccepting(false)
	w, r := httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "9.9.9.9:1234"
	if _, ok := h.Accept(w, r); ok {
		t.Fatal("update-mode accept unexpectedly admitted")
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("update-mode status = %d, want 503", w.Code)
	}
	h.SetAccepting(true)

	h.BanIP("1.2.3.4")
	w, r = httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "1.2.3.4:5678"
	if _, ok := h.Accept(w, r); ok {
		t.Fatal("banned accept unexpectedly admitted")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("banned status = %d, want 403", w.Code)
	}

	ip := "5.6.7.8"
	for i := 0; i < DefaultMaxConnectionsPerIP; i++ {
		if !h.limiter.Acquire(ip) {
			t.Fatalf("acquire %d unexpectedly rejected", i)
		}
	}
	w, r = httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = ip + ":9999"
	if _, ok := h.Accept(w, r); ok {
		t.Fatal("over-cap accept unexpectedly admitted")
	}
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("over-cap status = %d, want 429", w.Code)
	}

	// A failed upgrade must release the acquired slot: after the failure
	// the limiter count for a fresh IP is back to zero.
	w, r = httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "7.7.7.7:1111"
	if _, ok := h.Accept(w, r); ok {
		t.Fatal("non-WS accept unexpectedly admitted")
	}
	if n := h.limiter.Count("7.7.7.7"); n != 0 {
		t.Fatalf("limiter count after failed upgrade = %d, want 0", n)
	}
}
