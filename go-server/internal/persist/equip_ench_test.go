package persist

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	_ "modernc.org/sqlite"
)

// Enchanted equipment round-trips through the equipment row (relogin
// durability for Bloodsucking/Critical/Thorns gear).
func TestEquipmentEnchantmentsRoundTrip(t *testing.T) {
	s := openTestStore(t)
	equip := make([]Slot, 5)
	equip[0] = Slot{Key: "ironhelmet", Count: 1, Ench: `{"2":{"level":5}}`} // Thorns
	equip[4] = Slot{Key: "ironsword", Count: 1, Ench: `{"0":{"level":3}}`}  // Bloodsucking
	want := testState()
	want.Equip = equip
	if err := s.WritePlayer("hero", want); err != nil {
		t.Fatalf("WritePlayer: %v", err)
	}
	got, ok := s.LoadPlayer("hero")
	if !ok {
		t.Fatalf("LoadPlayer(hero) = false, want true")
	}
	if !reflect.DeepEqual(got.Equip, want.Equip) {
		t.Fatalf("Equip mismatch:\n got=%+v\nwant=%+v", got.Equip, want.Equip)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LoadPlayer mismatch:\n got=%+v\nwant=%+v", got, want)
	}
}

// A v3 database (equipment table without the enchantments column, version
// stamp 3) migrates expand-only on open: the column appears with DEFAULT
// '{}', pre-existing gear rows read back Ench '{}', new enchanted writes
// stick, and the version re-stamps to CurrentSchemaVersion.
func TestEquipmentEnchantmentsMigrationFromV3(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v3.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	for _, ddl := range []string{
		`CREATE TABLE players(instance TEXT PRIMARY KEY, name TEXT, x INT, y INT, level INT, hp INT, data TEXT, rank INT DEFAULT 0)`,
		`CREATE TABLE inventory(player TEXT, slot INT, item TEXT, count INT, enchantments TEXT, PRIMARY KEY(player, slot))`,
		`CREATE TABLE bank(player TEXT, slot INT, item TEXT, count INT, PRIMARY KEY(player, slot))`,
		`CREATE TABLE equipment(player TEXT, type INT, item TEXT, count INT, PRIMARY KEY(player, type))`,
		`CREATE TABLE skills(player TEXT, skill INT, level INT, xp INT, PRIMARY KEY(player, skill))`,
		`CREATE TABLE statistics(player TEXT PRIMARY KEY, data TEXT)`,
		`CREATE TABLE meta(k TEXT PRIMARY KEY, v TEXT)`,
		`INSERT INTO meta(k,v) VALUES('schema_version','3')`,
		`INSERT INTO players(instance,name,x,y,level,hp,data,rank) VALUES('v3hero','v3hero',100,96,1,100,'{}',0)`,
		`INSERT INTO equipment(player,type,item,count) VALUES('v3hero',4,'ironsword',1)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			db.Close()
			t.Fatalf("v3 setup %q: %v", ddl, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("setup close: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open v3 DB: %v", err)
	}
	defer s.Close(nil)
	if v, _ := s.GetMeta(SchemaVersionKey); v != strconv.Itoa(CurrentSchemaVersion) {
		t.Fatalf("schema_version after migrate = %q, want %q", v, strconv.Itoa(CurrentSchemaVersion))
	}
	got, ok := s.LoadPlayer("v3hero")
	if !ok {
		t.Fatalf("LoadPlayer(v3hero) = false after migrate, want true")
	}
	if len(got.Equip) != 5 || got.Equip[4].Key != "ironsword" {
		t.Fatalf("migrated Equip = %+v, want ironsword at type 4", got.Equip)
	}
	// Enchanted writes stick on the migrated row and survive reopen.
	got.Equip[4].Ench = `{"1":{"level":2}}`
	if err := s.WritePlayer("v3hero", got); err != nil {
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
	back, ok := s2.LoadPlayer("v3hero")
	if !ok || len(back.Equip) != 5 || back.Equip[4].Ench != `{"1":{"level":2}}` {
		t.Fatalf("Ench across reopen = %+v, %v; want {\"1\":{\"level\":2}}", back.Equip, ok)
	}
}
