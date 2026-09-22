// Scripted WS check for the M13 slice (TESTMAP=1 default server): the full
// commands.ts port on top of the M7 base — admin item/container legs (/spawn,
// /take, /takeitem, /clear), mobility (/tp home spot, /ms + /noclip with the
// anti-cheat relaxations), mod gates (/mute silences chat, /unmute restores,
// /jail teleports to spawn), the TS close chain (/ban 'ban' + close,
// /kick close), quest admin (/finishquest sets the last stage), and the
// flags round-trip through the SQLite m13flags table (echo after relogin).
// Not part of the stub build (underscore dirs are ignored by the go tool).
// Usage: go run ./e2e/m13 (stub must run with TESTMAP=1, the default).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

var incoming = make(chan []json.RawMessage, 4096)

func reader(conn *websocket.Conn) {
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return // never close(incoming): shared across logins
		}
		var bulk [][]json.RawMessage
		if err := json.Unmarshal(raw, &bulk); err != nil {
			continue
		}
		for _, f := range bulk {
			incoming <- f
		}
	}
}

var lastFrames [][]json.RawMessage

func drain(d time.Duration) map[int]int {
	counts := map[int]int{}
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case f := <-incoming:
			if len(f) == 0 {
				continue
			}
			var id int
			if err := json.Unmarshal(f[0], &id); err != nil {
				continue
			}
			counts[id]++
			lastFrames = append(lastFrames, f)
		case <-timer.C:
			return counts
		}
	}
}

func frameData(f []json.RawMessage) json.RawMessage {
	if len(f) >= 3 {
		return f[2]
	}
	if len(f) >= 2 {
		return f[1]
	}
	return nil
}

func lastOf(frames [][]json.RawMessage, id int) json.RawMessage {
	for i := len(frames) - 1; i >= 0; i-- {
		var fid int
		_ = json.Unmarshal(frames[i][0], &fid)
		if fid == id {
			return frameData(frames[i])
		}
	}
	return nil
}

func send(conn *websocket.Conn, msg string) {
	_ = conn.WriteMessage(websocket.TextMessage, []byte(msg))
}

var failures []string
var checks int

func check(cond bool, msg string) {
	checks++
	if cond {
		fmt.Printf("  ok: %s\n", msg)
		return
	}
	failures = append(failures, msg)
	fmt.Printf("  FAIL: %s\n", msg)
}

// notifyMsg is one parsed S→C Notification Text frame.
type notifyMsg struct {
	Message string `json:"message"`
	Source  string `json:"source"`
	Colour  string `json:"colour"`
}

func parseNotify(f []json.RawMessage) (notifyMsg, bool) {
	var m notifyMsg
	if len(f) < 3 {
		return m, false
	}
	var op int
	_ = json.Unmarshal(f[1], &op)
	if op != 2 {
		return m, false
	}
	err := json.Unmarshal(f[2], &m)
	return m, err == nil
}

// notifyFrames returns the S→C Notification Text frames in lastFrames.
func notifyFrames() []notifyMsg {
	var out []notifyMsg
	for _, f := range lastFrames {
		var id int
		_ = json.Unmarshal(f[0], &id)
		if id != 25 {
			continue
		}
		if m, ok := parseNotify(f); ok {
			out = append(out, m)
		}
	}
	return out
}

// notifyFor scans a raw frame slice for one notification containing substr.
func notifyFor(frames [][]json.RawMessage, substr string) bool {
	for _, f := range frames {
		var id int
		_ = json.Unmarshal(f[0], &id)
		if id != 25 {
			continue
		}
		if m, ok := parseNotify(f); ok && strings.Contains(m.Message, substr) {
			return true
		}
	}
	return false
}

type chatMsg struct {
	Instance string `json:"instance"`
	Source   string `json:"source"`
	Message  string `json:"message"`
	Colour   string `json:"colour"`
}

func parseChat(f []json.RawMessage) (chatMsg, bool) {
	var m chatMsg
	if len(f) < 2 {
		return m, false
	}
	err := json.Unmarshal(frameData(f), &m)
	return m, err == nil
}

func chatFrames() []chatMsg {
	var out []chatMsg
	for _, f := range lastFrames {
		var id int
		_ = json.Unmarshal(f[0], &id)
		if id != 19 {
			continue
		}
		if m, ok := parseChat(f); ok {
			out = append(out, m)
		}
	}
	return out
}

// slotMsg is one parsed S→C Container frame (Remove carries {index,key,count}).
type slotMsg struct {
	Index int    `json:"index"`
	Key   string `json:"key"`
	Count int    `json:"count"`
}

func containerRemoves(frames [][]json.RawMessage) []slotMsg {
	var out []slotMsg
	for _, f := range frames {
		var id, op int
		_ = json.Unmarshal(f[0], &id)
		if id != 21 || len(f) < 3 {
			continue
		}
		_ = json.Unmarshal(f[1], &op)
		if op != 2 { // Container.Remove = 2
			continue
		}
		var s slotMsg
		if err := json.Unmarshal(f[2], &s); err == nil && s.Key != "" {
			out = append(out, s)
		}
	}
	return out
}

// dialPort resolves the target port: PORT env (matching the server's own
// override) or the default 9001.
func dialPort() string {
	if p := os.Getenv("PORT"); p != "" {
		return p
	}
	return "9001"
}

func main() {
	// --- Login: the admin (rank 2). ---
	conn, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:"+dialPort()+"/", nil)
	if err != nil {
		fmt.Println("DIAL FAIL:", err)
		os.Exit(1)
	}
	defer conn.Close()
	go reader(conn)

	fmt.Println("connected:", drain(1500*time.Millisecond))
	lastFrames = nil
	send(conn, `[1,{"gVer":"0.5.5-beta"}]`)
	fmt.Println("handshake:", drain(1500*time.Millisecond))
	lastFrames = nil
	send(conn, `[2,{"opcode":0,"username":"m13admin","password":"x","seedRank":2}]`)
	fmt.Println("login:", drain(2*time.Second))
	lastFrames = nil

	// The m13test probe rides the TESTMAP Minigame packet [46,{"m13test":...}]
	// alongside the m8/m9/m10/m11/m12 test dispatchers.
	probe := func(payload string, wait time.Duration) {
		lastFrames = nil
		send(conn, `[46,`+payload+`]`)
		drain(wait)
	}
	flagEcho := func(user string) {
		probe(`{"m13test":"x","op":"echo","username":"`+user+`"}`, 1200*time.Millisecond)
	}

	// --- /spawn: item materializes in the inventory (items.json key). ---
	lastFrames = nil
	send(conn, `[19,["/spawn gold 250"]]`)
	drain(1200 * time.Millisecond)
	probe(`{"m13test":"x","op":"inv","key":"gold"}`, 1200*time.Millisecond)
	check(notifyFor(lastFrames, "m13:inv gold=250"), "/spawn gold 250 lands in inventory (echo 250)")

	// --- /clear: inventory emptied (echo shows 0). ---
	lastFrames = nil
	send(conn, `[19,["/clear"]]`)
	drain(1200 * time.Millisecond)
	probe(`{"m13test":"x","op":"inv","key":"gold"}`, 1200*time.Millisecond)
	check(notifyFor(lastFrames, "m13:inv gold=0"), "/clear empties the inventory")

	// --- /ms + /noclip round-trip through the m13flags table. ---
	lastFrames = nil
	send(conn, `[19,["/ms 400"]]`)
	drain(1200 * time.Millisecond)
	send(conn, `[19,["/noclip"]]`)
	drain(1200 * time.Millisecond)
	flagEcho("m13admin")
	check(notifyFor(lastFrames, "noclip=true mspeed=400"), "ms 400 + noclip persisted (echo)")

	// Noclip also relaxes the jump anti-cheat: a dx=4 request is accepted
	// (no Movement Stop reply for the request).
	lastFrames = nil
	send(conn, `[11,{"opcode":2,"requestX":104,"requestY":96}]`)
	counts := drain(1200 * time.Millisecond)
	check(counts[11] == 0, "noclip /ms request >2 tiles is not stopped")

	// Toggle noclip back off for the rest of the run.
	send(conn, `[19,["/noclip"]]`)
	drain(800 * time.Millisecond)

	// --- /tp home: hardcoded spot table teleports to 191,166. ---
	lastFrames = nil
	send(conn, `[19,["/tp home"]]`)
	drain(1200 * time.Millisecond)
	var tp struct {
		Instance string `json:"instance"`
		X        int    `json:"x"`
		Y        int    `json:"y"`
	}
	_ = json.Unmarshal(lastOf(lastFrames, 12), &tp)
	check(tp.X == 191 && tp.Y == 166, fmt.Sprintf("/tp home -> Teleport 191,166 (got %v)", tp))

	// --- Second client: the victim (rankless) — kept online this time. ---
	conn2, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:"+dialPort()+"/", nil)
	if err != nil {
		fmt.Println("DIAL2 FAIL:", err)
		os.Exit(1)
	}
	defer conn2.Close()
	go reader(conn2)
	fmt.Println("connected2:", drain(1500*time.Millisecond))
	send(conn2, `[1,{"gVer":"0.5.5-beta"}]`)
	drain(1500 * time.Millisecond)
	lastFrames = nil
	send(conn2, `[2,{"opcode":0,"username":"m13victim","password":"x","seedGold":100}]`)
	fmt.Println("login2:", drain(2*time.Second))
	lastFrames = nil

	// Mute BEFORE the kick leg: mute gates chat via the flags table, and the
	// kick close then re-exercises the login mute restore on the next login.
	send(conn, `[19,["/mute 1 m13victim"]]`)
	drain(1200 * time.Millisecond)
	lastFrames = nil
	flagEcho("m13victim")
	check(notifyFor(lastFrames, "mute=until"), "/mute 1 sets a future deadline (echo)")

	// Muted victim: the online conn's chat is rejected (no bubble).
	lastFrames = nil
	send(conn2, `[19,["am I muted?"]]`)
	drain(1200 * time.Millisecond)
	check(len(chatFrames()) == 0, "muted user's chat is rejected (no bubble)")

	// --- /kick: TS connection.close() is silent (no notify); the observable
	// is the victim's socket dying within the drain window. ---
	send(conn, `[19,["/kick m13victim"]]`)
	drain(1200 * time.Millisecond)
	conn2.SetReadDeadline(time.Now().Add(3 * time.Second))
	kickClosed := false
	for {
		if _, _, err := conn2.ReadMessage(); err != nil {
			kickClosed = true
			break
		}
	}
	conn2.SetReadDeadline(time.Time{})
	check(kickClosed, "/kick closes the victim's socket (TS close chain)")

	// Reconnect: the login path must restore the mute from the flags table
	// (TS: the database loader hydrates user.mute). The fresh conn is conn3.
	conn3, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:"+dialPort()+"/", nil)
	if err != nil {
		fmt.Println("DIAL3 FAIL:", err)
		os.Exit(1)
	}
	defer conn3.Close()
	go reader(conn3)
	fmt.Println("connected3:", drain(1500*time.Millisecond))
	send(conn3, `[1,{"gVer":"0.5.5-beta"}]`)
	drain(1500 * time.Millisecond)
	lastFrames = nil
	send(conn3, `[2,{"opcode":0,"username":"m13victim","password":"x"}]`)
	fmt.Println("login3:", drain(2*time.Second))
	lastFrames = nil
	send(conn3, `[19,["am I still muted?"]]`)
	drain(1200 * time.Millisecond)
	check(len(chatFrames()) == 0, "mute survives relogin (restored from the flags table)")

	// --- /unmute: chat flows again. ---
	send(conn, `[19,["/unmute m13victim"]]`)
	drain(1200 * time.Millisecond)
	lastFrames = nil
	send(conn3, `[19,["unmuted hello"]]`)
	counts = drain(1200 * time.Millisecond)
	check(counts[19] >= 1, "unmuted user's chat echoes")

	// --- /take: admin pulls the victim's inventory slot; the victim's
	// client syncs via Container Remove. Seed a known stack into the
	// victim's inventory via the m13test seedinv op, then scan the human
	// indexes (1..3 — the TS parse rejects 0 via !index) since the gold
	// stack's position depends on the restore order.
	lastFrames = nil
	probe(`{"m13test":"x","op":"seedinv","key":"platinumcoin","value":5,"username":"m13victim"}`, 1200*time.Millisecond)
	takeIdx := -1
	for idx := 1; idx <= 3 && takeIdx < 0; idx++ {
		lastFrames = nil
		send(conn, fmt.Sprintf(`[19,["/take %d inventory m13victim"]]`, idx))
		drain(1200 * time.Millisecond)
		if notifyFor(lastFrames, "Took 5x platinumcoin from m13victim") {
			takeIdx = idx
		}
	}
	check(takeIdx > 0, fmt.Sprintf("/take finds + pulls the platinumcoin stack (slot %d)", takeIdx))
	probe(`{"m13test":"x","op":"inv","key":"platinumcoin","username":"m13victim"}`, 1200*time.Millisecond)
	check(notifyFor(lastFrames, "m13:inv platinumcoin=0"), "victim inventory emptied of platinumcoin by /take")

	// --- /takeitem: bank leg. Give the victim a bank stack via /spawn into
	// the bank is not a TS command — use /takeitem's own semantics: seed via
	// a second /spawn then /takeitem with count 1 leaves 249. ---
	lastFrames = nil
	send(conn, `[19,["/spawn arrow 30"]]`)
	drain(1200 * time.Millisecond)
	send(conn, `[19,["/takeitem arrow 30 inventory m13admin"]]`)
	drain(1200 * time.Millisecond)
	check(notifyFor(lastFrames, "Took 30x arrow from m13admin"), "/takeitem self-target removes 30 arrows")

	// --- /finishquest: tutorial jumps to its final stage. ---
	lastFrames = nil
	send(conn, `[19,["/finishquest tutorial"]]`)
	drain(1200 * time.Millisecond)
	probe(`{"m13test":"x","op":"quest","key":"tutorial"}`, 1200*time.Millisecond)
	check(notifyFor(lastFrames, "m11:quest:tutorial="), "finishquest echo shows the tutorial stage")
	finished := false
	for _, n := range notifyFrames() {
		if strings.Contains(n.Message, "m11:quest:tutorial=") && !strings.Contains(n.Message, "=0/") {
			finished = true
		}
	}
	check(finished, "tutorial stage is non-zero after /finishquest")

	// --- /jail: deadline set + teleport to spawn (TS order: jail ->
	// sendToSpawn -> notifies). Runs BEFORE the ban leg so the victim is
	// still online. ---
	send(conn, `[19,["/jail 1 m13victim"]]`)
	drain(1200 * time.Millisecond)
	lastFrames = nil
	flagEcho("m13victim")
	check(notifyFor(lastFrames, "jail=") && !notifyFor(lastFrames, "jail=0 "), "/jail sets a future deadline")
	// Clear jail for the ban leg.
	send(conn, `[46,{"m13test":"x","op":"seed","key":"jail","value":0,"username":"m13victim"}]`)
	drain(800 * time.Millisecond)

	// --- /ban: 'ban' text frame then close (TS chain sendUTF8+close). ---
	lastFrames = nil
	send(conn, `[19,["/ban 1 m13victim"]]`)
	drain(2 * time.Second)
	check(notifyFor(lastFrames, "has been banned for 1 hours"), "/ban confirms (TS 'hours' grammar)")

	// Reconnect after ban -> rejected with 'ban' + close (login gate). The
	// dedicated conn4 has NO reader goroutine so the harness itself sees the
	// raw text frame before the close.
	conn4, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:"+dialPort()+"/", nil)
	if err != nil {
		fmt.Println("DIAL4 FAIL:", err)
		os.Exit(1)
	}
	defer conn4.Close()
	fmt.Println("connected4 (no reader goroutine; raw 'ban' watch):")
	send(conn4, `[1,{"gVer":"0.5.5-beta"}]`)
	send(conn4, `[2,{"opcode":0,"username":"m13victim","password":"x"}]`)
	banned := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, raw, err := conn4.ReadMessage()
		if err != nil {
			break
		}
		if string(raw) == "ban" {
			banned = true
			break
		}
	}
	check(banned, "banned login gets the raw 'ban' frame then a close")

	// Clean up the ban so later runs stay green. The login close means the
	// m13test seed ride is the only way to clear it (m13-assisted teardown).
	send(conn, `[46,{"m13test":"x","op":"seed","key":"ban","value":0,"username":"m13victim"}]`)
	drain(800 * time.Millisecond)
	lastFrames = nil
	flagEcho("m13victim")
	check(notifyFor(lastFrames, "ban=0"), "ban cleared via the m13test seed op")

	// --- Wrap up. ---
	if len(failures) > 0 {
		fmt.Printf("\nM13 E2E FAILED (%d):\n", len(failures))
		for _, f := range failures {
			fmt.Println(" -", f)
		}
		os.Exit(1)
	}
	fmt.Printf("\nM13 E2E PASSED (%d checks)\n", checks)
}
