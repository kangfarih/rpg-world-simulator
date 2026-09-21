package abilities

import (
	"os"
	"path/filepath"
	"testing"
)

// realData resolves the canonical TS data file relative to this package:
// go-server/internal/abilities -> rpg-world-sim/packages/server/data.
func realData(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "packages", "server", "data", "abilities.json")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("abilities.json not present at %s: %v", path, err)
	}
	return path
}

func loadReal(t *testing.T) *Registry {
	t.Helper()
	r, err := Load(realData(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return r
}

// The TS tree ships 8 ability keys (impl/index.ts + abilities.json);
// there is no 9th ability, so the registry must load exactly those 8.
func TestRegistryLoadsAllAbilities(t *testing.T) {
	r := loadReal(t)
	want := []string{
		"awareness", "dualistsmark", "hotshot", "intimidate",
		"precognition", "run", "secretcalling", "thickskin",
	}
	if got := r.Count(); got != len(want) {
		t.Fatalf("Count = %d, want %d (keys %v)", got, len(want), r.Keys())
	}
	for _, key := range want {
		if _, ok := r.ForKey(key, 1); !ok {
			t.Errorf("ForKey(%q, 1) missing", key)
		}
	}
}

func TestForKeyLevels(t *testing.T) {
	r := loadReal(t)

	// intimidate.json rows: L1 {60s cd, 15s dur, 15 mana}, L4 {60s, 30s, 21}.
	a, ok := r.ForKey("intimidate", 1)
	if !ok {
		t.Fatal("ForKey(intimidate, 1) missing")
	}
	if a != (Ability{Key: "intimidate", Name: "intimidate", Level: 1, ManaCost: 15, CooldownMs: 60000, DurationMs: 15000}) {
		t.Errorf("intimidate L1 = %+v", a)
	}
	a4, _ := r.ForKey("intimidate", 4)
	if a4.ManaCost != 21 || a4.CooldownMs != 60000 || a4.DurationMs != 30000 {
		t.Errorf("intimidate L4 = %+v", a4)
	}

	// run durations scale 15/17/19/22s at flat 15 mana.
	durs := []int{15000, 17000, 19000, 22000}
	for lvl, want := range durs {
		a, _ := r.ForKey("run", lvl+1)
		if a.DurationMs != want || a.ManaCost != 15 || a.CooldownMs != 60000 {
			t.Errorf("run L%d = %+v, want dur %d", lvl+1, a, want)
		}
	}

	// Passive abilities resolve with zero costs (no level rows).
	for _, key := range []string{"awareness", "precognition"} {
		a, ok := r.ForKey(key, 1)
		if !ok {
			t.Errorf("ForKey(%q) missing", key)
			continue
		}
		if a.ManaCost != 0 || a.CooldownMs != 0 || a.DurationMs != 0 {
			t.Errorf("%s passive = %+v, want zero costs", key, a)
		}
		if r.IsActive(key) {
			t.Errorf("IsActive(%q) = true, want false (passive)", key)
		}
	}

	// Level clamping mirrors Ability.setLevel (1-4).
	lo, _ := r.ForKey("run", 0)
	if lo.Level != 1 {
		t.Errorf("level 0 clamped to %d, want 1", lo.Level)
	}
	hi, _ := r.ForKey("run", 9)
	if hi.Level != 4 || hi.DurationMs != 22000 {
		t.Errorf("level 9 = %+v, want L4", hi)
	}

	if _, ok := r.ForKey("ghost", 1); ok {
		t.Error("ForKey(ghost) = true, want false")
	}
}

func TestCanCast(t *testing.T) {
	r := loadReal(t)
	a, _ := r.ForKey("intimidate", 1) // 15 mana, 60s cooldown.

	if !a.CanCast(15, 60000, 0) {
		t.Error("fresh cast with exact mana should pass")
	}
	if a.CanCast(14, 60000, 0) {
		t.Error("insufficient mana should fail")
	}
	if a.CanCast(100, 59999, 0) {
		t.Error("cast inside cooldown window should fail")
	}
	if !a.CanCast(100, 60000, 0) {
		t.Error("cast at exact cooldown boundary should pass")
	}
	if !a.CanCast(100, 1_000_000, 940_000) {
		t.Error("cast after cooldown elapsed should pass")
	}
}

func TestEffects(t *testing.T) {
	r := loadReal(t)

	cases := []struct {
		key   string
		level int
		kind  EffectKind
		power float64
		dur   int
	}{
		{"run", 1, EffectRunning, 0.9, 15000},
		{"run", 4, EffectRunning, 0.9, 22000},
		{"dualistsmark", 2, EffectDualistsMark, -200, 15000},
		{"thickskin", 3, EffectThickSkin, -0.2, 15000},
		{"intimidate", 1, EffectNone, 0, 15000},
		{"intimidate", 4, EffectNone, 0, 30000},
		{"hotshot", 1, EffectNone, 0, 15000},
		{"secretcalling", 1, EffectNone, 0, 15000},
		{"awareness", 1, EffectNone, 0, 0},
		{"precognition", 1, EffectNone, 0, 0},
	}
	for _, c := range cases {
		e, ok := r.Effect(c.key, c.level)
		if !ok {
			t.Errorf("Effect(%q) missing", c.key)
			continue
		}
		if e != (Effect{Kind: c.kind, Power: c.power, DurationMs: c.dur}) {
			t.Errorf("Effect(%s L%d) = %+v", c.key, c.level, e)
		}
	}

	// Active/passive split mirrors RawAbilityData.type.
	for _, key := range []string{"intimidate", "run", "dualistsmark", "hotshot", "thickskin", "secretcalling"} {
		if !r.IsActive(key) {
			t.Errorf("IsActive(%q) = false, want true", key)
		}
	}

	// hasTarget gates from the TS impls (precognition's gate is
	// unreachable in TS — passive — but present in its activate()).
	for _, key := range []string{"hotshot", "intimidate", "precognition", "secretcalling"} {
		if !r.RequiresTarget(key) {
			t.Errorf("RequiresTarget(%q) = false, want true", key)
		}
	}
	for _, key := range []string{"run", "dualistsmark", "thickskin", "awareness"} {
		if r.RequiresTarget(key) {
			t.Errorf("RequiresTarget(%q) = true, want false", key)
		}
	}

	if _, ok := r.Effect("ghost", 1); ok {
		t.Error("Effect(ghost) = true, want false")
	}
}

func TestLoadErrors(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("Load(missing) = nil, want error")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bad); err == nil {
		t.Error("Load(malformed) = nil, want error")
	}
	empty := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(empty, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(empty); err == nil {
		t.Error("Load(empty) = nil, want error")
	}
}
