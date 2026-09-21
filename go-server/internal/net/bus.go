// Package net is an ADDITIVE-ONLY seam for the root server's connection
// fan-out and region-interest routing. It defines the Bus interface that a
// future root adapter will implement; it imports nothing from the root
// package (that would be an import cycle) and changes no root behavior.
//
// Read-only copies of the root (package main) signatures this seam mirrors
// (main.go; DO NOT import, DO NOT duplicate logic here):
//
//	func broadcast(frames ...[]any)
//	func send(conn *websocket.Conn, frames ...[]any) error
//	func sendDirect(conn *websocket.Conn, frames ...[]any) error
//	func enqueueTo(conn *websocket.Conn, frames ...[]any)
//	func enqueueGlobal(frames ...[]any)
//	func regionOf(x, y int) int
//	func setEntityPos(instance string, x, y int)
//	func entityPos(instance string) (int, int, bool)
//	func updateClientRegion(c *playerConn)
//	func clientInterested(c *playerConn, x, y int) bool
//	func regionScoped(id int) bool
//	func frameInstance(frame []any) string
//	func removeClient(conn *websocket.Conn)
//
// Globals (signatures only, main.go):
//
//	writeMu sync.Mutex
//	subsMu  sync.Mutex
//	subs    = map[*websocket.Conn]struct{}{}
//	entitiesMu sync.Mutex
//	entities   = map[string]*Entity{}
//	playersMu sync.Mutex
//	players   = map[*websocket.Conn]*playerConn{}
package net

// Frame is one S->C packet frame: [id, data] or [id, opcode, data]
// (see internal/protocol pkt/pktOp). Alias so call sites can pass the
// root's []any frames without conversion.
type Frame = []any

// Bus is the network fan-out seam the root server will adapt later.
// Each method maps to a root function it will wrap:
//
//	Broadcast  -> broadcast(frames ...[]any): region-scoped frames enqueue
//	              only to conns whose interest set includes the entity tile
//	              (clientInterested over the surroundingRegions 9-set);
//	              global frames fan out via enqueueGlobal.
//	SendTo     -> enqueueTo(conn *websocket.Conn, frames ...Frame) (via the
//	              send(conn, frames...) unicast path): queue frames for the
//	              single conn keyed by instance; flushed by the 20Hz tick loop.
//	RegionOf   -> regionOf(x, y int) int: tile -> region id
//	              ((y/mapDivisionSize)*sideLen + (x/mapDivisionSize)).
//	Interested -> regionOf + clientInterested(c *playerConn, x, y int) bool:
//	              simplified to region ids — true when the entity region is in
//	              the client's surrounding-regions interest set.
type Bus interface {
	// Broadcast routes each frame by region scope (root: broadcast).
	Broadcast(frames ...Frame)
	// SendTo unicasts frames to one player instance (root: enqueueTo/send).
	SendTo(instance string, frames ...Frame)
	// RegionOf maps a tile to its region id (root: regionOf).
	RegionOf(x, y int) int
	// Interested reports whether entity region is visible to a client
	// centered in clientRegion (root: regionOf + clientInterested).
	Interested(region, clientRegion int) bool
}

// Config carries connection-gating knobs for a future net listener.
// Only the per-IP cap is modelled so far; the rest is TODO.
type Config struct {
	// MaxConnectionsPerIP caps concurrent conns per IP.
	// Mirrors the TS MAX_CONNECTIONS=16 (see docs/GO-SERVER-PLAN.md:
	// per-IP `MAX_CONNECTIONS`, per-conn msg/s + chat token buckets).
	MaxConnectionsPerIP int
	// TODO: per-conn messages-per-second bucket.
	// TODO: per-conn chat token bucket + global chat cooldown.
}
