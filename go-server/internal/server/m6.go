// M6 slice: stores + bank + NPC talk.
//
// Thin adapter over internal/controller (behavior-frozen move, task E6):
// all stores/bank/NPC-talk/container/equipment logic lives in the controller
// package operating on the EconomyConn/EconomyStore/EconomyBus/EconomyPeers/
// QuestTalk/PetHooks/WorldLookup seams below. This file only wires those
// seams to the root globals (players map, send/broadcast, pstates, m5/m11/
// pets hooks) and keeps the entry points main.go/m11/m12/m13 call — with
// UNCHANGED signatures — delegating to the controller. Packet shapes, prices,
// roll logic and tick cadence are identical.
package server

import (
	"encoding/json"

	"rpg-world-server/internal/controller"
	worldcore "rpg-world-server/internal/world"
)

// Type aliases so existing names keep resolving to the moved types.
type (
	m6ItemInfo  = controller.ItemInfo
	m6StoreItem = controller.EconStoreItem
	m6Store     = controller.EconStore
	m6NPCInfo   = controller.NPCInfo
)

// Registry mirrors for direct map readers (m13TestItems/m13FindNPC parity).
// The authoritative registries live in the controller; these copies are
// synced on load so external call sites compile untouched.
var (
	m6Items  = map[string]*m6ItemInfo{}
	m6NPCs   = map[string]*m6NPCInfo{}
	m6NPCsOK bool
)

// ---------------------------------------------------------------------------
// controller.EconomyConn seam (*playerConn satisfies it).
// ---------------------------------------------------------------------------

func (c *playerConn) StoreOpen() string {
	if c == nil {
		return ""
	}
	c.sessMu.RLock()
	defer c.sessMu.RUnlock()
	return c.storeOpen
}
func (c *playerConn) SetStoreOpen(s string) {
	if c == nil {
		return
	}
	c.sessMu.Lock()
	defer c.sessMu.Unlock()
	c.storeOpen = s
}
func (c *playerConn) CanAccess() bool {
	if c == nil {
		return false
	}
	c.sessMu.RLock()
	defer c.sessMu.RUnlock()
	return c.canAccessContainer
}
func (c *playerConn) SetCanAccess(b bool) {
	if c == nil {
		return
	}
	c.sessMu.Lock()
	defer c.sessMu.Unlock()
	c.canAccessContainer = b
}
func (c *playerConn) TalkKey() string {
	if c == nil {
		return ""
	}
	c.sessMu.RLock()
	defer c.sessMu.RUnlock()
	return c.talkNPC
}
func (c *playerConn) SetTalkKey(s string) {
	if c == nil {
		return
	}
	c.sessMu.Lock()
	defer c.sessMu.Unlock()
	c.talkNPC = s
}
func (c *playerConn) TalkIndex() int {
	if c == nil {
		return 0
	}
	c.sessMu.RLock()
	defer c.sessMu.RUnlock()
	return c.talkIndex
}
func (c *playerConn) SetTalkIndex(i int) {
	if c == nil {
		return
	}
	c.sessMu.Lock()
	defer c.sessMu.Unlock()
	c.talkIndex = i
}

// withTalk runs fn with the talk cursor held under the session lock so
// read-modify-write cursor updates (world sign TalkWith parity) stay atomic
// with the concurrent store-ticker reads above.
func (c *playerConn) withTalk(fn func(npc *string, idx *int)) {
	c.sessMu.Lock()
	defer c.sessMu.Unlock()
	fn(&c.talkNPC, &c.talkIndex)
}

// resetTalk clears the talk cursor (sign debug path parity).
func (c *playerConn) resetTalk() {
	c.sessMu.Lock()
	defer c.sessMu.Unlock()
	c.talkNPC = ""
	c.talkIndex = 0
}

// ---------------------------------------------------------------------------
// controller.EconomyStore seam (m5StateFor/pstateMu + m5 helpers).
// Store methods reuse m12store; bank/equip/total-level extend it.
// ---------------------------------------------------------------------------

type m6store struct{ m12store }

func (m6store) InventorySlots(username string) []controller.Slot {
	st := m5StateFor(username)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	out := make([]controller.Slot, len(st.Inv))
	for i, s := range st.Inv {
		out[i] = controller.Slot{Key: s.Key, Count: s.Count, Ench: s.Ench}
	}
	return out
}

func (m6store) SetInventory(username string, slots []controller.Slot) {
	st := m5StateFor(username)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	out := make([]m5Slot, len(slots))
	for i, s := range slots {
		out[i] = m5Slot{Key: s.Key, Count: s.Count, Ench: s.Ench}
	}
	st.Inv = out
}

func (m6store) BankSlots(username string) []controller.Slot {
	st := m5StateFor(username)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	out := make([]controller.Slot, len(st.Bank))
	for i, s := range st.Bank {
		out[i] = controller.Slot{Key: s.Key, Count: s.Count, Ench: s.Ench}
	}
	return out
}

func (m6store) SetBank(username string, slots []controller.Slot) {
	st := m5StateFor(username)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	out := make([]m5Slot, len(slots))
	for i, s := range slots {
		out[i] = m5Slot{Key: s.Key, Count: s.Count, Ench: s.Ench}
	}
	st.Bank = out
}

func (m6store) EquipSlot(username string, slotType int) (controller.Slot, bool) {
	st := m5StateFor(username)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	if slotType < 0 || slotType >= len(st.Equip) {
		return controller.Slot{}, false
	}
	s := st.Equip[slotType]
	return controller.Slot{Key: s.Key, Count: s.Count, Ench: s.Ench}, true
}

func (m6store) SetEquip(username string, slotType int, slot controller.Slot) {
	st := m5StateFor(username)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	if slotType < 0 || slotType >= len(st.Equip) {
		return
	}
	st.Equip[slotType] = m5Slot{Key: slot.Key, Count: slot.Count, Ench: slot.Ench}
}

func (m6store) EquipLen(username string) int {
	st := m5StateFor(username)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	return len(st.Equip)
}

func (m6store) TotalLevel(username string) int {
	st := m5StateFor(username)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	return st.Level
}

// ---------------------------------------------------------------------------
// controller.EconomyBus / EconomyPeers seams.
// ---------------------------------------------------------------------------

type m6bus struct{ m12bus }

func (m6bus) Broadcast(frames ...[]any) { worldcore.Broadcast(frames...) }

type m6peers struct{ m12peers }

func (m6peers) WithStoreOpen(key string) []controller.EconomyConn {
	var out []controller.EconomyConn
	for _, c := range worldcore.AllOf[*playerConn]() {
		// StoreOpen() takes the session read lock: the 20s ticker reads
		// off-goroutine while conn goroutines write via SetStoreOpen
		// (controller ClearAccess/OpenStore path). Never read c.storeOpen
		// directly here (-race under e2e/m6 load).
		if c.StoreOpen() == key {
			out = append(out, c)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// controller.QuestTalk / PetHooks / WorldLookup seams.
// ---------------------------------------------------------------------------

type m6quests struct{}

func (m6quests) Talk(c controller.EconomyConn, npcKey string) bool {
	return m11Talk(m12conn(c), npcKey)
}

type m6pets struct{}

func (m6pets) DropKey(c controller.EconomyConn, index int) (string, string, bool) {
	return petDropKey(m12conn(c), index)
}
func (m6pets) HasOwner(instance string) bool { return petHasOwner(instance) }
func (m6pets) Grant(c controller.EconomyConn, mob, item string) {
	petGrant(m12conn(c), mob, item)
}

type m6world struct{}

func (m6world) EntityPos(instance string) (int, int, bool) {
	return worldcore.EntityPos(instance)
}
func (m6world) SpawnNPCKey(instance string) (string, bool) {
	payload, ok := spawnPayload(instance)
	if !ok {
		return "", false
	}
	var probe struct {
		Type int    `json:"type"`
		Key  string `json:"key"`
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", false
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "", false
	}
	if probe.Type == EntityNPC && probe.Key != "" {
		return probe.Key, true
	}
	return "", false
}
func (m6world) ShowcaseKey(n int) (string, bool) {
	if n < 0 || n >= len(showNPCs) {
		return "", false
	}
	return showNPCs[n], true
}
func (m6world) ShowcaseCount() int { return len(showNPCs) }
func (m6world) SyncFrame(instance string, x, y int) []any {
	ph := welcomePlayer(instance)
	ph.X, ph.Y = x, y
	return pkt(PacketSync, ph)
}

func m6deps() controller.EconomyDeps {
	return controller.EconomyDeps{
		Store: m6store{}, Bus: m6bus{}, Peers: m6peers{},
		Quests: m6quests{}, Pets: m6pets{}, World: m6world{},
	}
}

// ---------------------------------------------------------------------------
// Entry points (signatures UNCHANGED; main.go/m5/m11/m12/m13 call sites
// compile as-is). All logic lives in the controller.
// ---------------------------------------------------------------------------

func m6LoadItems() error {
	if err := controller.LoadItems(); err != nil {
		return err
	}
	for _, k := range controller.ItemKeys() {
		m6Items[k] = controller.ItemInfoFor(k)
	}
	return nil
}

func m6ItemInfoFor(key string) *m6ItemInfo { return controller.ItemInfoFor(key) }

func m6ItemName(key string) string { return controller.ItemName(key) }

func m6MaxStack(key string) int { return controller.MaxStack(key) }

func m6LoadStores() error { return controller.LoadStores() }

func m6ItemPrice(key string) int { return controller.ItemPrice(key) }

func m6StoreFor(key string) *m6Store { return controller.StoreFor(key) }

func m6StartStoreTicker() { controller.StartStoreTicker(m6deps()) }

func m6StoreItems(store *m6Store) []m6StoreItem { return controller.StoreItems(store) }

func m6FindStoreItem(store *m6Store, itemKey string) int {
	return controller.FindStoreItem(store, itemKey)
}

func m6Serialize(store *m6Store) storePacketData { return controller.SerializeStore(store) }

func m6UpdatePlayers(key string) { controller.UpdatePlayers(key, m6deps()) }

func m6VerifyStore(c *playerConn, key string) *m6Store {
	if c == nil {
		return nil
	}
	return controller.VerifyStore(c, key)
}

func m6Notify(c *playerConn, message string) {
	if c == nil {
		return
	}
	controller.Notify(c, m6deps(), message)
}

func m6OpenStore(c *playerConn, key string) {
	if c == nil {
		return
	}
	controller.OpenStore(c, m6deps(), key)
}

func m6InventoryHasItem(key, itemKey string) bool {
	return controller.HasItem(m6deps(), key, itemKey)
}

func m6InventoryFindCurrency(key, itemKey string, count int) int {
	return controller.FindCurrency(m6deps(), key, itemKey, count)
}

func m6InventoryRemoveAt(c *playerConn, key string, index, count int) {
	if c == nil {
		return
	}
	controller.InventoryRemoveAt(c, m6deps(), key, index, count)
}

func m6Buy(c *playerConn, key string, index, count int) {
	if c == nil {
		return
	}
	controller.Buy(c, m6deps(), key, index, count)
}

func m6GetTotalCost(count, price, storeCount int) int {
	return controller.GetTotalCost(count, price, storeCount)
}

func m6Sell(c *playerConn, key string, index, count int) {
	if c == nil {
		return
	}
	controller.Sell(c, m6deps(), key, index, count)
}

func m6Select(c *playerConn, key string, index, count int) {
	if c == nil {
		return
	}
	controller.Select(c, m6deps(), key, index, count)
}

func m6HandleStore(c *playerConn, frame clientFrame) {
	if c == nil || len(frame) < 2 {
		return
	}
	controller.HandleStore(c, []byte(frame[1]), m6deps())
}

func m6SeedItem(key, itemKey string, count int) int {
	return controller.SeedItem(m6deps(), key, itemKey, count)
}

func m6SeedGold(key string, amount int) { controller.SeedGold(m6deps(), key, amount) }

func m6InvSlots(key string) []any { return controller.InvSlots(m6deps(), key) }

func m6BankAdd(key, itemKey string, count int) int {
	return controller.BankAdd(m6deps(), key, itemKey, count)
}

func m6BankBatch(key string) containerData { return controller.BankBatch(m6deps(), key) }

func clearContainerAccess(c *playerConn) {
	if c == nil {
		return
	}
	controller.ClearAccess(c)
}

func m6OpenBank(c *playerConn) {
	if c == nil {
		return
	}
	controller.OpenBank(c, m6deps())
}

func m6HandleContainerSelect(c *playerConn, msg *clientContainer) {
	if c == nil || msg == nil {
		return
	}
	controller.HandleContainerSelect(c, msg, m6deps())
}

func m6HandleContainerSwap(c *playerConn, fromIndex, toIndex int) {
	if c == nil {
		return
	}
	controller.HandleContainerSwap(c, m6deps(), fromIndex, toIndex)
}

func m6HandleContainer(c *playerConn, frame clientFrame) {
	if c == nil || len(frame) < 2 {
		return
	}
	controller.HandleContainer(c, []byte(frame[1]), m6deps())
}

func m6LoadNPCs() {
	controller.LoadNPCs()
	for k, v := range controller.NPCSnapshot() {
		m6NPCs[k] = v
	}
	m6NPCsOK = controller.NPCsOK()
}

func m6IsNPCKey(key string) bool { return controller.IsNPCKey(key) }

func m6ResolveNPCKey(c *playerConn, instance string) string {
	var ec controller.EconomyConn
	if c != nil {
		ec = c
	}
	return controller.ResolveNPCKey(ec, m6deps(), instance)
}

func m6HandleNPCTarget(c *playerConn, instance string) {
	if c == nil {
		return
	}
	controller.HandleNPCTarget(c, m6deps(), instance)
}

// min helper for the talk index clamp (shared with m11.go).
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func m6EquipmentType(itemType string) int { return controller.EquipmentType(itemType) }

func m6IsEquippable(itemType string) bool { return controller.IsEquippable(itemType) }

func m6EquipmentData(slotType int, key string, count int, clientInfo bool) map[string]any {
	return controller.EquipmentData(slotType, key, count, clientInfo)
}

func m6EquipmentSlots(key string) []any { return controller.EquipmentSlots(m6deps(), key) }

func m6SkillLevelFor(key string, skillID int) (string, int, int) {
	return controller.SkillLevelFor(m6deps(), key, skillID)
}

func m6CanEquip(c *playerConn, key string) bool {
	if c == nil {
		return false
	}
	return controller.CanEquip(c, m6deps(), key)
}

func m6SkillIDFor(key string) (int, bool) { return controller.SkillIDFor(key) }

func m6UnequipType(c *playerConn, slotType int) {
	if c == nil {
		return
	}
	controller.UnequipType(c, m6deps(), slotType)
}

func m6EquipFromInventory(c *playerConn, fromIndex int) {
	if c == nil {
		return
	}
	controller.EquipFromInventory(c, m6deps(), fromIndex)
}

func m6HandleEquipment(c *playerConn, frame clientFrame) {
	if c == nil || len(frame) < 2 {
		return
	}
	controller.HandleEquipment(c, []byte(frame[1]), m6deps())
}
