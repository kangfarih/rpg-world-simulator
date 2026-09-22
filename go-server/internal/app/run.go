// Canonical boot driver for the Kaetram stub server.
//
// Run executes the frozen boot sequence supplied by the game core
// (internal/server Steps): log the effective Config, run Init, print the
// listen line + mode line, then serve. Both entry points — the root shim
// (`go run .`) and the canonical runner (`go run ./cmd/server`) — call
// Run with the same Steps, so the PID-independent behavior (packet shapes,
// tick cadences, boot order, TESTMAP/CLEAN/COMBAT selection) is identical
// either way.
package app

import (
	"fmt"
	"log"
	"net/http"
)

// Steps is the game-core half of the canonical boot: the frozen init
// sequence plus the serve parameters. Built by internal/server.Steps from
// the behavior-frozen boot body that used to live in the root main().
type Steps struct {
	// Init runs the frozen boot sequence (persist open, registries,
	// schedulers, tick loop, entity seed, showcase/combat brains, API +
	// console starts). Order matches BootOrder.
	Init func()
	// Addr is the resolved listen address (ListenAddr(PORT)).
	Addr string
	// Modes is the preformatted mode log line
	// (cleanMode/testMode/combatMode + flag help).
	Modes string
	// Handler serves the WS endpoint (accept + handleConn + release).
	Handler http.Handler
}

// Run executes the canonical boot and serves until the listener fails.
// It returns only on ListenAndServe error (logged + fatal by the caller).
func Run(cfg Config, s Steps) error {
	LogConfig(cfg)
	s.Init()
	fmt.Printf("kaetram-stub listening on %s\n", s.Addr)
	log.Printf("%s", s.Modes)
	return http.ListenAndServe(s.Addr, s.Handler)
}
