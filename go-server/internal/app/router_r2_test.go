package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"rpg-world-server/internal/hub"
)

// /servers with two coexisting versions: preferred is the newest version's
// addr, version + region rows render, and the old version still lists (it
// keeps serving existing sessions — just no new logins).
func TestRouterServersVersions(t *testing.T) {
	h := hub.NewServer("", nil)
	if err := h.Register(hub.HubHandshake{
		Type: "hub", Name: "old", Version: "v1",
		Addr: "127.0.0.1:9001", Regions: []int{25},
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.Register(hub.HubHandshake{
		Type: "hub", Name: "new", Version: "v2",
		Addr: "127.0.0.1:9002", Regions: []int{26, 27},
	}); err != nil {
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
		t.Fatalf("preferred = %q, want newest version 9002", list.Preferred)
	}
	byName := map[string]ServerEntry{}
	for _, e := range list.Shards {
		byName[e.Name] = e
	}
	if byName["new"].Version != "v2" || !byName["new"].Newest {
		t.Fatalf("new row = %+v, want version v2 + newest", byName["new"])
	}
	if byName["old"].Version != "v1" || byName["old"].Newest {
		t.Fatalf("old row = %+v, want version v1 without newest", byName["old"])
	}
	if len(byName["new"].Regions) != 2 || len(byName["old"].Regions) != 1 {
		t.Fatalf("regions = %+v, want scope rows", byName)
	}
}
