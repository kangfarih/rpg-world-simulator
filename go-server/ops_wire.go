// Ops wiring (API + console + rate limits) — behavior-additive root glue over
// internal/api, internal/console and internal/net.
//
//   - API: an api.Server is started on API_PORT only when API_PORT is set
//     (default off). Providers read live state (players count, guild list,
//     status). A busy port never blocks game boot (log + continue).
//   - Console: a stdin bufio loop in a goroutine feeds console.Exec with a
//     Handler over the live world. Disabled when stdin is not a TTY or when
//     CONSOLE=0 (tests/harness must not hang on stdin).
//   - Limits: a shared net.Limiter enforces the per-IP connection cap at
//     accept (reject + log over 16), the per-message budget in the accept
//     read path (drop over budget) and the chat bucket in the m7 path (same
//     silent drop as the chatState bucket-exhaust today). Per-conn state is
//     forgotten on disconnect.
//
// With no env set the API never starts, the console never starts, and the
// limiter runs at the same budgets the server already enforced (16/IP,
// 300 msg/s, chat 3 burst @ 0.5/s), so existing behavior is unchanged.
package main

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"rpg-world-server/internal/api"
	"rpg-world-server/internal/console"
	opsnet "rpg-world-server/internal/net"
)

// opsGameVersion / opsMaxPlayers feed the API status snapshot (the stub has
// no config file; the handshake gVer is client-supplied so there is no
// server constant to reuse).
const (
	opsGameVersion = "1.0.0"
	opsMaxPlayers  = 200
)

// opsLimiter is the shared rate limiter (defaults mirror the TS/Go
// conventions: 16 conns/IP, 300 msg/s/conn, chat 3 burst @ 0.5/s).
var opsLimiter = opsnet.NewLimiterFromConfig(opsnet.Config{})

// opsAccepting gates new connections (console /update flips it; default true).
var opsAccepting atomic.Bool

// opsConns tracks admitted conns for observability (conn -> client IP).
var (
	opsConnsMu sync.Mutex
	opsConns   = map[*websocket.Conn]string{}
)

// opsIPBans is the console-managed IP ban set (empty by default, so the
// accept path behaves exactly as before until /ipban is used).
var (
	opsIPMu   sync.Mutex
	opsIPBans = map[string]bool{}
)

func init() { opsAccepting.Store(true) }

// opsClientIP strips the port from an HTTP remote address ("1.2.3.4:5678" ->
// "1.2.3.4"); unparseable input is returned as-is.
func opsClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// opsConnID is the limiter's per-connection key. RemoteAddr (ip:port) is
// unique per conn and stays available after close, so release-time Forget
// needs no extra bookkeeping.
func opsConnID(conn *websocket.Conn) string {
	if conn == nil {
		return ""
	}
	if a := conn.RemoteAddr(); a != nil {
		return a.String()
	}
	return ""
}

// opsAccept gates one HTTP request before the WS upgrade: update-mode and
// IP bans reject first, then the per-IP cap (reject + log over 16), then the
// upgrade. A failed upgrade releases the acquired slot.
func opsAccept(w http.ResponseWriter, r *http.Request) (*websocket.Conn, bool) {
	if !opsAccepting.Load() {
		http.Error(w, "server updating", http.StatusServiceUnavailable)
		return nil, false
	}
	ip := opsClientIP(r)
	opsIPMu.Lock()
	banned := opsIPBans[ip]
	opsIPMu.Unlock()
	if banned {
		log.Printf("ops: reject banned ip=%s", ip)
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, false
	}
	if !opsLimiter.Acquire(ip) {
		log.Printf("ops: reject ip=%s over per-IP cap (%d)", ip, opsnet.DefaultMaxConnectionsPerIP)
		http.Error(w, "too many connections", http.StatusTooManyRequests)
		return nil, false
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrade: %v", err)
		opsLimiter.Release(ip)
		return nil, false
	}
	opsConnsMu.Lock()
	opsConns[conn] = ip
	opsConnsMu.Unlock()
	log.Printf("client connected: %s", r.RemoteAddr)
	return conn, true
}

// opsRelease frees the limiter slot and per-conn msg/chat state for a conn
// whose handleConn loop has returned (every disconnect path funnels there:
// read errors, kicks, bans and the deferred removeClient).
func opsRelease(conn *websocket.Conn) {
	if conn == nil {
		return
	}
	opsConnsMu.Lock()
	ip, ok := opsConns[conn]
	if ok {
		delete(opsConns, conn)
	}
	opsConnsMu.Unlock()
	if !ok {
		return
	}
	opsLimiter.Forget(opsConnID(conn))
	opsLimiter.Release(ip)
}

// opsAllowMsg reports whether one inbound frame from id may be processed
// (drops over the per-message budget; caller logs the drop).
func opsAllowMsg(id string) bool {
	if id == "" {
		return true
	}
	return opsLimiter.AllowMsg(id, time.Now().UnixMilli())
}

// opsAllowChat reports whether conn may send one chat message under the
// limiter bucket. Rejection drops silently, matching the chatState
// bucket-exhaust path in m7HandleChat today (no notify).
func opsAllowChat(c *playerConn) bool {
	if c == nil || c.conn == nil {
		return true
	}
	return opsLimiter.AllowChat(opsConnID(c.conn), time.Now().UnixMilli())
}

// ---------------------------------------------------------------------------
// API.
// ---------------------------------------------------------------------------

// opsStartAPI starts the read-only REST surface when API_PORT is set
// (default off). A busy port logs and the game boots without the API.
func opsStartAPI() {
	addr, ok := api.AddrFromEnv()
	if !ok {
		return
	}
	srv := api.NewServer(opsPlayers{}, opsGuilds{}, opsStatus{})
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("ops: api listen %s failed (%v), continuing without API", addr, err)
		return
	}
	go func() {
		log.Printf("ops: api listening on %s", ln.Addr())
		if err := http.Serve(ln, srv.Handler()); err != nil && err != http.ErrServerClosed {
			log.Printf("ops: api stopped: %v", err)
		}
	}()
}

// opsPlayers serves live player snapshots to the API.
type opsPlayers struct{}

// opsPlayerUsernames snapshots online usernames (lock released before the
// caller touches player state, preserving the playersMu/pstateMu order).
func opsPlayerUsernames() []string {
	playersMu.Lock()
	defer playersMu.Unlock()
	names := make([]string, 0, len(players))
	for _, c := range players {
		if c.username != "" {
			names = append(names, c.username)
		}
	}
	return names
}

func opsPlayerLevel(username string) int {
	if st := m5StateFor(username); st != nil && st.Level > 0 {
		return st.Level
	}
	return 1
}

func (opsPlayers) ListPlayers() []api.Player {
	names := opsPlayerUsernames()
	out := make([]api.Player, 0, len(names))
	for _, n := range names {
		out = append(out, api.Player{Name: n, Level: opsPlayerLevel(n), Online: true})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (opsPlayers) GetPlayer(name string) (api.Player, bool) {
	for _, n := range opsPlayerUsernames() {
		if strings.EqualFold(n, name) {
			return api.Player{Name: n, Level: opsPlayerLevel(n), Online: true}, true
		}
	}
	return api.Player{}, false
}

// opsGuilds serves the live guild list to the API.
type opsGuilds struct{}

func (opsGuilds) ListGuilds() []api.Guild {
	socMu.Lock()
	ids := make([]string, 0, len(socGuildIDs))
	for id := range socGuildIDs {
		ids = append(ids, id)
	}
	socMu.Unlock()
	out := make([]api.Guild, 0, len(ids))
	for _, id := range ids {
		g, err := socGuilds.Get(id)
		if err != nil {
			continue
		}
		members := make([]string, 0, len(g.Members))
		for u := range g.Members {
			members = append(members, u)
		}
		sort.Strings(members)
		out = append(out, api.Guild{Name: g.Name, Members: members})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// opsStatus serves the server status snapshot to the API.
type opsStatus struct{}

func (opsStatus) Status() api.Status {
	port := 9001
	if p := os.Getenv("PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}
	playersMu.Lock()
	n := len(players)
	playersMu.Unlock()
	return api.Status{
		Name:        "kaetram-stub",
		Port:        port,
		GameVersion: opsGameVersion,
		MaxPlayers:  opsMaxPlayers,
		PlayerCount: n,
	}
}

// ---------------------------------------------------------------------------
// Console.
// ---------------------------------------------------------------------------

// opsStartConsole starts the stdin console loop unless CONSOLE=0 or stdin is
// not a TTY (pipes — tests, harnesses, CI — never block on stdin).
func opsStartConsole() {
	if v := os.Getenv("CONSOLE"); v == "0" || v == "false" || v == "off" || v == "no" {
		log.Printf("ops: console disabled (CONSOLE=%s)", v)
		return
	}
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		log.Printf("ops: console disabled (stdin not a TTY)")
		return
	}
	h := &opsConsole{}
	go func() {
		log.Printf("ops: console ready (slash commands)")
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			if out := console.Exec(h, sc.Text()); out != "" {
				log.Printf("console: %s", out)
			}
		}
		if err := sc.Err(); err != nil {
			log.Printf("ops: console ended: %v", err)
		}
	}()
}

// opsConsole applies console commands to the live world. Output strings
// mirror the TS console log.info lines; the caller prints them.
type opsConsole struct{}

func (opsConsole) Players() string {
	names := m7PlayerUsernames()
	if len(names) == 1 {
		return "There is currently 1 person online: " + names[0]
	}
	return fmt.Sprintf("There are currently %d people online: %s", len(names), strings.Join(names, ", "))
}

func (opsConsole) Total() string {
	if dbConn == nil {
		return "Total players: 0"
	}
	dbMu.Lock()
	defer dbMu.Unlock()
	var n int
	if err := dbConn.QueryRow(`SELECT COUNT(*) FROM players`).Scan(&n); err != nil {
		return fmt.Sprintf("Total players: unknown (%v)", err)
	}
	return fmt.Sprintf("Total players: %d", n)
}

func (opsConsole) Update() string {
	opsAccepting.Store(false)
	playersMu.Lock()
	conns := make([]*websocket.Conn, 0, len(players))
	for k := range players {
		conns = append(conns, k)
	}
	playersMu.Unlock()
	for _, k := range conns {
		removeClient(k)
	}
	return fmt.Sprintf("Server updating: rejected %d connection(s), new connections disabled.", len(conns))
}

func (opsConsole) Kill(username string) string {
	target := m7PlayerByName(username)
	if target == nil {
		return fmt.Sprintf("Player %s not found.", username)
	}
	m9DamagePlayer(target, m9PlayerHP(target), nil)
	return fmt.Sprintf("%s has been killed.", target.username)
}

func opsConsoleDrop(username, verb string) string {
	target := m7PlayerByName(username)
	if target == nil {
		return fmt.Sprintf("Player %s not found.", username)
	}
	name := target.username
	removeClient(target.conn)
	return fmt.Sprintf("%s has been %s.", name, verb)
}

func (o opsConsole) Kick(username string) string    { return opsConsoleDrop(username, "kicked") }
func (o opsConsole) Timeout(username string) string { return opsConsoleDrop(username, "timed out") }

func opsConsoleSetRank(username string, rank int, title string) string {
	target := m7PlayerByName(username)
	if target == nil {
		return fmt.Sprintf("Player %s not found.", username)
	}
	playersMu.Lock()
	target.rank = rank
	chatStateFor(target).rank = rank
	name := target.username
	playersMu.Unlock()
	return fmt.Sprintf("%s is now %s.", name, title)
}

func (o opsConsole) SetAdmin(username string) string {
	return opsConsoleSetRank(username, RankAdmin, "an admin")
}
func (o opsConsole) SetMod(username string) string {
	return opsConsoleSetRank(username, RankModerator, "a moderator")
}

func (opsConsole) IPBan(ip string) string {
	if ip == "list" {
		opsIPMu.Lock()
		out := make([]string, 0, len(opsIPBans))
		for k := range opsIPBans {
			out = append(out, k)
		}
		opsIPMu.Unlock()
		sort.Strings(out)
		if len(out) == 0 {
			return "No banned IPs."
		}
		return "Banned IPs: " + strings.Join(out, ", ")
	}
	opsIPMu.Lock()
	opsIPBans[ip] = true
	opsIPMu.Unlock()
	playersMu.Lock()
	var conns []*websocket.Conn
	for k := range players {
		host, _, err := net.SplitHostPort(opsConnID(k))
		if err != nil {
			host = opsConnID(k)
		}
		if host == ip {
			conns = append(conns, k)
		}
	}
	playersMu.Unlock()
	for _, k := range conns {
		removeClient(k)
	}
	return fmt.Sprintf("Banned %s (%d connection(s) dropped).", ip, len(conns))
}

func (opsConsole) Save() string {
	flushDirty()
	return "World saved."
}
