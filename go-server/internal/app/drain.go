// Drain lifecycle + instance roles (GO-PLAN §12 R1 / REWRITE-V2 V2-M1).
//
// Lifecycle is the per-instance RUNNING -> DRAINING -> SHUTDOWN state
// machine. SIGTERM enters DRAINING (the accept gate closes, the sim keeps
// running for existing conns); when the instance is empty or DRAIN_TIMEOUT
// elapses the caller runs its pre-shutdown flush barrier and exits
// (SHUTDOWN is terminal, observed on /healthz just before exit). This type
// owns no signals, sockets, or world hooks: the game driver (internal/
// server) and the router driver (Router in this package) own those and
// drive a shared Default lifecycle so /healthz reports one state.
//
// Roles (ROLE env / --role flag): all-in-one (default, today's behavior),
// router (hub server-list + login routing, no sim), shard (game + hub
// Client registration). Roles only select which drivers Run starts; the
// default all-in-one path is untouched.
package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"rpg-world-server/internal/version"
)

// Instance roles.
const (
	RoleAllInOne = "all-in-one"
	RoleRouter   = "router"
	RoleShard    = "shard"
)

// EnvRole selects the instance role (default all-in-one).
const EnvRole = "ROLE"

// ParseRole resolves the instance role: --role=<r> / --role <r> wins, then
// ROLE env, then all-in-one. Unknown values fall back to all-in-one (the
// caller logs the fallback; the default boot never changes).
func ParseRole(getenv func(string) string, args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		rest := ""
		if a == "--role" {
			if i+1 < len(args) {
				rest = args[i+1]
			}
		} else if strings.HasPrefix(a, "--role=") {
			rest = strings.TrimPrefix(a, "--role=")
		} else {
			continue
		}
		if r := normalizeRole(rest); r != "" {
			return r
		}
	}
	if getenv != nil {
		if r := normalizeRole(getenv(EnvRole)); r != "" {
			return r
		}
	}
	return RoleAllInOne
}

func normalizeRole(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case RoleAllInOne, "":
		if strings.TrimSpace(raw) == "" {
			return ""
		}
		return RoleAllInOne
	case RoleRouter:
		return RoleRouter
	case RoleShard:
		return RoleShard
	default:
		return ""
	}
}

// Health is the /healthz shape: lifecycle state + load (+ build stamps so
// the router and deploy watchers can tell builds apart).
type Health struct {
	State   string `json:"state"`
	Load    int    `json:"load"`
	BuildID string `json:"buildId,omitempty"`
	GVer    string `json:"gVer,omitempty"`
}

// Lifecycle is the per-instance drain state machine. Safe for concurrent
// use; the zero value is usable (RUNNING, following Default).
type Lifecycle struct {
	state atomic.Int32 // 0 RUNNING, 1 DRAINING, 2 SHUTDOWN
	load  atomic.Int32
}

const (
	lcRunning int32 = iota
	lcDraining
	lcShutdown
)

// Default is the process-wide lifecycle reported by /healthz.
var Default = &Lifecycle{}

// State reports RUNNING, DRAINING, or SHUTDOWN.
func (l *Lifecycle) State() string {
	if l == nil {
		return version.StateRunning
	}
	switch l.state.Load() {
	case lcDraining:
		return version.StateDraining
	case lcShutdown:
		return version.StateShutdown
	default:
		return version.StateRunning
	}
}

// BeginDraining moves RUNNING -> DRAINING (idempotent; reports whether this
// call flipped the state).
func (l *Lifecycle) BeginDraining() bool {
	if l == nil {
		return false
	}
	return l.state.CompareAndSwap(lcRunning, lcDraining)
}

// MarkShutdown moves to SHUTDOWN (terminal; called just before exit so a
// final /healthz scrape can observe it).
func (l *Lifecycle) MarkShutdown() {
	if l == nil {
		return
	}
	l.state.Store(lcShutdown)
}

// SetLoad records the current load (live conns / registered shards).
func (l *Lifecycle) SetLoad(n int) {
	if l == nil {
		return
	}
	l.load.Store(int32(n))
}

// Health snapshots the /healthz shape.
func (l *Lifecycle) Health() Health {
	load := 0
	if l != nil {
		load = int(l.load.Load())
	}
	return Health{State: l.State(), Load: load, BuildID: version.BuildID, GVer: version.GVer}
}

// ServeHealth serves /healthz: 200 while RUNNING, 503 once DRAINING or
// SHUTDOWN (so balancers stop sending new sessions), JSON body always.
func (l *Lifecycle) ServeHealth(w http.ResponseWriter, _ *http.Request) {
	h := l.Health()
	code := http.StatusOK
	if h.State != version.StateRunning {
		code = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(h)
}

// WaitEmptyOrTimeout blocks until empty() is true or timeout elapses,
// polling every 200ms. It reports true when the instance drained empty
// (a second shutdown signal should skip the wait and flush immediately).
func (l *Lifecycle) WaitEmptyOrTimeout(empty func() bool, timeout time.Duration) bool {
	if timeout <= 0 {
		timeout = version.DefaultDrainTimeout
	}
	deadline := time.Now().Add(timeout)
	for {
		if empty == nil || empty() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}
