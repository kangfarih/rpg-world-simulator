package net

import "testing"

// testSideLen/testDivision mirror the root sideLen/mapDivisionSize shape
// (main.go regionOf + surroundingRegions) on a small grid so the
// surrounding-region rule can be tested without root globals.
const (
	testSideLen  = 8
	testDivision = 48
)

// testRegionOf mirrors regionOf: (y/div)*sideLen + (x/div).
func testRegionOf(x, y int) int {
	return (y/testDivision)*testSideLen + (x / testDivision)
}

// testInterested mirrors clientInterested over the surroundingRegions 9-set:
// true when region == clientRegion or a (horizontal/vertical/diagonal)
// neighbour on the region grid. Col/row deltas encode the root's
// left/right/top/bottom edge guards.
func testInterested(region, clientRegion int) bool {
	if region == clientRegion {
		return true
	}
	rc, cc := region%testSideLen, clientRegion%testSideLen
	rr, cr := region/testSideLen, clientRegion/testSideLen
	dc := rc - cc
	if dc < 0 {
		dc = -dc
	}
	dr := rr - cr
	if dr < 0 {
		dr = -dr
	}
	return dc <= 1 && dr <= 1
}

// fakeBus is a test-only Bus implementation proving the interface is
// implementable with no root imports. Broadcast/SendTo record frames;
// RegionOf/Interested delegate to the minimal mirrors above.
type fakeBus struct {
	broadcast []Frame
	sent      map[string][]Frame
}

var _ Bus = (*fakeBus)(nil)

func newFakeBus() *fakeBus { return &fakeBus{sent: map[string][]Frame{}} }

func (f *fakeBus) Broadcast(frames ...Frame) { f.broadcast = append(f.broadcast, frames...) }

func (f *fakeBus) SendTo(instance string, frames ...Frame) {
	f.sent[instance] = append(f.sent[instance], frames...)
}

func (f *fakeBus) RegionOf(x, y int) int { return testRegionOf(x, y) }

func (f *fakeBus) Interested(region, clientRegion int) bool {
	return testInterested(region, clientRegion)
}

func TestBusImplementable(t *testing.T) {
	var b Bus = newFakeBus()
	b.Broadcast(Frame{5, "spawn"})
	b.SendTo("hero", Frame{11, "move"})
	if got := b.RegionOf(testDivision*2+1, testDivision*3+1); got != 3*testSideLen+2 {
		t.Fatalf("RegionOf = %d, want %d", got, 3*testSideLen+2)
	}
	if !b.Interested(50, 50) {
		t.Fatalf("Interested(same region) = false, want true")
	}
}

func TestInterestedSameRegion(t *testing.T) {
	f := newFakeBus()
	for _, region := range []int{0, 50, 63} {
		if !f.Interested(region, region) {
			t.Fatalf("Interested(%d,%d) = false, want true", region, region)
		}
	}
}

func TestInterestedNonSurrounding(t *testing.T) {
	f := newFakeBus()
	// Corners of the 8-wide grid are far apart (not in each other's 9-set).
	if f.Interested(0, 63) {
		t.Fatalf("Interested(0,63) = true, want false")
	}
	if f.Interested(63, 0) {
		t.Fatalf("Interested(63,0) = true, want false")
	}
	// Same row but 2+ columns apart is outside the neighbour band.
	if f.Interested(0, 2) {
		t.Fatalf("Interested(0,2) = true, want false")
	}
}

func TestInterestedNeighbour(t *testing.T) {
	f := newFakeBus()
	// 0's neighbours on the 8-wide grid: 1 (east), 8 (south), 9 (diagonal).
	for _, n := range []int{1, 8, 9} {
		if !f.Interested(n, 0) {
			t.Fatalf("Interested(%d,0) = false, want true", n)
		}
	}
}
