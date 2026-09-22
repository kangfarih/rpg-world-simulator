package persist

import (
	"path/filepath"
	"strings"
	"testing"
)

// Fresh DBs are stamped with the current schema version; reopening is
// idempotent (no migrate, no refuse).
func TestSchemaVersionStamped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ver.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	v, ok := s.GetMeta(SchemaVersionKey)
	if !ok || v != "1" {
		t.Fatalf("schema_version = %q, %v; want \"1\", true", v, ok)
	}
	if err := s.Close(nil); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("re-Open stamped DB: %v", err)
	}
	defer s2.Close(nil)
	if v, ok := s2.GetMeta(SchemaVersionKey); !ok || v != "1" {
		t.Fatalf("schema_version after reopen = %q, %v; want \"1\", true", v, ok)
	}
}

// A DB newer than the binary refuses boot with explicit text (the caller
// turns this error into log.Fatalf — a stale binary never serves a schema
// it cannot understand).
func TestSchemaVersionRefusesNewer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "newer.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.SetMeta(SchemaVersionKey, "999"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	if err := s.Close(nil); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err = Open(path)
	if err == nil {
		t.Fatal("Open of newer-than-known schema must refuse boot, got nil error")
	}
	if !strings.Contains(err.Error(), "refusing boot") || !strings.Contains(err.Error(), "999") {
		t.Fatalf("refuse error = %q, want explicit refusing-boot text with versions", err)
	}
}

// An unreadable schema_version also refuses boot (explicit, never silent).
func TestSchemaVersionRefusesUnreadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "badver.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.SetMeta(SchemaVersionKey, "v-next"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	if err := s.Close(nil); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err = Open(path); err == nil {
		t.Fatal("Open of unreadable schema_version must refuse boot, got nil error")
	} else if !strings.Contains(err.Error(), "refusing boot") {
		t.Fatalf("refuse error = %q, want refusing-boot text", err)
	}
}
