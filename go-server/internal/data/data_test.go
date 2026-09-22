package data

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// sampleNames covers every embedded layout: top-level tables, map,
// quests, quest_bases and crafting.
var sampleNames = []string{
	"map/world.json",
	"trees.json",
	"rocks.json",
	"fishing.json",
	"foraging.json",
	"tables.json",
	"mobs.json",
	"items.json",
	"spawns.json",
	"npcs.json",
	"stores.json",
	"abilities.json",
	"achievements.json",
	"minigames.json",
	"effectentities.json",
	"quests/tutorial.json",
	"quest_bases/codedmessage.json",
	"crafting/cooking.json",
}

func TestEmbedPresent(t *testing.T) {
	raw, err := EmbeddedRead("map/world.json")
	if err != nil {
		t.Fatalf("EmbeddedRead(map/world.json): %v", err)
	}
	if len(raw) == 0 {
		t.Fatalf("embedded world.json is empty")
	}
	for _, n := range sampleNames[1:] {
		if _, err := EmbeddedRead(n); err != nil {
			t.Fatalf("EmbeddedRead(%s): %v", n, err)
		}
	}
}

func TestReadFileByteIdentical(t *testing.T) {
	upstream := filepath.Join("..", "..", "..", "packages", "server", "data")
	if _, err := os.Stat(upstream); err != nil {
		t.Skipf("upstream checkout absent: %v", err)
	}
	for _, n := range sampleNames {
		want, err := os.ReadFile(filepath.Join(upstream, filepath.FromSlash(n)))
		if err != nil {
			t.Fatalf("read upstream %s: %v", n, err)
		}
		got, err := ReadFile(n)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", n, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("ReadFile(%s) differs from upstream (%d vs %d bytes)", n, len(got), len(want))
		}
	}
}

func TestWorldHashStable(t *testing.T) {
	h1, h2 := WorldHash(), WorldHash()
	if h1 == "" {
		t.Fatalf("WorldHash() is empty")
	}
	if h1 != h2 {
		t.Fatalf("WorldHash unstable: %s vs %s", h1, h2)
	}
	if len(h1) != 64 {
		t.Fatalf("WorldHash length = %d, want 64", len(h1))
	}
	raw, err := ReadFile("map/world.json")
	if err != nil {
		t.Fatalf("ReadFile(map/world.json): %v", err)
	}
	sum := sha256.Sum256(raw)
	if h1 != hex.EncodeToString(sum[:]) {
		t.Fatalf("WorldHash %s != sha256(world.json) %s", h1, hex.EncodeToString(sum[:]))
	}
}

func TestReadFileMissing(t *testing.T) {
	if _, err := ReadFile("nope/missing.json"); err == nil {
		t.Fatalf("ReadFile(missing) = nil error, want error")
	}
	if _, err := ReadFile("../escape.json"); err == nil {
		t.Fatalf("ReadFile(escape) = nil error, want error")
	}
}

func TestEnvOverrideWins(t *testing.T) {
	dir := t.TempDir()
	dev := filepath.Join(dir, "trees.json")
	if err := os.WriteFile(dev, []byte(`{"dev":true}`), 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}
	t.Setenv("RES_trees", dev)
	got, err := ReadFile("trees.json")
	if err != nil {
		t.Fatalf("ReadFile with RES_trees: %v", err)
	}
	if !bytes.Equal(got, []byte(`{"dev":true}`)) {
		t.Fatalf("override bytes = %q, want dev content", got)
	}

	world := filepath.Join(dir, "world.json")
	if err := os.WriteFile(world, []byte(`{"w":1}`), 0o644); err != nil {
		t.Fatalf("write world override: %v", err)
	}
	t.Setenv("WORLD_JSON", world)
	got, err = ReadFile("map/world.json")
	if err != nil {
		t.Fatalf("ReadFile with WORLD_JSON: %v", err)
	}
	if !bytes.Equal(got, []byte(`{"w":1}`)) {
		t.Fatalf("world override bytes = %q, want dev content", got)
	}
}
