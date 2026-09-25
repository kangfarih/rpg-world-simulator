package persist

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	_ "modernc.org/sqlite"
)

// Rank round-trips through the players row (offline /setrank durability).
func TestRankRoundTrip(t *testing.T) {
	s := openTestStore(t)
	want := testState()
	want.Rank = 2 // Modules.Ranks.Admin
	if err := s.WritePlayer("hero", want); err != nil {
		t.Fatalf("WritePlayer: %v", err)
	}
	got, ok := s.LoadPlayer("hero")
	if !ok {
		t.Fatalf("LoadPlayer(hero) = false, want true")
	}
	if got.Rank != 2 {
		t.Fatalf("Rank = %d, want 2", got.Rank)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LoadPlayer mismatch:\n got=%+v\nwant=%+v", got, want)
	}
}

// A v2 database (players table without the rank column, equipment table
// without the enchantments column, version stamp 2) migrates expand-only
// on open: the rank column appears with DEFAULT 0, the equipment
// enchantments column appears with DEFAULT '{}', existing rows read back
// Rank 0, and the version re-stamps to CurrentSchemaVersion.
func TestRankMigrationFromV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v2.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	for _, ddl := range []string{
		`CREATE TABLE players(instance TEXT PRIMARY KEY, name TEXT, x INT, y INT, level INT, hp INT, data TEXT)`,
		`CREATE TABLE inventory(player TEXT, slot INT, item TEXT, count INT, enchantments TEXT, PRIMARY KEY(player, slot))`,
		`CREATE TABLE bank(player TEXT, slot INT, item TEXT, count INT, PRIMARY KEY(player, slot))`,
		`CREATE TABLE equipment(player TEXT, type INT, item TEXT, count INT, PRIMARY KEY(player, type))`,
		`CREATE TABLE skills(player TEXT, skill INT, level INT, xp INT, PRIMARY KEY(player, skill))`,
		`CREATE TABLE statistics(player TEXT PRIMARY KEY, data TEXT)`,
		`CREATE TABLE meta(k TEXT PRIMARY KEY, v TEXT)`,
		`INSERT INTO meta(k,v) VALUES('schema_version','2')`,
		`INSERT INTO players(instance,name,x,y,level,hp,data) VALUES('v2hero','v2hero',100,96,1,100,'{}')`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			db.Close()
			t.Fatalf("v2 setup %q: %v", ddl, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("setup close: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open v2 DB: %v", err)
	}
	defer s.Close(nil)
	if v, _ := s.GetMeta(SchemaVersionKey); v != strconv.Itoa(CurrentSchemaVersion) {
		t.Fatalf("schema_version after migrate = %q, want %q", v, strconv.Itoa(CurrentSchemaVersion))
	}
	got, ok := s.LoadPlayer("v2hero")
	if !ok {
		t.Fatalf("LoadPlayer(v2hero) = false after migrate, want true")
	}
	if got.Rank != 0 {
		t.Fatalf("migrated Rank = %d, want 0 (column DEFAULT)", got.Rank)
	}
	// Rank writes stick on the migrated row and survive reopen.
	got.Rank = 1
	if err := s.WritePlayer("v2hero", got); err != nil {
		t.Fatalf("WritePlayer: %v", err)
	}
	if err := s.Close(nil); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer s2.Close(nil)
	back, ok := s2.LoadPlayer("v2hero")
	if !ok || back.Rank != 1 {
		t.Fatalf("Rank across reopen = %+v, %v; want Rank 1", back, ok)
	}
}
