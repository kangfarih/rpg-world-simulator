package controller

import (
	"encoding/json"
	"testing"
)

// ---------------------------------------------------------------------------
// Fakes for the progression seams.
// ---------------------------------------------------------------------------

type progSkills struct {
	xp       map[string]map[int]int
	level    map[string]map[int]int
	adds     []string
	sets     []string
	resets   []string
	maxed    []string
	synced   []string
	levelExp func(from, to int) int
}

func newProgSkills() *progSkills {
	return &progSkills{xp: map[string]map[int]int{}, level: map[string]map[int]int{}}
}

func (s *progSkills) MarkDirty(username string) {}
func (s *progSkills) SkillOf(c CommandConn, skill int) (int, int, bool) {
	xp, ok1 := s.xp[c.PlayerName()][skill]
	lv, ok2 := s.level[c.PlayerName()][skill]
	if !ok1 && !ok2 {
		return 0, 1, false
	}
	if lv == 0 {
		lv = 1
	}
	return xp, lv, true
}
func (s *progSkills) AddSkillXP(c CommandConn, skill, amount int) {
	s.adds = append(s.adds, c.PlayerName())
	if s.xp[c.PlayerName()] == nil {
		s.xp[c.PlayerName()] = map[int]int{}
	}
	if s.level[c.PlayerName()] == nil {
		s.level[c.PlayerName()] = map[int]int{}
	}
	s.xp[c.PlayerName()][skill] += amount
	s.level[c.PlayerName()][skill]++
}
func (s *progSkills) SetSkillXP(c CommandConn, skill, xp int) {
	s.sets = append(s.sets, c.PlayerName())
	if s.xp[c.PlayerName()] == nil {
		s.xp[c.PlayerName()] = map[int]int{}
	}
	if s.level[c.PlayerName()] == nil {
		s.level[c.PlayerName()] = map[int]int{}
	}
	s.xp[c.PlayerName()][skill] = xp
	s.level[c.PlayerName()][skill] = 1
}
func (s *progSkills) ResetSkills(c CommandConn) { s.resets = append(s.resets, c.PlayerName()) }
func (s *progSkills) MaxSkills(c CommandConn)   { s.maxed = append(s.maxed, c.PlayerName()) }
func (s *progSkills) SyncSkills(c CommandConn)  { s.synced = append(s.synced, c.PlayerName()) }
func (s *progSkills) LevelsToExperience(from, to int) int {
	if s.levelExp != nil {
		return s.levelExp(from, to)
	}
	return (to - from) * 100
}

type progAbilities struct {
	has    map[string]map[string]bool
	grants []string
	slots  map[string]int
	resets []string
}

func newProgAbilities() *progAbilities {
	return &progAbilities{has: map[string]map[string]bool{}, slots: map[string]int{}}
}

func (a *progAbilities) MarkDirty(username string) {}
func (a *progAbilities) HasAbility(username, key string) bool {
	return a.has[username][key]
}
func (a *progAbilities) GrantAbility(c CommandConn, username, key string, level int) bool {
	a.grants = append(a.grants, username+"|"+key)
	if a.has[username] == nil {
		a.has[username] = map[string]bool{}
	}
	a.has[username][key] = true
	return true
}
func (a *progAbilities) QuickSlotAbility(c CommandConn, username, key string, slot int) {
	a.slots[username+"|"+key] = slot
}
func (a *progAbilities) ResetAbilities(c CommandConn) {
	s := c.PlayerName()
	a.resets = append(a.resets, s)
}

type progRanks struct {
	set     []string
	offline []string
}

func (r *progRanks) MarkDirty(username string) {}
func (r *progRanks) SetRank(target CommandConn, rank int) {
	r.set = append(r.set, target.PlayerName())
}
func (r *progRanks) SetRankOffline(username string, rank int) {
	r.offline = append(r.offline, username)
}

type progPets struct{ grants []string }

func (p *progPets) GrantPet(c CommandConn, key string) { p.grants = append(p.grants, key) }

type progPoison struct {
	fx   map[string]bool
	kind map[string]string
	area []string
}

func newProgPoison() *progPoison {
	return &progPoison{fx: map[string]bool{}, kind: map[string]string{}}
}

func (p *progPoison) PoisonHas(instance string) bool             { return p.fx[instance] }
func (p *progPoison) PoisonApply(instance string)                { p.fx[instance] = true }
func (p *progPoison) PoisonClear(instance string)                { delete(p.fx, instance) }
func (p *progPoison) EntityKind(instance string) string          { return p.kind[instance] }
func (p *progPoison) RegionCharInstances(c CommandConn) []string { return p.area }

type progMisc struct {
	attackRange int
	debugs      []string
	resends     []string
	ips         map[string]string
	banned      []string
}

func newProgMisc() *progMisc { return &progMisc{ips: map[string]string{}} }

func (m *progMisc) AttackRange(c CommandConn) int { return m.attackRange }
func (m *progMisc) SendDebug(c CommandConn)       { m.debugs = append(m.debugs, c.InstanceID()) }
func (m *progMisc) ResendRegions(c CommandConn)   { m.resends = append(m.resends, c.InstanceID()) }
func (m *progMisc) PlayerIP(username string) (string, bool) {
	ip, ok := m.ips[username]
	return ip, ok
}
func (m *progMisc) BanIP(ip string) { m.banned = append(m.banned, ip) }

type progDeps struct {
	skills *progSkills
	ab     *progAbilities
	ranks  *progRanks
	pets   *progPets
	poison *progPoison
	misc   *progMisc
	inv    *cmdInv
}

func progTestDeps(bus *cmdBus, peers *cmdPeers, flags *cmdFlags) (CommandDeps, *progDeps) {
	p := &progDeps{
		skills: newProgSkills(), ab: newProgAbilities(), ranks: &progRanks{},
		pets: &progPets{}, poison: newProgPoison(), misc: newProgMisc(), inv: newCmdInv(),
	}
	d := cmdDeps(bus, peers, flags)
	d.Skills, d.Abilities, d.Ranks = p.skills, p.ab, p.ranks
	d.Pets, d.Poison, d.Misc = p.pets, p.poison, p.misc
	d.Inv = p.inv
	return d, p
}

// ---------------------------------------------------------------------------
// Rank gates.
// ---------------------------------------------------------------------------

func TestProgressionGateBlocksModerator(t *testing.T) {
	mod := newCmdConn("m1", "mod", CmdRankModerator)
	bus := newCmdBus()
	d, p := progTestDeps(bus, newCmdPeers(mod), newCmdFlags())

	for _, cmd := range []string{"addexp", "setlevel", "resetskills", "max", "attackrange",
		"resetregions", "debug", "poison", "poisonarea", "addability", "setability",
		"setquickslot", "resetabilities", "openbank", "setrank", "setpet", "ipban"} {
		AdminProgressionCommands(mod, cmd, []string{"x", "y", "z"}, d)
	}

	if len(bus.notifs["m1"]) != 0 {
		t.Fatalf("moderator progression commands notified: %v", bus.notifs["m1"])
	}
	if len(p.skills.adds)+len(p.ab.grants)+len(p.pets.grants)+len(p.misc.debugs)+len(p.misc.banned) != 0 {
		t.Fatalf("moderator progression commands had effects")
	}
}

// ---------------------------------------------------------------------------
// Malformed-arg notifies.
// ---------------------------------------------------------------------------

func TestProgressionMalformed(t *testing.T) {
	admin := newCmdConn("a1", "admin", CmdRankAdmin)
	bus := newCmdBus()
	d, _ := progTestDeps(bus, newCmdPeers(admin), newCmdFlags())

	AdminProgressionCommands(admin, "setlevel", []string{"health"}, d)
	if !bus.hasNotif("a1", "Malformed command, expected /setlevel [skill] [level] [username]") {
		t.Fatalf("setlevel malformed missing: %v", bus.notifs["a1"])
	}
	AdminProgressionCommands(admin, "addability", nil, d)
	if !bus.hasNotif("a1", "Malformed command, expected /addability key") {
		t.Fatalf("addability malformed missing: %v", bus.notifs["a1"])
	}
	AdminProgressionCommands(admin, "setability", []string{"run"}, d)
	if !bus.hasNotif("a1", "Malformed command, expected /setability key level") {
		t.Fatalf("setability malformed missing: %v", bus.notifs["a1"])
	}
	AdminProgressionCommands(admin, "setquickslot", []string{"run", "nope"}, d)
	if !bus.hasNotif("a1", "Malformed command, expected /setquickslot key quickslot") {
		t.Fatalf("setquickslot malformed missing: %v", bus.notifs["a1"])
	}
	AdminProgressionCommands(admin, "openbank", nil, d)
	if !bus.hasNotif("a1", "Malformed command, expected /openbank username") {
		t.Fatalf("openbank malformed missing: %v", bus.notifs["a1"])
	}
	AdminProgressionCommands(admin, "setrank", []string{"Admin"}, d)
	if !bus.hasNotif("a1", "Malformed command, expected /setrank username rank") {
		t.Fatalf("setrank malformed missing: %v", bus.notifs["a1"])
	}
	AdminProgressionCommands(admin, "setpet", nil, d)
	if !bus.hasNotif("a1", "Malformed command, expected /setpet key") {
		t.Fatalf("setpet malformed missing: %v", bus.notifs["a1"])
	}
}

// ---------------------------------------------------------------------------
// Happy paths.
// ---------------------------------------------------------------------------

func TestSetlevelUpAndDown(t *testing.T) {
	admin := newCmdConn("a1", "admin", CmdRankAdmin)
	victim := newCmdConn("v1", "victim", 0)
	bus := newCmdBus()
	d, p := progTestDeps(bus, newCmdPeers(admin, victim), newCmdFlags())
	p.skills.level["victim"] = map[int]int{3: 5}

	AdminProgressionCommands(admin, "setlevel", []string{"health", "10", "victim"}, d)
	if len(p.skills.adds) != 1 || p.skills.adds[0] != "victim" {
		t.Fatalf("setlevel up did not award: %v", p.skills.adds)
	}

	AdminProgressionCommands(admin, "setlevel", []string{"health", "2", "victim"}, d)
	if len(p.skills.sets) != 1 {
		t.Fatalf("setlevel down did not reset: %v", p.skills.sets)
	}

	AdminProgressionCommands(admin, "setlevel", []string{"smelting", "2", "victim"}, d)
	if !bus.hasNotif("a1", "Invalid skill.") {
		t.Fatalf("setlevel bad skill missing: %v", bus.notifs["a1"])
	}
	AdminProgressionCommands(admin, "setlevel", []string{"health", "2", "ghost"}, d)
	if !bus.hasNotif("a1", "Player ghost is not online.") {
		t.Fatalf("setlevel offline missing: %v", bus.notifs["a1"])
	}
}

func TestAddexpSilentMiss(t *testing.T) {
	admin := newCmdConn("a1", "admin", CmdRankAdmin)
	bus := newCmdBus()
	d, p := progTestDeps(bus, newCmdPeers(admin), newCmdFlags())

	AdminProgressionCommands(admin, "addexp", []string{"health", "50"}, d)
	if len(p.skills.adds) != 1 {
		t.Fatalf("addexp did not award: %v", p.skills.adds)
	}
	// Negative amounts pass through to the award path (TS addexp
	// subtraction: any non-zero x reaches skill.addExperience).
	AdminProgressionCommands(admin, "addexp", []string{"health", "-50"}, d)
	if len(p.skills.adds) != 2 {
		t.Fatalf("negative addexp did not award: %v", p.skills.adds)
	}
	AdminProgressionCommands(admin, "addexperience", []string{"nope", "50"}, d)
	AdminProgressionCommands(admin, "addexp", []string{"health"}, d)
	if len(p.skills.adds) != 2 {
		t.Fatalf("addexp miss was not silent: %v %v", p.skills.adds, bus.notifs["a1"])
	}
	if len(bus.notifs["a1"]) != 0 {
		t.Fatalf("addexp miss notified: %v", bus.notifs["a1"])
	}
}

func TestAddabilitySetability(t *testing.T) {
	admin := newCmdConn("a1", "admin", CmdRankAdmin)
	bus := newCmdBus()
	d, p := progTestDeps(bus, newCmdPeers(admin), newCmdFlags())

	AdminProgressionCommands(admin, "addability", []string{"run"}, d)
	if len(p.ab.grants) != 1 {
		t.Fatalf("addability did not grant: %v", p.ab.grants)
	}
	// Owned now: setability grants, quickslot stores (clamped), reset relays.
	AdminProgressionCommands(admin, "setability", []string{"run", "3"}, d)
	if len(p.ab.grants) != 2 {
		t.Fatalf("setability did not grant: %v", p.ab.grants)
	}
	AdminProgressionCommands(admin, "setability", []string{"ghost", "3"}, d)
	if len(p.ab.grants) != 2 {
		t.Fatalf("setability on unowned ability granted: %v", p.ab.grants)
	}
	AdminProgressionCommands(admin, "setquickslot", []string{"run", "99"}, d)
	if p.ab.slots["admin|run"] != 4 {
		t.Fatalf("setquickslot clamp = %v, want 4", p.ab.slots)
	}
	AdminProgressionCommands(admin, "resetabilities", nil, d)
	if len(p.ab.resets) != 1 {
		t.Fatalf("resetabilities did not relay: %v", p.ab.resets)
	}
}

func TestPoisonToggle(t *testing.T) {
	admin := newCmdConn("a1", "admin", CmdRankAdmin)
	bus := newCmdBus()
	d, p := progTestDeps(bus, newCmdPeers(admin), newCmdFlags())
	p.poison.kind["mob-1"] = "mob"

	AdminProgressionCommands(admin, "poison", []string{"mob-1"}, d)
	if !bus.hasNotif("a1", "Entity has been poisoned.") {
		t.Fatalf("poison missing: %v", bus.notifs["a1"])
	}
	AdminProgressionCommands(admin, "poison", []string{"mob-1"}, d)
	if !bus.hasNotif("a1", "Entity has been cured of poison.") {
		t.Fatalf("poison cure missing: %v", bus.notifs["a1"])
	}
	AdminProgressionCommands(admin, "poison", []string{"ghost"}, d)
	if !bus.hasNotif("a1", "Could not find entity with instance: ghost") {
		t.Fatalf("poison unknown missing: %v", bus.notifs["a1"])
	}
	AdminProgressionCommands(admin, "poison", nil, d)
	if !bus.hasNotif("a1", "You have been poisoned!") {
		t.Fatalf("self poison missing: %v", bus.notifs["a1"])
	}

	p.poison.area = []string{"mob-1", "mob-2"}
	p.poison.kind["mob-2"] = "mob"
	AdminProgressionCommands(admin, "poisonarea", nil, d)
	if !bus.hasNotif("a1", "All entities in the region will be nuked with poison.") {
		t.Fatalf("poisonarea missing: %v", bus.notifs["a1"])
	}
	if !p.poison.fx["mob-2"] {
		t.Fatalf("poisonarea did not apply: %v", p.poison.fx)
	}
}

func TestSetrankRanks(t *testing.T) {
	admin := newCmdConn("a1", "admin", CmdRankAdmin)
	victim := newCmdConn("v1", "victim", 0)
	bus := newCmdBus()
	d, p := progTestDeps(bus, newCmdPeers(admin, victim), newCmdFlags())

	AdminProgressionCommands(admin, "setrank", []string{"Moderator", "victim"}, d)
	if len(p.ranks.set) != 1 {
		t.Fatalf("setrank did not set: %v", p.ranks.set)
	}
	AdminProgressionCommands(admin, "setrank", []string{"Nope", "victim"}, d)
	if !bus.hasNotif("a1", "Invalid rank: Nope") {
		t.Fatalf("setrank invalid missing: %v", bus.notifs["a1"])
	}
	AdminProgressionCommands(admin, "setrank", []string{"Admin", "ghost"}, d)
	if len(p.ranks.offline) != 1 {
		t.Fatalf("setrank offline did not persist: %v", p.ranks.offline)
	}
	// Hollow admins stay silent.
	hollow := newCmdConn("h1", "hollow", CmdRankHollowAdmin)
	AdminProgressionCommands(hollow, "setrank", []string{"Admin", "victim"}, d)
	if len(p.ranks.set) != 1 {
		t.Fatalf("hollow setrank applied: %v", p.ranks.set)
	}
}

func TestOpenbankSetpetMisc(t *testing.T) {
	admin := newCmdConn("a1", "admin", CmdRankAdmin)
	victim := newCmdConn("v1", "victim", 0)
	bus := newCmdBus()
	d, p := progTestDeps(bus, newCmdPeers(admin, victim), newCmdFlags())
	p.inv.bank["victim"] = []CommandSlot{{Key: "gold", Count: 10}}
	p.misc.ips["victim"] = "9.9.9.9"
	p.misc.attackRange = 1

	AdminProgressionCommands(admin, "openbank", []string{"victim"}, d)
	if !admin.container {
		t.Fatal("openbank did not grant container access")
	}
	// TS NPC-Bank frame (commands.ts /openbank: NPCPacket Bank {slots});
	// the stock client renders it with no banker NPC context
	// (connection.ts handleNPC Bank -> menu.getBank().show(slots)).
	if !bus.hasOpcode("a1", 31, 2) {
		t.Fatalf("openbank did not send NPC Bank frame: %v", bus.sent["a1"])
	}
	// Payload shape: {slots:[{index,key,count,enchantments}]} carrying the
	// target's bank (bank.serialize() parity).
	found := false
	for _, f := range bus.sent["a1"] {
		if f.id != 31 || f.opcode != 2 {
			continue
		}
		var payload struct {
			Slots []struct {
				Key   string `json:"key"`
				Count int    `json:"count"`
			} `json:"slots"`
		}
		if err := json.Unmarshal(f.data, &payload); err != nil {
			t.Fatalf("openbank NPC Bank payload unreadable: %v", err)
		}
		if len(payload.Slots) == 1 && payload.Slots[0].Key == "gold" && payload.Slots[0].Count == 10 {
			found = true
		}
	}
	if !found {
		t.Fatalf("openbank NPC Bank payload missing victim slots: %v", bus.sent["a1"])
	}
	AdminProgressionCommands(admin, "openbank", []string{"ghost"}, d)
	if !bus.hasNotif("a1", "Could not find player: ghost") {
		t.Fatalf("openbank unknown missing: %v", bus.notifs["a1"])
	}

	AdminProgressionCommands(admin, "setpet", []string{"rat"}, d)
	if len(p.pets.grants) != 1 {
		t.Fatalf("setpet did not grant: %v", p.pets.grants)
	}
	AdminProgressionCommands(admin, "debug", nil, d)
	if len(p.misc.debugs) != 1 {
		t.Fatalf("debug did not send: %v", p.misc.debugs)
	}
	AdminProgressionCommands(admin, "resetregions", nil, d)
	if len(p.misc.resends) != 1 {
		t.Fatalf("resetregions did not resend: %v", p.misc.resends)
	}
	AdminProgressionCommands(admin, "attackrange", nil, d)
	if !bus.hasNotif("a1", "1") {
		t.Fatalf("attackrange missing: %v", bus.notifs["a1"])
	}
	AdminProgressionCommands(admin, "resetskills", nil, d)
	AdminProgressionCommands(admin, "max", nil, d)
	if len(p.skills.resets) != 1 || len(p.skills.maxed) != 1 {
		t.Fatalf("resetskills/max missing: %+v", p.skills)
	}

	AdminProgressionCommands(admin, "ipban", []string{"victim"}, d)
	if len(p.misc.banned) != 1 || p.misc.banned[0] != "9.9.9.9" {
		t.Fatalf("ipban did not ban: %v", p.misc.banned)
	}
	if !bus.hasNotif("a1", "Player victim has been IP banned") {
		t.Fatalf("ipban notify missing: %v", bus.notifs["a1"])
	}
}

func TestSkillRankTables(t *testing.T) {
	if SkillNameToID["Health"] != 3 || SkillNameToID["Alchemy"] != 18 {
		t.Fatalf("skill table wrong: %+v", SkillNameToID)
	}
	if _, ok := SkillNameToID["Smelting"]; ok {
		t.Fatal("Smelting must stay unresolvable (TS skills dict parity)")
	}
	if RankNameToID["Admin"] != 2 || RankNameToID["HollowAdmin"] != 14 {
		t.Fatalf("rank table wrong: %+v", RankNameToID)
	}
}
