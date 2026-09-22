// Ops wiring (API + console) — behavior-additive root glue over
// internal/api, internal/console, internal/net and internal/world.
//
//   - API: an api.Server is started on API_PORT only when API_PORT is set
//     (default off). Providers read live state (players count, guild list,
//     status). A busy port never blocks game boot (log + continue).
//   - Console: a stdin bufio loop in a goroutine feeds console.Exec with a
//     Handler over the live world. Disabled when stdin is not a TTY or when
//     CONSOLE=0 (tests/harness must not hang on stdin).
//   - Limits/accept gate: owned by internal/net (Hub: per-IP cap at accept,
//     per-message budget in the read path, chat bucket in the m7 path, IP
//     bans, update-mode gate). This file only drives the Hub from console
//     commands (/update, /ipban); per-conn state is forgotten on disconnect
//     by the Hub release path (D2a: no transport globals stay in root).
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

	"github.com/gorilla/websocket"

	"rpg-world-server/internal/api"
	"rpg-world-server/internal/console"
	gnet "rpg-world-server/internal/net"
	worldcore "rpg-world-server/internal/world"
)

// opsGameVersion / opsMaxPlayers feed the API status snapshot (the stub has
// no config file; the handshake gVer is client-supplied so there is no
// server constant to reuse).
const (
	opsGameVersion = "1.0.0"
	opsMaxPlayers  = 200
)

// Transport + accept-gate state moved to internal/net (D2a): the Hub owns
// the limiter, the update-mode gate, the admitted-conn table and the IP ban
// set. The accept/release/budget entry points live there as package funcs
// (gnet.Accept/Release/AllowMsg/AllowChat); console commands below drive
// the Hub via gnet.SetAccepting/gnet.BanIP/gnet.BannedIPs.

// Transport + accept-gate entry points moved to internal/net (D2a): use
// gnet.Accept/gnet.Release/gnet.AllowMsg/gnet.AllowChat at the former
// opsAccept/opsRelease/opsAllowMsg/opsAllowChat call sites (main.go
// handler, handleConn read path, m7 chat path).

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

// opsPlayerUsernames snapshots online usernames via the world Registry
// (the Registry mutex is released before the caller touches player state,
// preserving the old playersMu/pstateMu order).
func opsPlayerUsernames() []string {
	return m7PlayerUsernames()
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
	return api.Status{
		Name:        "kaetram-stub",
		Port:        port,
		GameVersion: opsGameVersion,
		MaxPlayers:  opsMaxPlayers,
		PlayerCount: worldcore.PlayerCount(),
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
	gnet.SetAccepting(false)
	conns := worldcore.AllWS()
	for _, k := range conns {
		worldcore.RemoveClient(k)
	}
	return fmt.Sprintf("Server updating: rejected %d connection(s), new connections disabled.", len(conns))
}

func (opsConsole) Kill(username string) string {
	target := m7PlayerByName(username)
	if target == nil {
		return fmt.Sprintf("Player %s not found.", username)
	}
	m9DamagePlayer(target, m9PlayerHP(target), nil)
	return fmt.Sprintf("%s has been killed.", target.Username)
}

func opsConsoleDrop(username, verb string) string {
	target := m7PlayerByName(username)
	if target == nil {
		return fmt.Sprintf("Player %s not found.", username)
	}
	name := target.Username
	worldcore.RemoveClient(target.Conn.WS)
	return fmt.Sprintf("%s has been %s.", name, verb)
}

func (o opsConsole) Kick(username string) string    { return opsConsoleDrop(username, "kicked") }
func (o opsConsole) Timeout(username string) string { return opsConsoleDrop(username, "timed out") }

func opsConsoleSetRank(username string, rank int, title string) string {
	target := m7PlayerByName(username)
	if target == nil {
		return fmt.Sprintf("Player %s not found.", username)
	}
	target.rank = rank
	chatStateFor(target).rank = rank
	name := target.Username
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
		out := gnet.BannedIPs()
		sort.Strings(out)
		if len(out) == 0 {
			return "No banned IPs."
		}
		return "Banned IPs: " + strings.Join(out, ", ")
	}
	gnet.BanIP(ip)
	var conns []*websocket.Conn
	for _, k := range worldcore.AllWS() {
		host, _, err := net.SplitHostPort(gnet.AddrID(k))
		if err != nil {
			host = gnet.AddrID(k)
		}
		if host == ip {
			conns = append(conns, k)
		}
	}
	for _, k := range conns {
		worldcore.RemoveClient(k)
	}
	return fmt.Sprintf("Banned %s (%d connection(s) dropped).", ip, len(conns))
}

func (opsConsole) Save() string {
	flushDirty()
	return "World saved."
}
