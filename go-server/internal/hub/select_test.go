package hub

import (
	"encoding/json"
	"testing"
	"time"

	"rpg-world-server/internal/version"
)

// Newest healthy RUNNING shard wins for NEW sessions: the later-registered
// build routes while the older one keeps its existing players.
func TestNewestRunning(t *testing.T) {
	s := NewServer("", nil)
	base := time.Now()
	s.now = func() time.Time { return base }
	if err := s.Register(HubHandshake{Type: "hub", Name: "old", BuildID: "aaa", GVer: version.GVer, Addr: "127.0.0.1:9001"}); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return base.Add(time.Minute) }
	if err := s.Register(HubHandshake{Type: "hub", Name: "new", BuildID: "bbb", GVer: version.GVer, Addr: "127.0.0.1:9002"}); err != nil {
		t.Fatal(err)
	}
	got, ok := s.NewestRunning()
	if !ok || got.Name != "new" || got.Addr != "127.0.0.1:9002" || got.BuildID != "bbb" {
		t.Fatalf("NewestRunning = %+v, %v; want new/9002/bbb", got, ok)
	}
}

// DRAINING shards are skipped (they keep existing players, take no new
// sessions); shards without stamps (pre-R1, "") count as RUNNING.
func TestNewestRunningSkipsDraining(t *testing.T) {
	s := NewServer("", nil)
	base := time.Now()
	s.now = func() time.Time { return base }
	if err := s.Register(HubHandshake{Type: "hub", Name: "legacy"}); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return base.Add(time.Minute) }
	if err := s.Register(HubHandshake{Type: "hub", Name: "fresh", State: version.StateRunning}); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.NewestRunning(); !ok || got.Name != "fresh" {
		t.Fatalf("NewestRunning = %+v, %v; want fresh", got, ok)
	}
	if err := s.HeartbeatEx("fresh", nil, version.StateDraining, 3); err != nil {
		t.Fatal(err)
	}
	got, ok := s.NewestRunning()
	if !ok || got.Name != "legacy" || got.State != version.StateRunning {
		t.Fatalf("after drain: NewestRunning = %+v, %v; want legacy/RUNNING", got, ok)
	}
	if err := s.HeartbeatEx("legacy", nil, version.StateDraining, 0); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.NewestRunning(); ok {
		t.Fatal("all DRAINING must yield no login target")
	}
}

// Same-name redeploy of a new build resets newness (it registered later);
func TestReregisterNewBuildResetsNewness(t *testing.T) {
	s := NewServer("", nil)
	base := time.Now()
	s.now = func() time.Time { return base }
	_ = s.Register(HubHandshake{Type: "hub", Name: "a", BuildID: "aaa"})
	s.now = func() time.Time { return base.Add(time.Minute) }
	_ = s.Register(HubHandshake{Type: "hub", Name: "b", BuildID: "bbb"})
	s.now = func() time.Time { return base.Add(2 * time.Minute) }
	_ = s.Register(HubHandshake{Type: "hub", Name: "a", BuildID: "ccc"})
	if got, _ := s.NewestRunning(); got.Name != "a" || got.BuildID != "ccc" {
		t.Fatalf("NewestRunning = %+v; want redeployed a/ccc", got)
	}
	// Same-build re-register keeps seniority.
	s.now = func() time.Time { return base.Add(3 * time.Minute) }
	_ = s.Register(HubHandshake{Type: "hub", Name: "a", BuildID: "ccc"})
	if got, _ := s.NewestRunning(); got.Name != "a" {
		t.Fatalf("NewestRunning = %+v; want a", got)
	}
}

// Idle shards (3 missed 5s beats = 15s) evict and stop routing: the roster
// shrinks and the survivor routes.
func TestEvictIdleReroutes(t *testing.T) {
	s := NewServer("", nil)
	base := time.Now()
	cur := base
	s.now = func() time.Time { return cur }
	_ = s.Register(HubHandshake{Type: "hub", Name: "old", Players: []string{"alice"}})
	cur = base.Add(time.Minute)
	_ = s.Register(HubHandshake{Type: "hub", Name: "new", Players: []string{"bob"}})
	// old goes quiet; new keeps beating.
	cur = base.Add(time.Minute).Add(EvictAfter + time.Second)
	_ = s.Heartbeat("new", []string{"bob"})
	if evicted := s.EvictIdle(); len(evicted) != 1 || evicted[0] != "old" {
		t.Fatalf("EvictIdle = %v, want [old]", evicted)
	}
	if _, ok := s.FindPlayer("alice"); ok {
		t.Fatal("evicted shard's players must leave the roster")
	}
	if got, _ := s.NewestRunning(); got.Name != "new" {
		t.Fatalf("NewestRunning = %+v; want new", got)
	}
}

// ListShards renders newest-first and flags the login target.
func TestListShards(t *testing.T) {
	s := NewServer("", nil)
	base := time.Now()
	cur := base
	s.now = func() time.Time { return cur }
	_ = s.Register(HubHandshake{Type: "hub", Name: "old", Load: 4})
	cur = base.Add(time.Minute)
	_ = s.Register(HubHandshake{Type: "hub", Name: "new", Load: 1})
	_ = s.HeartbeatEx("old", nil, version.StateDraining, 4)
	list := s.ListShards()
	if len(list) != 2 || list[0].Name != "new" || list[1].Name != "old" {
		t.Fatalf("ListShards order = %+v, want [new old]", list)
	}
	if !list[0].Newest || list[1].Newest {
		t.Fatalf("Newest flags = %+v, want [true false]", list)
	}
	if list[1].State != version.StateDraining || list[1].Load != 4 {
		t.Fatalf("old row = %+v, want DRAINING/load 4", list[1])
	}
}

// Heartbeat without stamps preserves state (pre-R1 shape never clobbers).
func TestHeartbeatPreservesStamps(t *testing.T) {
	s := NewServer("", nil)
	_ = s.Register(HubHandshake{Type: "hub", Name: "s", State: version.StateDraining, Load: 2})
	if err := s.Heartbeat("s", []string{"x"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.NewestRunning()
	_ = got
	if _, ok := s.NewestRunning(); ok {
		t.Fatal("plain Heartbeat must not clear DRAINING")
	}
	list := s.ListShards()
	if len(list) != 1 || list[0].Load != 2 {
		t.Fatalf("load lost: %+v", list)
	}
}

// Client register/heartbeat frames carry the R1 stamps.
func TestClientFramesCarryStamps(t *testing.T) {
	r := NewRouter()
	c := NewClient("ws://127.0.0.1:1/", "", "shardA", r, nil, nil)
	c.SetBuild("deadbeef", version.GVer, "127.0.0.1:9001")
	c.SetState(version.StateDraining)
	c.SetPlayersProvider(func() []string { return []string{"bob", "alice"} })
	raw, err := c.handshakeFrame()
	if err != nil {
		t.Fatal(err)
	}
	var frame []json.RawMessage
	if err := json.Unmarshal(raw, &frame); err != nil || len(frame) != 3 {
		t.Fatalf("handshake frame = %s", raw)
	}
	var hs HubHandshake
	if err := json.Unmarshal(frame[2], &hs); err != nil {
		t.Fatal(err)
	}
	if hs.BuildID != "deadbeef" || hs.GVer != version.GVer || hs.State != version.StateDraining ||
		hs.Addr != "127.0.0.1:9001" || hs.Load != 2 || len(hs.Players) != 2 {
		t.Fatalf("handshake stamps = %+v", hs)
	}
	raw, err = c.heartbeatFrame()
	if err != nil {
		t.Fatal(err)
	}
	var hb heartbeatMsg
	if err := json.Unmarshal(raw, &hb); err != nil {
		t.Fatal(err)
	}
	if hb.State != version.StateDraining || hb.Load != 2 || len(hb.Players) != 2 {
		t.Fatalf("heartbeat stamps = %+v", hb)
	}
}
