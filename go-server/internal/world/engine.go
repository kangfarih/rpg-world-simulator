package world

import "time"

// Tick cadences (frozen; E9b keeps every one identical):
//
//   - FlushInterval: central 20Hz outbox flush (main.go startTickLoop) that
//     also runs the per-tick subsystems below.
//   - MobTick: 500ms mob AI engine tick (m9Engine).
//   - MinigameTick: 1s minigame engines (m8LoadGames).
//   - ShowcaseTick: 5s showcase anim loop (main.go startShowcase).
//   - PersistFlush: 10s dirty-account flush (m5 dirty ticker).
//   - StockRefresh: 20s store stock refresh (m6StartStoreTicker).
const (
	FlushInterval  = 50 * time.Millisecond
	MobTick        = 500 * time.Millisecond
	MinigameTick   = time.Second
	ShowcaseTick   = 5 * time.Second
	PersistFlush   = 10 * time.Second
	StockRefresh   = 20 * time.Second
	DummyRespawnAt = 15 * time.Second // default BossDummy respawn (DUMMY_RESPAWN overrides)
)

// Subsystem is one per-tick unit wired into the Engine. The root registers
// its existing tick entry points (abStatusTick, petTick, worldEventTick —
// owned by frozen *_wire.go files) without changing them.
type Subsystem struct {
	Name string
	Tick func()
}

// Engine is the tick-loop orchestrator: every FlushInterval the root calls
// Tick, which runs the subsystems in registration order. Registration order
// mirrors main.go startTickLoop: abilities (DoT/expiry) -> pets (follow) ->
// events (rotation notices).
type Engine struct {
	Subs []Subsystem
}

// Tick runs every subsystem in order.
func (e *Engine) Tick() {
	for _, s := range e.Subs {
		s.Tick()
	}
}

// Names lists the registered subsystem names in tick order.
func (e *Engine) Names() []string {
	out := make([]string, 0, len(e.Subs))
	for _, s := range e.Subs {
		out = append(out, s.Name)
	}
	return out
}
