// M12 slice — trade, crafting, enchanting (finishes the M6 economy half).
//
// Thin adapter over internal/controller (behavior-frozen move, task E1a):
// all trade/craft/enchant logic lives in the controller package operating on
// the Conn/Store/Bus/Peers seams below. This file only wires those seams to
// the root globals (players map, send, pstates, m5/m6 helpers) and keeps the
// entry points main.go/m6.go/m7.go call — with UNCHANGED signatures —
// delegating to the controller. Packet shapes and DB schema are identical.
//
// Re-homed: m6RemoveItemAt (defined here despite its m6* name) now delegates
// to controller.RemoveItemAt.
package main

import (
	"rpg-world-server/internal/controller"
	gnet "rpg-world-server/internal/net"
	worldcore "rpg-world-server/internal/world"
)

// Type aliases so existing names keep resolving to the moved types.
type (
	m12PlayerState      = controller.PlayerState
	m12Trade            = controller.Trade
	m12OfferedItem      = controller.OfferedItem
	m12CraftItem        = controller.CraftItem
	m12CraftPreview     = controller.CraftPreview
	m12CraftRequirement = controller.CraftRequirement
)

// ---------------------------------------------------------------------------
// controller.Conn seam (*playerConn satisfies it, so identity is preserved).
// ---------------------------------------------------------------------------

func (c *playerConn) InstanceID() string { return c.Instance }
func (c *playerConn) PlayerName() string { return c.Username }
func (c *playerConn) TileX() int         { return c.Sess.PlayerX }
func (c *playerConn) TileY() int         { return c.Sess.PlayerY }

// GrantContainerAccess sets canAccessContainer (enchanter NPC branch).
func (c *playerConn) GrantContainerAccess() { c.canAccessContainer = true }

// m12conn unwraps the controller.Conn back to the root conn (frame-emitting
// store ops need it); falls back to an instance lookup for foreign impls.
func m12conn(c controller.Conn) *playerConn {
	if pc, ok := c.(*playerConn); ok {
		return pc
	}
	if c == nil {
		return nil
	}
	pc, _ := worldcore.Find[*playerConn](c.InstanceID())
	return pc
}

// ---------------------------------------------------------------------------
// controller.Store seam (m5StateFor/m5AddItem/markDirty + m6 item helpers).
// ---------------------------------------------------------------------------

type m12store struct{}

func (m12store) SlotAt(username string, index int) (controller.Slot, bool) {
	st := m5StateFor(username)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	if index < 0 || index >= len(st.Inv) {
		return controller.Slot{}, false
	}
	s := st.Inv[index]
	return controller.Slot{Key: s.Key, Count: s.Count, Ench: s.Ench}, true
}

func (m12store) InventoryLen(username string) int {
	st := m5StateFor(username)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	return len(st.Inv)
}

func (m12store) CountItem(username, itemKey string) int {
	st := m5StateFor(username)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	n := 0
	for _, s := range st.Inv {
		if s.Key == itemKey {
			n += s.Count
		}
	}
	return n
}

func (m12store) AddItem(username, itemKey string, count int) int {
	return m5AddItem(username, itemKey, count)
}

func (m12store) AddItemEnch(username, itemKey string, count int, ench Enchantments) int {
	return m5AddItemEnch(username, itemKey, count, ench)
}

func (m12store) RemoveItem(username, itemKey string, count int) {
	m6RemoveItem(username, itemKey, count)
}

func (m12store) RemoveItemAt(c controller.Conn, username string, index, count int) {
	m6InventoryRemoveAt(m12conn(c), username, index, count)
}

func (m12store) SetSlotEnchantments(username string, index int, ench Enchantments) (int, bool) {
	st := m5StateFor(username)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	if index < 0 || index >= len(st.Inv) {
		return 0, false
	}
	st.Inv[index].Ench = ench
	return st.Inv[index].Count, true
}

func (m12store) SkillLevel(username string, skill int) int {
	st := m5StateFor(username)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	if s := st.Skills[skill]; s != nil {
		return s.Level
	}
	return 1
}

func (m12store) AddXP(c controller.Conn, skill, amount int) int {
	return m5AddXP(m12conn(c), c.PlayerName(), skill, amount)
}

func (m12store) MarkDirty(username string) { markDirty(username) }

func (m12store) SeedItem(username, itemKey string, count int) int {
	return m6SeedItem(username, itemKey, count)
}

func (m12store) ClearInventoryButGold(username string) {
	st := m5StateFor(username)
	pstateMu.Lock()
	out := st.Inv[:0]
	for _, s := range st.Inv {
		if s.Key == "gold" {
			out = append(out, s)
		}
	}
	st.Inv = out
	pstateMu.Unlock()
	markDirty(username)
}

func (m12store) ItemUndroppable(key string) bool {
	info := m6ItemInfoFor(key)
	return info != nil && info.Undroppable
}

func (m12store) ItemName(key string) string { return m6ItemName(key) }

func (m12store) ItemType(key string) (string, bool) {
	info := m6ItemInfoFor(key)
	if info == nil {
		return "", false
	}
	return info.Type, true
}

func (m12store) MaxStack(key string) int { return m6MaxStack(key) }

// ---------------------------------------------------------------------------
// controller.Bus seam (send unicast + notify) and controller.Peers seam.
// ---------------------------------------------------------------------------

type m12bus struct{}

func (m12bus) SendTo(instance string, frames ...[]any) {
	c, _ := worldcore.Find[*playerConn](instance)
	if c == nil {
		return
	}
	_ = gnet.Send(c.Conn, frames...)
}

func (m12bus) Notify(instance string, message string) {
	c, _ := worldcore.Find[*playerConn](instance)
	if c == nil {
		return
	}
	m6Notify(c, message)
}

type m12peers struct{}

func (m12peers) ByInstance(instance string) (controller.Conn, bool) {
	c, _ := worldcore.Find[*playerConn](instance)
	if c == nil {
		return nil, false
	}
	return c, true
}

// connByUsername finds the live connection for a username (trade peers are
// players, so usernames are unique among connections here).
func (m12peers) ByUsername(username string) (controller.Conn, bool) {
	for _, c := range worldcore.AllOf[*playerConn]() {
		if c.Username == username {
			return c, true
		}
	}
	return nil, false
}

// m12deps wires the controller seams to the root globals.
func m12deps() controller.Deps {
	return controller.Deps{Store: m12store{}, Bus: m12bus{}, Peers: m12peers{}}
}

// ---------------------------------------------------------------------------
// Entry points (signatures UNCHANGED; main.go/m6.go/m7.go call sites compile
// as-is). The testMode gate stays here — it reads a root global.
// ---------------------------------------------------------------------------

// m12HandleTrade ports incoming.handleTrade's opcode switch.
func m12HandleTrade(c *playerConn, data []byte) {
	controller.HandleTrade(c, data, m12deps())
}

// m12HandleEnchant ports incoming.handleEnchant (Select/Confirm; the TS
// handler has NO canAccessContainer gate — the client only opens the menu
// from the enchanter NPC; server checks live inside the enchanter itself).
func m12HandleEnchant(c *playerConn, data []byte) {
	controller.HandleEnchant(c, data, m12deps())
}

// m12HandleCrafting ports incoming.handleCrafting (activeCraftingInterface
// gate + Select/Craft opcodes).
func m12HandleCrafting(c *playerConn, data []byte) {
	controller.HandleCrafting(c, data, m12deps())
}

// m12HandleTest serves the e2e harness: state probes + seeding.
func m12HandleTest(c *playerConn, data []byte) {
	if !testMode {
		return
	}
	controller.HandleTest(c, data, m12deps())
}

// m12ClearSession drops both sides' session state.
func m12ClearSession(me, peer *playerConn) {
	var p controller.Conn
	if peer != nil {
		p = peer
	}
	controller.ClearSession(me, p, m12deps())
}

// m12ForgetSession drops trade state when a connection leaves (world.ts
// clearActiveTrade parity happens via close; stale map entries are dropped).
func m12ForgetSession(key string) {
	controller.ForgetSession(key)
}

// m12OpenEnchanter ports the handler.ts enchanter NPC branch: container
// access + NPC Enchant [31,3].
func m12OpenEnchanter(c *playerConn) {
	controller.OpenEnchanter(c, m12deps())
}

// m6RemoveItemAt removes count from one inventory slot by index (enchanter
// uses the shard's slot index directly). Emits Container Remove.
func m6RemoveItemAt(c *playerConn, username string, index, count int) {
	controller.RemoveItemAt(c, username, index, count, m12deps())
}

// m12PlayerCommands ports the crafting interface open commands
// (commands.ts:1102-1121, names opencrafting/openalchemy/opencooking/
// opensmithing/opensmelting). Called from m7ParseCommand.
func m12PlayerCommands(c *playerConn, command string) {
	controller.PlayerCommands(c, command, m12deps())
}
