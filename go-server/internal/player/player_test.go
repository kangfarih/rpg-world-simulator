package player

import "testing"

// TestZeroValueUsable ensures the zero-value Player needs no constructor:
// accessors are nil-safe and slice/map states read back empty.
func TestZeroValueUsable(t *testing.T) {
	var p Player

	if p.InMinigame() {
		t.Fatal("zero Player must not be in a minigame")
	}
	if p.CanAccessBank() {
		t.Fatal("zero Player must not hold bank access")
	}
	if got := p.StoreKey(); got != "" {
		t.Fatalf("zero Player store key = %q, want empty", got)
	}
	if len(p.Inventory) != 0 || len(p.Bank) != 0 || len(p.Equipment) != 0 {
		t.Fatal("zero Player slot lists must be empty")
	}
	if len(p.Skills) != 0 {
		t.Fatal("zero Player skills map must be empty")
	}
	if p.Tokens != 0 || p.LastGlobalChat != 0 || p.Rank != 0 {
		t.Fatal("zero Player chat snapshot must be zero")
	}

	// Nil-receiver accessors must not panic.
	var nilP *Player
	if nilP.InMinigame() || nilP.CanAccessBank() || nilP.StoreKey() != "" {
		t.Fatal("nil Player accessors must report zero state")
	}
}

// TestFieldRoundTrip sets every slice/map/scalar state and reads it back.
func TestFieldRoundTrip(t *testing.T) {
	p := Player{
		Instance: "1-100-96",
		Username: "alice",
	}

	p.X, p.Y = 100, 96
	p.Level, p.HP = 12, 87

	p.Inventory = []Slot{{Key: "oak_logs", Count: 3}, {Key: "axe", Count: 1}}
	p.Bank = []Slot{{Key: "gold", Count: 250}}
	p.Equipment = []Slot{{Key: "bronze_sword", Count: 1}}
	p.Skills = map[int]Skill{0: {Level: 5, XP: 320}, 3: {Level: 12, XP: 1500}}

	p.BankState = BankState{StoreOpen: "general_store", CanAccessContainer: true}
	p.ChatState = ChatState{Rank: 2, Tokens: 2.5, LastGlobalChat: 1700000000000}
	p.MinigameState = MinigameState{Game: "coursing", Team: 2, Score: 40, Target: "1-101-96"}
	p.SocialState = SocialState{TalkNPC: "shopkeeper", TalkIndex: 2}

	if p.Instance != "1-100-96" || p.Username != "alice" {
		t.Fatalf("identity = %q/%q", p.Instance, p.Username)
	}
	if p.X != 100 || p.Y != 96 || p.Level != 12 || p.HP != 87 {
		t.Fatalf("persist snapshot = %+v", p.State)
	}
	if len(p.Inventory) != 2 || p.Inventory[0] != (Slot{Key: "oak_logs", Count: 3}) {
		t.Fatalf("inventory = %+v", p.Inventory)
	}
	if len(p.Bank) != 1 || p.Bank[0] != (Slot{Key: "gold", Count: 250}) {
		t.Fatalf("bank = %+v", p.Bank)
	}
	if len(p.Equipment) != 1 || p.Equipment[0].Key != "bronze_sword" {
		t.Fatalf("equipment = %+v", p.Equipment)
	}
	if p.Skills[0] != (Skill{Level: 5, XP: 320}) || p.Skills[3].XP != 1500 {
		t.Fatalf("skills = %+v", p.Skills)
	}
	if !p.CanAccessBank() || p.StoreKey() != "general_store" {
		t.Fatalf("bank state = %+v", p.BankState)
	}
	if p.Rank != 2 || p.Tokens != 2.5 || p.LastGlobalChat != 1700000000000 {
		t.Fatalf("chat state = %+v", p.ChatState)
	}
	if !p.InMinigame() || p.Team != 2 || p.Score != 40 || p.Target != "1-101-96" {
		t.Fatalf("minigame state = %+v", p.MinigameState)
	}
	if p.TalkNPC != "shopkeeper" || p.TalkIndex != 2 {
		t.Fatalf("social state = %+v", p.SocialState)
	}
}
