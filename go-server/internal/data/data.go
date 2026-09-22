// Package data embeds the read-only game data tables (a byte-identical
// vendor copy of packages/server/data/** JSON) and serves them with a
// filesystem-first fallback chain plus a stable world.json content hash.
//
// Resolution order in ReadFile (behavior-frozen):
//  1. Explicit dev overrides: WORLD_JSON for "map/world.json", RES_<base>
//     for top-level "<base>.json" (trees, abilities, ...), RES_<dir> as a
//     directory override for "quests/*", "quest_bases/*", "crafting/*"
//     (RES_achievements still covers achievements.json). An override that is
//     set is authoritative: its error is returned, never masked by embed.
//  2. Filesystem candidates mirroring the existing loaders (worldPath,
//     resourceDataPath, DataDir, economyDataPath): ../, ../../ and
//     ../../../packages/server/data plus packages/server/data for a repo-root
//     cwd. In a dev checkout the files are present, so the bytes served are
//     exactly the bytes the pre-embed loaders served.
//  3. The embedded copy (single-binary deploys where ../packages is absent).
//
// Filesystem-first is deliberate: dev edits to packages/server/data take
// effect without re-vendoring, and the embed can never shadow a working
// checkout. The vendored copy under gamedata/ must be refreshed (plain copy)
// whenever upstream data changes; WorldHash detects drift at boot.
//
// WorldHash is the hex sha256 of the world.json bytes as resolved by
// ReadFile, computed once and cached. The boot gate (internal/server)
// compares it against the persisted meta.world_hash row.
package data

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

//go:embed gamedata/*.json gamedata/map/*.json gamedata/quests/*.json gamedata/quest_bases/*.json gamedata/crafting/*.json
var embedded embed.FS

// fsCandidates mirrors the relative-path search of the existing loaders for
// every supported caller cwd: go-server/ (server runs, e2e), a package dir
// two levels deep (most go tests), internal/data itself, or the repo root.
var fsCandidates = []string{
	filepath.Join("..", "packages", "server", "data"),
	filepath.Join("..", "..", "packages", "server", "data"),
	filepath.Join("..", "..", "..", "packages", "server", "data"),
	filepath.Join("packages", "server", "data"),
}

// envOverride maps a data name to the dev-override env convention the
// existing loaders use. It returns ("", false) when no override is set.
func envOverride(name string) (string, bool) {
	if name == "map/world.json" {
		if p := os.Getenv("WORLD_JSON"); p != "" {
			return p, true
		}
		return "", false
	}
	// Directory tables (quest/crafting dirs): RES_<dir> names a directory.
	if dir, file, ok := strings.Cut(name, "/"); ok && file != "" {
		switch dir {
		case "quests", "quest_bases", "crafting":
			if p := os.Getenv("RES_" + dir); p != "" {
				return filepath.Join(p, file), true
			}
			return "", false
		case "map":
			// Non-world map files have no env convention; fall through
			// to the filesystem/embed chain.
		default:
			return "", false
		}
		return "", false
	}
	// Top-level "<base>.json": RES_<base> names the file.
	if base, ok := strings.CutSuffix(name, ".json"); ok && !strings.Contains(base, "/") {
		if p := os.Getenv("RES_" + base); p != "" {
			return p, true
		}
	}
	return "", false
}

// cleanName validates a slash-separated data name ("trees.json",
// "map/world.json", "quests/tutorial.json") and rejects escapes.
func cleanName(name string) (string, error) {
	n := filepath.ToSlash(filepath.Clean("/" + name))[1:]
	if n == "" || n == "." || strings.HasPrefix(name, "/") || n != filepath.ToSlash(name) {
		return "", fmt.Errorf("data: invalid name %q", name)
	}
	for _, part := range strings.Split(n, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("data: invalid name %q", name)
		}
	}
	return n, nil
}

// ReadFile returns the bytes for a data name, resolving env overrides first,
// then the checkout filesystem, then the embedded copy. Embedded content is
// byte-identical to the vendored upstream files, so behavior is frozen when
// the files are unchanged.
func ReadFile(name string) ([]byte, error) {
	n, err := cleanName(name)
	if err != nil {
		return nil, err
	}
	if p, ok := envOverride(n); ok {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("data: read override %s (%s): %w", n, p, err)
		}
		return raw, nil
	}
	for _, dir := range fsCandidates {
		p := filepath.Join(dir, filepath.FromSlash(n))
		if _, err := os.Stat(p); err == nil {
			raw, err := os.ReadFile(p)
			if err != nil {
				return nil, fmt.Errorf("data: read %s: %w", p, err)
			}
			return raw, nil
		}
	}
	raw, err := embedded.ReadFile("gamedata/" + n)
	if err != nil {
		return nil, fmt.Errorf("data: %s not found on filesystem or in embed: %w", n, err)
	}
	return raw, nil
}

// EmbeddedRead is the embed-only reader (single-binary path probe and
// tests). Most callers want ReadFile.
func EmbeddedRead(name string) ([]byte, error) {
	n, err := cleanName(name)
	if err != nil {
		return nil, err
	}
	return embedded.ReadFile("gamedata/" + n)
}

var (
	worldHashOnce sync.Once
	worldHash     string
)

// WorldHash returns the hex sha256 of the resolved map/world.json bytes,
// computed once and stable across calls. It returns "" when world.json
// cannot be read (the map loader will fail boot with its own error).
func WorldHash() string {
	worldHashOnce.Do(func() {
		raw, err := ReadFile("map/world.json")
		if err != nil {
			return
		}
		sum := sha256.Sum256(raw)
		worldHash = hex.EncodeToString(sum[:])
	})
	return worldHash
}
