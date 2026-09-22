package app

import (
	"testing"
	"time"
)

func getenvOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestListenAddr(t *testing.T) {
	if got := ListenAddr(""); got != "127.0.0.1:9001" {
		t.Fatalf("default = %q", got)
	}
	if got := ListenAddr("9156"); got != "127.0.0.1:9156" {
		t.Fatalf("port = %q", got)
	}
}

func TestFromEnv(t *testing.T) {
	cfg := FromEnv(getenvOf(nil), nil)
	if cfg.Addr != DefaultAddr || !cfg.Modes.Test || cfg.Modes.Clean || cfg.Modes.Combat {
		t.Fatalf("defaults = %+v", cfg)
	}
	if cfg.DummyHP != 5000 || cfg.DummyRespawn != 15*time.Second || cfg.ConsoleOff {
		t.Fatalf("tuning defaults = %+v", cfg)
	}
	cfg = FromEnv(getenvOf(map[string]string{
		"PORT": "9156", "DB_PATH": "/tmp/e9b.db", "CONSOLE": "0",
		"COMBAT": "1", "DUMMY_HP": "200", "DUMMY_RESPAWN": "3",
	}), nil)
	if cfg.Addr != "127.0.0.1:9156" || cfg.DBPath != "/tmp/e9b.db" || !cfg.ConsoleOff {
		t.Fatalf("overrides = %+v", cfg)
	}
	if !cfg.Modes.Combat || cfg.DummyHP != 200 || cfg.DummyRespawn != 3*time.Second {
		t.Fatalf("tuning overrides = %+v", cfg)
	}
}

func TestParseFallbacks(t *testing.T) {
	if ParseDummyHP("bogus") != 5000 || ParseDummyHP("-5") != 5000 {
		t.Fatal("bad DUMMY_HP must fall back")
	}
	if ParseDummyRespawn("bogus") != 15*time.Second {
		t.Fatal("bad DUMMY_RESPAWN must fall back")
	}
	if !ConsoleDisabled("0") || ConsoleDisabled("1") {
		t.Fatal("console gate mismatch")
	}
}

func TestBootOrderFrozen(t *testing.T) {
	steps := BootOrder()
	if len(steps) != 12 {
		t.Fatalf("boot steps = %d, want 12", len(steps))
	}
}
