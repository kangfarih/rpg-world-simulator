// Package persist owns the M5 SQLite persistence core: the single-writer
// Store (one *sql.DB, one dirty set, one mutex), the players/inventory/bank/
// equipment/skills schema, and the write/load/flush operations. Moved
// verbatim out of the root m5.go persist section (E9a) WITHOUT behavior
// change: identical DDL, identical WAL+NORMAL pragmas, identical SQL
// sequences and log text.
//
// What moved here (persistence only):
//   - DB open + pragmas + schema + M12 enchantments migration (Open,
//     EnsureSchema) from m5Init/dbPath.
//   - Dirty-set tracking (MarkDirty/MarkClean/DirtyList) from markDirty and
//     the dirty map.
//   - Row writes (WritePlayer) from writePlayer.
//   - Row reads (LoadPlayer) from the DB half of m5Load.
//   - Dirty flush + final close (FlushDirty/Close).
//   - State snapshot deep copy (Snapshot) from the copy half of m5Snapshot.
//
// What stayed in the root m5.go (NOT moved):
//   - Drops/loot rolls (m5RollEntry/m5GetDrops/m5SpawnLoot + loot timers).
//   - XP/skill awards (m5AddXP/m5AwardCombatXP/m5GatherXP + level curves).
//   - The in-memory pstates map + m5Snapshot (needs pstateMu) + the
//     m5State/m5Slot/m5Skill types (M12 Enchantments type is root-owned).
//   - m5Init/markDirty/flushDirty/m5SaveSync/m5Load/m5LoginWelcome keep
//     their signatures and delegate to Store (converting m5State <->
//     persist.State); dbConn/dbMu stay declared in the root so the
//     m11/m13/social/abilities/ops direct-table seams compile untouched.
//   - The 10s dirty-flush ticker + SIGTERM/SIGINT final-flush goroutines
//     (ownership stays in root m5Init; same cadence, same semantics).
//
// Locking: Store serializes its dirty set and every write/read sequence on
// its internal mutex (single writer, one connection via SetMaxOpenConns(1)).
// The root additionally holds its dbMu across the per-key write+clean and
// across m5Load, exactly as before, so the legacy direct-DB seams
// (m13 flags, social guilds/friends, abilities, ops console) keep the same
// mutual exclusion with the M5 write path. Lock order is always
// dbMu -> Store.mu -> pstateMu and never the reverse: snapshots are taken
// before either DB mutex is held, so the root "never hold dbMu and pstateMu
// at the same time" discipline is preserved.
//
// Errors: Open/EnsureSchema/WritePlayer wrap failures with the same text
// the root log.Fatalf/log.Printf lines used ("open db: ...",
// "pragma ...: ...", "ddl: ...", "m5: save ..."), so the root m5Init
// log.Fatalf("m5: %v", err) and the write-path logs read byte-identical.
package persist

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	_ "modernc.org/sqlite"
)

// Slot is one inventory/bank/equipment entry. Ench carries the raw
// enchantments JSON for the inventory.enchantments column ("{}" when none);
// bank/equipment rows carry "" (their tables have no such column, matching
// the root writePlayer which only persists enchantments for inventory).
type Slot struct {
	Key   string
	Count int
	Ench  string
}

// Skill is one skill row (level + XP).
type Skill struct {
	Level int
	XP    int
}

// State is the persist snapshot for one player: position/level/vitals plus
// the inventory, bank and equipment slot lists and the skills map.
// Equipment is dense by type index (holes are zero Slots); LoadPlayer sizes
// it to maxType+1 (nil when no rows) and the root pads/truncates it to the
// fixed ModulesEquipmentCount array exactly like the old m5Load.
type State struct {
	X      int
	Y      int
	Level  int
	HP     int
	Inv    []Slot
	Bank   []Slot
	Equip  []Slot
	Skills map[int]Skill
}

// Snapshot deep-copies a State (nil-safe for the Skills map).
func (s *Store) Snapshot(st State) State {
	cp := State{
		X: st.X, Y: st.Y, Level: st.Level, HP: st.HP,
		Skills: make(map[int]Skill, len(st.Skills)),
	}
	cp.Inv = append(cp.Inv, st.Inv...)
	cp.Bank = append(cp.Bank, st.Bank...)
	cp.Equip = append(cp.Equip, st.Equip...)
	for id, sk := range st.Skills {
		cp.Skills[id] = sk
	}
	return cp
}

// Store is the single-writer SQLite handle: one *sql.DB (single pooled
// connection), the dirty-player set, and the mutex serializing both.
type Store struct {
	db    *sql.DB
	mu    sync.Mutex
	dirty map[string]bool
	path  string
}

// Open opens the SQLite DB at path with the shipped pragmas and schema
// (WAL + NORMAL, single connection, players/inventory/bank/equipment/skills
// tables, M12 inventory.enchantments migration). Error text matches the old
// root m5Init fatal lines so the root log.Fatalf("m5: %v", err) reads
// identical.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, dirty: map[string]bool{}, path: path}
	for _, pr := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=NORMAL"} {
		if _, err := db.Exec(pr); err != nil {
			db.Close()
			return nil, fmt.Errorf("pragma %q: %w", pr, err)
		}
	}
	if err := s.EnsureSchema(); err != nil {
		db.Close()
		return nil, err
	}
	// M12 migration: pre-existing DBs lack the inventory enchantments column
	// (CREATE IF NOT EXISTS is a no-op there). Ignore failure = column exists.
	_, _ = db.Exec(`ALTER TABLE inventory ADD COLUMN enchantments TEXT`)
	return s, nil
}

// EnsureSchema creates the five persist tables when missing (verbatim DDL
// from the old m5Init).
func (s *Store) EnsureSchema() error {
	if s == nil || s.db == nil {
		return fmt.Errorf("ddl: nil store")
	}
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS players(instance TEXT PRIMARY KEY, name TEXT, x INT, y INT, level INT, hp INT, data TEXT)`,
		`CREATE TABLE IF NOT EXISTS inventory(player TEXT, slot INT, item TEXT, count INT, enchantments TEXT, PRIMARY KEY(player, slot))`,
		`CREATE TABLE IF NOT EXISTS bank(player TEXT, slot INT, item TEXT, count INT, PRIMARY KEY(player, slot))`,
		`CREATE TABLE IF NOT EXISTS equipment(player TEXT, type INT, item TEXT, count INT, PRIMARY KEY(player, type))`,
		`CREATE TABLE IF NOT EXISTS skills(player TEXT, skill INT, level INT, xp INT, PRIMARY KEY(player, skill))`,
	} {
		if _, err := s.db.Exec(ddl); err != nil {
			return fmt.Errorf("ddl: %w", err)
		}
	}
	return nil
}

// DB exposes the underlying handle for the legacy direct-table seams (m11
// quest rows, m13 flags, social guilds/friends, abilities, ops console),
// which keep issuing their own SQL under the root dbMu.
func (s *Store) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

// Path returns the DB path the Store was opened with.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// MarkDirty flags a player for the next flush ("" is ignored, as before).
func (s *Store) MarkDirty(key string) {
	if s == nil || key == "" {
		return
	}
	s.mu.Lock()
	s.dirty[key] = true
	s.mu.Unlock()
}

// MarkClean clears a player's dirty flag.
func (s *Store) MarkClean(key string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.dirty, key)
	s.mu.Unlock()
}

// DirtyList returns the currently dirty keys (order unspecified, as before).
func (s *Store) DirtyList() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	keys := make([]string, 0, len(s.dirty))
	for key := range s.dirty {
		keys = append(keys, key)
	}
	s.mu.Unlock()
	return keys
}

// WritePlayer writes one player's full row set (players upsert + inventory +
// bank + equipment + skills), verbatim SQL and log text from the old root
// writePlayer. The error return is for tests/callers; failures are already
// logged with the shipped "m5: ..." lines, so root callers ignore it.
func (s *Store) WritePlayer(key string, st State) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("m5: save %s: nil store", key)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	extra := map[string]any{"equip": []any{}}
	if len(st.Equip) > 0 {
		eqs := make([]any, 0, len(st.Equip))
		for t, e := range st.Equip {
			if e.Key == "" || e.Count < 1 {
				continue
			}
			eqs = append(eqs, map[string]any{"type": t, "key": e.Key, "count": e.Count})
		}
		extra["equip"] = eqs
	}
	extraRaw, _ := json.Marshal(extra)
	if _, err := s.db.Exec(
		`INSERT INTO players(instance,name,x,y,level,hp,data) VALUES(?,?,?,?,?,?,?) `+
			`ON CONFLICT(instance) DO UPDATE SET name=excluded.name,x=excluded.x,y=excluded.y,`+
			`level=excluded.level,hp=excluded.hp,data=excluded.data`,
		key, key, st.X, st.Y, st.Level, st.HP, string(extraRaw)); err != nil {
		log.Printf("m5: save players %s: %v", key, err)
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM inventory WHERE player=?`, key); err != nil {
		log.Printf("m5: clear inventory %s: %v", key, err)
		return err
	}
	for i, sl := range st.Inv {
		if _, err := s.db.Exec(
			`INSERT INTO inventory(player,slot,item,count,enchantments) VALUES(?,?,?,?,?)`, key, i, sl.Key, sl.Count, sl.Ench); err != nil {
			log.Printf("m5: save inventory %s: %v", key, err)
			return err
		}
	}
	if _, err := s.db.Exec(`DELETE FROM bank WHERE player=?`, key); err != nil {
		log.Printf("m5: clear bank %s: %v", key, err)
		return err
	}
	for i, sl := range st.Bank {
		if _, err := s.db.Exec(
			`INSERT INTO bank(player,slot,item,count) VALUES(?,?,?,?)`, key, i, sl.Key, sl.Count); err != nil {
			log.Printf("m5: save bank %s: %v", key, err)
			return err
		}
	}
	if _, err := s.db.Exec(`DELETE FROM equipment WHERE player=?`, key); err != nil {
		log.Printf("m5: clear equipment %s: %v", key, err)
		return err
	}
	for t, e := range st.Equip {
		if e.Key == "" || e.Count < 1 {
			continue
		}
		if _, err := s.db.Exec(
			`INSERT INTO equipment(player,type,item,count) VALUES(?,?,?,?)`, key, t, e.Key, e.Count); err != nil {
			log.Printf("m5: save equipment %s: %v", key, err)
			return err
		}
	}
	if _, err := s.db.Exec(`DELETE FROM skills WHERE player=?`, key); err != nil {
		log.Printf("m5: clear skills %s: %v", key, err)
		return err
	}
	for id, sk := range st.Skills {
		if _, err := s.db.Exec(
			`INSERT INTO skills(player,skill,level,xp) VALUES(?,?,?,?)`, key, id, sk.Level, sk.XP); err != nil {
			log.Printf("m5: save skills %s: %v", key, err)
			return err
		}
	}
	log.Printf("m5: saved %s (pos %d,%d level %d inv %d skills %d)", key, st.X, st.Y, st.Level, len(st.Inv), len(st.Skills))
	return nil
}

// LoadPlayer reads one player's full row set back (players + inventory +
// bank + equipment + skills), verbatim SQL from the old root m5Load. The
// second return is false when the player row is missing. Equipment comes
// back dense by type index (nil when the table is missing or empty); the
// root pads it to the fixed equipment array. Like the old code, the
// players.data JSON column is read but intentionally ignored (equipment
// restores from the equipment table).
func (s *Store) LoadPlayer(key string) (State, bool) {
	if s == nil || s.db == nil || key == "" {
		return State{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var name string
	st := State{Skills: map[int]Skill{}}
	var data string
	err := s.db.QueryRow(
		`SELECT name,x,y,level,hp,data FROM players WHERE instance=?`, key,
	).Scan(&name, &st.X, &st.Y, &st.Level, &st.HP, &data)
	if err != nil {
		return State{}, false
	}
	rows, err := s.db.Query(`SELECT item,count,enchantments FROM inventory WHERE player=? ORDER BY slot`, key)
	if err != nil {
		return State{}, false
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var c int
		var ench string
		if err := rows.Scan(&k, &c, &ench); err != nil {
			continue
		}
		st.Inv = append(st.Inv, Slot{Key: k, Count: c, Ench: ench})
	}
	rows.Close()
	brows, err := s.db.Query(`SELECT item,count FROM bank WHERE player=? ORDER BY slot`, key)
	if err != nil {
		return State{}, false
	}
	for brows.Next() {
		var k string
		var c int
		if err := brows.Scan(&k, &c); err != nil {
			continue
		}
		st.Bank = append(st.Bank, Slot{Key: k, Count: c})
	}
	brows.Close()
	if erows, err := s.db.Query(`SELECT type,item,count FROM equipment WHERE player=?`, key); err == nil {
		byType := map[int]Slot{}
		maxT := -1
		for erows.Next() {
			var t int
			var k string
			var c int
			if err := erows.Scan(&t, &k, &c); err != nil {
				continue
			}
			if t < 0 {
				continue
			}
			byType[t] = Slot{Key: k, Count: c}
			if t > maxT {
				maxT = t
			}
		}
		erows.Close()
		if maxT >= 0 {
			st.Equip = make([]Slot, maxT+1)
			for t, sl := range byType {
				st.Equip[t] = sl
			}
		}
	} // else: pre-equipment DB, Equip stays nil (root normalizes)
	srows, err := s.db.Query(`SELECT skill,level,xp FROM skills WHERE player=?`, key)
	if err != nil {
		return State{}, false
	}
	defer srows.Close()
	for srows.Next() {
		var id, lv, xp int
		if err := srows.Scan(&id, &lv, &xp); err != nil {
			continue
		}
		st.Skills[id] = Skill{Level: lv, XP: xp}
	}
	return st, true
}

// FlushDirty writes every dirty player using snap to supply the
// authoritative in-memory state, then clears its flag (even when snap
// reports the key missing, matching the old flushDirty delete semantics).
// A nil snap only clears flags. Snapshots are taken with no Store lock
// held, so snap may safely take the owner's state lock.
func (s *Store) FlushDirty(snap func(key string) (State, bool)) {
	if s == nil {
		return
	}
	for _, key := range s.DirtyList() {
		var st State
		var ok bool
		if snap != nil {
			st, ok = snap(key)
		}
		if ok {
			_ = s.WritePlayer(key, st)
		}
		s.MarkClean(key)
	}
}

// Close performs a final FlushDirty(snap) and closes the DB handle.
func (s *Store) Close(snap func(key string) (State, bool)) error {
	if s == nil {
		return nil
	}
	s.FlushDirty(snap)
	return s.db.Close()
}
