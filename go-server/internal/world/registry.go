// Entity + connection registry ownership (D2a).
//
// This file moves the root server's entity registry and connection table out
// of package main (main.go entities/players/entitiesMu/playersMu,
// setEntityPos/entityPos/regionOf/updateClientRegion/clientInterested,
// regionScoped/frameInstance routing, removeClient) with byte-identical
// behavior:
//
//   - entities: the central position index (every spawned instance with its
//     current tile) reuses the staged Store (store.go) as its backing map.
//   - players: the live connection table (conn -> root player value, stored
//     as any; the root registers *playerConn which embeds *net.Conn, so every
//     value satisfies Peer). One mutex owns both maps.
//   - region interest: tile -> region math (region.go) plus the configured
//     9-region surrounding set and sideLen/divSize (configured at boot from
//     the loaded world); UpdateRegion recomputes a conn's interest set and
//     fires the root region-enter hook (lamp fan-out).
//   - fan-out: Broadcast (region-scoped routing + TX log + enqueue, the root
//     broadcast verbatim), Unicast (instance lookup + net send), RemoveClient
//     (registry cleanup + Despawn fan-out + root disconnect hooks + close,
//     the root removeClient verbatim).
//
// Lock order (D2a contract, net outbox -> world registry -> persist store ->
// subsystem state):
//
//   - net.Conn.mu and Hub locks are leaves: Broadcast/UpdateRegion snapshot
//     conn pointers under mu, release mu, then touch conns (channel enqueue
//     is lock-free; Regions/Dropped go through Conn.mu).
//   - Registry.mu is leaf-safe: while holding it this package never calls
//     into transport writes, the persist store, or subsystem code. Disconnect
//     and region-enter hooks run AFTER mu is released, so the engine-tick
//     nesting (m9Tick holds m9Mu across broadcast, as before) cannot
//     deadlock: the inner Registry.mu never blocks on an outer subsystem
//     lock.
//   - Callers must never hold persist (dbMu) or subsystem mutexes across
//     Registry calls that block; all methods here are synchronous and
//     short-lived.
//
// Session field discipline (pre-existing, unchanged): Instance is written
// before registration (the Registry mutex provides the happens-before edge);
// Username/Sess keep their connection-goroutine-write discipline from the
// root map era. Regions/Dropped moved from playersMu to Conn.mu with no
// behavior change.
package world

import (
	"fmt"
	"log"
	"sync"

	"github.com/gorilla/websocket"

	transport "rpg-world-server/internal/net"
	"rpg-world-server/internal/protocol"
)

// Peer is the transport face the Registry needs from a stored player value.
// *transport.Conn implements it; the root *playerConn promotes the methods
// through its embedded *transport.Conn.
type Peer interface {
	PeerNet() *transport.Conn
}

// peerNet extracts the transport record from a stored player value.
func peerNet(v any) *transport.Conn {
	if v == nil {
		return nil
	}
	if p, ok := v.(Peer); ok {
		return p.PeerNet()
	}
	return nil
}

// Registry is the central entity + connection registry (D2a owner of the
// root entities/players maps). Use Default via the package-level wrappers;
// NewRegistry exists for tests.
type Registry struct {
	mu      sync.Mutex
	store   *Store
	players map[*websocket.Conn]any

	sideLen  int
	divSize  int
	surround func(region int) []int

	onRegion     func(v any)
	onDisconnect []func(v any)
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		store:   NewStore(),
		players: make(map[*websocket.Conn]any),
	}
}

// Default is the process-wide registry (replaces the root
// entities/players/entitiesMu/playersMu globals).
var Default = NewRegistry()

// Configure sets the region geometry and the surrounding-regions function
// plus the root region-enter hook (lamp fan-out). Called once at boot after
// the world loads; writes happen-before serving.
func (r *Registry) Configure(sideLen, divSize int, surround func(int) []int, onRegion func(any)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sideLen = sideLen
	r.divSize = divSize
	r.surround = surround
	r.onRegion = onRegion
}

// OnDisconnect registers a disconnect hook (subsystem forget + persist
// flushes). Hooks run in registration order, after Registry.mu is released.
// Register at boot only.
func (r *Registry) OnDisconnect(fn func(v any)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onDisconnect = append(r.onDisconnect, fn)
}

// ---------------------------------------------------------------------------
// Entities (Store backing).
// ---------------------------------------------------------------------------

// SetPos upserts the registry position for an instance (setEntityPos).
func (r *Registry) SetPos(instance string, x, y int) { r.store.Set(instance, x, y) }

// Pos returns the registry tile for an instance (entityPos).
func (r *Registry) Pos(instance string) (int, int, bool) { return r.store.Pos(instance) }

// Remove drops an instance (projectile-impact / loot-destroy path).
func (r *Registry) Remove(instance string) { r.store.Remove(instance) }

// Count reports the number of registered instances.
func (r *Registry) Count() int { return r.store.Count() }

// Snapshot lists every registered instance (handleList scan).
func (r *Registry) Snapshot() []Entry { return r.store.Snapshot() }

// ---------------------------------------------------------------------------
// Players (connection table).
// ---------------------------------------------------------------------------

// Add registers a live connection value (the root registers *playerConn).
func (r *Registry) Add(ws *websocket.Conn, v any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.players[ws] = v
}

// RemoveWS deletes a conn from the table, returning the stored value.
func (r *Registry) RemoveWS(ws *websocket.Conn) any {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := r.players[ws]
	delete(r.players, ws)
	return v
}

// ByWS returns the stored value for a conn.
func (r *Registry) ByWS(ws *websocket.Conn) (any, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.players[ws]
	return v, ok
}

// ByInstance returns the stored value for a player instance.
func (r *Registry) ByInstance(instance string) (any, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range r.players {
		if c := peerNet(v); c != nil && c.Instance == instance {
			return v, true
		}
	}
	return nil, false
}

// All returns every stored player value.
func (r *Registry) All() []any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]any, 0, len(r.players))
	for _, v := range r.players {
		out = append(out, v)
	}
	return out
}

// Conns returns every live transport record.
func (r *Registry) Conns() []*transport.Conn {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*transport.Conn, 0, len(r.players))
	for _, v := range r.players {
		if c := peerNet(v); c != nil {
			out = append(out, c)
		}
	}
	return out
}

// AllWS returns every live connection socket (console sweeps).
func (r *Registry) AllWS() []*websocket.Conn {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*websocket.Conn, 0, len(r.players))
	for ws := range r.players {
		out = append(out, ws)
	}
	return out
}

// CountPlayers reports the number of live connections.
func (r *Registry) CountPlayers() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.players)
}

// ---------------------------------------------------------------------------
// Region interest.
// ---------------------------------------------------------------------------

// RegionOf maps a tile to its region id (regionOf; 0 while unconfigured).
func (r *Registry) RegionOf(x, y int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return RegionOf(x, y, r.sideLen, r.divSize)
}

// Surrounding returns the 9-region interest set for a region id.
func (r *Registry) Surrounding(rid int) []int {
	r.mu.Lock()
	surround := r.surround
	r.mu.Unlock()
	if surround == nil {
		return []int{rid}
	}
	return surround(rid)
}

// UpdateRegion recomputes a conn's 9-region interest set from its
// authoritative tile, then fires the root region-enter hook with NO locks
// held (updateClientRegion; the hook pushes lamps and must be able to call
// back into the Registry).
func (r *Registry) UpdateRegion(v any, x, y int) {
	c := peerNet(v)
	if c == nil {
		return
	}
	rid := r.RegionOf(x, y)
	c.SetRegions(r.Surrounding(rid))
	r.mu.Lock()
	hook := r.onRegion
	r.mu.Unlock()
	if hook != nil {
		hook(v)
	}
}

// Interested reports whether v's regions include the entity tile (x,y)
// (clientInterested).
func (r *Registry) Interested(v any, x, y int) bool {
	c := peerNet(v)
	if c == nil {
		return false
	}
	return InterestHit(r.RegionOf(x, y), c.Regions())
}

// InterestedRegion reports whether an entity region is visible to a client
// centered in clientRegion (rootBus.Interested: surrounding-regions
// membership over region ids).
func (r *Registry) InterestedRegion(entityRegion, clientRegion int) bool {
	r.mu.Lock()
	sideLen, divSize := r.sideLen, r.divSize
	r.mu.Unlock()
	if sideLen <= 0 {
		return entityRegion == clientRegion
	}
	x := (entityRegion % sideLen) * divSize
	y := (entityRegion / sideLen) * divSize
	return InterestHit(RegionOf(x, y, sideLen, divSize), r.Surrounding(clientRegion))
}

// ---------------------------------------------------------------------------
// Fan-out.
// ---------------------------------------------------------------------------

// Broadcast routes each frame: region-scoped packets only enqueue to conns
// whose interest set includes the entity tile; global events fan out.
// Queued into tick outboxes (never direct-written); shapes unchanged (root
// broadcast verbatim, including TX logging and overflow lines).
func (r *Registry) Broadcast(frames ...[]any) {
	msg := protocol.Bulk(frames...)
	fmt.Printf("TX %s\n", msg)
	conns := r.All()
	for _, f := range frames {
		if len(f) == 0 {
			continue
		}
		id, ok := f[0].(int)
		if !ok {
			r.enqueueGlobal(f, conns)
			continue
		}
		if !transport.RegionScoped(id) {
			r.enqueueGlobal(f, conns)
			continue
		}
		inst := transport.FrameInstance(f)
		x, y, found := r.Pos(inst)
		if !found {
			r.enqueueGlobal(f, conns)
			continue
		}
		rid := r.RegionOf(x, y)
		for _, v := range conns {
			c := peerNet(v)
			if c == nil {
				continue
			}
			if InterestHit(rid, c.Regions()) {
				if !transport.TryEnqueue(c, f) {
					n := c.BumpDropped()
					log.Printf("outbox overflow instance=%s dropped=%d", c.Instance, n)
				}
			}
		}
	}
}

// enqueueGlobal queues one frame for every live connection (enqueueGlobal).
func (r *Registry) enqueueGlobal(f []any, conns []any) {
	for _, v := range conns {
		c := peerNet(v)
		if c == nil {
			continue
		}
		if !transport.TryEnqueue(c, f) {
			n := c.BumpDropped()
			log.Printf("outbox overflow instance=%s dropped=%d", c.Instance, n)
		}
	}
}

// Unicast queues frames for one player instance (SendTo path); unknown
// instances are dropped like the old enqueueTo unknown-conn no-op.
func (r *Registry) Unicast(instance string, frames ...[]any) bool {
	v, ok := r.ByInstance(instance)
	if !ok {
		return false
	}
	c := peerNet(v)
	if c == nil {
		return false
	}
	_ = transport.Send(c, frames...)
	return true
}

// RemoveClient drops a dead conn: table cleanup + entity removal + Despawn
// fan-out for its player (root removeClient verbatim, including hook order,
// close, Despawn broadcast and log line). Hooks run with no Registry locks
// held (persist -> subsystem order per the lock contract).
func (r *Registry) RemoveClient(ws *websocket.Conn) any {
	r.mu.Lock()
	v, ok := r.players[ws]
	if ok {
		delete(r.players, ws)
	}
	hooks := append([]func(any){}, r.onDisconnect...)
	r.mu.Unlock()
	transport.DelSub(ws)
	if !ok {
		return nil
	}
	instance := ""
	if c := peerNet(v); c != nil {
		instance = c.Instance
	}
	if instance != "" {
		r.store.Remove(instance)
	}
	for _, fn := range hooks {
		fn(v)
	}
	if ws != nil {
		_ = ws.Close()
	}
	r.Broadcast(protocol.Pkt(protocol.PacketDespawn,
		protocol.DespawnData{Instance: instance}))
	log.Printf("client removed: instance=%s (despawn broadcast)", instance)
	return v
}

// ---------------------------------------------------------------------------
// Package-level wrappers over Default.
// ---------------------------------------------------------------------------

// Configure sets the region geometry, surrounding function and region-enter
// hook (called once at boot).
func Configure(sideLen, divSize int, surround func(int) []int, onRegion func(any)) {
	Default.Configure(sideLen, divSize, surround, onRegion)
}

// OnDisconnect registers a disconnect hook (boot only).
func OnDisconnect(fn func(v any)) { Default.OnDisconnect(fn) }

// SetEntityPos upserts the registry position for an instance.
func SetEntityPos(instance string, x, y int) { Default.SetPos(instance, x, y) }

// EntityPos returns the registry tile for an instance.
func EntityPos(instance string) (int, int, bool) { return Default.Pos(instance) }

// RemoveEntity drops an instance from the registry.
func RemoveEntity(instance string) { Default.Remove(instance) }

// EntityCount reports the number of registered instances.
func EntityCount() int { return Default.Count() }

// EntitySnapshot lists every registered instance.
func EntitySnapshot() []Entry { return Default.Snapshot() }

// AddPlayer registers a live connection value.
func AddPlayer(ws *websocket.Conn, v any) { Default.Add(ws, v) }

// Lookup returns the stored value for a conn.
func Lookup(ws *websocket.Conn) (any, bool) { return Default.ByWS(ws) }

// LookupInstance returns the stored value for a player instance.
func LookupInstance(instance string) (any, bool) { return Default.ByInstance(instance) }

// Find returns the stored value for a player instance asserted to T
// (the root uses Find[*playerConn]).
func Find[T any](instance string) (T, bool) {
	v, ok := Default.ByInstance(instance)
	if !ok {
		var zero T
		return zero, false
	}
	t, ok := v.(T)
	if !ok {
		var zero T
		return zero, false
	}
	return t, true
}

// AllPlayers returns every stored player value.
func AllPlayers() []any { return Default.All() }

// AllOf returns every stored player value asserted to T.
func AllOf[T any]() []T {
	out := []T{}
	for _, v := range Default.All() {
		if t, ok := v.(T); ok {
			out = append(out, t)
		}
	}
	return out
}

// ByConn returns the stored value for a conn asserted to T.
func ByConn[T any](ws *websocket.Conn) (T, bool) {
	v, ok := Default.ByWS(ws)
	if !ok {
		var zero T
		return zero, false
	}
	t, ok := v.(T)
	if !ok {
		var zero T
		return zero, false
	}
	return t, true
}

// PlayerCount reports the number of live connections.
func PlayerCount() int { return Default.CountPlayers() }

// AllWS returns every live connection socket.
func AllWS() []*websocket.Conn { return Default.AllWS() }

// Conns returns every live transport record.
func Conns() []*transport.Conn { return Default.Conns() }

// TileRegion maps a tile to its region id.
func TileRegion(x, y int) int { return Default.RegionOf(x, y) }

// SurroundingOf returns the 9-region interest set for a region id.
func SurroundingOf(rid int) []int { return Default.Surrounding(rid) }

// UpdateRegion recomputes a conn's interest set and fires the region hook.
func UpdateRegion(v any, x, y int) { Default.UpdateRegion(v, x, y) }

// ClientInterested reports whether v's regions include the entity tile.
func ClientInterested(v any, x, y int) bool { return Default.Interested(v, x, y) }

// InterestedRegion reports whether an entity region is visible to a client
// centered in clientRegion.
func InterestedRegion(entityRegion, clientRegion int) bool {
	return Default.InterestedRegion(entityRegion, clientRegion)
}

// Broadcast routes each frame by region scope into tick outboxes.
func Broadcast(frames ...[]any) { Default.Broadcast(frames...) }

// Unicast queues frames for one player instance.
func Unicast(instance string, frames ...[]any) bool {
	return Default.Unicast(instance, frames...)
}

// RemoveClient drops a dead conn with the full disconnect fanout.
func RemoveClient(ws *websocket.Conn) any { return Default.RemoveClient(ws) }
