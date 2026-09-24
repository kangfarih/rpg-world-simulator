package controller

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"rpg-world-server/internal/protocol"
)

// ---------------------------------------------------------------------------
// Item-use plugin matrix + attack-style switching (Batch A).
// ---------------------------------------------------------------------------

type vitalsFake struct {
	hp, maxHP       int
	mana, maxMana   int
	poisoned        bool
	inCombat        bool
	effects         map[int]int64
	heals           [][2]int
	damages         []int
	appliedDuration map[int]int64
}

func newVitalsFake() *vitalsFake {
	return &vitalsFake{hp: 100, maxHP: 100, mana: 50, maxMana: 50, effects: map[int]int64{}, appliedDuration: map[int]int64{}}
}

func (v *vitalsFake) HeroHP(instance string) (int, int)     { return v.hp, v.maxHP }
func (v *vitalsFake) HeroMana(instance string) (int, int)   { return v.mana, v.maxMana }
func (v *vitalsFake) CurePoison(instance string)            { v.poisoned = false }
func (v *vitalsFake) InCombat(instance string) bool         { return v.inCombat }
func (v *vitalsFake) HasEffect(instance string, e int) bool { _, ok := v.effects[e]; return ok }

func (v *vitalsFake) HealHero(instance string, hpAmount, manaAmount int) {
	v.heals = append(v.heals, [2]int{hpAmount, manaAmount})
	v.hp += hpAmount
	if v.hp > v.maxHP {
		v.hp = v.maxHP
	}
	v.mana += manaAmount
	if v.mana > v.maxMana {
		v.mana = v.maxMana
	}
}

func (v *vitalsFake) DamageHero(instance string, dmg int) {
	if dmg < 0 {
		dmg = 0
	}
	v.damages = append(v.damages, dmg)
	v.hp -= dmg
	if v.hp < 0 {
		v.hp = 0
	}
}

func (v *vitalsFake) AddEffect(instance string, effect int, durationMs int64) {
	v.effects[effect] = durationMs
	v.appliedDuration[effect] = durationMs
}

func (v *vitalsFake) RemoveEffect(instance string, effect int) { delete(v.effects, effect) }

type econWorld struct{}

func (econWorld) EntityPos(instance string) (int, int, bool) { return 0, 0, false }
func (econWorld) SpawnNPCKey(instance string) (string, bool) { return "", false }
func (econWorld) ShowcaseKey(n int) (string, bool)           { return "", false }
func (econWorld) ShowcaseCount() int                         { return 0 }
func (econWorld) SyncFrame(instance string, x, y int) []any {
	return []any{protocol.PacketSync, map[string]any{"instance": instance}}
}

type econPeers struct{ *fakePeers }

func (econPeers) WithStoreOpen(key string) []EconomyConn { return nil }

func useDeps(s *fakeStore, b *fakeBus, v *vitalsFake, conns ...Conn) EconomyDeps {
	return EconomyDeps{
		Store: s, Bus: b, Peers: econPeers{newFakePeers(conns...)},
		Quests: nil, Pets: nil, World: econWorld{}, Vitals: v,
	}
}

func useIntp(v int) *int { return &v }

// notifTexts extracts Notification Text messages (controller.Notify sends
// [25,2,{message}] frames, not Bus.Notify calls).
func notifTexts(b *fakeBus, instance string) []string {
	var out []string
	for _, f := range b.sent[instance] {
		if f.id != protocol.PacketNotification || f.opcode != protocol.NotificationText {
			continue
		}
		var data struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(f.data, &data); err == nil {
			out = append(out, data.Message)
		}
	}
	return out
}

func hasNotifText(b *fakeBus, instance, substr string) bool {
	for _, m := range notifTexts(b, instance) {
		if len(m) >= len(substr) {
			for i := 0; i+len(substr) <= len(m); i++ {
				if m[i:i+len(substr)] == substr {
					return true
				}
			}
		}
	}
	return false
}

func resetUseState(username string) {
	edibleMu.Lock()
	delete(lastEdible, username)
	edibleMu.Unlock()
	ForgetAttackStyle(username)
}

func useCount(s *fakeStore, username, key string) int { return s.CountItem(username, key) }

func TestHealAtFullNotifiesNoConsume(t *testing.T) {
	s, b, v := newFakeStore(), newFakeBus(), newVitalsFake()
	c := &fakeConn{instance: "i1", username: "u1"}
	d := useDeps(s, b, v, c)
	s.inv["u1"] = []Slot{{Key: "burger", Count: 2}}
	resetUseState("u1")

	msg := &protocol.ClientContainer{Type: useIntp(protocol.ContainerTypeInventory), FromIndex: useIntp(0)}
	HandleContainerSelect(c, msg, d)

	if useCount(s, "u1", "burger") != 2 {
		t.Fatalf("burger count = %d, want 2 (no consume at full HP)", useCount(s, "u1", "burger"))
	}
	if !hasNotifText(b, "i1", "You are already at full health.") {
		t.Fatalf("notifs = %v, want full-health notify", b.notifs["i1"])
	}
	if len(v.heals) != 0 {
		t.Fatalf("heals = %v, want none", v.heals)
	}
}

func TestHealWhenHurtConsumesOne(t *testing.T) {
	s, b, v := newFakeStore(), newFakeBus(), newVitalsFake()
	v.hp = 50
	c := &fakeConn{instance: "i1", username: "u1"}
	d := useDeps(s, b, v, c)
	s.inv["u1"] = []Slot{{Key: "burger", Count: 2}}
	resetUseState("u1")

	msg := &protocol.ClientContainer{Type: useIntp(protocol.ContainerTypeInventory), FromIndex: useIntp(0)}
	HandleContainerSelect(c, msg, d)

	if useCount(s, "u1", "burger") != 1 {
		t.Fatalf("burger count = %d, want 1", useCount(s, "u1", "burger"))
	}
	if len(v.heals) != 1 || v.heals[0] != [2]int{200, 0} {
		t.Fatalf("heals = %v, want [{200 0}]", v.heals)
	}
	if v.hp != 100 {
		t.Fatalf("hp = %d, want 100 (clamped)", v.hp)
	}
	if s.xp["u1"][SkillEating] != 20 {
		t.Fatalf("eating xp = %d, want 20 (floor(200/10))", s.xp["u1"][SkillEating])
	}

	// Cooldown blocks the immediate second eat: count + heals unchanged.
	HandleContainerSelect(c, msg, d)
	if useCount(s, "u1", "burger") != 1 {
		t.Fatalf("burger count after cooldown eat = %d, want 1", useCount(s, "u1", "burger"))
	}
	if len(v.heals) != 1 {
		t.Fatalf("heals after cooldown eat = %v, want 1 entry", v.heals)
	}
}

func TestManaFullNotifiesNoConsume(t *testing.T) {
	s, b, v := newFakeStore(), newFakeBus(), newVitalsFake()
	c := &fakeConn{instance: "i1", username: "u1"}
	d := useDeps(s, b, v, c)
	s.inv["u1"] = []Slot{{Key: "manaflask", Count: 1}}
	resetUseState("u1")

	HandleContainerSelect(c, &protocol.ClientContainer{Type: useIntp(protocol.ContainerTypeInventory), FromIndex: useIntp(0)}, d)

	if useCount(s, "u1", "manaflask") != 1 {
		t.Fatal("manaflask consumed at full mana")
	}
	if !hasNotifText(b, "i1", "You are already at full mana.") {
		t.Fatalf("notifs = %v, want full-mana notify", b.notifs["i1"])
	}
}

func TestManaWhenLowConsumes(t *testing.T) {
	s, b, v := newFakeStore(), newFakeBus(), newVitalsFake()
	v.mana = 10
	c := &fakeConn{instance: "i1", username: "u1"}
	d := useDeps(s, b, v, c)
	s.inv["u1"] = []Slot{{Key: "manaflask", Count: 1}}
	resetUseState("u1")

	HandleContainerSelect(c, &protocol.ClientContainer{Type: useIntp(protocol.ContainerTypeInventory), FromIndex: useIntp(0)}, d)

	if useCount(s, "u1", "manaflask") != 0 {
		t.Fatal("manaflask not consumed")
	}
	if len(v.heals) != 1 || v.heals[0] != [2]int{0, 35} {
		t.Fatalf("heals = %v, want [{0 35}]", v.heals)
	}
	if v.mana != 45 {
		t.Fatalf("mana = %d, want 45", v.mana)
	}
}

func TestPoisonCureClearsAndConsumes(t *testing.T) {
	s, b, v := newFakeStore(), newFakeBus(), newVitalsFake()
	v.poisoned = true
	c := &fakeConn{instance: "i1", username: "u1"}
	d := useDeps(s, b, v, c)
	s.inv["u1"] = []Slot{{Key: "cure", Count: 1}}
	resetUseState("u1")

	HandleContainerSelect(c, &protocol.ClientContainer{Type: useIntp(protocol.ContainerTypeInventory), FromIndex: useIntp(0)}, d)

	if v.poisoned {
		t.Fatal("poison not cured")
	}
	if useCount(s, "u1", "cure") != 0 {
		t.Fatal("cure not consumed")
	}
}

func TestFirePotionClearsBurning(t *testing.T) {
	s, b, v := newFakeStore(), newFakeBus(), newVitalsFake()
	v.effects[EffectBurning] = 60_000
	c := &fakeConn{instance: "i1", username: "u1"}
	d := useDeps(s, b, v, c)
	s.inv["u1"] = []Slot{{Key: "firepotion", Count: 1}}
	resetUseState("u1")

	var scheduled []func()
	old := itemAfterFunc
	itemAfterFunc = func(dur time.Duration, f func()) *time.Timer {
		scheduled = append(scheduled, f)
		return nil
	}
	defer func() { itemAfterFunc = old }()

	HandleContainerSelect(c, &protocol.ClientContainer{Type: useIntp(protocol.ContainerTypeInventory), FromIndex: useIntp(0)}, d)

	if useCount(s, "u1", "firepotion") != 0 {
		t.Fatal("firepotion not consumed")
	}
	if v.HasEffect("i1", EffectBurning) {
		t.Fatal("burning not cleared")
	}
	if v.appliedDuration[EffectFirePotion] != 60_000 {
		t.Fatalf("firepotion duration = %d, want 60000", v.appliedDuration[EffectFirePotion])
	}
	if !hasNotifText(b, "i1", "misc:FIRE_IMMUNITY;duration=60") {
		t.Fatalf("notifs = %v, want FIRE_IMMUNITY", b.notifs["i1"])
	}
	if len(scheduled) != 1 {
		t.Fatalf("scheduled = %d, want 1 worn-off timer", len(scheduled))
	}
	// Effect still live at expiry: no worn-off notify.
	scheduled[0]()
	if b.hasNotif("i1", "misc:FIRE_IMMUNITY_WORN_OFF") {
		t.Fatal("worn-off notify fired while effect live")
	}
	// Effect expired: worn-off notify fires (timer path rides Bus.Notify,
	// the same m6Notify the live adapter fans out as a frame).
	delete(v.effects, EffectFirePotion)
	scheduled[0]()
	if !b.hasNotif("i1", "misc:FIRE_IMMUNITY_WORN_OFF") {
		t.Fatal("missing FIRE_IMMUNITY_WORN_OFF notify")
	}
}

func TestSnowPotionClearsFreezing(t *testing.T) {
	s, b, v := newFakeStore(), newFakeBus(), newVitalsFake()
	v.effects[EffectFreezing] = 60_000
	c := &fakeConn{instance: "i1", username: "u1"}
	d := useDeps(s, b, v, c)
	s.inv["u1"] = []Slot{{Key: "snowpotion", Count: 1}}
	resetUseState("u1")

	HandleContainerSelect(c, &protocol.ClientContainer{Type: useIntp(protocol.ContainerTypeInventory), FromIndex: useIntp(0)}, d)

	if v.HasEffect("i1", EffectFreezing) {
		t.Fatal("freezing not cleared")
	}
	if !v.HasEffect("i1", EffectSnowPotion) {
		t.Fatal("snowpotion effect missing")
	}
	if !hasNotifText(b, "i1", "misc:FREEZE_IMMUNITY;duration=60") {
		t.Fatalf("notifs = %v, want FREEZE_IMMUNITY", b.notifs["i1"])
	}
}

func TestBlackPotionDelayedSelfHit(t *testing.T) {
	s, b, v := newFakeStore(), newFakeBus(), newVitalsFake()
	v.hp = 50
	c := &fakeConn{instance: "i1", username: "u1"}
	d := useDeps(s, b, v, c)
	s.inv["u1"] = []Slot{{Key: "blackpotion", Count: 1}}
	resetUseState("u1")

	var scheduled []func()
	old := itemAfterFunc
	itemAfterFunc = func(dur time.Duration, f func()) *time.Timer {
		if dur != 5*time.Second {
			t.Fatalf("blackpotion delay = %v, want 5s", dur)
		}
		scheduled = append(scheduled, f)
		return nil
	}
	defer func() { itemAfterFunc = old }()

	HandleContainerSelect(c, &protocol.ClientContainer{Type: useIntp(protocol.ContainerTypeInventory), FromIndex: useIntp(0)}, d)

	if !hasNotifText(b, "i1", "misc:BLACK_POTION") {
		t.Fatalf("notifs = %v, want BLACK_POTION", b.notifs["i1"])
	}
	if len(scheduled) != 1 {
		t.Fatalf("scheduled = %d, want 1 delayed hit", len(scheduled))
	}
	scheduled[0]()
	if len(v.damages) != 1 || v.damages[0] != 49 {
		t.Fatalf("damages = %v, want [49] (hp-1)", v.damages)
	}
	if v.hp != 1 {
		t.Fatalf("hp = %d, want 1", v.hp)
	}
}

func TestEffectPotionBuff(t *testing.T) {
	s, b, v := newFakeStore(), newFakeBus(), newVitalsFake()
	c := &fakeConn{instance: "i1", username: "u1"}
	d := useDeps(s, b, v, c)
	s.inv["u1"] = []Slot{{Key: "accuracypotion", Count: 1}}
	resetUseState("u1")

	HandleContainerSelect(c, &protocol.ClientContainer{Type: useIntp(protocol.ContainerTypeInventory), FromIndex: useIntp(0)}, d)

	if !v.HasEffect("i1", EffectAccuracyBuff) {
		t.Fatal("accuracy buff missing")
	}
	if v.appliedDuration[EffectAccuracyBuff] != 60_000 {
		t.Fatalf("buff duration = %d, want 60000", v.appliedDuration[EffectAccuracyBuff])
	}
	if !hasNotifText(b, "i1", "You drink the accuracy potion.") {
		t.Fatalf("notifs = %v, want drink notify", b.notifs["i1"])
	}
	if useCount(s, "u1", "accuracypotion") != 0 {
		t.Fatal("accuracypotion not consumed")
	}
}

func TestHotSauceDuplicateBlocked(t *testing.T) {
	s, b, v := newFakeStore(), newFakeBus(), newVitalsFake()
	c := &fakeConn{instance: "i1", username: "u1"}
	d := useDeps(s, b, v, c)
	s.inv["u1"] = []Slot{{Key: "hotsauce", Count: 2}}
	resetUseState("u1")
	msg := &protocol.ClientContainer{Type: useIntp(protocol.ContainerTypeInventory), FromIndex: useIntp(0)}

	var scheduled []func()
	old := itemAfterFunc
	itemAfterFunc = func(dur time.Duration, f func()) *time.Timer {
		scheduled = append(scheduled, f)
		return nil
	}
	defer func() { itemAfterFunc = old }()

	HandleContainerSelect(c, msg, d)
	if !v.HasEffect("i1", EffectHotSauce) {
		t.Fatal("hotsauce effect missing")
	}
	if !hasNotifText(b, "i1", "You feel an intense rush of adrenaline") {
		t.Fatalf("notifs = %v, want adrenaline rush", b.notifs["i1"])
	}

	// Cooldown over, effect still live: duplicate blocked, kept.
	edibleMu.Lock()
	delete(lastEdible, "u1")
	edibleMu.Unlock()
	HandleContainerSelect(c, msg, d)
	if useCount(s, "u1", "hotsauce") != 1 {
		t.Fatalf("hotsauce count = %d, want 1 (duplicate kept)", useCount(s, "u1", "hotsauce"))
	}
	if !hasNotifText(b, "i1", "I really shouldn't be drinking multiple of these...") {
		t.Fatalf("notifs = %v, want duplicate notify", b.notifs["i1"])
	}

	// Timer clears both effects + faded notify.
	if len(scheduled) != 1 {
		t.Fatalf("scheduled = %d, want 1", len(scheduled))
	}
	scheduled[0]()
	if v.HasEffect("i1", EffectHotSauce) {
		t.Fatal("hotsauce effect not cleared by timer")
	}
	if !b.hasNotif("i1", "The hot sauce effect has faded.") {
		t.Fatal("missing faded notify")
	}
}

func craftingOpenType(b *fakeBus, instance string) (int, bool) {
	for _, f := range b.sent[instance] {
		if f.id != protocol.PacketCrafting || f.opcode != protocol.CraftingOpen {
			continue
		}
		var data struct {
			Type *int `json:"type"`
		}
		if err := json.Unmarshal(f.data, &data); err != nil || data.Type == nil {
			continue
		}
		return *data.Type, true
	}
	return 0, false
}

func TestKnifeOpensFletching(t *testing.T) {
	t.Setenv("RES_crafting", "/Users/appfuxion/repo/rpg-world-sim/packages/server/data/crafting")
	s, b, v := newFakeStore(), newFakeBus(), newVitalsFake()
	c := &fakeConn{instance: "i1", username: "u1"}
	d := useDeps(s, b, v, c)
	s.inv["u1"] = []Slot{{Key: "knife", Count: 1}}
	resetUseState("u1")

	HandleContainerSelect(c, &protocol.ClientContainer{Type: useIntp(protocol.ContainerTypeInventory), FromIndex: useIntp(0)}, d)

	iface, ok := craftingOpenType(b, "i1")
	if !ok || iface != protocol.SkillFletching {
		t.Fatalf("crafting open = %d,%v want fletching(%d)", iface, ok, protocol.SkillFletching)
	}
	if useCount(s, "u1", "knife") != 1 {
		t.Fatal("knife consumed (tools are never consumed)")
	}
}

func TestChiselOpensChiselingCombatGated(t *testing.T) {
	t.Setenv("RES_crafting", "/Users/appfuxion/repo/rpg-world-sim/packages/server/data/crafting")
	s, b, v := newFakeStore(), newFakeBus(), newVitalsFake()
	c := &fakeConn{instance: "i1", username: "u1"}
	d := useDeps(s, b, v, c)
	s.inv["u1"] = []Slot{{Key: "chisel", Count: 1}}
	resetUseState("u1")
	msg := &protocol.ClientContainer{Type: useIntp(protocol.ContainerTypeInventory), FromIndex: useIntp(0)}

	HandleContainerSelect(c, msg, d)
	iface, ok := craftingOpenType(b, "i1")
	if !ok || iface != protocol.SkillChiseling {
		t.Fatalf("crafting open = %d,%v want chiseling(%d)", iface, ok, protocol.SkillChiseling)
	}

	// In combat: gated with the TS-exact notify, tool kept.
	v.inCombat = true
	HandleContainerSelect(c, msg, d)
	if !hasNotifText(b, "i1", "You cannot activate the fletching menu while in combat.") {
		t.Fatalf("notifs = %v, want combat gate", b.notifs["i1"])
	}
	if useCount(s, "u1", "chisel") != 1 {
		t.Fatal("chisel consumed while gated")
	}
}

func TestBowlReturn(t *testing.T) {
	s, b, v := newFakeStore(), newFakeBus(), newVitalsFake()
	v.hp = 50
	v.maxHP = 1000 // clamchowder heals 750, stays hurt-adjacent
	c := &fakeConn{instance: "i1", username: "u1"}
	d := useDeps(s, b, v, c)
	s.inv["u1"] = []Slot{{Key: "clamchowder", Count: 1}}
	resetUseState("u1")

	HandleContainerSelect(c, &protocol.ClientContainer{Type: useIntp(protocol.ContainerTypeInventory), FromIndex: useIntp(0)}, d)

	if useCount(s, "u1", "clamchowder") != 0 {
		t.Fatal("clamchowder not consumed")
	}
	if useCount(s, "u1", "bowlsmall") != 1 {
		t.Fatalf("bowlsmall = %d, want 1", useCount(s, "u1", "bowlsmall"))
	}
	if !b.hasOpcode("i1", protocol.PacketContainer, protocol.ContainerAdd) {
		t.Fatal("missing Container Add bowl frame")
	}
}

func TestEquipStillWorksAfterPlugins(t *testing.T) {
	s, b, v := newFakeStore(), newFakeBus(), newVitalsFake()
	c := &fakeConn{instance: "i1", username: "u1"}
	d := useDeps(s, b, v, c)
	s.inv["u1"] = []Slot{{Key: "arrow", Count: 5}}
	resetUseState("u1")

	HandleContainerSelect(c, &protocol.ClientContainer{Type: useIntp(protocol.ContainerTypeInventory), FromIndex: useIntp(0)}, d)

	if !b.hasOpcode("i1", protocol.PacketEquipment, protocol.EquipmentEquip) {
		t.Fatal("missing Equipment Equip echo (equip path regressed)")
	}
	eq, ok := s.EquipSlot("u1", protocol.EquipmentArrows)
	if !ok || eq.Key != "arrow" {
		t.Fatalf("equip slot = %+v,%v want arrow", eq, ok)
	}
}

func styleEcho(b *fakeBus, instance string) (style, attackRange int, ok bool) {
	for _, f := range b.sent[instance] {
		if f.id != protocol.PacketEquipment || f.opcode != protocol.EquipmentStyle {
			continue
		}
		var data struct {
			AttackStyle *int `json:"attackStyle"`
			AttackRange *int `json:"attackRange"`
		}
		if err := json.Unmarshal(f.data, &data); err != nil || data.AttackStyle == nil || data.AttackRange == nil {
			continue
		}
		return *data.AttackStyle, *data.AttackRange, true
	}
	return 0, 0, false
}

func TestAttackStyleSwitchEcho(t *testing.T) {
	s, b, v := newFakeStore(), newFakeBus(), newVitalsFake()
	c := &fakeConn{instance: "i1", username: "u1", x: 100, y: 96}
	d := useDeps(s, b, v, c)
	s.SetEquip("u1", protocol.EquipmentWeapon, Slot{Key: "goldsword", Count: 1})
	resetUseState("u1")

	// C->S [8,{opcode:3,style:2}] routing (menu.ts handleProfileAttackStyle).
	HandleEquipment(c, []byte(`{"opcode":3,"style":2}`), d)

	style, attackRange, ok := styleEcho(b, "i1")
	if !ok || style != AttackStyleSlash || attackRange != 1 {
		t.Fatalf("style echo = %d,%d,%v want 2,1,true", style, attackRange, ok)
	}
	if !b.hasOpcode("broadcast", protocol.PacketSync, 0) {
		t.Fatal("missing Sync broadcast (player.sync parity)")
	}
	if got := AttackStyleFor(d, "u1"); got != AttackStyleSlash {
		t.Fatalf("AttackStyleFor = %d, want slash", got)
	}
}

func TestAttackStyleInvalidRejected(t *testing.T) {
	s, b, v := newFakeStore(), newFakeBus(), newVitalsFake()
	c := &fakeConn{instance: "i1", username: "u1"}
	d := useDeps(s, b, v, c)
	s.SetEquip("u1", protocol.EquipmentWeapon, Slot{Key: "goldsword", Count: 1})
	resetUseState("u1")

	// LongRange is not a sword style (hasAttackStyle gate parity).
	HandleEquipment(c, []byte(`{"opcode":3,"style":11}`), d)
	if _, _, ok := styleEcho(b, "i1"); ok {
		t.Fatal("echo for invalid style (gate regressed)")
	}
	if got := AttackStyleFor(d, "u1"); got != AttackStyleStab {
		t.Fatalf("AttackStyleFor = %d, want stab fallback (sword first style)", got)
	}
}

func TestAttackStyleNoWeaponRejected(t *testing.T) {
	s, b, v := newFakeStore(), newFakeBus(), newVitalsFake()
	c := &fakeConn{instance: "i1", username: "u1"}
	d := useDeps(s, b, v, c)
	resetUseState("u1")

	HandleEquipment(c, []byte(`{"opcode":3,"style":2}`), d)
	if _, _, ok := styleEcho(b, "i1"); ok {
		t.Fatal("echo with no weapon equipped")
	}
}

func TestStyleDamageMult(t *testing.T) {
	cases := map[int]float64{
		AttackStyleSlash:     1.1,
		AttackStyleFocused:   1.1,
		AttackStyleCrush:     1.05,
		AttackStyleHack:      1.05,
		AttackStyleShared:    1.03,
		AttackStyleNone:      1.0,
		AttackStyleStab:      1.0,
		AttackStyleDefensive: 1.0,
		AttackStyleAccurate:  1.0,
		AttackStyleFast:      1.0,
		AttackStyleLongRange: 1.0,
	}
	for style, want := range cases {
		if got := StyleDamageMult(style); math.Abs(got-want) > 1e-9 {
			t.Fatalf("StyleDamageMult(%d) = %v, want %v", style, got, want)
		}
	}
}

func TestAttackStyleLongRangeBowRange(t *testing.T) {
	s, b, v := newFakeStore(), newFakeBus(), newVitalsFake()
	c := &fakeConn{instance: "i1", username: "u1"}
	d := useDeps(s, b, v, c)
	s.SetEquip("u1", protocol.EquipmentWeapon, Slot{Key: "woodenbow", Count: 1})
	resetUseState("u1")

	HandleEquipment(c, []byte(`{"opcode":3,"style":11}`), d)
	style, attackRange, ok := styleEcho(b, "i1")
	if !ok || style != AttackStyleLongRange || attackRange != ArcherAttackRange+2 {
		t.Fatalf("bow longrange echo = %d,%d,%v want 11,10,true", style, attackRange, ok)
	}
}
