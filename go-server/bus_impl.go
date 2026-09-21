// Bus adapter over the root server globals (ADDITIVE-ONLY).
//
// rootBus implements net.Bus (internal/net/bus.go) by delegating to the
// existing root functions; it introduces no behavior change and re-wires
// no call sites (a follow-up task adopts defaultBus where suitable).
package main

import (
	opsnet "rpg-world-server/internal/net"
)

// rootBus adapts the root fan-out/region globals to the net.Bus seam:
// Broadcast -> broadcast, SendTo -> enqueueTo unicast, RegionOf ->
// regionOf, Interested -> regionOf + clientInterested (region-id form).
type rootBus struct{}

// Compile-time assertion that rootBus satisfies the seam.
var _ opsnet.Bus = rootBus{}

// defaultBus is the package-level Bus backed by the live root globals.
var defaultBus opsnet.Bus = rootBus{}

// Broadcast routes each frame by region scope (root: broadcast).
func (rootBus) Broadcast(frames ...opsnet.Frame) { broadcast(frames...) }

// SendTo unicasts frames to one player instance (root: enqueueTo via the
// send unicast path); unknown instances are dropped like enqueueTo's
// unknown-conn no-op.
func (rootBus) SendTo(instance string, frames ...opsnet.Frame) {
	c := connByInstance(instance)
	if c == nil {
		return
	}
	enqueueTo(c.conn, frames...)
}

// RegionOf maps a tile to its region id (root: regionOf).
func (rootBus) RegionOf(x, y int) int { return regionOf(x, y) }

// Interested reports whether an entity region is visible to a client
// centered in clientRegion (root: regionOf + clientInterested, simplified
// to region ids — true when the entity region is in the client's
// surrounding-regions interest set).
func (rootBus) Interested(region, clientRegion int) bool {
	if sideLen <= 0 {
		return region == clientRegion
	}
	// Representative tile of the entity region; regionOf maps it back to
	// region, so clientInterested reduces to the surrounding-regions
	// membership test over region ids.
	x := (region % sideLen) * mapDivisionSize
	y := (region / sideLen) * mapDivisionSize
	c := &playerConn{regions: surroundingRegions(clientRegion)}
	return clientInterested(c, x, y)
}
