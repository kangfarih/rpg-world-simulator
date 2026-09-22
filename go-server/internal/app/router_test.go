package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"rpg-world-server/internal/hub"
	"rpg-world-server/internal/version"
)

func TestRouterAddrFromEnv(t *testing.T) {
	if got := RouterAddrFromEnv(getenvOf(nil)); got != DefaultRouterAddr {
		t.Fatalf("default = %q, want %q", got, DefaultRouterAddr)
	}
	g := getenvOf(map[string]string{EnvHubListen: "127.0.0.1:9200"})
	if got := RouterAddrFromEnv(g); got != "127.0.0.1:9200" {
		t.Fatalf("HUB_LISTEN = %q", got)
	}
	g = getenvOf(map[string]string{hub.EnvHubAddr: "127.0.0.1:9105"})
	if got := RouterAddrFromEnv(g); got != "127.0.0.1:9105" {
		t.Fatalf("bare HUB_ADDR = %q", got)
	}
	// A ws:// URL belongs to shards, not the router listener.
	g = getenvOf(map[string]string{hub.EnvHubAddr: "ws://127.0.0.1:9101/"})
	if got := RouterAddrFromEnv(g); got != DefaultRouterAddr {
		t.Fatalf("ws HUB_ADDR = %q, want default", got)
	}
}

// /servers directs NEW sessions to the newest healthy RUNNING shard.
func TestRouterServersEndpoint(t *testing.T) {
	h := hub.NewServer("", nil)
	if err := h.Register(hub.HubHandshake{Type: "hub", Name: "old", BuildID: "aaa", Addr: "127.0.0.1:9001"}); err != nil {
		t.Fatal(err)
	}
	if err := h.Register(hub.HubHandshake{Type: "hub", Name: "new", BuildID: "bbb", Addr: "127.0.0.1:9002"}); err != nil {
		t.Fatal(err)
	}
	if err := h.HeartbeatEx("old", nil, version.StateDraining, 4); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(RouterHandler(h, &Lifecycle{}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/servers")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var list ServerList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if list.Preferred != "127.0.0.1:9002" {
		t.Fatalf("preferred = %q, want 9002", list.Preferred)
	}
	if len(list.Shards) != 2 || list.Shards[0].Name != "new" || !list.Shards[0].Newest {
		t.Fatalf("shards = %+v", list.Shards)
	}
	if list.Shards[1].State != version.StateDraining {
		t.Fatalf("old state = %+v", list.Shards[1])
	}
}

// Empty table: no preferred target (clients must not start sessions).
func TestRouterServersEmpty(t *testing.T) {
	h := hub.NewServer("", nil)
	list := BuildServerList(h)
	if list.Preferred != "" || len(list.Shards) != 0 {
		t.Fatalf("empty list = %+v", list)
	}
	if BuildServerList(nil).Preferred != "" {
		t.Fatal("nil hub must render empty")
	}
}

// /healthz mirrors the lifecycle (200 RUNNING, 503 DRAINING).
func TestRouterHealthEndpoint(t *testing.T) {
	lc := &Lifecycle{}
	h := hub.NewServer("", nil)
	srv := httptest.NewServer(RouterHandler(h, lc))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	var healthy Health
	if err := json.NewDecoder(resp.Body).Decode(&healthy); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || healthy.State != version.StateRunning {
		t.Fatalf("health = %d %+v", resp.StatusCode, healthy)
	}
	if healthy.BuildID == "" || healthy.GVer == "" {
		t.Fatalf("health missing stamps: %+v", healthy)
	}
	lc.BeginDraining()
	resp, err = http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("draining health = %d, want 503", resp.StatusCode)
	}
}
