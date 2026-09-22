// Package world is the E9b orchestrator seam for the root server, and (D2a)
// the owner of the entity + connection registry.
//
// The root package (package main: main.go + m5-m13.go + *_wire.go) owns all
// live game state, so this package CANNOT import it (import cycle). D2a
// moved transport + registry ownership here by inversion:
//
//   - modes.go:   TESTMAP/CLEAN/COMBAT parsing (env wins, then CLI flags).
//   - movement.go: movement/anticheat verify math (jump check, speed check,
//     resource-target exception).
//   - region.go:  tile -> region math + interest helpers.
//   - store.go:   entity-position Store backing the Registry.
//   - registry.go: the canonical Registry — entities Store + players table
//     (conn -> root player value via the Peer face) + 9-region interest +
//     Broadcast/Unicast routing + RemoveClient disconnect fanout (root
//     subsystem/persist hooks registered at boot; hooks run lock-free).
//     One mutex owns both maps; lock order is net outbox/Conn.mu ->
//     Registry.mu -> persist store -> subsystem state.
//   - engine.go:  the tick orchestrator: subsystem-tick interfaces plus the
//     frozen cadence constants (20Hz flush, 500ms mob, 1s minigame, 5s
//     showcase, 10s persist flush, 20s store refresh).
//
// Behavior is frozen: every function here mirrors the main.go logic it was
// extracted from (identical frames, cadences and log lines).
package world
