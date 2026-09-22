// Ability + status wiring — thin root adapter over internal/abilities.
//
// Canonical owner: internal/abilities (session.go: ability-session state,
// grant/use gating, DoT ticks, TESTMAP debug ops; abilities.go: registry;
// internal/status: tracker). This file only wires the package seams to the
// root globals (abilities table over dbConn, m5/m6/m9 helpers, gnet +
// worldcore transport) and keeps the entry points main.go/m5.go/m9.go/m11.go
// call — with UNCHANGED signatures — delegating to the package. Packet
// shapes, log strings and debug ops are identical (owned by the package).
package server

import (
	"rpg-world-server/internal/abilities"
	gnet "rpg-world-server/internal/net"
	worldcore "rpg-world-server/internal/world"
)

// Ability opcodes (Opcodes.Ability in opcodes.ts): Batch0 Add1 Update2
// Use3 QuickSlot4 Toggle5.
const (
	AbilityBatch     = abilities.AbilityBatch
	AbilityAdd       = abilities.AbilityAdd
	AbilityUpdate    = abilities.AbilityUpdate
	AbilityUse       = abilities.AbilityUse
	AbilityQuickSlot = abilities.AbilityQuickSlot
	AbilityToggle    = abilities.AbilityToggle
)

// Modules.AbilityType (modules.ts): Active0 Passive1.
const (
	AbilityTypeActive  = abilities.AbilityTypeActive
	AbilityTypePassive = abilities.AbilityTypePassive
)

// abConn converts a root conn to the package view (nil-safe; quest reward
// paths pass nil). Delivery stays direct (gnet.Send on the live socket,
// m6Notify on the same conn — never re-resolved).
func abConn(c *playerConn) *abilities.Conn {
	if c == nil {
		return nil
	}
	return &abilities.Conn{
		Instance: c.Instance,
		Username: c.Username,
		Send: func(frames ...[]any) {
			_ = gnet.Send(c.Conn, frames...)
		},
		Notify: func(message string) {
			m6Notify(c, message)
		},
	}
}

// abConfigure wires the ability-session seams (called once from m5Init,
// before login batches or ticks run — same point abEnsureTables ran).
func abConfigure() {
	abilities.ConfigureSessions(abilities.SessionDeps{
		DB:        dbConn,
		DataPath:  resourceDataPath,
		Test:      testMode,
		MarkDirty: markDirty,
		Broadcast: func(frames ...[]any) {
			worldcore.Broadcast(frames...)
		},
		TargetAlive: func(target string) (bool, bool) {
			// abLiveTarget parity: dummy first, then engine mobs,
			// else no liveness data.
			if target == combatDummyInstance {
				combatMu.Lock()
				defer combatMu.Unlock()
				return !combatDead, true
			}
			if m := m9MobFor(target); m != nil {
				m.mu.Lock()
				defer m.mu.Unlock()
				return !m.dead, true
			}
			return false, false
		},
		DamagePlayer: func(instance string, dmg int) (int, bool) {
			c, _ := worldcore.Find[*playerConn](instance)
			if c == nil {
				return 0, false
			}
			m9DamagePlayer(c, dmg, nil)
			return m9PlayerHP(c), true
		},
		DamageMob: func(instance string, dmg int) bool {
			m := m9MobFor(instance)
			if m == nil {
				return false
			}
			m9PlayerHit(m, nil, dmg)
			return true
		},
		HeroWeaponPoisonous: func(username string) bool {
			st := m5StateFor(username)
			if len(st.Equip) <= EquipmentWeapon {
				return false
			}
			key := st.Equip[EquipmentWeapon].Key
			if key == "" {
				return false
			}
			it := m6ItemInfoFor(key)
			return it != nil && it.Poisonous
		},
	})
}

func abLoadRegistry() *abilities.Registry { return abilities.LoadRegistry() }

func abEnsureTables() { abilities.EnsureTables() }

func abGrantAbility(c *playerConn, username, key string, level int) bool {
	return abilities.GrantAbility(abConn(c), username, key, level)
}

func abHas(username, key string) bool { return abilities.Has(username, key) }

func abLoadAbilities(username string) { abilities.LoadAbilities(username) }

func abLoginBatch(username string) []any { return abilities.LoginBatch(username) }

func abManaFor(instance string) int { return abilities.ManaFor(instance) }

func abSetTarget(instance, target string) { abilities.SetTarget(instance, target) }

func abLiveTarget(instance string) string { return abilities.LiveTarget(instance) }

func abHandleAbility(c *playerConn, data []byte) { abilities.HandleAbility(abConn(c), data) }

func abUse(c *playerConn, key string) { abilities.Use(abConn(c), key) }

func itoa(v int64) string { return abilities.Itoa(v) }

func abApplyPoison(instance string) { abilities.ApplyPoison(instance) }

func abHeroWeaponPoisonous(username string) bool { return abilities.HeroWeaponPoisonous(username) }

func abFreezeApply(instance string) { abilities.FreezeApply(instance) }

func abFreezeClear(instance string) { abilities.FreezeClear(instance) }

func abStatusTick() { abilities.StatusTick() }

func abForgetPlayer(c *playerConn) {
	if c == nil {
		return
	}
	abilities.ForgetPlayer(c.Instance)
}

func abTestHandler(c *playerConn, data []byte) { abilities.TestHandler(abConn(c), data) }
