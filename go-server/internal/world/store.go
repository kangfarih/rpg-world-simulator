package world

import "sync"

// Entity is one registry record: every spawned instance with its current
// tile (mirrors the main.go Entity shape).
type Entry struct {
	Instance string
	X, Y     int
}

// Store is the staged central entity registry (E9b target for the root
// entities/entitiesMu map + setEntityPos/entityPos/removeClient/initEntities
// + handleList iteration).
//
// STAGED, not yet canonical: m5.go, m13.go and pets_wire.go lock entitiesMu
// and read/delete the root map directly, and those files are behavior-frozen
// for E9b. The root adapters in main.go (setEntityPos/entityPos) therefore
// keep the root map canonical until those call sites leave package main, at
// which point the adapters flip to this store with no call-site changes.
type Store struct {
	mu sync.Mutex
	m  map[string]*Entry
}

// NewStore returns an empty registry.
func NewStore() *Store {
	return &Store{m: map[string]*Entry{}}
}

// Set upserts the registry position for an instance (main.go setEntityPos).
func (s *Store) Set(instance string, x, y int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.m[instance]; ok {
		e.X, e.Y = x, y
		return
	}
	s.m[instance] = &Entry{Instance: instance, X: x, Y: y}
}

// Pos returns the registry tile for an instance (main.go entityPos).
func (s *Store) Pos(instance string) (int, int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[instance]
	if !ok {
		return 0, 0, false
	}
	return e.X, e.Y, true
}

// Remove drops an instance (main.go removeClient/projectile-impact path).
func (s *Store) Remove(instance string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, instance)
}

// Count reports the number of registered instances.
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}

// Snapshot lists every registered instance (main.go handleList scan).
func (s *Store) Snapshot() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, 0, len(s.m))
	for _, e := range s.m {
		out = append(out, *e)
	}
	return out
}
