package protocol

import "testing"

// TestSkillIDsMatchTS pins the Modules.Skills ids (modules.ts:215-235 enum
// order: Lumberjacking0 Accuracy1 Archery2 Health3 Magic4 Mining5 Strength6
// Defense7 Fishing8 Cooking9 Smithing10 Crafting11 Chiseling12 Fletching13
// Smelting14 Foraging15 Eating16 Loitering17 Alchemy18) to their TS-exact
// values. Regression guard for the alchemy off-by-one (SkillAlchemy was 17,
// Loitering's id, so alchemy Crafting Open frames and alchemy craft XP went
// out under skill 17): the adjacent Loitering/Alchemy pair is asserted
// explicitly, including the Loitering+1 == Alchemy order invariant.
func TestSkillIDsMatchTS(t *testing.T) {
	if SkillCooking != 9 {
		t.Fatalf("SkillCooking = %d, want 9", SkillCooking)
	}
	if SkillSmithing != 10 {
		t.Fatalf("SkillSmithing = %d, want 10", SkillSmithing)
	}
	if SkillCraftingS != 11 {
		t.Fatalf("SkillCraftingS = %d, want 11", SkillCraftingS)
	}
	if SkillChiseling != 12 {
		t.Fatalf("SkillChiseling = %d, want 12", SkillChiseling)
	}
	if SkillFletching != 13 {
		t.Fatalf("SkillFletching = %d, want 13", SkillFletching)
	}
	if SkillSmelting != 14 {
		t.Fatalf("SkillSmelting = %d, want 14", SkillSmelting)
	}
	if SkillLoitering != 17 {
		t.Fatalf("SkillLoitering = %d, want 17", SkillLoitering)
	}
	if SkillAlchemy != 18 {
		t.Fatalf("SkillAlchemy = %d, want 18 (was 17, Loitering's id)", SkillAlchemy)
	}
	if SkillLoitering+1 != SkillAlchemy {
		t.Fatalf("skill order broken: Loitering %d + 1 != Alchemy %d",
			SkillLoitering, SkillAlchemy)
	}
}
