package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Two versions registered: preferred is the newest version (not just the
// newest shard), and the old version keeps heartbeating without stealing
// new logins while still serving its existing sessions.
func TestVersionPreferred(t *testing.T) {
	s := NewServer("", nil)
	base := time.Now()
	s.now = func() time.Time { return base }
	if err := s.Register(HubHandshake{
		Type: "hub", Name: "old", BuildID: "aaa", GVer: "1",
		Players: []string{"alice"},
	}); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return base.Add(time.Minute) }
	if err := s.Register(HubHandshake{
		Type: "hub", Name: "new", BuildID: "bbb", GVer: "1",
		Addr: "127.0.0.1:9002", Players: []string{"bob"},
	}); err != nil {
		t.Fatal(err)
	}

	oldVer := VersionOf("aaa", "1", "")
	newVer := VersionOf("bbb", "1", "")
	if oldVer == newVer {
		t.Fatalf("versions must differ: %q", oldVer)
	}
	if pref, ok := s.PreferredVersion(); !ok || pref != newVer {
		t.Fatalf("PreferredVersion = %q, %v; want %q", pref, ok, newVer)
	}
	if got, ok := s.NewestRunning(); !ok || got.Name != "new" || got.Version != newVer {
		t.Fatalf("NewestRunning = %+v, %v; want new/%s", got, ok, newVer)
	}

	// Old version keeps heartbeating (presence reconciles) without stealing
	// new logins; its existing sessions keep resolving.
	s.now = func() time.Time { return base.Add(2 * time.Minute) }
	if err := s.HeartbeatFull("old", []string{"alice"}, "", 5, "", nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.NewestRunning(); got.Name != "new" {
		t.Fatalf("after old heartbeat: NewestRunning = %+v, want new", got)
	}
	if pref, _ := s.PreferredVersion(); pref != newVer {
		t.Fatalf("after old heartbeat: PreferredVersion = %q, want %q", pref, newVer)
	}
	if shard, ok := s.FindPlayer("alice"); !ok || shard != "old" {
		t.Fatalf("FindPlayer(alice) = %q, %v; old version must keep serving", shard, ok)
	}
}

// Explicit VERSION tags group shards: two shards on the same tag share one
// version row, and the preferred version is the newest tag.
func TestVersionExplicitTag(t *testing.T) {
	if got := VersionOf("aaa", "1", "v2"); got != "v2" {
		t.Fatalf("VersionOf with tag = %q, want v2", got)
	}
	if got := VersionOf("aaa", "1", ""); got != "aaa+1" {
		t.Fatalf("VersionOf derived = %q, want aaa+1", got)
	}
	s := NewServer("", nil)
	base := time.Now()
	s.now = func() time.Time { return base }
	_ = s.Register(HubHandshake{Type: "hub", Name: "a1", Version: "v1", Load: 9})
	s.now = func() time.Time { return base.Add(time.Minute) }
	_ = s.Register(HubHandshake{Type: "hub", Name: "a2", Version: "v1", Load: 1})
	s.now = func() time.Time { return base.Add(2 * time.Minute) }
	_ = s.Register(HubHandshake{Type: "hub", Name: "b1", Version: "v2", Load: 7})
	if pref, _ := s.PreferredVersion(); pref != "v2" {
		t.Fatalf("PreferredVersion = %q, want v2", pref)
	}
	// Within the winning version the lowest-load shard serves new logins.
	if got, _ := s.NewestRunning(); got.Name != "b1" {
		t.Fatalf("NewestRunning = %+v, want b1", got)
	}
	for _, in := range s.ListShards() {
		if in.Version == "" {
			t.Fatalf("shard %q lost its version row", in.Name)
		}
	}
}

// Handoff gate matrix: same version proceeds, mismatched builds are
// rejected (must go disconnect+reconnect, never bare Teleport), unknown or
// self targets are rejected.
func TestHandoffGateMatrix(t *testing.T) {
	s := NewServer("", nil)
	_ = s.Register(HubHandshake{Type: "hub", Name: "a", Version: "v1"})
	_ = s.Register(HubHandshake{Type: "hub", Name: "a2", Version: "v1"})
	_ = s.Register(HubHandshake{Type: "hub", Name: "b", Version: "v2"})
	_ = s.Register(HubHandshake{Type: "hub", Name: "legacy"}) // unknown version

	cases := []struct {
		name    string
		from    string
		to      string
		wantErr bool
	}{
		{"same version", "a", "a2", false},
		{"unknown side passes hub (receiver gates)", "a", "legacy", false},
		{"cross-build rejected", "a", "b", true},
		{"cross-build rejected reverse", "b", "a", true},
		{"unknown target", "a", "ghost", true},
		{"self target", "a", "a", true},
		{"empty target", "a", "", true},
	}
	for _, tc := range cases {
		if err := s.CheckHandoff(tc.from, tc.to); (err != nil) != tc.wantErr {
			t.Errorf("%s: CheckHandoff(%q,%q) err = %v, wantErr %v",
				tc.name, tc.from, tc.to, err, tc.wantErr)
		}
	}
	if !HandoffCompatible("v1", "v1") || HandoffCompatible("v1", "v2") {
		t.Fatal("HandoffCompatible equality broken")
	}
	if !HandoffCompatible("", "v1") || !HandoffCompatible("v1", "") {
		t.Fatal("HandoffCompatible must pass unknown sides (receiver gates)")
	}
}

// Region->shard lookup: scoped RUNNING shards claim their regions,
// unscoped shards match nothing, DRAINING shards are skipped.
func TestLookupRegion(t *testing.T) {
	s := NewServer("", nil)
	_ = s.Register(HubHandshake{Type: "hub", Name: "east", Regions: []int{25, 26}})
	_ = s.Register(HubHandshake{Type: "hub", Name: "west", Regions: []int{27}})
	_ = s.Register(HubHandshake{Type: "hub", Name: "plain"})
	if got, ok := s.LookupRegion(25); !ok || got != "east" {
		t.Fatalf("LookupRegion(25) = %q, %v; want east", got, ok)
	}
	if _, ok := s.LookupRegion(99); ok {
		t.Fatal("LookupRegion(99) must miss (unscoped shards match nothing)")
	}
	_ = s.HeartbeatEx("east", nil, "DRAINING", 0)
	if _, ok := s.LookupRegion(25); ok {
		t.Fatal("LookupRegion must skip DRAINING shards")
	}
	if got := s.RegionsOf("west"); len(got) != 1 || got[0] != 27 {
		t.Fatalf("RegionsOf(west) = %v, want [27]", got)
	}
}

// Cross-shard guild/global fanout over two live sockets: A's FanoutGuild
// reaches B's member (skipping local + unknown members) and A's FanoutGlobal
// reaches every remote player, verbatim inner frames.
func TestFanoutReachesRemote(t *testing.T) {
	s := NewServer("", NewMailer(nil))
	srv := httptest.NewServer(s)
	defer srv.Close()
	wsURL := "ws" + srv.URL[len("http"):]

	var mu sync.Mutex
	var gotTo []string
	var gotInner []json.RawMessage
	onRelayB := func(to string, inner json.RawMessage) {
		mu.Lock()
		defer mu.Unlock()
		gotTo = append(gotTo, to)
		gotInner = append(gotInner, inner)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clientA := NewClient(wsURL, "", "shardA", NewRouter(), NewMailer(nil), nil)
	clientB := NewClient(wsURL, "", "shardB", NewRouter(), NewMailer(nil), onRelayB)
	clientA.SetHeartbeatInterval(20 * time.Millisecond)
	clientB.SetHeartbeatInterval(20 * time.Millisecond)
	clientB.SetLocalCheck(func(u string) bool { return u == "bob" || u == "carol" })
	go clientA.Start(ctx)
	go clientB.Start(ctx)
	defer clientA.Stop()
	defer clientB.Stop()

	if !clientA.WaitConnected(5*time.Second) || !clientB.WaitConnected(5*time.Second) {
		t.Fatal("clients did not connect")
	}
	clientA.Register("alice")
	clientB.Register("bob")
	clientB.Register("carol")

	deadline := time.Now().Add(5 * time.Second)
	for {
		if rp := clientA.RemotePlayers(); len(rp) == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if rp := clientA.RemotePlayers(); len(rp) != 2 {
		t.Fatalf("RemotePlayers = %v, want [bob carol]", rp)
	}

	frame := []any{float64(35), float64(11), map[string]any{"message": "hi-guild"}}
	remote := clientA.FanoutGuild([]string{"alice", "bob", "ghost"}, frame)
	if len(remote) != 1 || remote[0] != "bob" {
		t.Fatalf("FanoutGuild remote = %v, want [bob] (skip local alice + unknown ghost)", remote)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(gotTo)
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	if len(gotTo) != 1 || gotTo[0] != "bob" {
		mu.Unlock()
		t.Fatalf("shardB deliveries = %v, want [bob]", gotTo)
	}
	var inner []any
	if err := json.Unmarshal(gotInner[0], &inner); err != nil {
		mu.Unlock()
		t.Fatalf("inner unmarshal: %v", err)
	}
	mu.Unlock()
	if len(inner) != 3 {
		t.Fatalf("inner frame = %v, want verbatim guild frame", inner)
	}

	gframe := []any{float64(19), map[string]any{"message": "hi-all"}}
	if remote := clientA.FanoutGlobal(gframe); len(remote) != 2 {
		t.Fatalf("FanoutGlobal remote = %v, want [bob carol]", remote)
	}
}

// Roster pushes carry the preferred version; the client tracks it and fires
// the change callback (refresh-banner input).
func TestRosterCarriesPreferred(t *testing.T) {
	s := NewServer("", NewMailer(nil))
	srv := httptest.NewServer(s)
	defer srv.Close()
	wsURL := "ws" + srv.URL[len("http"):]

	if err := s.Register(HubHandshake{Type: "hub", Name: "old", Version: "v1"}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := NewClient(wsURL, "", "shardNew", NewRouter(), NewMailer(nil), nil)
	c.SetHeartbeatInterval(20 * time.Millisecond)
	c.SetVersion("v2")
	prefCh := make(chan string, 4)
	c.SetOnPreferred(func(p string) { prefCh <- p })
	go c.Start(ctx)
	defer c.Stop()
	if !c.WaitConnected(5 * time.Second) {
		t.Fatal("client did not connect")
	}
	select {
	case p := <-prefCh:
		if p != "v2" {
			t.Fatalf("preferred = %q, want v2 (newest version wins)", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("never learned the preferred version")
	}
	if got := c.PreferredVersion(); got != "v2" {
		t.Fatalf("PreferredVersion = %q, want v2", got)
	}
}

// Live handoff relay: same-version request routes to the target socket and
// the target's ack routes back to the sender; cross-build requests are
// rejected by the hub without ever reaching the target.
func TestHandoffRelayLive(t *testing.T) {
	s := NewServer("", NewMailer(nil))
	srv := httptest.NewServer(s)
	defer srv.Close()
	wsURL := "ws" + srv.URL[len("http"):]

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sender := NewClient(wsURL, "", "shardA", NewRouter(), NewMailer(nil), nil)
	target := NewClient(wsURL, "", "shardB", NewRouter(), NewMailer(nil), nil)
	other := NewClient(wsURL, "", "shardC", NewRouter(), NewMailer(nil), nil)
	for _, c := range []*Client{sender, target, other} {
		c.SetHeartbeatInterval(20 * time.Millisecond)
	}
	sender.SetVersion("v1")
	target.SetVersion("v1")
	other.SetVersion("v2")
	var targetHits int
	var mu sync.Mutex
	target.SetHandoffHandler(func(req HandoffRequest) HandoffAck {
		mu.Lock()
		targetHits++
		mu.Unlock()
		if req.Player != "alice" {
			return HandoffAck{Ok: false, Reason: "unknown player"}
		}
		return HandoffAck{Ok: true, Addr: "127.0.0.1:9003"}
	})
	go sender.Start(ctx)
	go target.Start(ctx)
	go other.Start(ctx)
	defer sender.Stop()
	defer target.Stop()
	defer other.Stop()
	if !sender.WaitConnected(5*time.Second) || !target.WaitConnected(5*time.Second) ||
		!other.WaitConnected(5*time.Second) {
		t.Fatal("clients did not connect")
	}
	// Let registrations land (version rows must be visible to the gate).
	deadline := time.Now().Add(5 * time.Second)
	for {
		pref, ok := s.PreferredVersion()
		_ = pref
		if ok && s.ShardCount() == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shards never registered")
		}
		time.Sleep(20 * time.Millisecond)
	}

	reqCtx, reqCancel := context.WithTimeout(ctx, 5*time.Second)
	defer reqCancel()
	ack, err := sender.RequestHandoff(reqCtx, "shardB", HandoffRequest{Player: "alice"})
	if err != nil {
		t.Fatalf("RequestHandoff: %v", err)
	}
	if !ack.Ok || ack.Addr != "127.0.0.1:9003" {
		t.Fatalf("ack = %+v, want ok with target addr", ack)
	}
	mu.Lock()
	hits := targetHits
	mu.Unlock()
	if hits != 1 {
		t.Fatalf("target hits = %d, want 1", hits)
	}

	// Cross-build: hub rejects without reaching the target.
	ack, err = sender.RequestHandoff(reqCtx, "shardC", HandoffRequest{Player: "alice"})
	if err != nil {
		t.Fatalf("RequestHandoff cross-build: %v", err)
	}
	if ack.Ok {
		t.Fatalf("cross-build ack = %+v, want reject", ack)
	}
	if ack.Reason == "" {
		t.Fatal("cross-build reject must carry a reason")
	}
	mu.Lock()
	hits = targetHits
	mu.Unlock()
	if hits != 1 {
		t.Fatalf("target hits after cross-build = %d, want still 1", hits)
	}

	// Unknown target: reject, and the sender keeps its session (no error).
	ack, err = sender.RequestHandoff(reqCtx, "ghost", HandoffRequest{Player: "alice"})
	if err != nil {
		t.Fatalf("RequestHandoff unknown: %v", err)
	}
	if ack.Ok {
		t.Fatalf("unknown-target ack = %+v, want reject", ack)
	}
	var _ = errors.Is
}
