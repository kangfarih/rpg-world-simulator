// Package api is an additive, read-only REST surface for the Go server
// stub. It owns HTTP routing only: the caller supplies data through the
// Players, Guilds, and StatusProvider interfaces and owns starting,
// stopping, and env-gating the underlying http.Server. There are no
// goroutines, timers, database calls, or network sends in this package.
//
// TS source mirrored here (shape conventions only):
//   - packages/server/src/network/api.ts — API (express app gated on
//     config.apiEnabled || config.hubEnabled, listens on config.apiPort,
//     GET / returns {name, port, gameVersion, maxPlayers, playerCount}).
//   - packages/common/config.ts — apiEnabled/apiPort fields (env-derived).
//
// Env gating (API_PORT default-off pattern): this package never reads the
// environment and never listens on its own. The caller gates startup:
//
//	if port := os.Getenv("API_PORT"); port != "" {
//	    srv := api.NewServer(players, guilds, status)
//	    http.ListenAndServe(":"+port, srv.Handler())
//	}
//
// When API_PORT is unset or empty the caller does not start the server,
// so the API is off by default. See AddrFromEnv for a small helper that
// keeps that check in one place (it still only returns an address; the
// caller decides whether to listen).
//
// Routes (all GET, read-only; anything else gets 405 from the mux):
//   - GET /status       — server status snapshot (TS GET / parity).
//   - GET /guilds       — guild list snapshot.
//   - GET /players/{name} — single player lookup; 404 when unknown.
//   - GET /healthz      — drain-lifecycle snapshot {state, load, buildId,
//     gVer}; 503 once DRAINING/SHUTDOWN (new surface, no TS counterpart).
package api

import (
	"encoding/json"
	"net/http"
	"os"
)

// ---------------------------------------------------------------------------
// Shapes.
// ---------------------------------------------------------------------------

// Player is one player's read-only snapshot.
type Player struct {
	Name   string `json:"name"`
	Level  int    `json:"level"`
	Online bool   `json:"online"`
}

// Guild is one guild's read-only snapshot.
type Guild struct {
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

// Status is the server status snapshot. Field names mirror TS GET /
// (api.ts handleRouter): name, port, gameVersion, maxPlayers, playerCount.
// R1 adds buildId + gVer (omitempty: older providers render the TS shape
// byte-identical).
type Status struct {
	Name        string `json:"name"`
	Port        int    `json:"port"`
	GameVersion string `json:"gameVersion"`
	MaxPlayers  int    `json:"maxPlayers"`
	PlayerCount int    `json:"playerCount"`
	BuildID     string `json:"buildId,omitempty"`
	GVer        string `json:"gVer,omitempty"`
}

// ---------------------------------------------------------------------------
// Provider interfaces (caller supplies data; package owns HTTP routing only).
// ---------------------------------------------------------------------------

// Players supplies player snapshots. GetPlayer reports false when the
// named player is unknown; ListPlayers backs potential list views.
type Players interface {
	GetPlayer(name string) (Player, bool)
	ListPlayers() []Player
}

// Guilds supplies guild snapshots.
type Guilds interface {
	ListGuilds() []Guild
}

// StatusProvider supplies the server status snapshot.
type StatusProvider interface {
	Status() Status
}

// HealthProvider supplies the drain-lifecycle snapshot for /healthz. The
// shape is intentionally minimal ({state, load} + stamps); the game, the
// router, and the api test stub all satisfy it structurally via Health()
// adapters or inline funcs. Implementations must be safe for concurrent use
// (the handler runs on the caller's HTTP server).
type HealthProvider interface {
	Health() Health
}

// Health is the /healthz snapshot: drain state + load (+ build stamps so
// deploy watchers can tell builds apart).
type Health struct {
	State   string `json:"state"`
	Load    int    `json:"load"`
	BuildID string `json:"buildId,omitempty"`
	GVer    string `json:"gVer,omitempty"`
}

// ---------------------------------------------------------------------------
// Server.
// ---------------------------------------------------------------------------

// Server routes read-only API requests to the caller-supplied providers.
// The zero value is unusable; construct with NewServer. It implements
// http.Handler so the caller can mount it on any http.Server it owns.
// Health is optional (nil => /healthz reports a static RUNNING stub);
// set it after construction to report the live drain state.
type Server struct {
	Players Players
	Guilds  Guilds
	Status  StatusProvider
	Health  HealthProvider

	mux *http.ServeMux
}

// NewServer wires GET /status, GET /guilds, GET /players/{name} and GET
// /healthz. Providers may be nil (see per-handler fallbacks), but callers
// normally supply all three. NewServer registers routes only; it starts
// nothing.
func NewServer(players Players, guilds Guilds, status StatusProvider) *Server {
	s := &Server{Players: players, Guilds: guilds, Status: status, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /status", s.handleStatus)
	s.mux.HandleFunc("GET /guilds", s.handleGuilds)
	s.mux.HandleFunc("GET /players/{name}", s.handlePlayer)
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	return s
}

// Handler returns the read-only route set for the caller's http.Server.
func (s *Server) Handler() http.Handler { return s.mux }

// ServeHTTP delegates to the internal mux.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// AddrFromEnv returns the listen address derived from API_PORT. ok is false
// when API_PORT is unset or empty (default-off); the caller must not start
// the server in that case. Values already containing a colon (e.g.
// ":8080", "127.0.0.1:8080") are used as-is; bare ports gain a ":" prefix.
func AddrFromEnv() (addr string, ok bool) {
	port := os.Getenv("API_PORT")
	if port == "" {
		return "", false
	}
	for i := 0; i < len(port); i++ {
		if port[i] == ':' {
			return port, true
		}
	}
	return ":" + port, true
}

// ---------------------------------------------------------------------------
// Handlers.
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	if s.Status == nil {
		writeError(w, http.StatusServiceUnavailable, "status unavailable")
		return
	}
	writeJSON(w, http.StatusOK, s.Status.Status())
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	h := Health{State: "RUNNING"}
	code := http.StatusOK
	if s.Health != nil {
		h = s.Health.Health()
		if h.State != "" && h.State != "RUNNING" {
			code = http.StatusServiceUnavailable
		}
		if h.State == "" {
			h.State = "RUNNING"
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(h)
}

func (s *Server) handleGuilds(w http.ResponseWriter, _ *http.Request) {
	var out []Guild
	if s.Guilds != nil {
		out = s.Guilds.ListGuilds()
	}
	if out == nil {
		out = []Guild{}
	}
	for i := range out {
		if out[i].Members == nil {
			out[i].Members = []string{}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePlayer(w http.ResponseWriter, r *http.Request) {
	if s.Players == nil {
		writeError(w, http.StatusNotFound, "player not found")
		return
	}
	name := r.PathValue("name")
	p, ok := s.Players.GetPlayer(name)
	if !ok {
		writeError(w, http.StatusNotFound, "player not found")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// Divergences from TS (documented):
//   - TS api.ts exposes only GET / (status); /players/{name} and /guilds
//     are new read-only views for the Go stub (no TS counterpart).
//   - TS gating is config.apiEnabled || config.hubEnabled + listen on
//     config.apiPort; here gating is API_PORT default-off owned by the
//     caller (this package never reads env except via AddrFromEnv and
//     never listens).
//   - TS sends express.json()/urlencoded middleware and Sentry handlers;
//     this package uses stdlib net/http with GET-only routes (no body
//     parsing, no middleware, non-GET yields 405).
//   - TS status shape {name, port, gameVersion, maxPlayers, playerCount}
//     is preserved verbatim in Status; Player/Guild shapes are new
//     (stub-level snapshots, exact-case names like TS member.username).
