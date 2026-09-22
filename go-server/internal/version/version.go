// Package version stamps the binary (buildID) and the wire contract
// (gVer), and owns the drain-lifecycle states shared by the game, the
// router, and the hub server-list.
//
// BuildID is stamped at link time:
//
//	go build -ldflags "-X rpg-world-server/internal/version.BuildID=<git sha>" ./...
//
// and defaults to "dev" for unstamped builds (go run, tests).
//
// GVer is the game wire version. It MUST equal the stock client's gVer
// (packages/common client handshake: connection.ts handleConnected sends
// {gVer: config.version}; config.version is GVER from .env.defaults,
// currently '0.5.5-beta'). The TS game server enforces the same contract
// (packages/server incoming.ts: gVer mismatch -> connection.reject).
// Bump GVer only on a wire break, and keep it in lockstep with GVER in
// .env.defaults / .env / .env.e2e.
//
// Drain states (GO-PLAN §12 R1 / REWRITE-V2 V2-M1): every instance moves
// RUNNING -> DRAINING -> SHUTDOWN. SIGTERM enters DRAINING (no new conns,
// sim continues); empty-or-timeout runs the pre-shutdown flush barrier and
// exits (SHUTDOWN is terminal, observed on /healthz just before exit).
package version

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"time"
)

// BuildID identifies the binary (git SHA via ldflags; "dev" when unstamped).
var BuildID = "dev"

// GVer is the game wire version. Keep in lockstep with GVER in the repo
// .env.defaults (stock client sends config.version as Handshake{gVer}).
const GVer = "0.5.5-beta"

// Lifecycle states. StateUnknown ("") is reported by shards that predate
// the field and is treated as RUNNING by the router selector.
const (
	StateRunning  = "RUNNING"
	StateDraining = "DRAINING"
	StateShutdown = "SHUTDOWN"
)

// Env keys.
const (
	// EnvStrict gates the handshake gVer check. "0"/"false"/"off"/"no"
	// disables the gate (dev escape hatch); anything else (incl. unset)
	// enforces it.
	EnvStrict = "GVER_STRICT"
	// EnvDrainTimeout bounds DRAINING (Go duration, e.g. "30m"; a bare
	// number means seconds). Default DefaultDrainTimeout.
	EnvDrainTimeout = "DRAIN_TIMEOUT"
)

// DefaultDrainTimeout bounds DRAINING when DRAIN_TIMEOUT is unset/invalid.
const DefaultDrainTimeout = 30 * time.Minute

// Strict reports whether the gVer gate is enforced (default true; the
// GVER_STRICT=0 escape hatch is for dev only).
func Strict() bool { return StrictFromEnv(os.Getenv) }

// StrictFromEnv is Strict over an injected env lookup (tests).
func StrictFromEnv(getenv func(string) string) bool {
	raw := ""
	if getenv != nil {
		raw = getenv(EnvStrict)
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}

// DrainTimeout returns the DRAIN_TIMEOUT bound (default 30m; invalid falls
// back to the default; bare numbers mean seconds).
func DrainTimeout() time.Duration { return DrainTimeoutFromEnv(os.Getenv) }

// DrainTimeoutFromEnv is DrainTimeout over an injected env lookup (tests).
func DrainTimeoutFromEnv(getenv func(string) string) time.Duration {
	raw := ""
	if getenv != nil {
		raw = strings.TrimSpace(getenv(EnvDrainTimeout))
	}
	if raw == "" {
		return DefaultDrainTimeout
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	if n, err := strconv.Atoi(raw); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return DefaultDrainTimeout
}

// Pass reports whether a client gVer string satisfies the gate. Strict mode
// (default) requires exact equality with GVer; the GVER_STRICT=0 escape
// hatch accepts everything (dev only, logged at boot).
func Pass(clientGVer string) bool { return !Strict() || clientGVer == GVer }

// PassForEnv is Pass over an injected strict flag (unit tests without env).
func PassForEnv(clientGVer string, strict bool) bool { return !strict || clientGVer == GVer }

// ExtractGVer pulls the gVer string out of a Handshake data element. It
// returns "" when the element is missing, malformed, or gVer is not a JSON
// string (numeric/legacy values such as {"gVer":1} therefore fail a strict
// gate — same as the TS incoming.ts !== comparison).
func ExtractGVer(data json.RawMessage) string {
	if len(data) == 0 {
		return ""
	}
	var hs struct {
		GVer any `json:"gVer"`
	}
	if err := json.Unmarshal(data, &hs); err != nil {
		return ""
	}
	s, _ := hs.GVer.(string)
	return s
}
