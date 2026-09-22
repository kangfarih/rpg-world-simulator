package persist

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Snapshot round-trip: write rows, VACUUM INTO a timestamped archive (no
// sqlite3 CLI), reopen the archive, same rows back.
func TestSnapshotRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvBackupDir, dir)
	src := filepath.Join(t.TempDir(), "src.db")
	s, err := Open(src)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := testState()
	if err := s.WritePlayer("hero", want); err != nil {
		t.Fatalf("WritePlayer: %v", err)
	}
	arch, err := s.SnapshotDB("")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !strings.HasPrefix(arch, dir) || !strings.HasSuffix(arch, ".db") {
		t.Fatalf("Snapshot path = %q, want timestamped .db under %q", arch, dir)
	}
	if err := s.Close(nil); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// The archive is a full DB copy: reopen through the same stack.
	r, err := Open(arch)
	if err != nil {
		t.Fatalf("re-Open archive: %v", err)
	}
	defer r.Close(nil)
	got, ok := r.LoadPlayer("hero")
	if !ok {
		t.Fatalf("LoadPlayer(hero) on archive = false, want true")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("archive LoadPlayer mismatch:\n got=%+v\nwant=%+v", got, want)
	}
}

// Package-level Snapshot(dbPath) snapshots a path without a Store handle.
func TestSnapshotPathFunc(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvBackupDir, dir)
	src := filepath.Join(t.TempDir(), "src.db")
	s, err := Open(src)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.WritePlayer("hero", testState()); err != nil {
		t.Fatalf("WritePlayer: %v", err)
	}
	arch, err := Snapshot(src)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if err := s.Close(nil); err != nil {
		t.Fatalf("Close: %v", err)
	}
	r, err := Open(arch)
	if err != nil {
		t.Fatalf("re-Open archive: %v", err)
	}
	defer r.Close(nil)
	if _, ok := r.LoadPlayer("hero"); !ok {
		t.Fatalf("LoadPlayer(hero) on archive = false, want true")
	}
}

// BACKUP_ON_BOOT=1 snapshots before EnsureSchema migrates (archive appears
// in BACKUP_DIR); default (unset) snapshots nothing.
func TestBackupOnBootHook(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvBackupDir, dir)
	t.Setenv(EnvBackupOnBoot, "1")
	src := filepath.Join(t.TempDir(), "hooked.db")
	s, err := Open(src)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = s.Close(nil)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".db") {
		t.Fatalf("BACKUP_DIR entries = %v, want one timestamped .db", entries)
	}
}

func TestBackupOnBootDefaultOff(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvBackupDir, dir)
	src := filepath.Join(t.TempDir(), "plain.db")
	s, err := Open(src)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = s.Close(nil)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("default boot wrote backups = %v, want none", entries)
	}
}

func TestBackupEnvParsing(t *testing.T) {
	if got := BackupDirFromEnv(getenvOfBackup(nil)); got != DefaultBackupDir {
		t.Fatalf("default dir = %q, want %q", got, DefaultBackupDir)
	}
	g := func(k string) string {
		if k == EnvBackupDir {
			return "/tmp/archives"
		}
		return ""
	}
	if got := BackupDirFromEnv(g); got != "/tmp/archives" {
		t.Fatalf("dir = %q", got)
	}
	if BackupOnBootFromEnv(getenvOfBackup(nil)) {
		t.Fatal("unset BACKUP_ON_BOOT must be off")
	}
	on := func(k string) string {
		if k == EnvBackupOnBoot {
			return "1"
		}
		return ""
	}
	if !BackupOnBootFromEnv(on) {
		t.Fatal("BACKUP_ON_BOOT=1 must be on")
	}
}

func getenvOfBackup(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}
