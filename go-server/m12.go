// M12 slice — trade, crafting, enchanting (finishes the M6 economy half).
//
// Trade ports game/entity/character/player/trade.ts: mutual Request (1-tile
// range), Open for both parties, per-player offers (add/remove with the
// undroppable + count clamps), acceptance reset on any offer change, and the
// double-accept exchange (remove-before-add with space checks, empty-trade
// and flag handling). Frames ride PacketTrade [33]:
//
//	C->S Request{instance} Add{index,count} Remove{index} Accept{} Close{}
//	S->C Open{instance} Add{instance,index,count,key} Remove{instance,index}
//	    Accept{message} Close{}
//
// Crafting ports controllers/crafting.ts over data/crafting/*.json (8 skill
// files). PacketCrafting [54]: S Open{type,previews} / Select{key,name,level,
// result,requirements}; C Select{key} / Craft{key,count}. Failure rolls, the
// actualCount clamp, and the Smelting→Smithing / Chiseling→Crafting skill
// mapping mirror crafting.ts exactly.
//
// Enchanting ports controllers/enchanter.ts + item.ts getAvailableEnchant-
// ments + Formulas.getEnchantChance. PacketEnchant [34]: C Select{index} /
// Confirm{index,shardIndex}; S Select{index,isShard?}. The enchanter NPC
// (role "enchanter", e.g. vendingmachine) grants canAccessContainer + NPC
// Enchant [31,3] (handler.ts:730-741).
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
)

// ---------------------------------------------------------------------------
// Packet payload types (common/network/impl trade.ts / crafting.ts / enchant.ts).
// ---------------------------------------------------------------------------

type tradePacketData struct {
	Instance string `json:"instance,omitempty"`
	Index    *int   `json:"index,omitempty"`
	Count    *int   `json:"count,omitempty"`
	Key      string `json:"key,omitempty"`
	Message  string `json:"message,omitempty"`
}

type craftingPacketData struct {
	Type         *int                  `json:"type,omitempty"` // Modules.Skills interface id
	Previews     []m12CraftPreview     `json:"previews,omitempty"`
	Key          string                `json:"key,omitempty"`
	Name         string                `json:"name,omitempty"`
	Level        *int                  `json:"level,omitempty"`
	Result       *int                  `json:"result,omitempty"`
	Requirements []m12CraftRequirement `json:"requirements,omitempty"`
	Count        *int                  `json:"count,omitempty"`
}

type m12CraftPreview struct {
	Key   string `json:"key"`
	Level int    `json:"level"`
}

type m12CraftRequirement struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
	Name  string `json:"name,omitempty"`
}

type enchantPacketData struct {
	Index      *int `json:"index,omitempty"`
	ShardIndex *int `json:"shardIndex,omitempty"`
	IsShard    bool `json:"isShard,omitempty"`
}

// ---------------------------------------------------------------------------
// Data: data/crafting/*.json (CraftingInfo shape).
// ---------------------------------------------------------------------------

type m12CraftItem struct {
	Level        int                   `json:"level"`
	Experience   int                   `json:"experience"`
	Chance       *int                  `json:"chance"` // out of 100; unset = 100
	Requirements []m12CraftRequirement `json:"requirements"`
	Result       struct {
		Count int `json:"count"`
	} `json:"result"`
}

var (
	m12Once       sync.Once
	m12CraftData  = map[string]map[string]*m12CraftItem{} // skill file name -> key -> item
	m12CraftFiles int
)

// m12DataDir resolves a packages/server/data/<name> dir (same order m11 uses;
// m11DataDir itself needs a dir, so this mirrors it for reuse clarity).
func m12DataDir(name string) string {
	if p := os.Getenv("RES_" + name); p != "" {
		return p
	}
	for _, rel := range []string{
		filepath.Join("..", "packages", "server", "data", name),
		filepath.Join("..", "..", "packages", "server", "data", name),
	} {
		if st, err := os.Stat(rel); err == nil && st.IsDir() {
			return rel
		}
	}
	return name
}

// m12Load reads the crafting tables once (m12CraftFiles logged for the e2e).
func m12Load() {
	m12Once.Do(func() {
		dir := m12DataDir("crafting")
		entries, err := os.ReadDir(dir)
		if err != nil {
			log.Printf("m12: read crafting dir: %v (crafting disabled)", err)
			return
		}
		for _, f := range entries {
			if f.IsDir() || filepath.Ext(f.Name()) != ".json" {
				continue
			}
			skill := f.Name()[:len(f.Name())-5]
			raw, err := os.ReadFile(filepath.Join(dir, f.Name()))
			if err != nil {
				continue
			}
			table := map[string]*m12CraftItem{}
			if err := json.Unmarshal(raw, &table); err != nil {
				log.Printf("m12: parse crafting/%s: %v", f.Name(), err)
				continue
			}
			m12CraftData[skill] = table
			m12CraftFiles++
		}
		log.Printf("m12: crafting files=%d", m12CraftFiles)
	})
}

// m12SkillForInterface ports crafting.getSkillByInterface: Smelting rewards
// Smithing XP, Chiseling rewards Crafting XP.
func m12SkillForInterface(iface int) int {
	switch iface {
	case SkillSmelting:
		return SkillSmithing
	case SkillChiseling:
		return SkillCraftingS
	}
	return iface
}

// m12SkillFileName maps the interface id to its crafting/ file name.
func m12SkillFileName(iface int) string {
	switch iface {
	case SkillCooking:
		return "cooking"
	case SkillSmithing, SkillSmelting:
		return "smithing"
	case SkillCraftingS, SkillChiseling:
		return "crafting"
	case SkillFletching:
		return "fletching"
	case SkillAlchemy:
		return "alchemy"
	}
	return ""
}

// m12IfaceName returns the Modules.Skills label for notify strings.
func m12IfaceName(iface int) string {
	switch iface {
	case SkillCooking:
		return "cooking"
	case SkillSmithing:
		return "smithing"
	case SkillCraftingS:
		return "crafting"
	case SkillChiseling:
		return "chiseling"
	case SkillFletching:
		return "fletching"
	case SkillSmelting:
		return "smelting"
	case SkillAlchemy:
		return "alchemy"
	}
	return fmt.Sprintf("skill%d", iface)
}

// ---------------------------------------------------------------------------
// Trade (player/trade.ts).
// ---------------------------------------------------------------------------

type m12OfferedItem struct {
	InventoryIndex int
	MaxCount       int // slot count at offer time
	Key            string
	Count          int
	Ench           Enchantments // nil = plain (copy semantics, never shared)
}

// m12PlayerState carries the trade session + crafting interface (per player).
type m12PlayerState struct {
	Trades   map[string]*m12Trade // other instance -> session
	TradeReq map[string]string    // other instance -> who requested last
	Iface    int                  // activeCraftingInterface (-1 none)
}

var (
	m12StateMu sync.Mutex
	m12States  = map[string]*m12PlayerState{}
)

// m12StateFor returns the M12 per-player state (lazily created).
func m12StateFor(key string) *m12PlayerState {
	m12StateMu.Lock()
	defer m12StateMu.Unlock()
	return m12StateLocked(key)
}

// m12StateLocked is m12StateFor for callers already holding m12StateMu
// (the mutex is NOT reentrant — never call m12StateFor under it).
func m12StateLocked(key string) *m12PlayerState {
	st, ok := m12States[key]
	if !ok {
		st = &m12PlayerState{Trades: map[string]*m12Trade{}, TradeReq: map[string]string{}, Iface: -1}
		m12States[key] = st
	}
	return st
}

// m12ForgetSession drops trade state when a connection leaves (world.ts
// clearActiveTrade parity happens via close; stale map entries are dropped).
func m12ForgetSession(key string) {
	m12StateMu.Lock()
	delete(m12States, key)
	m12StateMu.Unlock()
}

// m12Trade is one side of a session; other strings the peer's username.
type m12Trade struct {
	other  string
	offers map[int]*m12OfferedItem
	accept bool
	done   bool // exchange started (inProgress)
}

func m12Other(c *playerConn) *playerConn {
	st := m12StateFor(c.username)
	// Find the peer connection by username.
	if t, ok := st.Trades[c.instance]; ok && t != nil {
		return connByUsername(t.other)
	}
	// Session keyed by the peer's instance as seen from this side.
	if t, ok := st.Trades[m12PeerInst(c)]; ok {
		return connByUsername(t.other)
	}
	return nil
}

// m12PeerInst returns the instance the current trade was opened under.
func m12PeerInst(c *playerConn) string {
	st := m12StateFor(c.username)
	for inst, t := range st.Trades {
		if t != nil {
			return inst
		}
	}
	return ""
}

// connByUsername finds the live connection for a username (trade peers are
// players, so usernames are unique among connections here).
func connByUsername(name string) *playerConn {
	playersMu.Lock()
	defer playersMu.Unlock()
	for _, c := range players {
		if c.username == name {
			return c
		}
	}
	return nil
}

// m12Pair returns both sides of the session in (me, peer) order plus the peer
// state; nil when the peer disconnected (session torn down).
func m12Pair(c *playerConn) (*m12Trade, *playerConn) {
	st := m12StateFor(c.username)
	m12StateMu.Lock()
	var t *m12Trade
	var peerInst string
	for inst, tr := range st.Trades {
		if tr != nil {
			t, peerInst = tr, inst
			break
		}
	}
	m12StateMu.Unlock()
	if t == nil {
		return nil, nil
	}
	peer := connByInstance(peerInst)
	if peer == nil {
		// Fall back to username lookup (instance maps churn on reconnect).
		peer = connByUsername(t.other)
	}
	if peer == nil {
		return nil, nil
	}
	return t, peer
}

func m12NotifyTrade(c *playerConn, message string) {
	m6Notify(c, message)
}

// m12SignalAdd relays an offer add to both parties (trade.signalAdd).
func m12SignalAdd(me, peer *playerConn, fromInst string, offerIndex, count int, key string) {
	payload := tradePacketData{Instance: fromInst, Index: &offerIndex, Count: &count, Key: key}
	_ = send(me.conn, pktOp(PacketTrade, TradeAdd, payload))
	_ = send(peer.conn, pktOp(PacketTrade, TradeAdd, payload))
}

// m12SignalRemove relays an offer removal to both parties.
func m12SignalRemove(me, peer *playerConn, fromInst string, offerIndex int) {
	payload := tradePacketData{Instance: fromInst, Index: &offerIndex}
	_ = send(me.conn, pktOp(PacketTrade, TradeRemove, payload))
	_ = send(peer.conn, pktOp(PacketTrade, TradeRemove, payload))
}

// m12SignalAccept relays acceptance to both parties (acceptCallback(message)).
func m12SignalAccept(me, peer *playerConn, message string) {
	_ = send(me.conn, pktOp(PacketTrade, TradeAccept, tradePacketData{Message: message}))
	_ = send(peer.conn, pktOp(PacketTrade, TradeAccept, tradePacketData{Message: message}))
}

// m12TradeClose closes the session for both parties (trade.close).
func m12TradeClose(me *playerConn) {
	t, peer := m12Pair(me)
	if t == nil {
		// No active session: still echo Close for client parity (incoming
		// Close calls trade.close() which early-returns without a frame).
		return
	}
	_ = send(me.conn, pktOp(PacketTrade, TradeClose, tradePacketData{}))
	if peer != nil {
		_ = send(peer.conn, pktOp(PacketTrade, TradeClose, tradePacketData{}))
	}
	m12ClearSession(me, peer)
}

// m12ClearSession drops both sides' session state.
func m12ClearSession(me, peer *playerConn) {
	myInst := m12PeerInst(me)
	m12StateMu.Lock()
	delete(m12StateLocked(me.username).Trades, myInst)
	m12StateMu.Unlock()
	if peer != nil {
		peerInst := m12PeerInst(peer)
		m12StateMu.Lock()
		delete(m12StateLocked(peer.username).Trades, peerInst)
		m12StateMu.Unlock()
	}
}

// m12TradeRequest ports trade.request: 1-tile range, hollow-admin/cheater
// guards are stubbed out (no such flags in the Go slice); mutual requests
// open the session, otherwise notify both parties of the pending request.
func m12TradeRequest(c *playerConn, targetInst string) {
	peer := connByInstance(targetInst)
	if peer == nil || peer == c {
		return
	}
	// getDistance > 1 (Chebyshev) rejects.
	dx, dy := c.sess.playerX-peer.sess.playerX, c.sess.playerY-peer.sess.playerY
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	if dx > 1 || dy > 1 {
		return
	}
	if m12StateFor(peer.username).TradeReq[c.instance] == c.instance || m12StateFor(peer.username).TradeReq[m12PeerInst(c)] == c.instance {
		m12TradeOpen(c, peer)
		return
	}
	// trade.lastRequest = target.instance (mutual request uses either key).
	st := m12StateFor(c.username)
	st.TradeReq[peer.instance] = peer.instance
	m12NotifyTrade(peer, fmt.Sprintf("misc:TRADE_REQUEST_OTHER;username=%s", c.username))
	m12NotifyTrade(c, fmt.Sprintf("misc:TRADE_REQUEST;username=%s", peer.username))
}

// m12TradeOpen ports trade.open: Trade Open to both (each with the other's
// instance), active sessions both ways, lastRequest cleared.
func m12TradeOpen(me, peer *playerConn) {
	log.Printf("m12: opening trade between %s and %s", me.username, peer.username)
	meInst, peerInst := me.instance, peer.instance
	_ = send(me.conn, pktOp(PacketTrade, TradeOpen, tradePacketData{Instance: peerInst}))
	_ = send(peer.conn, pktOp(PacketTrade, TradeOpen, tradePacketData{Instance: meInst}))
	m12StateMu.Lock()
	meSt := m12StateLocked(me.username)
	peerSt := m12StateLocked(peer.username)
	meSt.Trades[peerInst] = &m12Trade{other: peer.username, offers: map[int]*m12OfferedItem{}}
	peerSt.Trades[meInst] = &m12Trade{other: me.username, offers: map[int]*m12OfferedItem{}}
	delete(meSt.TradeReq, peerInst)
	delete(peerSt.TradeReq, meInst)
	m12StateMu.Unlock()
}

// m12TradeAdd ports trade.add: inventory slot -> offer slot (offerIndex ==
// inventory index in the Go port: our offers are keyed by the inventory slot
// they came from, which is what the client menu displays anyway).
func m12TradeAdd(c *playerConn, index, count int) {
	t, peer := m12Pair(c)
	if t == nil || peer == nil || t.done {
		return
	}
	if count < 1 {
		return // isNaN/count<1 guard
	}
	if t.offers[index] != nil && t.offers[index].Count == t.offers[index].MaxCount {
		return // isAdded: fully offered already
	}
	st := m5StateFor(c.username)
	pstateMu.Lock()
	if index < 0 || index >= len(st.Inv) {
		pstateMu.Unlock()
		return
	}
	slot := st.Inv[index]
	pstateMu.Unlock()
	if slot.Key == "" {
		return
	}
	// Undroppable items cannot be traded (misc:CANNOT_TRADE_ITEM).
	info := m6ItemInfoFor(slot.Key)
	if info != nil && info.Undroppable {
		m12NotifyTrade(c, "misc:CANNOT_TRADE_ITEM")
		return
	}
	offer, existed := t.offers[index]
	if !existed || offer == nil {
		// New offer: clamp to the slot count.
		offerCount := count
		if offerCount > slot.Count {
			offerCount = slot.Count
		}
		t.offers[index] = &m12OfferedItem{
			InventoryIndex: index, MaxCount: slot.Count, Key: slot.Key,
			Count: offerCount, Ench: m12CopyEnch(slot.Ench),
		}
		m12ResetAccept(t, c, peer)
		m12SignalAdd(c, peer, c.instance, index, offerCount, slot.Key)
		return
	}
	// Existing offer for the same slot: stack onto it up to maxCount.
	newCount := offer.Count + count
	if newCount > offer.MaxCount {
		newCount = offer.MaxCount
	}
	offer.Count = newCount
	m12ResetAccept(t, c, peer)
	m12SignalAdd(c, peer, c.instance, index, newCount, slot.Key)
}

// m12CopyEnch clones an enchantment map (item.copy parity: never share the
// map between slots).
func m12CopyEnch(e Enchantments) Enchantments {
	if e == nil {
		return nil
	}
	out := Enchantments{}
	for k, v := range e {
		out[k] = v
	}
	return out
}

// m12ResetAccept clears both sides' acceptance (any offer change).
func m12ResetAccept(t *m12Trade, me, peer *playerConn) {
	t.accept = false
	if pt := m12StateFor(peer.username); pt != nil {
		m12StateMu.Lock()
		for _, other := range pt.Trades {
			if other != nil {
				other.accept = false
			}
		}
		m12StateMu.Unlock()
	}
}

// m12TradeRemove ports trade.remove.
func m12TradeRemove(c *playerConn, index int) {
	t, peer := m12Pair(c)
	if t == nil || peer == nil || t.done {
		return
	}
	if t.offers[index] == nil {
		return
	}
	delete(t.offers, index)
	m12ResetAccept(t, c, peer)
	m12SignalRemove(c, peer, c.instance, index)
}

// m12TradeAccept ports trade.accept: single accept relays ACCEPTED_TRADE to
// both; double accept runs the space check + exchange.
func m12TradeAccept(c *playerConn) {
	t, peer := m12Pair(c)
	if t == nil || peer == nil || t.done {
		return
	}
	pt := m12StateFor(peer.username)
	m12StateMu.Lock()
	var other *m12Trade
	for _, tr := range pt.Trades {
		if tr != nil {
			other = tr
		}
	}
	m12StateMu.Unlock() // pt fetched before the lock (non-reentrant mutex)
	if other == nil {
		return
	}
	if other.accept {
		// Both accepted: exchange (inProgress guard).
		if t.done {
			return
		}
		m12TradeExchange(c, t, peer, other)
		return
	}
	t.accept = true
	m12SignalAccept(c, peer, "misc:ACCEPTED_TRADE")
	m12SignalAccept(peer, c, "misc:ACCEPTED_TRADE_OTHER")
}

// m12EmptySlots counts free inventory slots (inventory.getEmptySlots).
func m12EmptySlots(key string) int {
	st := m5StateFor(key)
	pstateMu.Lock()
	defer pstateMu.Unlock()
	return ModulesInventorySize - len(st.Inv)
}

// m12CountItem counts total items of key across slots (inventory.count).
func m12CountItem(key, itemKey string) int {
	st := m5StateFor(key)
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

// m12RemoveItemsBefore removes both sides' offered items from their
// inventories; returns flagged (inventory.hasItem failed mid-way).
func m12RemoveItemsBefore(me, peer *playerConn, t, other *m12Trade) bool {
	flagged := false
	for _, o := range t.offers {
		if o == nil || flagged {
			continue
		}
		if m12CountItem(me.username, o.Key) < o.Count {
			flagged = true
			break
		}
		m6RemoveItem(me.username, o.Key, o.Count)
	}
	for _, o := range other.offers {
		if o == nil || flagged {
			continue
		}
		if m12CountItem(peer.username, o.Key) < o.Count {
			flagged = true
			break
		}
		m6RemoveItem(peer.username, o.Key, o.Count)
	}
	return flagged
}

// m12TradeExchange ports trade.exchange: remove-before-add, space checks,
// empty-trade notify, then close.
func m12TradeExchange(me *playerConn, t *m12Trade, peer *playerConn, other *m12Trade) {
	t.done = true
	other.done = true

	// Space check (accept() diff logic): positive diff = we offer more.
	mySlots := m12EmptySlots(me.username)
	peerSlots := m12EmptySlots(peer.username)
	myCount := m12TotalOfferedCount(t)
	peerCount := m12TotalOfferedCount(other)
	diff := myCount - peerCount
	if diff != mySlots {
		if diff < 0 && mySlots < -diff {
			m12ResetAccept(t, me, peer)
			m12SignalAccept(me, peer, "")
			m12NotifyTrade(peer, "misc:NO_SPACE_OTHER")
			m12NotifyTrade(me, "misc:NO_SPACE")
			m12TradeClose(me)
			return
		}
		if diff > 0 && peerSlots < diff {
			m12ResetAccept(t, me, peer)
			m12SignalAccept(me, peer, "")
			m12NotifyTrade(peer, "misc:NO_SPACE")
			m12NotifyTrade(me, "misc:NO_SPACE_OTHER")
			m12TradeClose(me)
			return
		}
	}

	if m12RemoveItemsBefore(me, peer, t, other) {
		m12NotifyTrade(me, "misc:PLEASE_REPORT_BUG")
		m12NotifyTrade(peer, "misc:PLEASE_REPORT_BUG")
		log.Printf("m12: trade exchange failed for %s and %s", me.username, peer.username)
		m12TradeClose(me)
		return
	}

	total := 0
	for _, o := range other.offers { // peer's offers -> my inventory
		if o == nil {
			continue
		}
		idx := m5AddItemEnch(me.username, o.Key, o.Count, o.Ench)
		_ = send(me.conn, pktOp(PacketContainer, ContainerAdd, containerData{
			Type: ContainerTypeInventory,
			Slot: &slotData{Index: idx, Key: o.Key, Count: o.Count, Enchantments: enchAny(o.Ench)},
		}))
		total++
	}
	for _, o := range t.offers { // my offers -> peer inventory
		if o == nil {
			continue
		}
		idx := m5AddItemEnch(peer.username, o.Key, o.Count, o.Ench)
		_ = send(peer.conn, pktOp(PacketContainer, ContainerAdd, containerData{
			Type: ContainerTypeInventory,
			Slot: &slotData{Index: idx, Key: o.Key, Count: o.Count, Enchantments: enchAny(o.Ench)},
		}))
		total++
	}
	markDirty(me.username)
	markDirty(peer.username)

	if total == 0 {
		m12NotifyTrade(me, "misc:TRADE_EMPTY")
		m12NotifyTrade(peer, "misc:TRADE_EMPTY")
	} else {
		m12NotifyTrade(me, "misc:TRADE_COMPLETE")
		m12NotifyTrade(peer, "misc:TRADE_COMPLETE")
		log.Printf("m12: trade %s <-> %s (%d items)", me.username, peer.username, total)
	}
	m12TradeClose(me)
}

// m12TotalOfferedCount ports getTotalOfferedCount (distinct offer slots).
func m12TotalOfferedCount(t *m12Trade) int {
	n := 0
	for _, o := range t.offers {
		if o != nil {
			n++
		}
	}
	return n
}

// m12OfferList returns the non-nil offers sorted by inventory index (stable
// exchange order).
func m12OfferList(t *m12Trade) []*m12OfferedItem {
	out := make([]*m12OfferedItem, 0, len(t.offers))
	for _, o := range t.offers {
		if o != nil {
			out = append(out, o)
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].InventoryIndex < out[j-1].InventoryIndex; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Crafting (controllers/crafting.ts).
// ---------------------------------------------------------------------------

// m12CraftOpen ports crafting.open: previews for the interface's skill file +
// activeCraftingInterface = type.
func m12CraftOpen(c *playerConn, iface int) {
	m12Load()
	table := m12CraftData[m12SkillFileName(iface)]
	if table == nil {
		m12NotifyTrade(c, "misc:CANNOT_DO_THAT")
		return
	}
	m12StateFor(c.username).Iface = iface
	previews := make([]m12CraftPreview, 0, len(table))
	for key, item := range table {
		previews = append(previews, m12CraftPreview{Key: key, Level: item.Level})
	}
	for i := 1; i < len(previews); i++ { // stable key order (deterministic e2e)
		for j := i; j > 0 && previews[j].Key < previews[j-1].Key; j-- {
			previews[j], previews[j-1] = previews[j-1], previews[j]
		}
	}
	_ = send(c.conn, pktOp(PacketCrafting, CraftingOpen, craftingPacketData{
		Type: &iface, Previews: previews,
	}))
}

// m12CraftSelect ports crafting.select: requirements + result for one key.
func m12CraftSelect(c *playerConn, key string) {
	m12Load()
	iface := m12StateFor(c.username).Iface
	table := m12CraftData[m12SkillFileName(iface)]
	if table == nil {
		m12NotifyTrade(c, "crafting:INVALID_DATA")
		return
	}
	item := table[key]
	if item == nil {
		m12NotifyTrade(c, "crafting:INVALID_ITEM")
		return
	}
	reqs := make([]m12CraftRequirement, len(item.Requirements))
	for i, r := range item.Requirements {
		reqs[i] = r
		reqs[i].Name = m6ItemName(r.Key)
	}
	result := item.Result.Count
	_ = send(c.conn, pktOp(PacketCrafting, CraftingSelect, craftingPacketData{
		Key: key, Name: m6ItemName(key), Level: &item.Level,
		Result: &result, Requirements: reqs,
	}))
}

// m12Craft ports crafting.craft: level gate, actualCount clamp, failure rolls
// (Utils.randomInt(0,100) > (chance||100)+level per crafted unit), XP, result.
func m12Craft(c *playerConn, key string, count int) {
	m12Load()
	iface := m12StateFor(c.username).Iface
	if iface == -1 {
		m12NotifyTrade(c, "misc:CANNOT_DO_THAT")
		return
	}
	if count != 1 && count != 5 && count != 10 {
		count = 1
	}
	table := m12CraftData[m12SkillFileName(iface)]
	if table == nil {
		m12NotifyTrade(c, "crafting:INVALID_DATA")
		return
	}
	item := table[key]
	if item == nil {
		m12NotifyTrade(c, "crafting:INVALID_ITEM")
		return
	}
	st := m5StateFor(c.username)
	skill := m12SkillForInterface(iface)
	level := 1
	pstateMu.Lock()
	if s := st.Skills[skill]; s != nil {
		level = s.Level
	}
	pstateMu.Unlock()
	if level < item.Level {
		m12NotifyTrade(c, fmt.Sprintf("crafting:INVALID_LEVEL;skillText=%s;levelText=%d", m12IfaceName(iface), item.Level))
		return
	}
	for _, r := range item.Requirements {
		if m12CountItem(c.username, r.Key) < r.Count {
			m12NotifyTrade(c, "crafting:INVALID_ITEMS")
			return
		}
	}
	actual := count
	for _, r := range item.Requirements {
		have := m12CountItem(c.username, r.Key)
		if r.Count*count > have {
			actual = have / r.Count
		}
	}
	if actual < 1 {
		m12NotifyTrade(c, "crafting:INVALID_ITEMS")
		return
	}
	for _, r := range item.Requirements {
		m6RemoveItem(c.username, r.Key, r.Count*actual)
	}
	chance := 100
	if item.Chance != nil && *item.Chance > 0 {
		chance = *item.Chance
	}
	failures := 0
	for i := 0; i < actual; i++ {
		// Utils.randomInt(0,100): inclusive both ends.
		if rand.Intn(101) > chance+level {
			failures++
		}
	}
	actual -= failures
	if failures > 0 {
		if failures > 1 {
			m12NotifyTrade(c, fmt.Sprintf("crafting:FAILED_CRAFT;failedText=%dx %s.", failures, m6ItemName(key)))
		} else {
			m12NotifyTrade(c, "crafting:FAILED_CRAFT_ONE")
		}
	}
	m5AddXP(c, c.username, skill, item.Experience*actual)
	for i := 0; i < actual; i++ {
		idx := m5AddItem(c.username, key, item.Result.Count)
		_ = send(c.conn, pktOp(PacketContainer, ContainerAdd, containerData{
			Type: ContainerTypeInventory,
			Slot: &slotData{Index: idx, Key: key, Count: item.Result.Count, Enchantments: map[string]any{}},
		}))
	}
	markDirty(c.username)
	log.Printf("m12: %s crafted %s x%d (failures %d, skill %d)", c.username, key, actual*item.Result.Count, failures, skill)
}

// ---------------------------------------------------------------------------
// Enchanting (controllers/enchanter.ts + item.ts + formulas.getEnchantChance).
// ---------------------------------------------------------------------------

// m12EnchantableTypes mirrors item.isEquippable's type list minus arrows
// (arrows stack -> count>1 check rejects them anyway; keep the list exact).
var m12EnchantableTypes = map[string]bool{
	"helmet": true, "chestplate": true, "legplates": true, "skin": true,
	"weapon": true, "weaponarcher": true, "weaponmagic": true, "weaponskin": true,
	"pendant": true, "boots": true, "ring": true, "arrow": true, "shield": true, "cape": true,
}

// m12AvailableEnchantments mirrors item.getAvailableEnchantments.
func m12AvailableEnchantments(itemType string) []int {
	switch itemType {
	case "weapon":
		return []int{0, 1, 8} // Bloodsucking, Critical, DoubleEdged
	case "weaponarcher", "weaponmagic":
		return []int{4, 5} // Explosive, Stun
	case "armour", "armourarcher":
		return []int{2, 3, 6} // Evasion, Thorns, AntiStun
	}
	return nil
}

// m12EnchantChance mirrors Formulas.getEnchantChance: randomInt(0,100) < 8*tier.
func m12EnchantChance(tier int) bool {
	return rand.Intn(101) < 8*tier
}

// m12OpenEnchanter ports the handler.ts enchanter NPC branch: container
// access + NPC Enchant [31,3].
func m12OpenEnchanter(c *playerConn) {
	c.canAccessContainer = true
	_ = send(c.conn, pktOp(PacketNPC, NPCEnchant, map[string]any{}))
}

// m12EnchantSelect ports enchanter.select.
func m12EnchantSelect(c *playerConn, index int) {
	if index == -1 {
		m12NotifyTrade(c, "enchant:CANNOT_ENCHANT")
		return
	}
	st := m5StateFor(c.username)
	pstateMu.Lock()
	var key string
	var count int
	if index >= 0 && index < len(st.Inv) {
		key, count = st.Inv[index].Key, st.Inv[index].Count
	}
	pstateMu.Unlock()
	if key == "" {
		m12NotifyTrade(c, "enchant:CANNOT_ENCHANT")
		return
	}
	if len(key) >= 6 && key[:6] == "shardt" {
		_ = send(c.conn, pktOp(PacketEnchant, EnchantSelect, enchantPacketData{Index: &index, IsShard: true}))
		return
	}
	info := m6ItemInfoFor(key)
	if info == nil || count > 1 || !m12EnchantableTypes[info.Type] || m6MaxStack(key) > 1 {
		m12NotifyTrade(c, "enchant:CANNOT_ENCHANT")
		return
	}
	if len(m12AvailableEnchantments(info.Type)) == 0 {
		m12NotifyTrade(c, "enchant:CANNOT_ENCHANT")
		return
	}
	_ = send(c.conn, pktOp(PacketEnchant, EnchantSelect, enchantPacketData{Index: &index}))
}

// m12EnchantConfirm ports enchanter.enchant: shard tier chance roll, shard
// consume, random enchantment + level, apply-when-higher, inventory resync.
func m12EnchantConfirm(c *playerConn, index, shardIndex int) {
	if index == -1 || shardIndex == -1 {
		m12NotifyTrade(c, "enchant:NO_ITEM_SELECTED")
		return
	}
	st := m5StateFor(c.username)
	pstateMu.Lock()
	var itemKey, shardKey string
	var itemEnch Enchantments
	if index >= 0 && index < len(st.Inv) {
		itemKey, itemEnch = st.Inv[index].Key, st.Inv[index].Ench
	}
	if shardIndex >= 0 && shardIndex < len(st.Inv) {
		shardKey = st.Inv[shardIndex].Key
	}
	pstateMu.Unlock()
	if itemKey == "" || shardKey == "" {
		m12NotifyTrade(c, "enchant:NO_ITEM_SELECTED")
		return
	}
	if len(shardKey) < 6 || shardKey[:6] != "shardt" {
		m12NotifyTrade(c, "enchant:NO_SHARD")
		return
	}
	info := m6ItemInfoFor(itemKey)
	if info == nil {
		m12NotifyTrade(c, "enchant:NO_ITEM_SELECTED")
		return
	}
	enchantments := m12AvailableEnchantments(info.Type)
	if len(enchantments) == 0 {
		m12NotifyTrade(c, "enchant:NO_ITEM_SELECTED")
		return
	}
	tier := 1
	fmt.Sscanf(shardKey, "shardt%d", &tier)
	chance := m12EnchantChance(tier)

	// Remove one shard (inventory.remove(shardIndex, 1)).
	m6RemoveItemAt(c, c.username, shardIndex, 1)

	if !chance {
		m12NotifyTrade(c, "enchant:FAILED_ENCHANT")
	}
	// Random enchantment + level 1..tier (Utils.randomInt(1, tier)).
	enchantment := enchantments[rand.Intn(len(enchantments))]
	level := 1 + rand.Intn(tier)

	// canEnchant: apply only when new or strictly higher level.
	cur, has := itemEnch[enchantment]
	if has && level <= cur.Level {
		m12NotifyTrade(c, "enchant:FAILED_ENCHANT")
		// Failure still consumed the shard; the item keeps its old enchant.
	} else {
		if itemEnch == nil {
			itemEnch = Enchantments{}
		}
		itemEnch[enchantment] = Enchantment{Level: level}
		pstateMu.Lock()
		st.Inv[index].Ench = itemEnch
		pstateMu.Unlock()
		m12NotifyTrade(c, "enchant:SUCCESSFUL_ENCHANT")
		// Synchronize the slot (inventory.loadCallback parity: Container Add
		// with the updated enchantments overwrites the client slot).
		count := st.Inv[index].Count
		_ = send(c.conn, pktOp(PacketContainer, ContainerAdd, containerData{
			Type: ContainerTypeInventory,
			Slot: &slotData{Index: index, Key: itemKey, Count: count, Enchantments: enchAny(itemEnch)},
		}))
	}
	markDirty(c.username)
	log.Printf("m12: %s enchants %s with %s t%d -> ench %d level %d (roll %v)", c.username, itemKey, shardKey, tier, enchantment, level, chance)
}

// m6RemoveItemAt removes count from one inventory slot by index (enchanter
// uses the shard's slot index directly). Emits Container Remove.
func m6RemoveItemAt(c *playerConn, username string, index, count int) {
	m6InventoryRemoveAt(c, username, index, count)
}

// ---------------------------------------------------------------------------
// C->S dispatch (incoming.ts handleTrade/handleEnchant + crafting packets).
// ---------------------------------------------------------------------------

// m12HandleTrade ports incoming.handleTrade's opcode switch.
func m12HandleTrade(c *playerConn, data []byte) {
	var pkt struct {
		Opcode   int    `json:"opcode"`
		Instance string `json:"instance"`
		Index    *int   `json:"index"`
		Count    *int   `json:"count"`
	}
	if err := json.Unmarshal(data, &pkt); err != nil {
		return
	}
	index := 0
	if pkt.Index != nil {
		index = *pkt.Index
	}
	count := 1
	if pkt.Count != nil {
		count = *pkt.Count
	}
	switch pkt.Opcode {
	case TradeRequest:
		m12TradeRequest(c, pkt.Instance)
	case TradeAdd:
		m12TradeAdd(c, index, count)
	case TradeRemove:
		m12TradeRemove(c, index)
	case TradeAccept:
		m12TradeAccept(c)
	case TradeClose:
		m12TradeClose(c)
	}
}

// m12HandleEnchant ports incoming.handleEnchant (Select/Confirm; the TS
// handler has NO canAccessContainer gate — the client only opens the menu
// from the enchanter NPC; server checks live inside the enchanter itself).
func m12HandleEnchant(c *playerConn, data []byte) {
	var pkt struct {
		Opcode     int  `json:"opcode"`
		Index      *int `json:"index"`
		ShardIndex *int `json:"shardIndex"`
	}
	if err := json.Unmarshal(data, &pkt); err != nil {
		return
	}
	index, shard := -1, -1
	if pkt.Index != nil {
		index = *pkt.Index
	}
	if pkt.ShardIndex != nil {
		shard = *pkt.ShardIndex
	}
	switch pkt.Opcode {
	case EnchantSelect:
		m12EnchantSelect(c, index)
	case EnchantConfirm:
		m12EnchantConfirm(c, index, shard)
	}
}

// m12HandleCrafting ports incoming.handleCrafting (activeCraftingInterface
// gate + Select/Craft opcodes).
func m12HandleCrafting(c *playerConn, data []byte) {
	var pkt struct {
		Opcode int    `json:"opcode"`
		Key    string `json:"key"`
		Count  *int   `json:"count"`
	}
	if err := json.Unmarshal(data, &pkt); err != nil {
		return
	}
	if m12StateFor(c.username).Iface == -1 {
		m12NotifyTrade(c, "misc:CANNOT_DO_THAT")
		return
	}
	count := 1
	if pkt.Count != nil {
		count = *pkt.Count
	}
	switch pkt.Opcode {
	case CraftingSelect:
		m12CraftSelect(c, pkt.Key)
	case CraftingCraft:
		m12Craft(c, pkt.Key, count)
	}
}

// ---------------------------------------------------------------------------
// TESTMAP debug dispatcher [46 {m12test}] + player commands.
// ---------------------------------------------------------------------------

// m12HandleTest serves the e2e harness: state probes + seeding.
func m12HandleTest(c *playerConn, data []byte) {
	if !testMode {
		return
	}
	var d struct {
		M12Test string `json:"m12test"`
		Key     string `json:"key"`
		Count   int    `json:"count"`
		Iface   int    `json:"iface"`
		Slot    int    `json:"slot"`
		Echo    string `json:"echo"`
	}
	if err := json.Unmarshal(data, &d); err != nil || d.M12Test == "" || c == nil {
		return
	}
	m12Load()
	switch d.M12Test {
	case "craftif": // set activeCraftingInterface (mirrors /crafting open)
		m12StateFor(c.username).Iface = d.Iface
	case "seed": // seedItem: append a stack, reply the slot index
		idx := m6SeedItem(c.username, d.Key, maxInt(d.Count, 1))
		m6Notify(c, fmt.Sprintf("m12:seed:%s=%d", d.Key, idx))
	case "invcount": // inventory.count probe
		m6Notify(c, fmt.Sprintf("m12:count:%s=%d", d.Key, m12CountItem(c.username, d.Key)))
	case "invclear": // drop everything but gold (deterministic space checks)
		st := m5StateFor(c.username)
		pstateMu.Lock()
		out := st.Inv[:0]
		for _, s := range st.Inv {
			if s.Key == "gold" {
				out = append(out, s)
			}
		}
		st.Inv = out
		pstateMu.Unlock()
		markDirty(c.username)
		m6Notify(c, "m12:invclear=ok")
	case "echo":
		var reply string
		switch d.Echo {
		case "offers": // "m12:offers=me N peer N"
			t, peer := m12Pair(c)
			if t == nil || peer == nil {
				reply = "m12:offers=none"
				break
			}
			pt := m12StateFor(peer.username)
			reply = fmt.Sprintf("m12:offers=me %d peer %d", m12TotalOfferedCount(t), m12TotalOfferedCount(pt.Trades[m12PeerInst(c)]))
		case "accepted":
			t, _ := m12Pair(c)
			if t == nil {
				reply = "m12:accepted=none"
			} else {
				reply = fmt.Sprintf("m12:accepted=%v", t.accept)
			}
		case "ench": // enchantments of inventory slot d.Slot
			st := m5StateFor(c.username)
			pstateMu.Lock()
			ench := Enchantments{}
			if d.Slot >= 0 && d.Slot < len(st.Inv) && st.Inv[d.Slot].Ench != nil {
				ench = st.Inv[d.Slot].Ench
			}
			pstateMu.Unlock()
			raw, _ := json.Marshal(ench)
			reply = fmt.Sprintf("m12:ench:%d=%s", d.Slot, string(raw))
		}
		if reply != "" {
			m6Notify(c, reply)
		}
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// m12PlayerCommands ports the crafting interface open commands
// (commands.ts:1102-1121, names opencrafting/openalchemy/opencooking/
// opensmithing/opensmelting). Called from m7ParseCommand.
func m12PlayerCommands(c *playerConn, command string) {
	switch command {
	case "opencrafting":
		m12CraftOpen(c, SkillCraftingS)
	case "openalchemy":
		m12CraftOpen(c, SkillAlchemy)
	case "opencooking":
		m12CraftOpen(c, SkillCooking)
	case "opensmithing":
		m12CraftOpen(c, SkillSmithing)
	case "opensmelting":
		m12CraftOpen(c, SkillSmelting)
	}
}
