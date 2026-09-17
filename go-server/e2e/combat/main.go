// Scripted WS check for COMBAT party mode (COMBAT=1): pure original 9
// regions, 4 bots (WarBot gold warrior, ArchBot woodenbow archer, MageBot
// naturestaff mage, SupBot unarmed support) + BossDummy (golem, 5000 HP)
// spawns; autos at class speeds + Sunder/Volley/Storm skills (per-bot 1s
// GCD); support stays silent at full HP (heal skipped — the dummy never
// retaliates, so no Heal/Healing-FX/bot-Points overheal spam; heal amount
// clamps to missing HP with an Idle anim when it does fire) + party buffs
// (Effect Add DefenseBuff/StrengthSuperBuff); per-class Spawn attackRange
// (war 1 / archer 8 / mage 9 / support 1); boss death (Despawn) + respawn
// (Spawn).
// Not part of the stub build (run via `go run ./_combatcheck` from the
// stub dir; underscore dir ignored by go tooling).
// Usage: restart stub with COMBAT=1, then go run ./_combatcheck.
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

var incoming = make(chan []json.RawMessage, 65536)

func reader(conn *websocket.Conn) {
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			close(incoming)
			return
		}
		var bulk [][]json.RawMessage
		if err := json.Unmarshal(raw, &bulk); err != nil {
			fmt.Println("parse fail:", string(raw))
			continue
		}
		for _, f := range bulk {
			incoming <- f
		}
	}
}

type stamped struct {
	f []json.RawMessage
	t time.Duration
}

var lastFrames []stamped

func drain(d time.Duration) map[int]int {
	counts := map[int]int{}
	deadline := time.Now().Add(d)
	for {
		timeout := time.Until(deadline)
		if timeout <= 0 {
			return counts
		}
		select {
		case f, ok := <-incoming:
			if !ok {
				return counts
			}
			if len(f) == 0 {
				continue
			}
			var id int
			if err := json.Unmarshal(f[0], &id); err != nil {
				continue
			}
			counts[id]++
			lastFrames = append(lastFrames, stamped{f, time.Since(startT)})
		case <-time.After(timeout):
			return counts
		}
	}
}

var startT = time.Now()

var failures []string

func check(cond bool, msg string) {
	if !cond {
		failures = append(failures, msg)
		fmt.Println("  FAIL:", msg)
	} else {
		fmt.Println("  ok:", msg)
	}
}

func frameData(f []json.RawMessage) json.RawMessage {
	if len(f) == 0 {
		return nil
	}
	return f[len(f)-1]
}

func send(conn *websocket.Conn, msg string) {
	if err := conn.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
		fmt.Println("WRITE FAIL:", err)
		os.Exit(1)
	}
}

type tile struct {
	X    int             `json:"x"`
	Y    int             `json:"y"`
	Data json.RawMessage `json:"data"`
	C    bool            `json:"c"`
}

func main() {
	conn, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:9001/", nil)
	if err != nil {
		fmt.Println("DIAL FAIL:", err)
		os.Exit(1)
	}
	defer conn.Close()
	go reader(conn)

	fmt.Println("connected:", drain(1500*time.Millisecond))
	lastFrames = nil

	send(conn, `[1,{"gVer":1}]`)
	fmt.Println("handshake:", drain(1500*time.Millisecond))
	lastFrames = nil

	send(conn, `[2,{"opcode":0,"username":"tester","password":"x"}]`)
	fmt.Println("login:", drain(2*time.Second))

	var welcome struct {
		Instance string `json:"instance"`
		X        int    `json:"x"`
		Y        int    `json:"y"`
	}
	var mapB64 string
	var mapBuf float64
	var mapElems int
	for _, s := range lastFrames {
		var id int
		_ = json.Unmarshal(s.f[0], &id)
		if id == 3 {
			_ = json.Unmarshal(frameData(s.f), &welcome)
		}
		if id == 4 {
			mapElems = len(s.f)
			_ = json.Unmarshal(s.f[1], &mapB64)
			if len(s.f) >= 3 {
				_ = json.Unmarshal(s.f[2], &mapBuf)
			}
		}
	}
	check((welcome.Instance == "p1" || strings.HasPrefix(welcome.Instance, "p-")) && welcome.X == 100 && welcome.Y == 96,
		fmt.Sprintf("hero Welcome p1 at 100,96 (got %s %d,%d)", welcome.Instance, welcome.X, welcome.Y))
	check(mapElems == 3, fmt.Sprintf("map framing [4,b64,bufSize] (got %d elems)", mapElems))

	gz, err := base64.StdEncoding.DecodeString(mapB64)
	check(err == nil, "map base64 decodes")
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	check(err == nil, "map gzip opens")
	rawJSON, err := io.ReadAll(zr)
	check(err == nil, "map gzip reads")
	check(int(mapBuf) == len(rawJSON),
		fmt.Sprintf("map bufSize==json bytes (%d==%d)", int(mapBuf), len(rawJSON)))

	var regions map[string][]tile
	check(json.Unmarshal(rawJSON, &regions) == nil, "map regions JSON parses")
	check(len(regions) == 9, fmt.Sprintf("exactly 9 pure regions (got %d)", len(regions)))
	byXY := map[[2]int]tile{}
	for _, tiles := range regions {
		for _, t := range tiles {
			byXY[[2]int{t.X, t.Y}] = t
		}
	}
	if t, ok := byXY[[2]int{104, 104}]; ok {
		check(string(t.Data) == "3907" && !t.C,
			fmt.Sprintf("(104,104) pure base 3907 walkable, no pond (got data=%s c=%v)", string(t.Data), t.C))
	} else {
		check(false, "tile (104,104) present")
	}
	for _, xy := range [][2]int{{102, 96}, {106, 96}, {100, 98}, {102, 98}, {100, 96}} {
		if t, ok := byXY[xy]; ok {
			check(!t.C, fmt.Sprintf("combat tile %v walkable (data=%s c=%v)", xy, string(t.Data), t.C))
		} else {
			check(false, fmt.Sprintf("combat tile %v present in map", xy))
		}
	}
	lastFrames = nil

	send(conn, `[9,{"regionsLoaded":true,"userAgent":"combatcheck"}]`)
	fmt.Println("ready:", drain(2*time.Second))

	type equip struct {
		Type int    `json:"type"`
		Key  string `json:"key"`
	}
	type spawn struct {
		Instance     string  `json:"instance"`
		Type         int     `json:"type"`
		Key          string  `json:"key"`
		Name         string  `json:"name"`
		X            int     `json:"x"`
		Y            int     `json:"y"`
		Orientation  int     `json:"orientation"`
		Level        int     `json:"level"`
		HitPoints    int     `json:"hitPoints"`
		MaxHitPoints int     `json:"maxHitPoints"`
		AttackRange  int     `json:"attackRange"`
		Equipments   []equip `json:"equipments"`
	}
	spawns := map[string]spawn{}
	for _, s := range lastFrames {
		var id int
		_ = json.Unmarshal(s.f[0], &id)
		if id != 5 {
			continue
		}
		var sp spawn
		_ = json.Unmarshal(frameData(s.f), &sp)
		if sp.Instance != "" {
			spawns[sp.Instance] = sp
		}
	}
	lastFrames = nil

	_, dummyAlive := spawns["m-dummy"]
	if dummyAlive {
		check(len(spawns) == 5, fmt.Sprintf("exactly 5 Spawn frames (got %d)", len(spawns)))
	} else {
		// Fast-HP race: the checker connected during the respawn gap, so
		// only the 4 bots spawn now — the dummy shape is verified when it
		// respawns below.
		check(len(spawns) == 4, fmt.Sprintf("boss dead at connect: 4 bot Spawns, dummy respawns later (got %d)", len(spawns)))
		fmt.Println("  info: boss dead at connect — dummy shape verified via respawn")
	}
	bot, ok := spawns["p-warbot"]
	check(ok, "WarBot p-warbot spawned")
	arch, aok := spawns["p-archer"]
	check(aok, "ArchBot p-archer spawned")
	mage, mok := spawns["p-mage"]
	check(mok, "MageBot p-mage spawned")
	sup, sok := spawns["p-support"]
	check(sok, "SupBot p-support spawned")
	dummy, dok := spawns["m-dummy"]
	if ok {
		check(bot.Type == 0 && bot.Key == "base" && bot.X == 102 && bot.Y == 96,
			fmt.Sprintf("WarBot Player base at 102,96 (got type=%d key=%s %d,%d)", bot.Type, bot.Key, bot.X, bot.Y))
		check(bot.Level == 20, fmt.Sprintf("WarBot Lv20 (got %d)", bot.Level))
		check(bot.Orientation == 3, fmt.Sprintf("WarBot faces right at boss (got %d)", bot.Orientation))
		check(bot.AttackRange == 1, fmt.Sprintf("WarBot attackRange 1 melee (got %d)", bot.AttackRange))
		keys := map[string]int{}
		for _, e := range bot.Equipments {
			keys[e.Key] = e.Type
		}
		for k, slot := range map[string]int{"goldsword": 4, "goldshield": 5, "goldhelmet": 0, "goldchestplate": 3, "goldlegplates": 9, "goldboots": 11} {
			s, has := keys[k]
			check(has && s == slot, fmt.Sprintf("WarBot gold piece slot=%d key=%s", slot, k))
		}
	}
	if aok {
		check(arch.Type == 0 && arch.X == 100 && arch.Y == 98,
			fmt.Sprintf("ArchBot Player at 100,98 (got type=%d %d,%d)", arch.Type, arch.X, arch.Y))
		check(arch.Orientation == 3, fmt.Sprintf("ArchBot faces right at boss (got %d)", arch.Orientation))
		check(arch.AttackRange == 8, fmt.Sprintf("ArchBot attackRange 8 (ARCHER_ATTACK_RANGE, got %d)", arch.AttackRange))
		keys := map[string]int{}
		for _, e := range arch.Equipments {
			keys[e.Key] = e.Type
		}
		for k, slot := range map[string]int{"woodenbow": 4, "arrow": 2, "leatherhelmet": 0, "leatherchest": 3, "leatherleggings": 9, "leatherboots": 11} {
			s, has := keys[k]
			check(has && s == slot, fmt.Sprintf("ArchBot piece slot=%d key=%s", slot, k))
		}
	}
	if mok {
		check(mage.Type == 0 && mage.X == 102 && mage.Y == 98,
			fmt.Sprintf("MageBot Player at 102,98 (got type=%d %d,%d)", mage.Type, mage.X, mage.Y))
		check(mage.Orientation == 3, fmt.Sprintf("MageBot faces right at boss (got %d)", mage.Orientation))
		check(mage.AttackRange == 9, fmt.Sprintf("MageBot attackRange 9 (naturestaff items.json, got %d)", mage.AttackRange))
		keys := map[string]int{}
		for _, e := range mage.Equipments {
			keys[e.Key] = e.Type
		}
		s, has := keys["naturestaff"]
		check(has && s == 4, "MageBot naturestaff weapon slot=4")
	}
	if sok {
		check(sup.Type == 0 && sup.X == 100 && sup.Y == 96,
			fmt.Sprintf("SupBot Player at 100,96 (got type=%d %d,%d)", sup.Type, sup.X, sup.Y))
		check(sup.Orientation == 3, fmt.Sprintf("SupBot faces right at boss (got %d)", sup.Orientation))
		check(sup.AttackRange == 1, fmt.Sprintf("SupBot attackRange 1 unarmed (got %d)", sup.AttackRange))
		keys := map[string]int{}
		for _, e := range sup.Equipments {
			keys[e.Key] = e.Type
		}
		check(len(sup.Equipments) >= 4, fmt.Sprintf("SupBot leather set equipped (got %d pieces)", len(sup.Equipments)))
		_, hasWeapon := keys["woodenbow"]
		_, hasStaff := keys["naturestaff"]
		_, hasGold := keys["goldsword"]
		check(!hasWeapon && !hasStaff && !hasGold, "SupBot unarmed (no weapon — never attacks)")
	}
	dummyMax := 0
	if dok {
		check(dummy.Type == 3 && dummy.Key == "golem" && dummy.X == 106 && dummy.Y == 96,
			fmt.Sprintf("BossDummy golem Mob at 106,96 (got type=%d key=%s %d,%d)", dummy.Type, dummy.Key, dummy.X, dummy.Y))
		check(dummy.Name == "BossDummy", fmt.Sprintf("boss named BossDummy (got %q)", dummy.Name))
		check(dummy.Orientation == 2, fmt.Sprintf("boss faces left at party (got %d)", dummy.Orientation))
		check(dummy.MaxHitPoints >= 60 && dummy.HitPoints > 0 && dummy.HitPoints <= dummy.MaxHitPoints,
			fmt.Sprintf("Boss HP alive 0<hp<=max (got %d/%d)", dummy.HitPoints, dummy.MaxHitPoints))
		dummyMax = dummy.MaxHitPoints
	}
	for _, gone := range []string{"p2", "m1", "t1", "t-test-1", "m-show-1", "n-show-1", "p-show-1", "p-show-2", "p-adv-1"} {
		_, dup := spawns[gone]
		check(!dup, "overlay gone: "+gone)
	}

	// --- Combat window: 15s of live party brain ---
	fmt.Println("waiting 15s for combat window...")
	window := drain(15 * time.Second)

	type hit struct {
		Type   int      `json:"type"`
		Damage int      `json:"damage"`
		Ranged *bool    `json:"ranged,omitempty"`
		Skills []string `json:"skills"`
	}
	type combat struct {
		Instance string `json:"instance"`
		Target   string `json:"target"`
		Hit      hit    `json:"hit"`
	}
	type points struct {
		Instance     string `json:"instance"`
		HitPoints    int    `json:"hitPoints"`
		MaxHitPoints int    `json:"maxHitPoints"`
	}
	autos := map[string]int{}
	var sunderTimes, volleyTimes, stormTimes []time.Duration
	var anims int
	var supportAtkAnims int
	var boulders, fireballs int
	var hpFirst, hpLast = -1, -1
	var combatHits int
	var supportAttacks int
	var heals int
	var healTargets = map[string]int{}
	var healingFX int
	var buffFX = map[string]int{} // bot -> count of 21/25
	var botPoints = map[string]int{}
	var windowDeaths int
	rangedOK := true
	for _, s := range lastFrames {
		var id int
		_ = json.Unmarshal(s.f[0], &id)
		switch id {
		case 15:
			if len(s.f) < 3 {
				continue
			}
			var opcode int
			_ = json.Unmarshal(s.f[1], &opcode)
			if opcode != 1 {
				continue
			}
			var c combat
			if json.Unmarshal(frameData(s.f), &c) != nil {
				continue
			}
			if c.Target != "m-dummy" {
				continue
			}
			if c.Instance == "p-support" {
				supportAttacks++
				continue
			}
			combatHits++
			isSkill := false
			for _, sk := range c.Hit.Skills {
				switch sk {
				case "sunder":
					isSkill = true
					if c.Hit.Type == 6 && c.Hit.Damage == 40 {
						sunderTimes = append(sunderTimes, s.t)
					} else {
						check(false, fmt.Sprintf("sunder shape type=6 dmg=40 (got type=%d dmg=%d)", c.Hit.Type, c.Hit.Damage))
					}
				case "volley":
					isSkill = true
					if c.Hit.Type == 6 && c.Hit.Damage == 30 {
						volleyTimes = append(volleyTimes, s.t)
					} else {
						check(false, fmt.Sprintf("volley shape type=6 dmg=30 (got type=%d dmg=%d)", c.Hit.Type, c.Hit.Damage))
					}
					if c.Hit.Ranged == nil || !*c.Hit.Ranged {
						rangedOK = false
					}
				case "storm":
					isSkill = true
					if c.Hit.Type == 6 && c.Hit.Damage == 45 {
						stormTimes = append(stormTimes, s.t)
					} else {
						check(false, fmt.Sprintf("storm shape type=6 dmg=45 (got type=%d dmg=%d)", c.Hit.Type, c.Hit.Damage))
					}
					if c.Hit.Ranged == nil || !*c.Hit.Ranged {
						rangedOK = false
					}
				}
			}
			if !isSkill {
				switch c.Instance {
				case "p-warbot":
					if c.Hit.Type == 0 && c.Hit.Damage >= 15 && c.Hit.Damage <= 25 {
						autos[c.Instance]++
					} else {
						check(false, fmt.Sprintf("unexpected war auto type=%d dmg=%d", c.Hit.Type, c.Hit.Damage))
					}
				case "p-archer":
					if c.Hit.Type == 0 && c.Hit.Damage >= 12 && c.Hit.Damage <= 20 {
						autos[c.Instance]++
					} else {
						check(false, fmt.Sprintf("unexpected archer auto type=%d dmg=%d", c.Hit.Type, c.Hit.Damage))
					}
					if c.Hit.Ranged == nil || !*c.Hit.Ranged {
						rangedOK = false
					}
				case "p-mage":
					if c.Hit.Type == 0 && c.Hit.Damage >= 18 && c.Hit.Damage <= 28 {
						autos[c.Instance]++
					} else {
						check(false, fmt.Sprintf("unexpected mage auto type=%d dmg=%d", c.Hit.Type, c.Hit.Damage))
					}
					if c.Hit.Ranged == nil || !*c.Hit.Ranged {
						rangedOK = false
					}
				default:
					check(false, fmt.Sprintf("combat from unknown instance %s", c.Instance))
				}
			}
		case 16:
			var a struct {
				Instance string `json:"instance"`
				Action   int    `json:"action"`
			}
			if json.Unmarshal(frameData(s.f), &a) == nil && a.Action == 1 {
				anims++
				if a.Instance == "p-support" {
					supportAtkAnims++
				}
			}
		case 13:
			var d struct {
				Instance string `json:"instance"`
			}
			if json.Unmarshal(frameData(s.f), &d) == nil && d.Instance == "m-dummy" {
				windowDeaths++
			}
		case 17:
			var p points
			if json.Unmarshal(frameData(s.f), &p) == nil {
				if p.Instance == "m-dummy" {
					if hpFirst < 0 {
						hpFirst = p.HitPoints
					}
					hpLast = p.HitPoints
				} else {
					botPoints[p.Instance]++
				}
			}
		case 27:
			var h struct {
				Instance string `json:"instance"`
				Type     string `json:"type"`
				Amount   int    `json:"amount"`
			}
			if json.Unmarshal(frameData(s.f), &h) == nil {
				if h.Type == "hitpoints" && h.Amount >= 1 && h.Amount <= 40 {
					heals++
					healTargets[h.Instance]++
				} else {
					check(false, fmt.Sprintf("unexpected heal %+v", h))
				}
			}
		case 47:
			if len(s.f) < 3 {
				continue
			}
			var opcode int
			_ = json.Unmarshal(s.f[1], &opcode)
			var e struct {
				Instance string `json:"instance"`
				Effect   int    `json:"effect"`
			}
			if opcode == 0 && json.Unmarshal(frameData(s.f), &e) == nil {
				switch {
				case e.Instance == "m-dummy" && e.Effect == 9:
					boulders++
				case e.Instance == "m-dummy" && e.Effect == 6:
					fireballs++
				case e.Effect == 5:
					healingFX++
				case e.Effect == 21 || e.Effect == 25:
					buffFX[e.Instance]++
				}
			}
		}
	}
	fmt.Printf("  info: window=%v combatHits=%d autos=%v anims=%d sunders=%d volleys=%d storms=%d boulders=%d fireballs=%d hp=%d->%d heals=%d healTargets=%v healingFX=%d buffFX=%v botPoints=%v\n",
		window, combatHits, autos, anims, len(sunderTimes), len(volleyTimes), len(stormTimes), boulders, fireballs, hpFirst, hpLast, heals, healTargets, healingFX, buffFX, botPoints)
	lastFrames = nil

	check(supportAttacks == 0, fmt.Sprintf("support never attacks boss (got %d)", supportAttacks))
	check(autos["p-warbot"] >= 5, fmt.Sprintf("warrior autos ~1200ms (got %d in 15s)", autos["p-warbot"]))
	check(autos["p-archer"] >= 4, fmt.Sprintf("archer autos ~1600ms (got %d in 15s)", autos["p-archer"]))
	check(autos["p-mage"] >= 3, fmt.Sprintf("mage autos ~2000ms (got %d in 15s)", autos["p-mage"]))
	check(rangedOK, "archer/mage hits carry ranged=true (projectile path)")
	check((hpFirst >= 0 && hpLast >= 0 && hpLast < hpFirst) || windowDeaths > 0,
		fmt.Sprintf("boss HP drops via Points or dies mid-window (got %d->%d deaths=%d)", hpFirst, hpLast, windowDeaths))
	check(combatHits >= 10, fmt.Sprintf("splat packets observed (Combat Hit ≥10, got %d)", combatHits))
	check(len(sunderTimes) >= 1, fmt.Sprintf("≥1 Sunder cast (got %d)", len(sunderTimes)))
	check(len(volleyTimes) >= 3, fmt.Sprintf("≥1 Volley = 3 arrow hits (got %d)", len(volleyTimes)))
	check(len(stormTimes) >= 1, fmt.Sprintf("≥1 Storm cast (got %d)", len(stormTimes)))
	for name, times := range map[string][]time.Duration{"sunder": sunderTimes, "storm": stormTimes} {
		gcdOK := true
		for i := 1; i < len(times); i++ {
			if times[i]-times[i-1] < 4*time.Second {
				gcdOK = false
			}
		}
		check(gcdOK, fmt.Sprintf("%s respects cadence/GCD: casts ≥4s apart (times=%v)", name, times))
	}
	// Volley bursts: the 3 hits of one cast emit synchronously, so a >2s gap
	// starts a new burst. The trailing group may be partial (1-2 hits) when
	// the 15s window deadline lands mid-burst — that is truncation, not a bug.
	volleyGroups := [][]time.Duration{}
	for _, t := range volleyTimes {
		last := len(volleyGroups) - 1
		if last < 0 || t-volleyGroups[last][len(volleyGroups[last])-1] > 2*time.Second {
			volleyGroups = append(volleyGroups, []time.Duration{t})
		} else {
			volleyGroups[last] = append(volleyGroups[last], t)
		}
	}
	volleyOK := len(volleyTimes) >= 3
	fullBursts := 0
	groupLens := make([]int, len(volleyGroups))
	for i, g := range volleyGroups {
		groupLens[i] = len(g)
		if i < len(volleyGroups)-1 {
			if len(g) != 3 {
				volleyOK = false
			} else {
				fullBursts++
			}
		} else if len(g) < 1 || len(g) > 3 {
			volleyOK = false
		} else if len(g) == 3 {
			fullBursts++
		}
	}
	check(volleyOK && fullBursts >= 1,
		fmt.Sprintf("volley fires in 3-hit bursts (groups=%v total=%d)", groupLens, len(volleyTimes)))
	check(boulders >= len(sunderTimes) && boulders >= 1,
		fmt.Sprintf("Boulder effect per sunder (got %d)", boulders))
	check(fireballs >= len(stormTimes) && fireballs >= 1,
		fmt.Sprintf("Fireball effect per storm (got %d)", fireballs))
	check(heals == 0, fmt.Sprintf("support silent at full HP (heal skipped, no overheal spam — got %d Heal packets)", heals))
	check(healingFX == 0,
		fmt.Sprintf("no Healing FX without a heal (got %d)", healingFX))
	nBotPoints := 0
	for _, n := range botPoints {
		nBotPoints += n
	}
	check(nBotPoints == 0,
		fmt.Sprintf("no bot HP bar updates without a heal (got %v)", botPoints))
	check(supportAtkAnims == 0,
		fmt.Sprintf("support never swings (heal anim is Idle, got %d Attack anims)", supportAtkAnims))
	buffTotal := 0
	for _, n := range buffFX {
		buffTotal += n
	}
	check(buffTotal >= 4, fmt.Sprintf("party buff hits all 4 bots (got %d buff FX: %v)", buffTotal, buffFX))

	// --- Death + respawn: wait for Despawn then full-HP Spawn ---
	fmt.Println("waiting for boss death (Despawn, up to 150s)...")
	dead := false
	deadline := time.Now().Add(150 * time.Second)
	for !dead && time.Now().Before(deadline) {
		drain(5 * time.Second)
		for _, s := range lastFrames {
			var id int
			_ = json.Unmarshal(s.f[0], &id)
			if id != 13 {
				continue
			}
			var d struct {
				Instance string `json:"instance"`
			}
			if json.Unmarshal(frameData(s.f), &d) == nil && d.Instance == "m-dummy" {
				dead = true
			}
		}
		lastFrames = nil
	}
	check(dead, "boss dies at 0 HP (Despawn m-dummy)")

	fmt.Println("waiting for boss respawn (Spawn full HP, up to 30s)...")
	respawned := false
	deadline = time.Now().Add(30 * time.Second)
	for !respawned && time.Now().Before(deadline) {
		drain(5 * time.Second)
		for _, s := range lastFrames {
			var id int
			_ = json.Unmarshal(s.f[0], &id)
			if id != 5 {
				continue
			}
			var sp spawn
			if json.Unmarshal(frameData(s.f), &sp) == nil && sp.Instance == "m-dummy" {
				if dummyMax == 0 {
					// Connected while the boss was dead: no baseline max HP.
					check(sp.HitPoints == sp.MaxHitPoints && sp.MaxHitPoints > 0,
						fmt.Sprintf("respawn full HP %d/%d (boss was dead at connect)", sp.HitPoints, sp.MaxHitPoints))
				} else {
					check(sp.HitPoints == sp.MaxHitPoints && sp.MaxHitPoints == dummyMax,
						fmt.Sprintf("respawn full HP %d/%d (max %d)", sp.HitPoints, sp.MaxHitPoints, dummyMax))
				}
				respawned = true
			}
		}
		lastFrames = nil
	}
	check(respawned, "boss respawns after ~15s (Spawn m-dummy)")

	if len(failures) > 0 {
		fmt.Printf("CHECK FAILED (%d failures)\n", len(failures))
		os.Exit(1)
	}
	fmt.Println("CHECK PASSED")
}
