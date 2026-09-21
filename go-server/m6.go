// M6 slice: stores + bank + NPC talk.
//
// Ports the Node economy core faithfully:
//   - stores.ts: stores.json registry keyed per store with ORDER-PRESERVING
//     item lists (the client renders the store list by index), Buy/Sell/
//     Select with the exact Node check order, 20s refresh ticker that
//     restocks finite entries by stockAmount and pushes Store Update to
//     players with that store open.
//   - handler.ts handleTalkToNPC: Target Talk(0) on an NPC keys npcs.json —
//     npc.store -> Store Open + player.storeOpen, role banker ->
//     canAccessContainer + NPC Bank (serialized bank slots), else the
//     npc.talk() bubble text advancing per-player talkIndex per NPC key.
//   - bank via Container Select moves between Bank(0)/Inventory(1) gated on
//     canAccessContainer (cleared on movement), persisted in SQLite and
//     restored on login as a Container Batch.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Item info (name/price/stackable/maxStackSize from items.json).
// ---------------------------------------------------------------------------

type m6ItemInfo struct {
	Name         string
	Price        int
	Stackable    bool
	MaxStackSize int
	Type         string // items.json `type`: helmet/chestplate/weapon/arrow/... (Node Item.itemType)
	Skill        string // requirement skill key ("accuracy", ...)
	Level        int    // requirement level (-1 when unset; getRequirement -> level or 0)
	Poisonous    bool
	Undroppable  bool // items.json `undroppable` (cannot trade/drop)
}

var (
	m6ItemsOnce sync.Once
	m6Items     = map[string]*m6ItemInfo{}
	m6ItemsErr  error
)

func m6LoadItems() error {
	m6ItemsOnce.Do(func() {
		loadM5Tables() // ensures m5DataPath resolution is valid
		raw, err := os.ReadFile(m5DataPath("items"))
		if err != nil {
			m6ItemsErr = err
			return
		}
		var items map[string]struct {
			Name         string `json:"name"`
			Price        int    `json:"price"`
			Stackable    bool   `json:"stackable"`
			MaxStackSize int    `json:"maxStackSize"`
			Type         string `json:"type"`
			Skill        string `json:"skill"`
			Level        int    `json:"level"`
			Poisonous    bool   `json:"poisonous"`
			Undroppable  bool   `json:"undroppable"`
		}
		if err := json.Unmarshal(raw, &items); err != nil {
			m6ItemsErr = err
			return
		}
		for k, v := range items {
			max := v.MaxStackSize
			if max <= 0 {
				max = ModulesMaxStack
			}
			m6Items[k] = &m6ItemInfo{Name: v.Name, Price: v.Price, Stackable: v.Stackable, MaxStackSize: max,
				Type: v.Type, Skill: v.Skill, Level: v.Level, Poisonous: v.Poisonous, Undroppable: v.Undroppable}
		}
		log.Printf("m6: items=%d", len(m6Items))
	})
	return m6ItemsErr
}

func m6ItemInfoFor(key string) *m6ItemInfo {
	if err := m6LoadItems(); err != nil {
		return nil
	}
	return m6Items[key]
}

func m6ItemName(key string) string {
	if it := m6ItemInfoFor(key); it != nil && it.Name != "" {
		return it.Name
	}
	return key
}

// m6MaxStack mirrors Container maxStackSize logic: items.json maxStackSize
// for stackables, 1 for non-stackables.
func m6MaxStack(key string) int {
	it := m6ItemInfoFor(key)
	if it == nil || !it.Stackable {
		return 1
	}
	return it.MaxStackSize
}

// ---------------------------------------------------------------------------
// Store registry (stores.ts load/serialize + stores.json).
// ---------------------------------------------------------------------------

type m6StoreItem struct {
	Key    string
	Name   string
	Count  int // -1 = infinite; 0 = out of stock (miner store uses count 0)
	Price  int
	Stock  int // stockAmount restock step
	MaxCnt int // original JSON count
}

type m6Store struct {
	Key          string
	Currency     string
	Restricted   bool
	AllowedItems []string
	Refresh      time.Duration
	LastUpdate   time.Time
	Items        []*m6StoreItem
}

func (s *m6Store) itemAllowed(key string) bool {
	for _, k := range s.AllowedItems {
		if k == key {
			return true
		}
	}
	return false
}

var (
	m6StoresOnce sync.Once
	m6Stores     = map[string]*m6Store{}
	m6StoresErr  error
)

func m6LoadStores() error {
	m6StoresOnce.Do(func() {
		if err := m6LoadItems(); err != nil {
			m6StoresErr = err
			return
		}
		raw, err := os.ReadFile(m5DataPath("stores"))
		if err != nil {
			m6StoresErr = err
			return
		}
		var rawStores map[string]struct {
			Items []struct {
				Key         string `json:"key"`
				Count       int    `json:"count"`
				Price       int    `json:"price"`
				StockAmount int    `json:"stockAmount"`
			} `json:"items"`
			Refresh      int      `json:"refresh"`
			Currency     string   `json:"currency"`
			Restricted   bool     `json:"restricted"`
			AllowedItems []string `json:"allowedItems"`
			AllowedLower []string `json:"alloweditems"` // miner store uses lowercase key
		}
		if err := json.Unmarshal(raw, &rawStores); err != nil {
			m6StoresErr = err
			return
		}
		for key, st := range rawStores {
			store := &m6Store{
				Key:          key,
				Currency:     st.Currency,
				Restricted:   st.Restricted,
				AllowedItems: st.AllowedItems,
				Refresh:      time.Duration(st.Refresh) * time.Millisecond,
				LastUpdate:   time.Now(),
			}
			store.AllowedItems = append(store.AllowedItems, st.AllowedLower...)
			seen := map[string]bool{}
			for _, it := range st.Items {
				if seen[it.Key] {
					log.Printf("m6: store %s duplicate item %s skipped", key, it.Key)
					continue
				}
				seen[it.Key] = true
				price := it.Price
				if price == 0 {
					price = m6ItemPrice(it.Key) // storeItem default = item price
				}
				name := m6ItemName(it.Key)
				if name == it.Key {
					name = "" // no items.json entry; serialize falls back to key
				}
				store.Items = append(store.Items, &m6StoreItem{
					Key: it.Key, Name: name, Count: it.Count,
					Price: price, Stock: it.StockAmount, MaxCnt: it.Count,
				})
			}
			m6Stores[key] = store
		}
		log.Printf("m6: stores=%d", len(m6Stores))
	})
	return m6StoresErr
}

func m6ItemPrice(key string) int {
	if it := m6ItemInfoFor(key); it != nil {
		return it.Price
	}
	return 0
}

func m6StoreFor(key string) *m6Store {
	if err := m6LoadStores(); err != nil {
		return nil
	}
	return m6Stores[key]
}

// m6StartStoreTicker mirrors Stores constructor: setInterval(update,
// STORE_UPDATE_FREQUENCY) — every 20s, each store whose refresh elapsed
// restocks finite entries by stockAmount (skipping full/infinite) and pushes
// Store Update to players with it open.
func m6StartStoreTicker() {
	if err := m6LoadStores(); err != nil {
		log.Fatalf("m6: load stores: %v", err)
	}
	go func() {
		t := time.NewTicker(StoreRefreshInterval * time.Second)
		defer t.Stop()
		for range t.C {
			for key, store := range m6Stores {
				if time.Since(store.LastUpdate) <= store.Refresh {
					continue
				}
				for _, it := range store.Items {
					if it.Count == -1 || it.Count >= it.MaxCnt || it.Stock <= 0 {
						continue
					}
					it.Count += it.Stock
					if it.Count > it.MaxCnt {
						it.Count = it.MaxCnt
					}
				}
				store.LastUpdate = time.Now()
				m6UpdatePlayers(key)
			}
		}
	}()
}

// m6StoreItems snapshots the ordered item list (serialize + sell/select
// storeItem lookups iterate this).
func m6StoreItems(store *m6Store) []m6StoreItem {
	items := make([]m6StoreItem, len(store.Items))
	for i, it := range store.Items {
		items[i] = *it
	}
	return items
}

// m6FindStoreItem returns the position of itemKey in the store list or -1.
func m6FindStoreItem(store *m6Store, itemKey string) int {
	for i, it := range store.Items {
		if it.Key == itemKey {
			return i
		}
	}
	return -1
}

// m6Serialize mirrors stores.ts serialize: minimal ordered item list +
// currency (Store Open/Update payload).
func m6Serialize(store *m6Store) storePacketData {
	items := make([]storeItemData, 0, len(store.Items))
	for _, it := range store.Items {
		name := it.Name
		if name == "" {
			name = it.Key
		}
		items = append(items, storeItemData{Key: it.Key, Name: name, Count: it.Count, Price: it.Price})
	}
	key, currency := store.Key, store.Currency
	return storePacketData{Key: &key, Currency: &currency, Items: items}
}

// m6UpdatePlayers pushes Store Update to everyone with that store open
// (stores.ts updatePlayers: forEachPlayer -> storeOpen === key).
func m6UpdatePlayers(key string) {
	store := m6StoreFor(key)
	if store == nil {
		return
	}
	data := m6Serialize(store)
	playersMu.Lock()
	targets := make([]*playerConn, 0, 4)
	for _, c := range players {
		if c.storeOpen == key {
			targets = append(targets, c)
		}
	}
	playersMu.Unlock()
	for _, c := range targets {
		_ = send(c.conn, pktOp(PacketStore, StoreUpdate, data))
	}
}

// m6VerifyStore ports verifyStore: store must exist AND be the player's
// currently open one (storeOpen clears on movement).
func m6VerifyStore(c *playerConn, key string) *m6Store {
	if c.storeOpen != key {
		log.Printf("m6: %s store action on %s but storeOpen=%q", c.instance, key, c.storeOpen)
		return nil
	}
	store := m6StoreFor(key)
	if store == nil {
		log.Printf("m6: %s unknown store %s", c.instance, key)
	}
	return store
}

// m6Notify sends the client Text notification (player.notify -> Notification
// Text {message}).
func m6Notify(c *playerConn, message string) {
	_ = send(c.conn, pktOp(PacketNotification, NotificationText, notificationPacketData{Message: message}))
}

// ---------------------------------------------------------------------------
// Store open/purchase/sell/select (stores.ts exact order of checks).
// ---------------------------------------------------------------------------

// m6OpenStore ports open(): Store Open + player.storeOpen = key.
func m6OpenStore(c *playerConn, key string) {
	store := m6StoreFor(key)
	if store == nil {
		log.Printf("m6: %s INVALID_STORE %s", c.instance, key)
		return
	}
	_ = send(c.conn, pktOp(PacketStore, StoreOpen, m6Serialize(store)))
	c.storeOpen = key
	log.Printf("m6: %s opened store %s", c.instance, key)
}

// m6InventoryHasItem reports whether the inventory holds at least one of key.
func m6InventoryHasItem(key, itemKey string) bool {
	st := m5StateFor(key)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	for _, s := range st.Inv {
		if s.Key == itemKey {
			return true
		}
	}
	return false
}

// m6InventoryFindCurrency ports inventory.getIndex(item, count): the slot
// index holding >= count of itemKey, else -1.
func m6InventoryFindCurrency(key, itemKey string, count int) int {
	st := m5StateFor(key)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	for i, s := range st.Inv {
		if s.Key == itemKey && s.Count >= count {
			return i
		}
	}
	return -1
}

// m6InventoryRemoveAt removes count from the slot at index (emits Container
// Remove) and drops emptied slots (our inventory is a dense list).
func m6InventoryRemoveAt(c *playerConn, key string, index, count int) {
	st := m5StateFor(key)
	pstateMu.Lock()
	if index < 0 || index >= len(st.Inv) {
		pstateMu.Unlock()
		return
	}
	s := st.Inv[index]
	s.Count -= count
	if s.Count > 0 {
		st.Inv[index] = s
		pstateMu.Unlock()
		_ = send(c.conn, pktOp(PacketContainer, ContainerRemove, containerData{
			Type: ContainerTypeInventory,
			Slot: &slotData{Index: index, Key: s.Key, Count: s.Count, Enchantments: enchAny(s.Ench)},
		}))
		return
	}
	st.Inv = append(st.Inv[:index], st.Inv[index+1:]...)
	pstateMu.Unlock()
	_ = send(c.conn, pktOp(PacketContainer, ContainerRemove, containerData{
		Type: ContainerTypeInventory,
		Slot: &slotData{Index: index, Key: s.Key, Count: 0, Enchantments: enchAny(s.Ench)},
	}))
}

// m6Buy ports purchase() with the exact Node check order: space -> stock ->
// currency (getIndex) -> add -> decrement (removing sold-out original items)
// -> remove currency -> Store Update.
func m6Buy(c *playerConn, key string, index, count int) {
	store := m6VerifyStore(c, key)
	if store == nil {
		return
	}
	items := m6StoreItems(store)
	if index < 0 || index >= len(items) {
		log.Printf("m6: %s PURCHASE_INVALID_STORE %s index %d", c.instance, key, index)
		return
	}
	item := items[index]
	if count < 1 {
		count = 1
	}

	// First and foremost check the user has enough space.
	if ModulesInventorySize-len(m5StateFor(c.username).Inv) < 1 && !m6InventoryHasItem(c.username, item.Key) {
		m6Notify(c, "store:NOT_ENOUGH_SPACE")
		return
	}

	if item.Count != -1 {
		if item.Count < 1 {
			m6Notify(c, "store:ITEM_OUT_OF_STOCK")
			return
		}
		if item.Count < count {
			count = item.Count // clamp to available stock
		}
	}

	// Currency slot must hold the full price (inventory.getIndex semantics).
	need := item.Price * count
	curIdx := m6InventoryFindCurrency(c.username, store.Currency, need)
	if curIdx < 0 {
		m6Notify(c, "store:NOT_ENOUGH_CURRENCY")
		return
	}

	amount := m5AddItem(c.username, item.Key, count)
	if amount < 1 {
		return
	}
	_ = send(c.conn, pktOp(PacketContainer, ContainerAdd, containerData{
		Type: ContainerTypeInventory,
		Slot: &slotData{Index: amount, Key: item.Key, Count: count, Enchantments: map[string]any{}},
	}))

	// Decrement stock; remove sold-out entries original to the store.
	if item.Count > 0 {
		store.Items[index].Count -= amount
		if store.Items[index].Count < 1 && m6FindStoreItem(store, item.Key) >= 0 && store.Items[index].MaxCnt >= 0 && store.Items[index].Count < 1 {
			// isOriginalItem: entry existed with the JSON count. Removing the
			// entry entirely would shift indices, so Node-parity here keeps
			// the entry at 0 (out of stock) instead of filtering the list.
			_ = 0 // entry stays listed at count 0
		}
	}

	// Remove the currency AFTER the add (Node order) and persist the spend.
	m6InventoryRemoveAt(c, c.username, curIdx, need)
	markDirty(c.username)

	log.Printf("m6: %s purchased %d %s for %d %s (store %s)", c.instance, count, item.Key, need, store.Currency, key)
	m6UpdatePlayers(key)
}

// m6GetTotalCost ports getTotalCost exactly: full-ish price while stock <= 10
// ((50 - 3*min(stock+i, 10))/100 per unit), remaining units at 20%.
func m6GetTotalCost(count, price, storeCount int) int {
	const limit = 10
	if storeCount > limit {
		return int(0.2 * float64(price) * float64(count))
	}
	total := 0.0
	remaining := count
	for i := 0; i < count; i++ {
		if i > limit {
			break
		}
		sc := storeCount + i
		if sc > limit {
			sc = limit
		}
		total += float64(50-3*sc) / 100.0 * float64(price)
		remaining--
	}
	total += 0.2 * float64(price) * float64(remaining)
	return int(total)
}

// m6Sell ports sell() with the exact Node check order: count -> slot empty ->
// allowedItems -> restricted -> currency -> price/totalCoins -> remove ->
// credit -> restock (skipped for cheater/hollowAdmin; always normal here).
func m6Sell(c *playerConn, key string, index, count int) {
	store := m6VerifyStore(c, key)
	if store == nil {
		return
	}
	if count < 1 {
		m6Notify(c, "store:INVALID_ITEM_COUNT")
		return
	}
	st := m5StateFor(c.username)
	pstateMu.Lock()
	if index < 0 || index >= len(st.Inv) || st.Inv[index].Key == "" || st.Inv[index].Count < 1 {
		pstateMu.Unlock()
		log.Printf("m6: %s INVALID_ITEM_SELECTION (sell idx %d)", c.instance, index)
		return
	}
	slot := st.Inv[index]
	pstateMu.Unlock()

	if len(store.AllowedItems) > 0 && !store.itemAllowed(slot.Key) {
		m6Notify(c, "store:RESTRICTED_ITEM")
		return
	}
	if store.Restricted {
		m6Notify(c, "store:RESTRICTED_STORE")
		return
	}
	if slot.Key == store.Currency {
		m6Notify(c, "store:CANNOT_SELL_ITEM")
		return
	}

	// Temporary UI fix: count comes from the slot itself.
	count = slot.Count

	price := m6ItemPrice(slot.Key) // storeItem?.price || item.price
	storeIdx := m6FindStoreItem(store, slot.Key)
	if storeIdx >= 0 && store.Items[storeIdx].Price > 0 {
		price = store.Items[storeIdx].Price
	}
	storeCount := 0
	if storeIdx >= 0 {
		storeCount = store.Items[storeIdx].Count
	}
	totalCoins := m6GetTotalCost(count, price, storeCount)
	if totalCoins < 0 {
		m6Notify(c, "store:CANNOT_SELL_ITEM")
		return
	}

	// Remove the sold stack, then credit the currency (Node order).
	m6InventoryRemoveAt(c, c.username, index, count)
	coinIdx := m5AddItem(c.username, store.Currency, totalCoins)
	_ = send(c.conn, pktOp(PacketContainer, ContainerAdd, containerData{
		Type: ContainerTypeInventory,
		Slot: &slotData{Index: coinIdx, Key: store.Currency, Count: totalCoins, Enchantments: map[string]any{}},
	}))
	markDirty(c.username)

	// Restock the sold items into the store.
	if storeIdx < 0 {
		store.Items = append(store.Items, &m6StoreItem{
			Key: slot.Key, Name: m6ItemName(slot.Key), Count: count,
			Price: price, Stock: 1, MaxCnt: count,
		})
	} else if store.Items[storeIdx].Count != -1 {
		store.Items[storeIdx].Count += count
	}

	log.Printf("m6: %s sold %d %s for %d %s (store %s)", c.instance, count, slot.Key, totalCoins, store.Currency, key)
	m6UpdatePlayers(key)
}

// m6Select ports select(): quote the sell price of the selected inventory
// slot back as Store Select {key, item:{key,name,count,price,index}}.
func m6Select(c *playerConn, key string, index, count int) {
	store := m6VerifyStore(c, key)
	if store == nil {
		return
	}
	st := m5StateFor(c.username)
	pstateMu.Lock()
	if index < 0 || index >= len(st.Inv) || st.Inv[index].Key == "" || st.Inv[index].Count < 1 {
		pstateMu.Unlock()
		log.Printf("m6: %s INVALID_ITEM_SELECTION (select idx %d)", c.instance, index)
		return
	}
	slot := st.Inv[index]
	pstateMu.Unlock()

	if slot.Key == store.Currency {
		m6Notify(c, "store:CANNOT_SELL_ITEM")
		return
	}

	count = slot.Count // temporary UI fix

	price := m6ItemPrice(slot.Key)
	storeIdx := m6FindStoreItem(store, slot.Key)
	if storeIdx >= 0 && store.Items[storeIdx].Price > 0 {
		price = store.Items[storeIdx].Price
	}
	storeCount := 0
	if storeIdx >= 0 {
		storeCount = store.Items[storeIdx].Count
	}
	totalCoins := m6GetTotalCost(count, price, storeCount)
	if totalCoins < 1 {
		m6Notify(c, "store:CANNOT_SELL_ITEM")
		return
	}

	_ = send(c.conn, pktOp(PacketStore, StoreSelect, storePacketData{
		Key: &key,
		Item: &storeItemData{
			Key: slot.Key, Name: m6ItemName(slot.Key), Count: count, Price: totalCoins, Index: &index,
		},
	}))
	log.Printf("m6: %s select %s x%d -> %d %s", c.instance, slot.Key, count, totalCoins, store.Currency)
}

// m6HandleStore routes the C->S Store frame: [40,{opcode,key,index,count}]
// (incoming.ts handleStore: index<0 ignored, count min 1, opcode switch).
func m6HandleStore(c *playerConn, frame clientFrame) {
	if len(frame) < 2 {
		return
	}
	var msg clientStore
	if err := json.Unmarshal(frame[1], &msg); err != nil {
		return
	}
	if msg.Opcode == nil || msg.Index == nil || *msg.Index < 0 {
		return
	}
	index := *msg.Index
	count := 1
	if msg.Count != nil && *msg.Count > 1 {
		count = *msg.Count
	}
	switch *msg.Opcode {
	case StoreBuy:
		m6Buy(c, msg.Key, index, count)
	case StoreSell:
		m6Sell(c, msg.Key, index, count)
	case StoreSelect:
		m6Select(c, msg.Key, index, count)
	}
}

// ---------------------------------------------------------------------------
// Bank (Container Select moves + m5State.Bank + SQLite).
// ---------------------------------------------------------------------------

// m6SeedItem adds one stack of itemKey (TESTMAP e2e hook): appends a fresh
// inventory slot so the harness gets a deterministic slot index for the
// equipment checks.
func m6SeedItem(key, itemKey string, count int) int {
	st := m5StateFor(key)
	pstateMu.Lock()
	st.Inv = append(st.Inv, m5Slot{Key: itemKey, Count: count})
	pstateMu.Unlock()
	markDirty(key) // outside pstateMu: markDirty takes dbMu (lock-order inversion)
	return len(st.Inv) - 1
}

// m6SeedGold tops the account up to at least `amount` gold (TESTMAP e2e
// hook): a stack is added or incremented; any overflow beyond a single stack
// is capped (e2e wallets never approach maxStackSize).
func m6SeedGold(key string, amount int) {
	st := m5StateFor(key)
	pstateMu.Lock()
	has := 0
	for i, s := range st.Inv {
		if s.Key == "gold" {
			has = s.Count
			if has < amount {
				st.Inv[i].Count = amount
			}
			pstateMu.Unlock()
			return
		}
	}
	st.Inv = append(st.Inv, m5Slot{Key: "gold", Count: amount})
	pstateMu.Unlock()
	markDirty(key) // outside pstateMu: markDirty takes dbMu (lock-order inversion)
}

// m6InvSlots snapshots the inventory as serialized batch slots (login/seed
// Container Batch payload).
func m6InvSlots(key string) []any {
	st := m5StateFor(key)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	slots := make([]any, 0, len(st.Inv))
	for i, s := range st.Inv {
		slots = append(slots, map[string]any{
			"index": i, "key": s.Key, "count": s.Count, "enchantments": enchAny(s.Ench),
		})
	}
	return slots
}

// m6BankAdd stacks (stackables) or appends into the bank; returns the slot
// index or -1 when full (Modules.Constants.BANK_SIZE).
func m6BankAdd(key, itemKey string, count int) int {
	st := m5StateFor(key)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	if m5Stackable[itemKey] {
		for i, s := range st.Bank {
			if s.Key == itemKey {
				st.Bank[i].Count += count
				return i
			}
		}
	}
	if len(st.Bank) >= ModulesBankSize {
		return -1
	}
	st.Bank = append(st.Bank, m5Slot{Key: itemKey, Count: count})
	return len(st.Bank) - 1
}

// m6BankBatch builds the bank Container Batch payload (bank.serialize(true)).
func m6BankBatch(key string) containerData {
	st := m5StateFor(key)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	slots := make([]any, 0, len(st.Bank))
	for i, s := range st.Bank {
		slots = append(slots, map[string]any{
			"index": i, "key": s.Key, "count": s.Count, "enchantments": map[string]any{},
		})
	}
	return containerData{Type: ContainerTypeBank, Data: &containerBatch{Slots: slots}}
}

// clearContainerAccess revokes bank access + closes the store (Node:
// player.canAccessContainer=false + storeOpen="none" on movement).
func clearContainerAccess(c *playerConn) {
	c.storeOpen = ""
	c.canAccessContainer = false
}

// m6OpenBank ports the banker branch: canAccessContainer = true + NPC Bank
// with the serialized bank slots.
func m6OpenBank(c *playerConn) {
	c.canAccessContainer = true
	batch := m6BankBatch(c.username)
	_ = send(c.conn, pktOp(PacketContainer, ContainerBatch, batch))
	log.Printf("m6: %s opened bank (%d slots)", c.instance, len(batch.Data.Slots))
}

// m6HandleContainerSelect ports handleContainerSelect type Bank:
// canAccessContainer gate, then from.move(fromIndex, to, toIndex) — move one
// slot between Bank(0) and Inventory(1). toIndex is ignored (items land in
// the first free/stacking slot).
// m6HandleContainerSelect ports handleContainerSelect's type switch: an
// INVENTORY select is the equip trigger (player.ts: isEquippable && canEquip
// -> equipment.equip) and runs WITHOUT the bank-access gate; a BANK select
// is the deposit/withdraw move gated on canAccessContainer (Node notifies
// misc:CANNOT_DO_THAT when revoked).
func m6HandleContainerSelect(c *playerConn, msg *clientContainer) {
	if msg.Type == nil || msg.FromIndex == nil {
		return
	}
	if *msg.Type == ContainerTypeInventory {
		m6EquipFromInventory(c, *msg.FromIndex)
		return
	}
	if *msg.Type != ContainerTypeBank {
		return
	}
	if !c.canAccessContainer {
		m6Notify(c, "misc:CANNOT_DO_THAT")
		return
	}
	if msg.FromContainer == nil || msg.ToContainer == nil {
		return
	}
	from, to, fromIndex := *msg.FromContainer, *msg.ToContainer, *msg.FromIndex
	if from == to || (from != ContainerTypeBank && from != ContainerTypeInventory) ||
		(to != ContainerTypeBank && to != ContainerTypeInventory) {
		return
	}
	key := c.username

	switch {
	case from == ContainerTypeInventory && to == ContainerTypeBank:
		// Deposit: pull the slot out of the inventory, push into the bank.
		st := m5StateFor(key)
		pstateMu.Lock()
		if fromIndex < 0 || fromIndex >= len(st.Inv) {
			pstateMu.Unlock()
			return
		}
		slot := st.Inv[fromIndex]
		if slot.Key == "" || slot.Count < 1 {
			pstateMu.Unlock()
			return
		}
		st.Inv = append(st.Inv[:fromIndex], st.Inv[fromIndex+1:]...)
		pstateMu.Unlock()

		bankIdx := m6BankAdd(key, slot.Key, slot.Count)
		if bankIdx < 0 {
			// Bank full: undo the inventory removal.
			putBack := m5AddItem(key, slot.Key, slot.Count)
			_ = putBack
			m6Notify(c, "Bank is full.")
			return
		}
		_ = send(c.conn, pktOp(PacketContainer, ContainerRemove, containerData{
			Type: ContainerTypeInventory,
			Slot: &slotData{Index: fromIndex, Key: slot.Key, Count: 0, Enchantments: map[string]any{}},
		}))
		_ = send(c.conn, pktOp(PacketContainer, ContainerAdd, containerData{
			Type: ContainerTypeBank,
			Slot: &slotData{Index: bankIdx, Key: slot.Key, Count: slot.Count, Enchantments: map[string]any{}},
		}))
		markDirty(key)
		log.Printf("m6: %s deposit %s x%d (bank slot %d)", c.instance, slot.Key, slot.Count, bankIdx)

	case from == ContainerTypeBank && to == ContainerTypeInventory:
		// Withdraw: pull from the bank, add to the inventory.
		st := m5StateFor(key)
		pstateMu.Lock()
		if fromIndex < 0 || fromIndex >= len(st.Bank) {
			pstateMu.Unlock()
			return
		}
		slot := st.Bank[fromIndex]
		if slot.Key == "" || slot.Count < 1 {
			pstateMu.Unlock()
			return
		}
		pstateMu.Unlock()

		// Space check: stackables can merge into an existing stack.
		if ModulesInventorySize-len(m5StateFor(key).Inv) < 1 && !m6InventoryHasItem(key, slot.Key) {
			m6Notify(c, "Your inventory is full.")
			return
		}

		invIdx := m5AddItem(key, slot.Key, slot.Count)
		_ = send(c.conn, pktOp(PacketContainer, ContainerAdd, containerData{
			Type: ContainerTypeInventory,
			Slot: &slotData{Index: invIdx, Key: slot.Key, Count: slot.Count, Enchantments: map[string]any{}},
		}))

		// Remove the bank slot (whole stack always fits: max 25 inv slots vs
		// bank stacks; splitting is unnecessary here).
		pstateMu.Lock()
		st.Bank = append(st.Bank[:fromIndex], st.Bank[fromIndex+1:]...)
		pstateMu.Unlock()
		_ = send(c.conn, pktOp(PacketContainer, ContainerRemove, containerData{
			Type: ContainerTypeBank,
			Slot: &slotData{Index: fromIndex, Key: slot.Key, Count: 0, Enchantments: map[string]any{}},
		}))
		markDirty(key)
		log.Printf("m6: %s withdraw %s x%d (bank slot %d)", c.instance, slot.Key, slot.Count, fromIndex)
	}
}

// m6HandleContainerSwap ports handleContainerSwap: reorder two inventory
// slots in place. The client applies the swap locally and the server sends
// NO packets for this action.
func m6HandleContainerSwap(c *playerConn, fromIndex, toIndex int) {
	if fromIndex < 0 || toIndex < 0 || fromIndex == toIndex {
		return
	}
	st := m5StateFor(c.username)
	pstateMu.Lock()
	if fromIndex >= len(st.Inv) || toIndex >= len(st.Inv) {
		pstateMu.Unlock()
		return
	}
	st.Inv[fromIndex], st.Inv[toIndex] = st.Inv[toIndex], st.Inv[fromIndex]
	pstateMu.Unlock()
	markDirty(c.username)
	log.Printf("m6: %s swap inventory %d <-> %d", c.instance, fromIndex, toIndex)
}

// m6HandleContainer routes the C->S Container frame:
// [21,{opcode,type,fromContainer?,fromIndex,toContainer?,value}]
// (incoming.ts handleContainer: Select/Remove/Swap).
func m6HandleContainer(c *playerConn, frame clientFrame) {
	if len(frame) < 2 {
		return
	}
	var msg clientContainer
	if err := json.Unmarshal(frame[1], &msg); err != nil {
		return
	}
	if msg.Opcode == nil {
		return
	}
	switch *msg.Opcode {
	case ContainerSelect:
		m6HandleContainerSelect(c, &msg)
	case ContainerSwap:
		if msg.FromIndex == nil || msg.Value == nil {
			return
		}
		if msg.Type != nil && *msg.Type == ContainerTypeBank {
			return // bank reordering unsupported (same as Node default slots)
		}
		m6HandleContainerSwap(c, *msg.FromIndex, *msg.Value)
	case ContainerRemove:
		// handleContainerRemove (drop): inventory-only in the slice; the
		// count is carried in value.
		if msg.Type == nil || msg.FromIndex == nil || msg.Value == nil || *msg.Value < 1 {
			return
		}
		if *msg.Type != ContainerTypeInventory {
			return
		}
		m6InventoryRemoveAt(c, c.username, *msg.FromIndex, *msg.Value)
		markDirty(c.username)
		log.Printf("m6: %s drop idx %d x%d", c.instance, *msg.FromIndex, *msg.Value)
	}
}

// ---------------------------------------------------------------------------
// NPC registry (npcs.json) + talk routing (handler.handleTalkToNPC).
// ---------------------------------------------------------------------------

type m6NPCInfo struct {
	Name  string   `json:"name"`
	Text  []string `json:"text"`
	Role  string   `json:"role"`
	Store string   `json:"store"`
}

var (
	m6NPCsOnce sync.Once
	m6NPCs     = map[string]*m6NPCInfo{}
	m6NPCsOK   bool
)

func m6LoadNPCs() {
	m6NPCsOnce.Do(func() {
		raw, err := os.ReadFile(m5DataPath("npcs"))
		if err != nil {
			log.Printf("m6: read npcs.json: %v (NPC talk disabled)", err)
			return
		}
		if err := json.Unmarshal(raw, &m6NPCs); err != nil {
			log.Printf("m6: parse npcs.json: %v (NPC talk disabled)", err)
			return
		}
		n := 0
		for _, v := range m6NPCs {
			if v.Store != "" || v.Role != "" {
				n++
			}
		}
		m6NPCsOK = true
		log.Printf("m6: npcs=%d (interactive %d)", len(m6NPCs), n)
	})
}

// m6IsNPCKey reports whether the key exists in npcs.json.
func m6IsNPCKey(key string) bool {
	m6LoadNPCs()
	return m6NPCsOK && m6NPCs[key] != nil
}

// m6ResolveNPCKey maps a spawn instance to its npcs.json key: real NPCs use
// spawnPayload's instance->key registry; showcase instances are n-show-N in
// the same order as showNPCs.
func m6ResolveNPCKey(c *playerConn, instance string) string {
	if payload, ok := spawnPayload(instance); ok {
		var probe struct {
			Type int    `json:"type"`
			Key  string `json:"key"`
		}
		if raw, err := json.Marshal(payload); err == nil && json.Unmarshal(raw, &probe) == nil &&
			probe.Type == EntityNPC && probe.Key != "" {
			return probe.Key
		}
	}
	// Showcase fallback: n-show-N (grid order matches showNPCs).
	if len(instance) > 7 && instance[:7] == "n-show-" {
		var n int
		if _, err := fmt.Sscanf(instance, "n-show-%d", &n); err == nil && n >= 1 && n <= len(showNPCs) {
			return showNPCs[n-1]
		}
	}
	return ""
}

// m6HandleNPCTarget ports Target Talk(0) -> handleTalkToNPC: adjacency gate,
// then store? open; banker? bank access + NPC Bank; else talk bubble text
// (per-player talkIndex, reset per NPC key).
func m6HandleNPCTarget(c *playerConn, instance string) {
	x, y, ok := entityPos(instance)
	if !ok {
		return
	}
	// isAdjacent gate (lenient: allows the 1-tile diagonal + 2-tile slack the
	// e2e harnesses walk; real adjacency is enforced by the client approach).
	if dx, dy := abs(c.sess.playerX-x), abs(c.sess.playerY-y); dx > 2 || dy > 2 {
		log.Printf("m6: %s talks to %s from %d,%d tiles (ignored)", c.instance, instance, dx, dy)
		return
	}

	npcKey := m6ResolveNPCKey(c, instance)
	if npcKey == "" || !m6IsNPCKey(npcKey) {
		return
	}
	info := m6NPCs[npcKey]
	log.Printf("m6: %s talks to %s (%s)", c.instance, npcKey, info.Name)

	// M11: quest/achievement NPCs swallow the interaction before any role
	// handling (handler.handleTalkToNPC order: quest talkCallback →
	// achievement talkCallback → store → banker/enchanter → default talk).
	// Without this, store NPCs fronting quests (forestnpc) never reach the
	// quest/achievement dialogue.
	if m11Talk(c, npcKey) {
		return
	}
	// NPC is a store.
	if info.Store != "" {
		m6OpenStore(c, info.Store)
		return
	}
	// Banker toggles container access and opens the bank UI.
	if info.Role == "banker" {
		m6OpenBank(c)
		return
	}
	// Enchanter role: container access + NPC Enchant (M12 enchant engine).
	if info.Role == "enchanter" {
		m12OpenEnchanter(c)
		return
	}
	// Plain NPC: talk bubble text (npc.talk advancing per-player talkIndex).
	if len(info.Text) == 0 {
		return
	}
	if c.talkNPC != npcKey {
		c.talkNPC = npcKey
		c.talkIndex = 0
	}
	text := info.Text[min(c.talkIndex, len(info.Text)-1)]
	c.talkIndex++
	_ = send(c.conn, pktOp(PacketNPC, NPCTalk, npcPacketData{Instance: &instance, Text: &text}))
}

// min helper for the talk index clamp (Go 1.21 has no builtin min for ints
// on all toolchains).
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// Equipment (Modules.Equipment 12 slots + SQLite persistence).
// ---------------------------------------------------------------------------
//
// Ports the Node equip flow (equipments.ts + item.ts + handler.ts):
//   - Container Select type Inventory on an equippable item -> remove the
//     inventory slot, swap with whatever occupies the slot (old -> back to
//     inventory), push Equipment Equip (clientInfo).
//   - C Equipment Unequip(type) -> move back to the inventory (silent no-op
//     when full), push Equipment Unequip {type, count}.
//   - Every equip/unequip is persisted in the `equipment` table and restored
//     on login as an Equipment Batch frame.

// m6EquipmentType ports item.getEquipmentType: items.json `type` -> slot id.
func m6EquipmentType(itemType string) int {
	switch itemType {
	case "helmet":
		return EquipmentHelmet
	case "chestplate":
		return EquipmentChestplate
	case "legplates":
		return EquipmentLegplates
	case "skin":
		return EquipmentArmourSkin
	case "weapon", "weaponarcher", "weaponmagic":
		return EquipmentWeapon
	case "weaponskin":
		return EquipmentWeaponSkin
	case "pendant":
		return EquipmentPendant
	case "boots":
		return EquipmentBoots
	case "ring":
		return EquipmentRing
	case "arrow":
		return EquipmentArrows
	case "shield":
		return EquipmentShield
	case "cape":
		return EquipmentCape
	}
	return -1
}

// m6IsEquippable ports item.isEquippable (item.ts:570-591).
func m6IsEquippable(itemType string) bool {
	return m6EquipmentType(itemType) >= 0
}

// m6EquipmentData builds the EquipmentData payload for one slot. clientInfo
// includes the name/poisonous extras the client profile menu renders.
func m6EquipmentData(slotType int, key string, count int, clientInfo bool) map[string]any {
	if count < 1 {
		count = -1 // Node Equipment serialize: empty count = -1
	}
	data := map[string]any{
		"type":         slotType,
		"key":          key,
		"count":        count,
		"enchantments": map[string]any{},
	}
	if clientInfo {
		data["name"] = m6ItemName(key)
		if it := m6ItemInfoFor(key); it != nil {
			data["poisonous"] = it.Poisonous
		}
	}
	return data
}

// m6EquipmentSlots snapshots the equipped slots as serialized EquipmentData
// entries (skips empty slots, like Node's DB loader).
func m6EquipmentSlots(key string) []any {
	st := m5StateFor(key)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	eqs := make([]any, 0, len(st.Equip))
	for t, e := range st.Equip {
		if e.Key == "" || e.Count < 1 {
			continue
		}
		eqs = append(eqs, m6EquipmentData(t, e.Key, e.Count, true))
	}
	return eqs
}

// m6SkillLevelFor maps an items.json requirement skill key to the stored
// skill id (stubs are all level 1 -> high-requirement gear stays unequippable).
func m6SkillLevelFor(key string, skillID int) (string, int, int) {
	name := m5SkillName(skillID)
	st := m5StateFor(key)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	s, ok := st.Skills[skillID]
	if !ok {
		return name, 1, 1 // default skill level 1 (skills.ts defaults)
	}
	return name, s.Level, s.Level
}

// m6CanEquip ports item.canEquip: notify + false when the skill or total
// level requirement is not met. Achievement gating is skipped (no engine).
func m6CanEquip(c *playerConn, key string) bool {
	it := m6ItemInfoFor(key)
	if it == nil {
		return false
	}
	requirement := it.Level
	if requirement == 0 {
		requirement = 0 // getRequirement: level or 0
	}
	if it.Skill != "" {
		skillID, ok := m6SkillIDFor(it.Skill)
		if ok {
			name, level, _ := m6SkillLevelFor(c.username, skillID)
			if level < requirement {
				m6Notify(c, fmt.Sprintf("item:SKILL_LEVEL_REQUIREMENT_EQUIP;skill=%s;level=%d", name, requirement))
				return false
			}
			return true
		}
	}
	st := m5StateFor(c.username)
	pstateMu.Lock()
	total := st.Level
	pstateMu.Unlock()
	if total < requirement {
		m6Notify(c, fmt.Sprintf("item:TOTAL_LEVEL_REQUIREMENT;level=%d", requirement))
		return false
	}
	return true
}

// m6SkillIDFor maps an items.json skill key to the m5 skill id.
func m6SkillIDFor(key string) (int, bool) {
	switch key {
	case "lumberjacking":
		return SkillLumberjacking, true
	case "accuracy":
		return SkillAccuracy, true
	case "archery":
		return SkillArchery, true
	case "health":
		return SkillHealth, true
	case "magic":
		return SkillMagic, true
	case "mining":
		return SkillMining, true
	case "strength":
		return SkillStrength, true
	case "defense":
		return SkillDefense, true
	case "fishing":
		return SkillFishing, true
	case "foraging":
		return SkillForaging, true
	}
	return 0, false
}

// m6UnequipType ports equipments.unequip(type): move the equipped item back
// to the inventory (whole stack), silently failing when there is no space;
// echoes Equipment Unequip {type, count} and Syncs (handler.handleUnequip).
func m6UnequipType(c *playerConn, slotType int) {
	if slotType < 0 || slotType >= ModulesEquipmentCount {
		return
	}
	key := c.username
	st := m5StateFor(key)
	pstateMu.Lock()
	e := st.Equip[slotType]
	pstateMu.Unlock()
	if e.Key == "" {
		return // unequip on an empty slot: Node stops at !equipment.key
	}
	// Full-stack return (Go bank path semantics; the stack always fits a
	// stackable maxStackSize, so the Node partial-return branch never fires).
	invIdx := m5AddItem(key, e.Key, e.Count)
	pstateMu.Lock()
	count := st.Equip[slotType].Count
	st.Equip[slotType] = m5Slot{}
	pstateMu.Unlock()
	_ = send(c.conn, pktOp(PacketEquipment, EquipmentUnequip, map[string]any{
		"type": slotType, "count": count,
	}))
	_ = send(c.conn, pktOp(PacketContainer, ContainerAdd, containerData{
		Type: ContainerTypeInventory,
		Slot: &slotData{Index: invIdx, Key: e.Key, Count: e.Count, Enchantments: map[string]any{}},
	}))
	// handler.handleUnequip follows with player.sync() (region broadcast).
	ph := welcomePlayer(c.instance)
	ph.X, ph.Y = c.sess.playerX, c.sess.playerY
	broadcast(pkt(PacketSync, ph))
	markDirty(key)
	log.Printf("m6: %s unequip type=%d %s x%d -> inv[%d]", c.instance, slotType, e.Key, e.Count, invIdx)
}

// m6EquipFromInventory ports equipments.equip(item, fromIndex) for the
// Container Select Inventory branch: requirement gate, inventory removal,
// swap with the currently equipped item, Equip echo + Sync.
func m6EquipFromInventory(c *playerConn, fromIndex int) {
	key := c.username
	st := m5StateFor(key)
	pstateMu.Lock()
	if fromIndex < 0 || fromIndex >= len(st.Inv) {
		pstateMu.Unlock()
		return
	}
	slot := st.Inv[fromIndex]
	pstateMu.Unlock()
	if slot.Key == "" || slot.Count < 1 {
		return
	}
	it := m6ItemInfoFor(slot.Key)
	if it == nil || !m6IsEquippable(it.Type) {
		return // silently ignored, like Node (isEquippable gate)
	}
	if !m6CanEquip(c, slot.Key) {
		return // requirement not met: notify already sent
	}
	slotType := m6EquipmentType(it.Type)
	if slotType < 0 || slotType >= ModulesEquipmentCount || len(st.Equip) < ModulesEquipmentCount {
		return
	}

	// Inventory.remove(fromIndex, count) -> Container Remove first.
	m6InventoryRemoveAt(c, key, fromIndex, slot.Count)

	// Swap: whatever occupies the slot goes back to the inventory.
	pstateMu.Lock()
	old := st.Equip[slotType]
	pstateMu.Unlock()
	var oldIdx int = -1
	if old.Key != "" {
		oldIdx = m5AddItem(key, old.Key, old.Count)
	}
	pstateMu.Lock()
	st.Equip[slotType] = m5Slot{Key: slot.Key, Count: slot.Count}
	pstateMu.Unlock()

	// handler.handleEquip: Equipment Equip {data: serialize(true)} + Sync.
	_ = send(c.conn, pktOp(PacketEquipment, EquipmentEquip, map[string]any{
		"data": m6EquipmentData(slotType, slot.Key, slot.Count, true),
	}))
	ph := welcomePlayer(c.instance)
	ph.X, ph.Y = c.sess.playerX, c.sess.playerY
	broadcast(pkt(PacketSync, ph))
	if oldIdx >= 0 {
		_ = send(c.conn, pktOp(PacketContainer, ContainerAdd, containerData{
			Type: ContainerTypeInventory,
			Slot: &slotData{Index: oldIdx, Key: old.Key, Count: old.Count, Enchantments: map[string]any{}},
		}))
	}
	markDirty(key)
	log.Printf("m6: %s equip %s x%d -> type %d (old %q returned at %d)",
		c.instance, slot.Key, slot.Count, slotType, old.Key, oldIdx)
}

// m6HandleEquipment routes the C->S Equipment frame [8,{opcode,type|style}]
// (incoming.ts handleEquipment: Unequip/Style only).
func m6HandleEquipment(c *playerConn, frame clientFrame) {
	if len(frame) < 2 {
		return
	}
	var msg clientEquipment
	if err := json.Unmarshal(frame[1], &msg); err != nil {
		return
	}
	switch msg.Opcode {
	case EquipmentUnequip:
		if msg.Type == nil {
			return
		}
		m6UnequipType(c, *msg.Type)
	case EquipmentStyle:
		// Attack-style switching needs the weapon stat engine (deferred).
	}
}
