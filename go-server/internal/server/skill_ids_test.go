package server

import (
	"testing"

	"rpg-world-server/internal/controller"
	"rpg-world-server/internal/player"
	"rpg-world-server/internal/protocol"
)

// TestSkillIDTableMatchesTS asserts the full 0-18 Modules.Skills id table
// (modules.ts:215-235 enum order) across every Go copy: internal/player
// (XP awards), internal/protocol (crafting interfaces + frames),
// internal/server re-exports (packets.go) and controller.SkillNameToID
// (addexp/setlevel). Regression guard for the alchemy off-by-one
// (SkillAlchemy was 17, Loitering's id): any future drift in any copy
// fails here before it can corrupt Skill Update / Crafting Open frames.
func TestSkillIDTableMatchesTS(t *testing.T) {
	// TS-exact table: skill name -> Modules.Skills id.
	want := map[string]int{
		"Lumberjacking": 0, "Accuracy": 1, "Archery": 2, "Health": 3,
		"Magic": 4, "Mining": 5, "Strength": 6, "Defense": 7,
		"Fishing": 8, "Cooking": 9, "Smithing": 10, "Crafting": 11,
		"Chiseling": 12, "Fletching": 13, "Smelting": 14, "Foraging": 15,
		"Eating": 16, "Loitering": 17, "Alchemy": 18,
	}

	// Every id defined in internal/player.
	playerIDs := map[string]int{
		"Lumberjacking": player.SkillLumberjacking, "Accuracy": player.SkillAccuracy,
		"Archery": player.SkillArchery, "Health": player.SkillHealth,
		"Magic": player.SkillMagic, "Mining": player.SkillMining,
		"Strength": player.SkillStrength, "Defense": player.SkillDefense,
		"Fishing": player.SkillFishing, "Foraging": player.SkillForaging,
		"Eating": player.SkillEating, "Loitering": player.SkillLoitering,
		"Alchemy": player.SkillAlchemy,
	}
	// Every id defined in internal/protocol (+ the server re-export, which
	// must track it exactly).
	protocolIDs := map[string]int{
		"Cooking": protocol.SkillCooking, "Smithing": protocol.SkillSmithing,
		"Crafting": protocol.SkillCraftingS, "Chiseling": protocol.SkillChiseling,
		"Fletching": protocol.SkillFletching, "Smelting": protocol.SkillSmelting,
		"Loitering": protocol.SkillLoitering, "Alchemy": protocol.SkillAlchemy,
	}
	serverIDs := map[string]int{
		"Cooking": SkillCooking, "Smithing": SkillSmithing,
		"Crafting": SkillCraftingS, "Chiseling": SkillChiseling,
		"Fletching": SkillFletching, "Smelting": SkillSmelting,
		"Loitering": SkillLoitering, "Alchemy": SkillAlchemy,
	}

	for name, id := range want {
		if got, ok := playerIDs[name]; ok && got != id {
			t.Errorf("player.%s = %d, want TS-exact %d", name, got, id)
		}
		if got, ok := protocolIDs[name]; ok && got != id {
			t.Errorf("protocol.%s = %d, want TS-exact %d", name, got, id)
		}
		if got, ok := serverIDs[name]; ok && got != id {
			t.Errorf("server.%s = %d, want TS-exact %d", name, got, id)
		}
		// The server re-exports must track protocol exactly (no drift).
		if p, ok := protocolIDs[name]; ok {
			if s, ok2 := serverIDs[name]; ok2 && s != p {
				t.Errorf("server.%s = %d, want protocol value %d", name, s, p)
			}
		}
		// Where player and protocol both define an id, they must agree.
		if p, ok := playerIDs[name]; ok {
			if q, ok2 := protocolIDs[name]; ok2 && p != q {
				t.Errorf("player/protocol %s disagree: %d vs %d", name, p, q)
			}
		}
	}

	// controller.SkillNameToID must resolve exactly the TS skills-dict
	// entries (Chiseling/Smelting stay unresolvable, TS parity).
	for name, id := range want {
		got, ok := controller.SkillNameToID[name]
		switch name {
		case "Chiseling", "Smelting":
			if ok {
				t.Errorf("SkillNameToID[%q] = %d, want absent (TS skills dict parity)", name, got)
			}
		default:
			if !ok || got != id {
				t.Errorf("SkillNameToID[%q] = %d,%v, want %d", name, got, ok, id)
			}
		}
	}

	// The fixed pair, spelled out: alchemy crafts and alchemy XP ride 18.
	if protocol.SkillAlchemy != 18 || player.SkillAlchemy != 18 || SkillAlchemy != 18 {
		t.Errorf("alchemy id not 18 everywhere: protocol=%d player=%d server=%d",
			protocol.SkillAlchemy, player.SkillAlchemy, SkillAlchemy)
	}
	if protocol.SkillLoitering != 17 || player.SkillLoitering != 17 || SkillLoitering != 17 {
		t.Errorf("loitering id not 17 everywhere: protocol=%d player=%d server=%d",
			protocol.SkillLoitering, player.SkillLoitering, SkillLoitering)
	}
}
