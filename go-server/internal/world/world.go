// Package world is the E9b orchestrator seam for the root server.
//
// The root package (package main: main.go + m5-m13.go + *_wire.go) owns all
// live game state, so this package CANNOT import it (import cycle) and the
// frozen root files (m5-m13.go, *_wire.go) still touch the root globals
// (entities/entitiesMu, players/playersMu, testMode, ...) directly. The full
// physical move of transport + registry + dispatch therefore stays staged.
//
// What lives here today (all pure / dependency-free, all on the root hot
// path via thin delegates in main.go):
//
//   - modes.go:   TESTMAP/CLEAN/COMBAT parsing (env wins, then CLI flags).
//   - movement.go: movement/anticheat verify math (jump check, speed check,
//     resource-target exception).
//   - region.go:  tile -> region math + interest helpers.
//   - store.go:   the staged entity-registry Store (tested; root adapters in
//     main.go adopt it once m5.go/pets_wire.go/m13.go stop touching the
//     root entities map directly).
//   - engine.go:  the tick orchestrator: subsystem-tick interfaces plus the
//     frozen cadence constants (20Hz flush, 500ms mob, 1s minigame, 5s
//     showcase, 10s persist flush, 20s store refresh).
//
// Behavior is frozen: every function here mirrors the main.go logic it was
// extracted from, and main.go delegates to it (see the E9b notes there).
package world
