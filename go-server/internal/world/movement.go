package world

import "time"

// Movement/anticheat verify core (E9b extraction of the main.go movement
// section). All functions are pure: the root session shape stays in package
// main (m13.go reads/writes sess.movementSpeed), so main.go adapts its
// session to SpeedState on each check.
type SpeedState struct {
	// MovementSpeed is ms per tile (Welcome default 220).
	MovementSpeed int
	LastStep      time.Time
}

// Abs mirrors main.go abs.
func Abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// JumpTooFar mirrors the far-jump (noclip) gate in handleMovement: a Request
// or Started report more than 2 tiles off the authoritative tile is
// rejected/resynced (player.ts handleMovementRequest diff>2).
func JumpTooFar(dx, dy int) bool {
	return dx > 2 || dy > 2
}

// CheckSpeed enforces max tiles/sec vs movementSpeed with the main.go
// semantics verbatim: 220ms/tile default, first step after idle (>2s)
// always passes, 5% margin per tile (verifyMovement), sliding lastStep
// window advanced even on reject. Returns true when the step is too fast
// (caller rejects + teleport-back; >15 disconnects).
func CheckSpeed(st *SpeedState, tiles int, now time.Time) bool {
	if st.MovementSpeed <= 0 {
		st.MovementSpeed = 220
	}
	if st.LastStep.IsZero() {
		st.LastStep = now
		return false
	}
	// Grace: first step after idle (>2s) always passes (region-change rule).
	if now.Sub(st.LastStep) > 2*time.Second {
		st.LastStep = now
		return false
	}
	if tiles < 1 {
		tiles = 1
	}
	minInterval := time.Duration(st.MovementSpeed) * time.Millisecond
	// 5% margin like verifyMovement, per-tile with no +2 padding.
	allowance := time.Duration(float64(minInterval) * 0.95 * float64(tiles))
	if now.Sub(st.LastStep) < allowance {
		// Sliding window: advance lastStep even on reject so legit
		// players paced at the legal rate never accumulate cheatScore.
		st.LastStep = now
		return true
	}
	st.LastStep = now
	return false
}

// TargetsOccupant mirrors main.go targetsResource without the tile lookup:
// reports whether any of the given target instances is the resource
// occupying the destination tile. An empty occupant never matches, so
// static collisions are always rejected even with no target set.
func TargetsOccupant(occupant string, targets ...string) bool {
	if occupant == "" {
		return false
	}
	for _, t := range targets {
		if t != "" && t == occupant {
			return true
		}
	}
	return false
}
