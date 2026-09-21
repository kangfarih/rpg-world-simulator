// Package events is an additive, transport-free timed global event
// scheduler for the Go server stub. The caller owns ticking and all
// packet I/O: it calls Due(nowMs) from its own tick loop and applies the
// returned events itself (global chat message + effect flags). There are
// no timers, goroutines, or network sends in this package.
//
// TS sources mirrored here:
//   - packages/server/src/controllers/events.ts — Events: weekend-only
//     rotation over ['double drops', '1.5x experience', 'lumberjacking',
//     'mining'], hourly check via setInterval(EVENTS_CHECK_INTERVAL),
//     week-number remainder picks the active event, world.globalMessage
//     announces start/end, Utils.doubleLumberjacking/doubleMining flags.
//   - packages/common/network/modules.ts — Constants.EVENTS_CHECK_INTERVAL
//     (3_600_000 ms, one hour).
//   - No events data file exists (packages/server/data carries no
//     events*.json); the rotation list lives inline in events.ts.
//
// Caller pattern (context only, not wired here): go-server/main.go
// startTickLoop ticks abStatusTick/petTick from a central 50ms ticker and
// flushes packets. A future caller would tick Due(nowMs) from that loop
// (or a coarser hourly ticker) and fan out the world-message/effect
// consequences itself.
package events

// CheckIntervalMs mirrors Modules.Constants.EVENTS_CHECK_INTERVAL
// (modules.ts): the TS hourly re-check cadence, 3_600_000 ms.
const CheckIntervalMs = int64(3_600_000)

// Event is one timed global event. Key is the machine key (rotation
// identity), Name the TS display string announced to players, IntervalMs
// the per-event re-fire cadence in milliseconds.
type Event struct {
	Key        string
	Name       string
	IntervalMs int64
}

// DefaultEvents returns the TS rotation list in TS order
// (events.ts:14), each on the hourly check cadence.
func DefaultEvents() []Event {
	return []Event{
		{Key: "double-drops", Name: "double drops", IntervalMs: CheckIntervalMs},
		{Key: "experience", Name: "1.5x experience", IntervalMs: CheckIntervalMs},
		{Key: "lumberjacking", Name: "lumberjacking", IntervalMs: CheckIntervalMs},
		{Key: "mining", Name: "mining", IntervalMs: CheckIntervalMs},
	}
}

// Scheduler fires configured events at their per-event cadence. It is a
// pure check: Due(nowMs) reports which events are due at nowMs and records
// their last-fired times. The caller ticks it; nothing runs in the
// background.
type Scheduler struct {
	events []Event
	last   map[string]int64
	active bool
	seeded bool
}

// NewScheduler builds a Scheduler over events (copied). A nil or empty
// slice selects DefaultEvents.
func NewScheduler(events []Event) *Scheduler {
	if len(events) == 0 {
		events = DefaultEvents()
	}
	cp := make([]Event, len(events))
	copy(cp, events)
	return &Scheduler{events: cp, last: make(map[string]int64, len(cp))}
}

// Start activates the scheduler. The first Due call after Start only seeds
// the last-fired clocks and reports nothing, so events fire at cadence,
// never immediately. Start is idempotent and re-seeds after Stop.
func (s *Scheduler) Start() {
	s.active = true
	s.seeded = false
	for k := range s.last {
		delete(s.last, k)
	}
}

// Stop deactivates the scheduler: Due reports nothing while stopped.
func (s *Scheduler) Stop() {
	s.active = false
}

// IsActive reports whether the scheduler is started and not stopped.
func (s *Scheduler) IsActive() bool {
	return s.active
}

// Due returns the events whose cadence has elapsed at nowMs (in rotation
// order) and advances their last-fired clocks to nowMs. It returns nil
// while stopped, and the seeding call right after Start also returns nil.
// A non-positive IntervalMs means the event is due on every Due call after
// seeding.
func (s *Scheduler) Due(nowMs int64) []Event {
	if !s.active {
		return nil
	}
	if !s.seeded {
		for _, e := range s.events {
			s.last[e.Key] = nowMs
		}
		s.seeded = true
		return nil
	}
	var due []Event
	for _, e := range s.events {
		if e.IntervalMs <= 0 || nowMs-s.last[e.Key] >= e.IntervalMs {
			due = append(due, e)
			s.last[e.Key] = nowMs
		}
	}
	return due
}

// Divergences from TS (documented):
//   - No calendar gating: TS only activates on weekends (getDay() % 6 == 0)
//     and picks a single active event by week-number remainder; the weekend
//     pick persists until a weekday disables it. This scheduler has no clock
//     calendar — the caller decides when to Start/Stop (e.g. weekend policy)
//     and Due fires each configured event on its own IntervalMs cadence.
//   - No single-active latch: TS keeps one activeEvent until disable(); here
//     each event re-fires independently per cadence and Due may return
//     several at once. Callers wanting one-at-a-time rotation should
//     configure staggered intervals or consume only the first due event.
//   - No side effects: TS broadcasts world.globalMessage frames and flips
//     Utils.doubleLumberjacking/doubleMining (plus exposes
//     doubleDropProbability/experiencePerHit multipliers). This package only
//     reports due events; messages, drop/XP multipliers, and harvest flags
//     are caller-side.
//   - No internal timer: TS drives check() from setInterval; here the caller
//     ticks Due(nowMs) (e.g. from the main.go-style tick loop). Millisecond
//     clocks are caller-supplied int64 unix-millis, so tests stay
//     deterministic.
