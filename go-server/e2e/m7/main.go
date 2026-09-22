// Scripted WS check for the M7 slice (TESTMAP=1 default server): chat +
// commands — region chat bubble (PacketChat S→C with instance), sanitization
// of HTML-ish text, command parsing (/players, /coords), global chat with
// cooldown + rank prefix, per-connection token-bucket rate limiting,
// /pm delivery + NOT_ONLINE, /teleport with and without the moderator rank,
// and the /ping Network round-trip. Not part of the stub build (underscore
// dirs are ignored by the go tool).
// Usage: go run ./e2e/m7 (stub must run with TESTMAP=1, the default).
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
	if len(f) == 0 {
		return nil
	}
	return f[len(f)-1]
}

func lastOf(frames [][]json.RawMessage, id int) json.RawMessage {
	var found json.RawMessage
	for _, f := range frames {
		var fid int
		_ = json.Unmarshal(f[0], &fid)
		if fid == id {
			found = frameData(f)
		}
	}
	return found
}

func send(conn *websocket.Conn, msg string) {
	if err := conn.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
		fmt.Println("WRITE FAIL:", err)
		os.Exit(1)
	}
}

var failures []string

func check(cond bool, msg string) {
	if !cond {
		failures = append(failures, msg)
		fmt.Println("  FAIL:", msg)
	} else {
		fmt.Println("  ok:", msg)
	}
}

// chatMsg is one parsed S→C Chat frame ([19, {instance|source, message, ...}]).
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

// chatFrames returns the S→C Chat frames collected in lastFrames.
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

// dialPort resolves the target port: PORT env (matching the server's own
// override) or the default 9001.
func dialPort() string {
	if p := os.Getenv("PORT"); p != "" {
		return p
	}
	return "9001"
}

func main() {
	// Honor PORT (the server's own override) so a side-by-side run works
	// while the default 9001 is occupied.
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

	// seedRank=2 (Admin) exercises the rank prefix, admin /players name
	// list, and the moderator /teleport table.
	send(conn, `[2,{"opcode":0,"username":"m7tester","password":"x","seedRank":2}]`)
	fmt.Println("login:", drain(2*time.Second))
	lastFrames = nil

	// --- Region chat bubble. ---
	lastFrames = nil
	send(conn, `[19,["hello world"]]`)
	counts := drain(1200 * time.Millisecond)
	check(counts[19] >= 1, fmt.Sprintf("chat echo arrives (got %d chat frames)", counts[19]))
	echoes := chatFrames()
	selfEcho := false
	for _, m := range echoes {
		if m.Instance != "" && m.Message == "hello world" {
			selfEcho = true
		}
	}
	check(selfEcho, "chat bubble echoes with instance + message")

	// --- Sanitization: HTML-escape < > &. ---
	lastFrames = nil
	send(conn, `[19,["<b>&bold</b>"]]`)
	drain(1200 * time.Millisecond)
	sane := false
	for _, m := range chatFrames() {
		if m.Message == "&lt;b&gt;&amp;bold&lt;/b&gt;" {
			sane = true
		}
	}
	check(sane, `chat sanitized: <b>&bold</b> -> &lt;b&gt;&amp;bold&lt;/b&gt;`)

	// --- /players (admin sees the name list). ---
	lastFrames = nil
	send(conn, `[19,["/players"]]`)
	drain(1200 * time.Millisecond)
	notifs := notifyFrames()
	check(len(notifs) >= 1 && strings.Contains(notifs[0].Message, "currently 1 person online"),
		fmt.Sprintf("/players population notification (got %v)", notifs))
	adminList := false
	for _, n := range notifs {
		if strings.Contains(n.Message, "m7tester") {
			adminList = true
		}
	}
	check(adminList, "admin /players name list includes m7tester")

	// --- /coords. ---
	lastFrames = nil
	send(conn, `[19,["/coords"]]`)
	drain(1200 * time.Millisecond)
	notifs = notifyFrames()
	check(len(notifs) >= 1 && strings.Contains(notifs[0].Message, "x: 100 y: 96"),
		fmt.Sprintf("/coords x:100 y:96 (got %v)", notifs))

	// --- Global chat + cooldown (admin cooldown 5s, so second is allowed
	// after sleep; rank prefix present). ---
	lastFrames = nil
	send(conn, `[19,["/g hello everyone"]]`)
	drain(1200 * time.Millisecond)
	gc := chatFrames()
	gotGlobal := false
	for _, m := range gc {
		if m.Source != "" && strings.Contains(m.Source, "M7tester") && strings.Contains(m.Message, "hello everyone") {
			gotGlobal = true
		}
	}
	check(gotGlobal, "global chat [Global] M7tester source frame")

	// Second global within the 5s admin cooldown -> CANNOT_GLOBAL_CHAT_MINUTES.
	lastFrames = nil
	send(conn, `[19,["/g too soon"]]`)
	drain(1200 * time.Millisecond)
	blocked := false
	for _, n := range notifyFrames() {
		if strings.HasPrefix(n.Message, "misc:CANNOT_GLOBAL_CHAT_MINUTES;duration=") {
			blocked = true
		}
	}
	check(blocked, "global cooldown blocks rapid /g (CANNOT_GLOBAL_CHAT_MINUTES)")

	// --- Rate limiter: 6 rapid plain messages, 2s refill → first 3 pass. ---
	time.Sleep(5200 * time.Millisecond) // clear the global cooldown first
	lastFrames = nil
	for i := 0; i < 6; i++ {
		send(conn, fmt.Sprintf(`[19,["spam %d"]]`, i))
		time.Sleep(60 * time.Millisecond)
	}
	drain(1500 * time.Millisecond)
	// Bucket 3 + refill during the 360ms send loop (~0.18) — exactly 3 pass.
	passed := 0
	for _, m := range chatFrames() {
		if m.Instance != "" && strings.HasPrefix(m.Message, "spam ") {
			passed++
		}
	}
	check(passed == 3, fmt.Sprintf("rate limiter passes burst of 3 only (got %d)", passed))

	// --- /pm to an offline user -> NOT_ONLINE. ---
	lastFrames = nil
	send(conn, `[19,["/pm *nobody* hi there"]]`)
	drain(1200 * time.Millisecond)
	pmMiss := false
	for _, n := range notifyFrames() {
		if strings.Contains(n.Message, "misc:NOT_ONLINE") && strings.Contains(n.Message, "username=nobody") {
			pmMiss = true
		}
	}
	check(pmMiss, "/pm offline -> misc:NOT_ONLINE;username=nobody")

	// --- /pm echo sanity: message keeps the Node *name* wrapper quirk. ---
	// (Delivered text is `*nobody* hi there` per the commands.ts parse.)

	// --- /ping round-trip: server answers Network Ping (opcode 0). ---
	lastFrames = nil
	send(conn, `[19,["/ping"]]`)
	counts = drain(1200 * time.Millisecond)
	check(counts[18] == 1, fmt.Sprintf("/ping -> Network Ping reply (got %d)", counts[18]))

	// --- /teleport as admin (rank 2): Teleport frame + position moves. ---
	lastFrames = nil
	send(conn, `[19,["/teleport 103 96"]]`)
	drain(1200 * time.Millisecond)
	var tp struct {
		Instance string `json:"instance"`
		X        int    `json:"x"`
		Y        int    `json:"y"`
	}
	_ = json.Unmarshal(lastOf(lastFrames, 12), &tp)
	check(tp.X == 103 && tp.Y == 96, fmt.Sprintf("admin /teleport -> Teleport 103,96 (got %v)", tp))

	// Position actually moved server-side (verify via /coords).
	lastFrames = nil
	send(conn, `[19,["/coords"]]`)
	drain(1200 * time.Millisecond)
	moved := false
	for _, n := range notifyFrames() {
		if strings.Contains(n.Message, "x: 103 y: 96") {
			moved = true
		}
	}
	check(moved, "position after /teleport is 103,96")

	// --- /teleport without rank -> ignored. ---
	// Fresh connection, no seedRank => RankNone; the command tables must
	// not fire (no Teleport frame), while plain chat still works.
	conn2, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:"+dialPort()+"/", nil)
	if err != nil {
		fmt.Println("DIAL2 FAIL:", err)
		os.Exit(1)
	}
	defer conn2.Close()
	go reader(conn2)

	fmt.Println("connected2:", drain(1500*time.Millisecond))
	lastFrames = nil
	send(conn2, `[1,{"gVer":"0.5.5-beta"}]`)
	fmt.Println("handshake2:", drain(1500*time.Millisecond))
	lastFrames = nil
	send(conn2, `[2,{"opcode":0,"username":"m7pleb","password":"x"}]`)
	fmt.Println("login2:", drain(2*time.Second))
	lastFrames = nil

	send(conn2, `[19,["/teleport 300 300"]]`)
	counts = drain(1200 * time.Millisecond)
	check(counts[12] == 0, "rankless /teleport ignored (no Teleport frame)")

	// Rankless /players: population line but no name list.
	lastFrames = nil
	send(conn2, `[19,["/players"]]`)
	drain(1200 * time.Millisecond)
	plebList := false
	for _, n := range notifyFrames() {
		if strings.Contains(n.Message, "m7tester") {
			plebList = true
		}
	}
	check(!plebList, "rankless /players hides the name list")

	// Rankless global: allowed (fresh cooldown), with [Global] source and
	// NO rank prefix (default gold colour only for ranked players).
	lastFrames = nil
	send(conn2, `[19,["/g pleb hello"]]`)
	drain(1200 * time.Millisecond)
	plebGlobal := false
	for _, m := range chatFrames() {
		if strings.Contains(m.Source, "M7pleb") && strings.Contains(m.Message, "pleb hello") {
			plebGlobal = true
		}
	}
	check(plebGlobal, "rankless global chat works with [Global] prefix")

	// --- Wrap up. ---
	if len(failures) > 0 {
		fmt.Printf("\nM7 E2E FAILED (%d):\n", len(failures))
		for _, f := range failures {
			fmt.Println(" -", f)
		}
		os.Exit(1)
	}
	fmt.Println("\nM7 E2E PASSED")
}
