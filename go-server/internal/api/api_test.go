package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type stubPlayers struct {
	byName map[string]Player
}

func (s stubPlayers) GetPlayer(name string) (Player, bool) {
	p, ok := s.byName[name]
	return p, ok
}

func (s stubPlayers) ListPlayers() []Player {
	out := make([]Player, 0, len(s.byName))
	for _, p := range s.byName {
		out = append(out, p)
	}
	return out
}

type stubGuilds struct{ guilds []Guild }

func (s stubGuilds) ListGuilds() []Guild { return s.guilds }

type stubStatus struct{ st Status }

func (s stubStatus) Status() Status { return s.st }

func newTestServer() *Server {
	return NewServer(
		stubPlayers{byName: map[string]Player{
			"Alice": {Name: "Alice", Level: 10, Online: true},
		}},
		stubGuilds{guilds: []Guild{
			{Name: "Ember", Members: []string{"Alice", "Bob"}},
			{Name: "Frost", Members: []string{}},
		}},
		stubStatus{st: Status{Name: "test", Port: 1337, GameVersion: "1.0", MaxPlayers: 100, PlayerCount: 1}},
	)
}

func TestStatusShape(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", rec.Code)
	}
	var got Status
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if got.Name != "test" || got.Port != 1337 || got.GameVersion != "1.0" || got.MaxPlayers != 100 || got.PlayerCount != 1 {
		t.Fatalf("unexpected status shape: %+v", got)
	}
}

func TestPlayerFoundAndUnknown404(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest(http.MethodGet, "/players/Alice", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("player code = %d, want 200", rec.Code)
	}
	var p Player
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode player: %v", err)
	}
	if p.Name != "Alice" || p.Level != 10 || !p.Online {
		t.Fatalf("unexpected player: %+v", p)
	}

	req = httptest.NewRequest(http.MethodGet, "/players/Nobody", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown player code = %d, want 404", rec.Code)
	}
}

func TestGuildsList(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/guilds", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("guilds code = %d, want 200", rec.Code)
	}
	var got []Guild
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode guilds: %v", err)
	}
	if len(got) != 2 || got[0].Name != "Ember" || len(got[0].Members) != 2 || got[1].Name != "Frost" {
		t.Fatalf("unexpected guilds: %+v", got)
	}
}
