// Package controller holds stateful world orchestration over the pure
// internal/warps, internal/events and internal/globals packages.
//
// Warp orchestration mirrors packages/server/src/controllers/warps.ts
// (menu-driven warp(id): jailed/cooldown/level/quest/achievement gates,
// random landing tile in the target rect, Teleport delivery, `warps:*`
// notifies). This package owns the registry + gating table + cooldown
// clocks and computes gate outcomes; the root adapter (package main)
// owns transport (playerConn, send/broadcast) and implements WarpStore
// to supply jail/rank/level/quest/achievement reads.
//
// Transport-free: no websocket, no send/broadcast, no timers. The caller
// supplies nowMs clocks and the randIntn picker so tests stay
// deterministic.
package controller

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"rpg-world-server/internal/warps"
)

// WarpNames mirrors Modules.Warps order (modules.ts): the C->S Warp {id}
// indexes this enum; GetWarp lowercases the name to find the world.json
// entry.
var WarpNames = []string{
	"mudwich", "aynor", "lakesworld", "patsow", "crullfield", "undersea",
}

// WarpEntry is one world.json areas.warps entry with the menu-gating
// fields the geometry-only internal/warps package intentionally drops.
type WarpEntry struct {
	Name  string
	X, Y  int
	W, H  int
	Level int
	Quest string
	Ach   string
}

// WarpStore supplies the gate reads the stub owns (jail flags, ranks,
// levels, quests, achievements, display names). Implemented by the root
// adapter; kept as an interface so this package never imports main.
type WarpStore interface {
	IsJailed(username string) bool
	IsAdmin(username string) bool
	PlayerLevel(username string) int
	QuestFinished(username, quest string) bool
	AchievementDone(username, ach string) bool
	FormatName(name string) string
}

// WarpController owns the warp registry, the menu-gating table and the
// per-user cooldown clocks.
type WarpController struct {
	mu      sync.Mutex
	entries []WarpEntry
	reg     *warps.Registry
	last    map[string]int64
}

// NewWarpController returns an empty controller (Load populates it).
func NewWarpController() *WarpController {
	return &WarpController{last: map[string]int64{}}
}

// CooldownMs is the TS warpTimeout (warps.ts: 300s between warps).
// WORLD_WARP_COOLDOWN_MS overrides it (0 disables, for the e2e).
func CooldownMs() int64 {
	if v := os.Getenv("WORLD_WARP_COOLDOWN_MS"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			return n
		}
	}
	return 300_000
}

// Load loads the warp registry plus the menu-gating table from the same
// world.json path the map loader uses.
func (c *WarpController) Load(path string) error {
	reg, err := warps.Load(path)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc struct {
		Areas struct {
			Warps []struct {
				Name        string `json:"name"`
				X           int    `json:"x"`
				Y           int    `json:"y"`
				Width       int    `json:"width"`
				Height      int    `json:"height"`
				Level       int    `json:"level"`
				Quest       string `json:"quest"`
				Achievement string `json:"achievement"`
			} `json:"warps"`
		} `json:"areas"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	entries := make([]WarpEntry, 0, len(doc.Areas.Warps))
	for _, w := range doc.Areas.Warps {
		entries = append(entries, WarpEntry{
			Name: strings.ToLower(w.Name), X: w.X, Y: w.Y,
			W: w.Width, H: w.Height,
			Level: w.Level, Quest: w.Quest, Ach: w.Achievement,
		})
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reg = reg
	c.entries = entries
	return nil
}

// Count reports the number of loaded warps.
func (c *WarpController) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Loaded reports whether a registry has been loaded (Load success).
func (c *WarpController) Loaded() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reg != nil
}

// Names returns the loaded warp names in file order.
func (c *WarpController) Names() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.entries))
	for _, w := range c.entries {
		out = append(out, w.Name)
	}
	return out
}

// FindByID resolves a warp by Modules.Warps enum id (getWarp parity).
func (c *WarpController) FindByID(id int) *WarpEntry {
	if id < 0 || id >= len(WarpNames) {
		return nil
	}
	return c.FindByName(WarpNames[id])
}

// FindByName resolves a warp by (case-insensitive) name.
func (c *WarpController) FindByName(name string) *WarpEntry {
	key := strings.ToLower(strings.TrimSpace(name))
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.entries {
		if c.entries[i].Name == key {
			w := c.entries[i]
			return &w
		}
	}
	return nil
}

// At returns the first warp whose rect contains (x, y), or nil.
// Pure geometry lookup through the underlying registry.
func (c *WarpController) At(x, y int) *warps.Warp {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reg == nil {
		return nil
	}
	return c.reg.At(x, y)
}

// Authorize ports the controllers/warps.ts warp() gates: jail, cooldown,
// level, quest and achievement requirements. It returns the deny notify
// message ("" when allowed). Cooldown reads use nowMs; the clock advances
// only via Record on success, so callers record after applying side
// effects. Tutorial/combat gates are documented skips (no stub state).
func (c *WarpController) Authorize(username string, w *WarpEntry, nowMs int64, store WarpStore) string {
	if w == nil {
		return ""
	}
	if w.W <= 0 || w.H <= 0 {
		return ""
	}
	if store != nil && store.IsJailed(username) {
		return "warps:CANNOT_WARP_JAIL"
	}
	if cd := CooldownMs(); cd > 0 && (store == nil || !store.IsAdmin(username)) {
		c.mu.Lock()
		last := c.last[username]
		c.mu.Unlock()
		if nowMs-last < cd {
			left := cd - (nowMs - last)
			dur := fmt.Sprintf("%d seconds", left/1000)
			if left > 60_000 {
				dur = fmt.Sprintf("%d minutes", (left+59_999)/60_000)
			}
			return "warps:CANNOT_WARP_COOLDOWN;time=" + dur
		}
	}
	if w.Level > 0 {
		lvl := 0
		if store != nil {
			lvl = store.PlayerLevel(username)
		}
		if lvl < w.Level {
			return "warps:CANNOT_WARP_LEVEL;level=" + strconv.Itoa(w.Level)
		}
	}
	if w.Quest != "" {
		done := false
		if store != nil {
			done = store.QuestFinished(username, w.Quest)
		}
		if !done {
			name := w.Name
			if store != nil {
				name = store.FormatName(w.Name)
			}
			return "warps:CANNOT_WARP_QUEST;questName=" + w.Quest + ";name=" + name
		}
	}
	if w.Ach != "" {
		done := false
		if store != nil {
			done = store.AchievementDone(username, w.Ach)
		}
		if !done {
			return "warps:CANNOT_WARP_ACHIEVEMENT"
		}
	}
	return ""
}

// Landing picks the random landing tile in the target rect
// (Utils.randomInt(x, x+width-1) parity). randIntn is rand.Intn;
// a nil picker falls back to the origin corner. Reports false when the
// rect is degenerate.
func Landing(w *WarpEntry, randIntn func(int) int) (int, int, bool) {
	if w == nil || w.W <= 0 || w.H <= 0 {
		return 0, 0, false
	}
	if randIntn == nil {
		return w.X, w.Y, true
	}
	return w.X + randIntn(w.W), w.Y + randIntn(w.H), true
}

// Record advances the per-user warp clock (called on success after the
// caller applies teleport side effects).
func (c *WarpController) Record(username string, nowMs int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last == nil {
		c.last = map[string]int64{}
	}
	c.last[username] = nowMs
}
