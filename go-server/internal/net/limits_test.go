package net

import "testing"

// IP cap enforced at the DefaultMaxConnectionsPerIP=16 boundary and freed by
// Release (mirrors TS isMaxConnections gating + removeAddress cleanup).
func TestLimiterIPCapEnforcedAndReleased(t *testing.T) {
	l := NewLimiter()
	ip := "10.0.0.1"

	for i := 0; i < DefaultMaxConnectionsPerIP; i++ {
		if !l.Acquire(ip) {
			t.Fatalf("Acquire #%d = false, want true (cap %d)", i+1, DefaultMaxConnectionsPerIP)
		}
	}
	if got := l.Count(ip); got != DefaultMaxConnectionsPerIP {
		t.Fatalf("Count = %d, want %d", got, DefaultMaxConnectionsPerIP)
	}
	if l.Acquire(ip) {
		t.Fatalf("Acquire over cap = true, want false")
	}
	// A different IP is unaffected by the first IP's cap.
	if !l.Acquire("10.0.0.2") {
		t.Fatalf("Acquire(other IP) = false, want true")
	}

	// Releasing one slot admits exactly one more conn.
	l.Release(ip)
	if got := l.Count(ip); got != DefaultMaxConnectionsPerIP-1 {
		t.Fatalf("Count after Release = %d, want %d", got, DefaultMaxConnectionsPerIP-1)
	}
	if !l.Acquire(ip) {
		t.Fatalf("Acquire after Release = false, want true")
	}
	if l.Acquire(ip) {
		t.Fatalf("Acquire over cap (2nd) = true, want false")
	}

	// Draining all slots returns the IP to zero with no negatives.
	for i := 0; i < DefaultMaxConnectionsPerIP; i++ {
		l.Release(ip)
	}
	if got := l.Count(ip); got != 0 {
		t.Fatalf("Count after drain = %d, want 0", got)
	}
	l.Release(ip) // extra release must not go negative
	if got := l.Count(ip); got != 0 {
		t.Fatalf("Count after extra Release = %d, want 0", got)
	}
	if !l.Acquire(ip) {
		t.Fatalf("Acquire after drain = false, want true")
	}
}

// Config seam: bus Config.MaxConnectionsPerIP is honored; zero falls back to
// the TS-mirroring default.
func TestLimiterFromConfigHonorsMaxConnectionsPerIP(t *testing.T) {
	l := NewLimiterFromConfig(Config{MaxConnectionsPerIP: 2})
	if !l.Acquire("1.2.3.4") || !l.Acquire("1.2.3.4") {
		t.Fatalf("Acquire x2 with cap=2 failed")
	}
	if l.Acquire("1.2.3.4") {
		t.Fatalf("Acquire over custom cap=2 = true, want false")
	}

	d := NewLimiterFromConfig(Config{})
	for i := 0; i < DefaultMaxConnectionsPerIP; i++ {
		if !d.Acquire("5.6.7.8") {
			t.Fatalf("default Acquire #%d = false, want true", i+1)
		}
	}
	if d.Acquire("5.6.7.8") {
		t.Fatalf("default Acquire over cap = true, want false")
	}
}

// Per-conn msg/s sliding window: bursts up to DefaultMaxMsgsPerSecond pass,
// the next message floods (rejected), and the budget returns once the 1s
// window slides past. Uses a fixed nowMs clock (no sleeps).
func TestLimiterMsgFloodRejected(t *testing.T) {
	l := NewLimiter()
	const nowMs = int64(1_000_000)

	for i := 0; i < DefaultMaxMsgsPerSecond; i++ {
		if !l.AllowMsg("conn-a", nowMs) {
			t.Fatalf("AllowMsg #%d = false, want true", i+1)
		}
	}
	if l.AllowMsg("conn-a", nowMs) {
		t.Fatalf("AllowMsg over budget = true, want false (flood must reject)")
	}
	// Rejection consumes nothing: still rejected without advancing the clock.
	if l.AllowMsg("conn-a", nowMs) {
		t.Fatalf("AllowMsg retry without time passing = true, want false")
	}
	// Per-conn isolation: another conn still has its full budget.
	if !l.AllowMsg("conn-b", nowMs) {
		t.Fatalf("AllowMsg(other conn) = false, want true")
	}
	// Sliding the window past the burst refills the budget.
	if !l.AllowMsg("conn-a", nowMs+msgWindowMs) {
		t.Fatalf("AllowMsg after window slide = false, want true")
	}
}

// Small-window variant proving the sliding (not fixed-reset) behavior: with
// staggered timestamps only the expired prefix frees up.
func TestLimiterMsgSlidingWindow(t *testing.T) {
	l := NewLimiterWithLimits(Limits{MaxMsgsPerSecond: 3})
	if !l.AllowMsg("c", 0) || !l.AllowMsg("c", 400) || !l.AllowMsg("c", 800) {
		t.Fatalf("initial burst of 3 must pass")
	}
	if l.AllowMsg("c", 900) {
		t.Fatalf("4th msg inside window = true, want false")
	}
	// At t=1000 the t=0 message expires (1000-0 !< 1000) but t=400/800 remain:
	// exactly one slot frees.
	if !l.AllowMsg("c", 1000) {
		t.Fatalf("AllowMsg at t=1000 = false, want true (one expiry)")
	}
	if l.AllowMsg("c", 1000) {
		t.Fatalf("AllowMsg 2nd at t=1000 = true, want false (only one freed)")
	}
}

// Chat token bucket mirrors m7.go (burst 3, refill 0.5/s): three immediate
// sends pass, the fourth rejects, and the bucket refills after the interval.
func TestLimiterChatBucketRefills(t *testing.T) {
	l := NewLimiter()
	const nowMs = int64(2_000_000)

	for i := 0; i < 3; i++ {
		if !l.AllowChat("chatter", nowMs) {
			t.Fatalf("AllowChat #%d = false, want true", i+1)
		}
	}
	if l.AllowChat("chatter", nowMs) {
		t.Fatalf("AllowChat over burst = true, want false")
	}
	// One refill per 2s: +1999ms is still empty, +2000ms frees one token.
	if l.AllowChat("chatter", nowMs+1999) {
		t.Fatalf("AllowChat at +1999ms = true, want false")
	}
	if !l.AllowChat("chatter", nowMs+2000) {
		t.Fatalf("AllowChat at +2000ms = false, want true (one token refilled)")
	}
	// Full recharge after a long idle interval restores the whole burst.
	if !l.AllowChat("chatter2", nowMs) {
		t.Fatalf("AllowChat(new conn) = false, want true")
	}
	for i := 0; i < 3; i++ {
		l.AllowChat("chatter2", nowMs) // drain whatever remains; ignore results
	}
	if l.AllowChat("chatter2", nowMs) {
		t.Fatalf("AllowChat drained = true, want false")
	}
	if !l.AllowChat("chatter2", nowMs+6000) {
		t.Fatalf("AllowChat after 6s idle = false, want true (full burst)")
	}
	for i := 0; i < 2; i++ {
		if !l.AllowChat("chatter2", nowMs+6000) {
			t.Fatalf("AllowChat burst #%d after recharge = false, want true", i+2)
		}
	}
	if l.AllowChat("chatter2", nowMs+6000) {
		t.Fatalf("AllowChat 4th after recharge = true, want false")
	}
}

// Forget drops per-conn state so a reconnected id starts fresh.
func TestLimiterForgetResetsConnState(t *testing.T) {
	l := NewLimiterWithLimits(Limits{MaxMsgsPerSecond: 1, ChatBurst: 1, ChatRefillPerSec: 0.5})
	const nowMs = int64(3_000_000)

	if !l.AllowMsg("c", nowMs) {
		t.Fatalf("AllowMsg = false, want true")
	}
	if l.AllowMsg("c", nowMs) {
		t.Fatalf("AllowMsg 2nd = true, want false")
	}
	if !l.AllowChat("c", nowMs) {
		t.Fatalf("AllowChat = false, want true")
	}
	if l.AllowChat("c", nowMs) {
		t.Fatalf("AllowChat 2nd = true, want false")
	}
	l.Forget("c")
	if !l.AllowMsg("c", nowMs) {
		t.Fatalf("AllowMsg after Forget = false, want true")
	}
	if !l.AllowChat("c", nowMs) {
		t.Fatalf("AllowChat after Forget = false, want true")
	}
}
