package app

import (
	"testing"
	"time"

	"rpg-world-server/internal/hub"
	"rpg-world-server/internal/version"
)

// Two coexisting versions: old v1 on 9001, new v2 on 9002 (registration
// order = build newness: old first, new second).
func twoVersionHub(t *testing.T) *hub.Server {
	t.Helper()
	h := hub.NewServer("", nil)
	if err := h.Register(hub.HubHandshake{
		Type: "hub", Name: "old", Version: "v1",
		Addr: "127.0.0.1:9001", State: version.StateRunning, Load: 4,
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.Register(hub.HubHandshake{
		Type: "hub", Name: "new", Version: "v2",
		Addr: "127.0.0.1:9002", State: version.StateRunning, Load: 1,
	}); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestCanaryPctEnv(t *testing.T) {
	if got := CanaryPctFromEnv(getenvOf(nil)); got != 0 {
		t.Fatalf("default = %d, want 0 (100%% newest)", got)
	}
	if got := CanaryPctFromEnv(getenvOf(map[string]string{EnvCanaryPct: "5"})); got != 5 {
		t.Fatalf("5 = %d", got)
	}
	for _, raw := range []string{"bogus", "-3", "0"} {
		if got := CanaryPctFromEnv(getenvOf(map[string]string{EnvCanaryPct: raw})); got != 0 {
			t.Fatalf("%q = %d, want 0", raw, got)
		}
	}
	if got := CanaryPctFromEnv(getenvOf(map[string]string{EnvCanaryPct: "150"})); got != 100 {
		t.Fatalf("150 = %d, want clamp 100", got)
	}
}

func TestWarmHoldEnv(t *testing.T) {
	if got := WarmHoldFromEnv(getenvOf(nil)); got != DefaultWarmHold {
		t.Fatalf("default = %v, want %v", got, DefaultWarmHold)
	}
	if got := WarmHoldFromEnv(getenvOf(map[string]string{EnvWarmHold: "5m"})); got != 5*time.Minute {
		t.Fatalf("5m = %v", got)
	}
	if got := WarmHoldFromEnv(getenvOf(map[string]string{EnvWarmHold: "60"})); got != 60*time.Second {
		t.Fatalf("bare 60 = %v, want 60s", got)
	}
	if got := WarmHoldFromEnv(getenvOf(map[string]string{EnvWarmHold: "bogus"})); got != DefaultWarmHold {
		t.Fatalf("bogus = %v, want default", got)
	}
}

// CANARY_PCT=0 (default): every NEW login routes to the newest version,
// even with a previous healthy version registered.
func TestCanaryZeroAllNewest(t *testing.T) {
	h := twoVersionHub(t)
	for i := 0; i < 200; i++ {
		key := string(rune('a'+i%26)) + "-user"
		got, ok := RouteLogin(h, key, 0)
		if !ok || got.Addr != "127.0.0.1:9002" {
			t.Fatalf("RouteLogin(%q, 0) = %+v, %v; want 9002", key, got, ok)
		}
	}
}

// CANARY_PCT=5: ~5% of NEW logins route to the newest version, the rest to
// the previous healthy version; sticky per key (deterministic, no seed).
func TestCanarySplitFivePct(t *testing.T) {
	h := twoVersionHub(t)
	const n = 2000
	toNew := 0
	for i := 0; i < n; i++ {
		key := "canary-user-" + itoa(i)
		a, ok := RouteLogin(h, key, 5)
		if !ok {
			t.Fatalf("RouteLogin(%q) not ok", key)
		}
		// Sticky: same key decides the same build every time.
		b, _ := RouteLogin(h, key, 5)
		if a.Addr != b.Addr {
			t.Fatalf("RouteLogin(%q) flapped: %q vs %q", key, a.Addr, b.Addr)
		}
		// Hash rule: bucket<5 -> newest, else previous.
		want := "127.0.0.1:9001"
		if CanaryToNewest(key, 5) {
			want = "127.0.0.1:9002"
		}
		if a.Addr != want {
			t.Fatalf("RouteLogin(%q) = %q, want hash rule %q", key, a.Addr, want)
		}
		if a.Addr == "127.0.0.1:9002" {
			toNew++
		}
	}
	frac := float64(toNew) / n
	if frac < 0.02 || frac > 0.08 {
		t.Fatalf("canary 5%% over %d logins = %.2f%% (%d), want ~5%% ± tolerance", n, frac*100, toNew)
	}
	t.Logf("canary 5%% over %d logins = %.2f%% newest", n, frac*100)
}

// PreviousRunning names the older healthy version; none when single.
func TestPreviousRunning(t *testing.T) {
	h := twoVersionHub(t)
	prev, ok := PreviousRunning(h)
	if !ok || prev.Addr != "127.0.0.1:9001" {
		t.Fatalf("PreviousRunning = %+v, %v; want 9001", prev, ok)
	}
	single := hub.NewServer("", nil)
	if err := single.Register(hub.HubHandshake{Type: "hub", Name: "only", Version: "v1", Addr: "127.0.0.1:9001"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := PreviousRunning(single); ok {
		t.Fatal("single version must have no previous")
	}
	if _, ok := PreviousRunning(nil); ok {
		t.Fatal("nil hub must have no previous")
	}
}

// Warm-hold: the old version stays routable for WARM_HOLD after the newer
// version registers, then the hold expires (operator may TERM old).
func TestWarmHoldKeepsOldRoutable(t *testing.T) {
	h := hub.NewServer("", nil)
	base := time.Now()
	if err := h.Register(hub.HubHandshake{
		Type: "hub", Name: "old", Version: "v1", Addr: "127.0.0.1:9001",
	}); err != nil {
		t.Fatal(err)
	}
	tr := NewWarmTracker()
	tr.Observe(h, base)
	if err := h.Register(hub.HubHandshake{
		Type: "hub", Name: "new", Version: "v2", Addr: "127.0.0.1:9002",
	}); err != nil {
		t.Fatal(err)
	}
	tr.Observe(h, base.Add(time.Minute))

	const hold = 15 * time.Minute
	old, ok := tr.WarmOld(h, base.Add(time.Minute).Add(14*time.Minute), hold)
	if !ok || old.Addr != "127.0.0.1:9001" {
		t.Fatalf("within hold: WarmOld = %+v, %v; want old 9001", old, ok)
	}
	if _, ok := tr.WarmOld(h, base.Add(time.Minute).Add(16*time.Minute), hold); ok {
		t.Fatal("after hold expiry WarmOld must be false (old may shut down)")
	}
}

// Default list (no key, pct 0) keeps today's routing; personalized canary
// keys split newest/previous.
func TestBuildServerListForLogin(t *testing.T) {
	h := twoVersionHub(t)
	tr := NewWarmTracker()
	now := time.Now()
	tr.Observe(h, now)
	def := BuildServerListForLogin(h, "", 0, tr, now)
	if def.Preferred != "127.0.0.1:9002" {
		t.Fatalf("default preferred = %q, want 9002", def.Preferred)
	}
	if def.CanaryPct != 0 || def.WarmUntil == "" || def.Previous != "127.0.0.1:9001" {
		t.Fatalf("default list = %+v, want previous + warmUntil, no canaryPct", def)
	}
	// Single version: no previous, no warmUntil — today's shape exactly.
	single := hub.NewServer("", nil)
	if err := single.Register(hub.HubHandshake{Type: "hub", Name: "only", Version: "v1", Addr: "127.0.0.1:9001"}); err != nil {
		t.Fatal(err)
	}
	plain := BuildServerListForLogin(single, "", 0, NewWarmTracker(), now)
	if plain.Preferred != "127.0.0.1:9001" || plain.Previous != "" || plain.WarmUntil != "" || plain.CanaryPct != 0 {
		t.Fatalf("single-version list = %+v, want today's shape", plain)
	}
	// Personalized: a key with bucket>=5 lands on previous under pct=5.
	key := ""
	for i := 0; i < 1000 && key == ""; i++ {
		k := "login-" + itoa(i)
		if !CanaryToNewest(k, 5) {
			key = k
		}
	}
	if key == "" {
		t.Fatal("no remainder-bucket key found (hash broken?)")
	}
	got := BuildServerListForLogin(h, key, 5, tr, now)
	if got.Preferred != "127.0.0.1:9001" || got.CanaryPct != 5 {
		t.Fatalf("canary list for %q = %+v, want previous + canaryPct 5", key, got)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
