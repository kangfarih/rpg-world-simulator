package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// All-in-one default (no HUB_ADDR) must stay exactly as today: transport
// disabled, no client constructed, Router miss still ErrOffline.
func TestAllInOneDefaultUntouched(t *testing.T) {
	t.Setenv(EnvHubAddr, "")
	if Enabled() {
		t.Fatalf("Enabled() = true with HUB_ADDR unset, want false")
	}
	if c := ClientFromEnv(NewRouter(), NewMailer(nil), nil); c != nil {
		t.Fatalf("ClientFromEnv() = %v with HUB_ADDR unset, want nil", c)
	}
	// Nil-client answers equal the all-in-one answers.
	var nc *Client
	if nc.Connected() {
		t.Fatalf("nil client Connected() = true, want false")
	}
	if _, err := nc.RouteOrForward(Message{Kind: KindChat, To: "bob"}); !errors.Is(err, ErrOffline) {
		t.Fatalf("nil client RouteOrForward err = %v, want ErrOffline", err)
	}
	r := NewRouter()
	r.Register("alice")
	if _, err := r.Route(Message{From: "alice", To: "ghost", Kind: KindChat}); !errors.Is(err, ErrOffline) {
		t.Fatalf("Route miss err = %v, want ErrOffline", err)
	}
}

// Register adopts the handshake player list, Heartbeat reconciles it, and
// EvictIdle drops shards idle past 3 missed 5s beats (fake clock).
func TestServerRegisterHeartbeatEvict(t *testing.T) {
	s := NewServer("", NewMailer(nil))
	now := time.Now()
	s.now = func() time.Time { return now }

	if err := s.Register(HubHandshake{Type: "hub", Name: "shardA", Players: []string{"alice"}}); err != nil {
		t.Fatalf("Register err = %v", err)
	}
	if got, ok := s.FindPlayer("alice"); !ok || got != "shardA" {
		t.Fatalf("FindPlayer(alice) = %q,%v want shardA,true", got, ok)
	}

	// Heartbeat replaces the player list: alice leaves, bob joins.
	if err := s.Heartbeat("shardA", []string{"bob"}); err != nil {
		t.Fatalf("Heartbeat err = %v", err)
	}
	if _, ok := s.FindPlayer("alice"); ok {
		t.Fatalf("FindPlayer(alice) still hits after heartbeat without her")
	}
	if got, ok := s.FindPlayer("bob"); !ok || got != "shardA" {
		t.Fatalf("FindPlayer(bob) = %q,%v want shardA,true", got, ok)
	}

	// 2 missed beats: still present.
	now = now.Add(2 * HeartbeatInterval)
	if evicted := s.EvictIdle(); len(evicted) != 0 {
		t.Fatalf("EvictIdle at 2 beats = %v, want none", evicted)
	}
	// 4th missed beat (> 3*5s): evicted, roster cleared.
	now = now.Add(2*HeartbeatInterval + time.Second)
	evicted := s.EvictIdle()
	if len(evicted) != 1 || evicted[0] != "shardA" {
		t.Fatalf("EvictIdle past 3 beats = %v, want [shardA]", evicted)
	}
	if _, ok := s.FindPlayer("bob"); ok {
		t.Fatalf("FindPlayer(bob) still hits after eviction")
	}
	if n := s.ShardCount(); n != 0 {
		t.Fatalf("ShardCount = %d after eviction, want 0", n)
	}
}

// Offline mail round-trips through both stores and pops exactly once.
func TestOfflineStoreDeliver(t *testing.T) {
	stores := map[string]func(t *testing.T) (MailStore, func()){
		"memory": func(t *testing.T) (MailStore, func()) {
			t.Helper()
			return NewMemoryMail(), func() {}
		},
		"sqlite": func(t *testing.T) (MailStore, func()) {
			t.Helper()
			sm, err := OpenSQLiteMail(":memory:")
			if err != nil {
				t.Fatalf("OpenSQLiteMail: %v", err)
			}
			return sm, func() { _ = sm.Close() }
		},
	}
	for name, open := range stores {
		t.Run(name, func(t *testing.T) {
			store, closeFn := open(t)
			defer closeFn()
			m := NewMailer(store)
			if err := m.Store(Message{From: "alice", To: "bob", Kind: KindChat, Payload: "hello"}); err != nil {
				t.Fatalf("Store err = %v", err)
			}
			if err := m.Store(Message{From: "alice", To: "bob", Kind: KindChat, Payload: []any{float64(19), "x"}}); err != nil {
				t.Fatalf("Store err = %v", err)
			}
			got := m.Deliver("bob")
			if len(got) != 2 || got[0].To != "bob" || got[1].To != "bob" {
				t.Fatalf("Deliver(bob) = %+v, want 2 messages", got)
			}
			if got[0].Payload != "hello" {
				t.Fatalf("Deliver(bob)[0].Payload = %#v, want %q", got[0].Payload, "hello")
			}
			if again := m.Deliver("bob"); len(again) != 0 {
				t.Fatalf("second Deliver(bob) = %+v, want empty (pop once)", again)
			}
			if err := m.Store(Message{From: "alice", Kind: KindChat}); err == nil {
				t.Fatalf("Store without To err = nil, want ErrNoRecipient")
			}
		})
	}
}

// Wrong tokens are rejected (unit-level Register); right ones register.
func TestServerAuthReject(t *testing.T) {
	s := NewServer("s3cret", NewMailer(nil))
	bad := HubHandshake{Type: "hub", Name: "shardX", AccessToken: "wrong"}
	if err := s.Register(bad); !errors.Is(err, ErrOfflineAuth) {
		t.Fatalf("Register(bad token) err = %v, want ErrOfflineAuth", err)
	}
	if n := s.ShardCount(); n != 0 {
		t.Fatalf("ShardCount after rejected register = %d, want 0", n)
	}
	good := HubHandshake{Type: "hub", Name: "shardX", AccessToken: "s3cret"}
	if err := s.Register(good); err != nil {
		t.Fatalf("Register(good token) err = %v", err)
	}
}

// A mismatched bearer header is rejected at upgrade with 401.
func TestServerHTTPBearerReject(t *testing.T) {
	s := NewServer("s3cret", NewMailer(nil))
	srv := httptest.NewServer(s)
	defer srv.Close()

	wsURL := "ws" + srv.URL[len("http"):]
	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	header := http.Header{}
	header.Set("Authorization", "Bearer wrong")
	if _, resp, err := d.Dial(wsURL, header); err == nil {
		t.Fatalf("dial with wrong bearer succeeded, want 401")
	} else if resp == nil || resp.StatusCode != 401 {
		t.Fatalf("dial with wrong bearer status = %v err = %v, want 401", resp, err)
	}
	if n := s.ShardCount(); n != 0 {
		t.Fatalf("ShardCount after bearer reject = %d, want 0", n)
	}
}

// Relay across two live sockets: shardA forwards a local Route miss for a
// player on shardB; shardB's handler receives the verbatim inner frame.
// Then an unknown target falls back to offline mail, delivered on Register.
func TestRelayAcrossSockets(t *testing.T) {
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

	routerA := NewRouter()
	routerB := NewRouter()
	mailA := NewMailer(nil)
	clientA := NewClient(wsURL, "", "shardA", routerA, mailA, nil)
	clientB := NewClient(wsURL, "", "shardB", routerB, NewMailer(nil), onRelayB)
	clientA.SetHeartbeatInterval(20 * time.Millisecond)
	clientB.SetHeartbeatInterval(20 * time.Millisecond)
	go clientA.Start(ctx)
	go clientB.Start(ctx)
	defer clientA.Stop()
	defer clientB.Stop()

	if !clientA.WaitConnected(5*time.Second) || !clientB.WaitConnected(5*time.Second) {
		t.Fatalf("clients did not connect")
	}
	clientA.Register("alice")
	clientB.Register("bob")

	// Wait until A learns bob is remote (roster push after B's heartbeat).
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := clientA.RemoteOf("bob"); ok || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, ok := clientA.RemoteOf("bob"); !ok {
		t.Fatalf("shardA never learned bob is remote")
	}

	// Local hit still resolves locally.
	clientA.Register("local")
	if _, err := clientA.RouteOrForward(Message{From: "alice", To: "local", Kind: KindChat}); err != nil {
		t.Fatalf("local RouteOrForward err = %v, want nil", err)
	}

	// Remote miss forwards (no local fallback, no mail).
	if _, err := clientA.RouteOrForward(Message{From: "alice", To: "bob", Kind: KindChat, Payload: []any{float64(19), "hi-bob"}}); !errors.Is(err, ErrForwarded) {
		t.Fatalf("remote RouteOrForward err = %v, want ErrForwarded", err)
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
	defer mu.Unlock()
	if len(gotTo) != 1 || gotTo[0] != "bob" {
		t.Fatalf("shardB deliveries = %v, want [bob]", gotTo)
	}
	var inner []any
	if err := json.Unmarshal(gotInner[0], &inner); err != nil {
		t.Fatalf("inner frame unmarshal: %v", err)
	}
	if len(inner) != 2 || inner[1] != "hi-bob" {
		t.Fatalf("inner frame = %v, want verbatim payload", inner)
	}

	// Unknown target: offline mail, delivered on next Register.
	if _, err := clientA.RouteOrForward(Message{From: "alice", To: "ghost", Kind: KindChat, Payload: "missed"}); !errors.Is(err, ErrOffline) {
		t.Fatalf("unknown RouteOrForward err = %v, want ErrOffline", err)
	}
	pending := clientA.Register("ghost")
	if len(pending) != 1 || pending[0].Payload != "missed" {
		t.Fatalf("Deliver(ghost) = %+v, want the stored message", pending)
	}
}

// HUB_ADDR-gated constructor returns a live client when set.
func TestClientFromEnvWhenSet(t *testing.T) {
	t.Setenv(EnvHubAddr, "ws://127.0.0.1:1/")
	t.Setenv(EnvHubToken, "tok")
	c := ClientFromEnv(NewRouter(), NewMailer(nil), nil)
	if c == nil {
		t.Fatalf("ClientFromEnv() = nil with HUB_ADDR set, want client")
	}
	if c.addr != "ws://127.0.0.1:1/" || c.token != "tok" {
		t.Fatalf("ClientFromEnv addr/token = %q/%q", c.addr, c.token)
	}
	c.Stop()
}

// Hub-side relay to a nowhere-online player (or a known shard whose socket
// is down) is kept as central offline mail instead of dropped.
func TestHubOfflineFallback(t *testing.T) {
	mail := NewMailer(nil)
	s := NewServer("", mail)

	raw, err := encodeRelay("ghost", json.RawMessage(`"hi"`))
	if err != nil {
		t.Fatalf("encodeRelay: %v", err)
	}
	s.Relay(raw) // ghost online nowhere -> stored
	got := mail.Deliver("ghost")
	if len(got) != 1 || got[0].To != "ghost" {
		t.Fatalf("Deliver(ghost) = %+v, want 1 stored message", got)
	}

	// Known shard, no live socket (transport-free Register): stored too.
	if err := s.Register(HubHandshake{Type: "hub", Name: "shardZ"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := s.Heartbeat("shardZ", []string{"zed"}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	raw, err = encodeRelay("zed", json.RawMessage(`"hi-zed"`))
	if err != nil {
		t.Fatalf("encodeRelay: %v", err)
	}
	s.Relay(raw)
	got = mail.Deliver("zed")
	if len(got) != 1 || got[0].To != "zed" {
		t.Fatalf("Deliver(zed) = %+v, want 1 stored message", got)
	}
}

// Relay envelope keeps the TS [53, [username, inner]] shape.
func TestRelayEnvelopeShape(t *testing.T) {
	raw, err := encodeRelay("bob", json.RawMessage(`[19,"hi"]`))
	if err != nil {
		t.Fatalf("encodeRelay: %v", err)
	}
	var frame []json.RawMessage
	if err := json.Unmarshal(raw, &frame); err != nil || len(frame) != 3 {
		t.Fatalf("relay frame = %s, want 3-element array", raw)
	}
	var packet int
	if err := json.Unmarshal(frame[0], &packet); err != nil || packet != 53 {
		t.Fatalf("relay packet = %s, want 53", frame[0])
	}
	user, inner, err := decodeRelay(raw)
	if err != nil || user != "bob" || string(inner) != `[19,"hi"]` {
		t.Fatalf("decodeRelay = %q %s %v", user, inner, err)
	}
}
