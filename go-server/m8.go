package main

// M8 — minigames (coursing + teamwar, the minigames slice of the clone).
//
// Ports game/minigames/minigame.ts (base: lobby areas, add/remove, stop,
// disconnect, tick), impl/coursing.ts (hunter/prey split + distance-from-
// centre scoring) and impl/teamwar.ts (red/blue kills). Areas come from
// world.json areas.minigame (mObjectType lobby/hunterspawn/preyspawn/centre/
// redteamspawn/blueteamspawn), linked by the `minigame` key exactly like
// minigames.ts linkAreas. S→C frames are Minigame(46,[opcode,{action,...}])
// (common/network/impl/minigame.ts) and Pointer(36,[opcode,{instance}]).
//
// Documented divergences (stub world): players never die in the stub
// (no PVP deaths), so teamwar kill points are driven through the M8TEST
// debug frame; the lobby countdown packets arrive on their real 1s cadence
// but the harness only samples them.

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Constants (Opcodes.Minigame, MinigameState/Actions, Modules.MinigameConstants).
// ---------------------------------------------------------------------------

const (
	MinigameTeamWar  = 0 // Opcodes.Minigame.TeamWar
	MinigameCoursing = 1 // Opcodes.Minigame.Coursing
)

const (
	MinigameActionScore = 0 // MinigameActions.Score
	MinigameActionEnd   = 1 // MinigameActions.End
	MinigameActionLobby = 2 // MinigameActions.Lobby
	MinigameActionExit  = 3 // MinigameActions.Exit
)

// MinigameState (opcodes.ts:184) — Lobby0 End1 Exit2. The base minigame.ts
// uses THESE for addPlayer/removePlayer (a Node inconsistency with the
// MinigameActions values used by the ticks; cloned verbatim).
const (
	minigameStateLobby = 0
	minigameStateEnd   = 1
	minigameStateExit  = 2
)

const (
	PointerLocation = 0 // Opcodes.Pointer.Location
	PointerEntity   = 1 // Opcodes.Pointer.Entity
	PointerRemove   = 3 // Opcodes.Pointer.Remove
)

const teamWarCountdown = 240      // TEAM_WAR_COUNTDOWN (lobby + in-game seconds)
const teamWarMinPlayers = 2       // TEAM_WAR_MIN_PLAYERS
const coursingCountdown = 45      // COURSING_COUNTDOWN
const coursingMinPlayers = 2      // COURSING_MIN_PLAYERS
const coursingScoreDivisor = 10   // COURSING_SCORE_DIVISOR
const coursingTeamPrey = 2        // Team.Prey (api/minigame.ts)
const coursingTeamHunter = 3      // Team.Hunter
const teamWarTeamRed = 0          // Team.Red
const teamWarTeamBlue = 1         // Team.Blue
const coursingDeadPenalty = -10   // coursing.ts dead-player per-tick score
const coursingScoreTick = 4       // score update every 4 ticks
const coursingPointerDelay = 1500 // ms before sendPointers (coursing.ts)
const minigameTickInterval = 1000 // minigame.ts tickInterval (1s)
const killPointsPerKill = 1       // teamwar.kill increments by one per kill

// Countdown overrides for fast e2e runs (same convention as M4_RESPAWN_MS).
// Defaults are the exact Node constants; env values >0 win.
func m8EnvCountdown(env string, def int) int {
	if v := os.Getenv(env); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// ---------------------------------------------------------------------------
// Area model (areas/area.ts + areas/impl/minigame.ts).
// ---------------------------------------------------------------------------

// m8Area is one world.json areas.minigame entry.
type m8Area struct {
	ID          int    `json:"id"`
	X           int    `json:"x"`
	Y           int    `json:"y"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	MObjectType string `json:"mObjectType"`
	Minigame    string `json:"minigame"`
}

// inside reports whether (x,y) falls within the area rectangle (area.ts
// boundary math: x <= px < x+width).
func (a *m8Area) inside(x, y int) bool {
	if a == nil || a.Width <= 0 || a.Height <= 0 {
		return false
	}
	return x >= a.X && x < a.X+a.Width && y >= a.Y && y < a.Y+a.Height
}

// m8RandomPointIn returns a random point inside the area using the Node
// spawn formula (coursing/teamwar getSpawnPoint: x+1 .. x+width-1).
func m8RandomPointIn(a *m8Area) (int, int) {
	if a == nil || a.Width <= 2 || a.Height <= 2 {
		if a != nil {
			return a.X, a.Y
		}
		return 100, 96
	}
	return a.X + 1 + rand.Intn(a.Width-1), a.Y + 1 + rand.Intn(a.Height-1)
}

// m8LobbyPoint ports Minigame.getLobbyPosition: x+2 .. x+width-3 (the tighter
// inset used for teleport-backs, distinct from the spawn formula).
func m8LobbyPoint(a *m8Area) (int, int) {
	if a == nil || a.Width <= 5 || a.Height <= 5 {
		if a != nil {
			return a.X, a.Y
		}
		return 100, 96
	}
	return a.X + 2 + rand.Intn(a.Width-4), a.Y + 2 + rand.Intn(a.Height-4)
}

// ---------------------------------------------------------------------------
// Minigame runtime (minigame.ts base class fields).
// ---------------------------------------------------------------------------

// m8Member is one player inside a minigame.
type m8Member struct {
	conn *playerConn
	team int // Team enum value
	// coursing state (player.ts coursingScore/coursingTarget)
	score  int
	target string
}

// m8Game holds the shared minigame state. One mutex guards all games (the
// tick cadence is 1s and operations are tiny; per-game locks add risk
// without benefit at this scale).
type m8Game struct {
	mu        sync.Mutex
	key       string // "coursing" | "teamwar"
	opcode    int    // Opcodes.Minigame value
	name      string
	countdown int
	started   bool
	// lobbyAreas = every 'lobby' mObjectType (each fires addPlayer in Node);
	// lobby = the LAST loaded one (this.lobby field — getLobbyPosition uses
	// it for teleport-backs).
	lobbyAreas []*m8Area
	lobby      *m8Area
	lobbyWait  map[string]*m8Member // playersInLobby keyed by instance
	inGame     map[string]*m8Member // playersInGame keyed by instance

	// coursing extras
	hunterSpawn *m8Area
	preySpawns  []*m8Area
	centre      struct{ x, y int }

	// teamwar extras
	redSpawn  *m8Area
	blueSpawn *m8Area
	redKills  int
	blueKills int
}

var (
	m8Mu    sync.Mutex
	m8Games = map[string]*m8Game{}
)

// m8SendPacket ports Minigame.sendPacket: Minigame frame to each member.
func m8SendPacket(members map[string]*m8Member, opcode int, data map[string]any) {
	if len(members) == 0 {
		return
	}
	// Deterministic order for logs; fan-out is per-conn anyway.
	keys := make([]string, 0, len(members))
	for k := range members {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		mem := members[k]
		_ = send(mem.conn.conn, pktOp(PacketMinigame, opcode, data))
	}
}

// m8AddPlayer ports the private addPlayer: register in lobby, Lobby packet,
// ENTERED_LOBBY notify. NOTE: Node sends {action: MinigameState.Lobby} here
// (enum value 0) — NOT MinigameActions.Lobby (2). The two enums collide on
// the client (0 = Score in MinigameActions); cloned verbatim.
func (g *m8Game) addPlayer(c *playerConn) {
	g.mu.Lock()
	if _, ok := g.lobbyWait[c.instance]; ok {
		g.mu.Unlock()
		return
	}
	g.lobbyWait[c.instance] = &m8Member{conn: c}
	members := map[string]*m8Member{c.instance: g.lobbyWait[c.instance]}
	name := g.name
	g.mu.Unlock()

	m8SendPacket(members, g.opcode, map[string]any{
		"action": minigameStateLobby, // MinigameState.Lobby = 0 (Node quirk)
	})
	m6Notify(c, "misc:ENTERED_LOBBY;name="+name)
}

// m8RemovePlayer ports the private removePlayer: drop from lobby, Exit
// packet ({action: MinigameState.Exit = 2}), EXITED_LOBBY notify.
func (g *m8Game) removePlayer(c *playerConn) {
	g.mu.Lock()
	mem, ok := g.lobbyWait[c.instance]
	if ok {
		delete(g.lobbyWait, c.instance)
	}
	g.mu.Unlock()
	if !ok {
		return
	}
	members := map[string]*m8Member{c.instance: mem}
	m8SendPacket(members, g.opcode, map[string]any{"action": minigameStateExit})
	m6Notify(c, "misc:EXITED_LOBBY;name="+g.name)
}

// snapshotCountdown reads the current countdown under the game lock.
func (g *m8Game) snapshotCountdown() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.countdown
}

// m8Stop ports Minigame.stop: End packet to in-game players, clear state,
// teleport everyone back to a random lobby position.
func (g *m8Game) stop() {
	g.mu.Lock()
	members := g.inGame
	lobby := g.lobby
	g.inGame = map[string]*m8Member{}
	g.started = false
	if g.key == "teamwar" {
		g.redKills = 0
		g.blueKills = 0
	}
	// Node re-arms the countdown inside the NEXT tick (countdown <= 0 branch
	// only fires when the tick runs), so 0 keeps that exact behavior.
	g.countdown = 0
	g.mu.Unlock()

	m8SendPacket(members, g.opcode, map[string]any{"action": MinigameActionEnd})
	for _, mem := range members {
		mem.conn.m8Game = ""
		mem.conn.m8Team = 0
		mem.conn.m8Score = 0
		mem.conn.m8Target = ""
		x, y := m8LobbyPoint(lobby)
		m8Teleport(mem.conn, x, y)
	}
}

// m8Teleport ports character.teleport for minigame moves (position update +
// Teleport broadcast; the region recompute happens inside the broadcast's
// interest resolution and updateClientRegion here).
func m8Teleport(c *playerConn, x, y int) {
	c.sess.playerX = x
	c.sess.playerY = y
	setEntityPos(c.instance, x, y)
	updateClientRegion(c)
	broadcast(pkt(PacketTeleport, teleportData{Instance: c.instance, X: x, Y: y}))
}

// m8StartCoursing ports Coursing.start: shuffle, split hunters/prey 1:1,
// assign targets, teleport to team spawns, schedule pointers.
func (g *m8Game) startCoursing() {
	g.mu.Lock()
	// Shuffle lobby (minigame.ts shuffleLobby).
	keys := make([]string, 0, len(g.lobbyWait))
	for k := range g.lobbyWait {
		keys = append(keys, k)
	}
	rand.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })

	if len(keys) < coursingMinPlayers {
		g.mu.Unlock()
		for _, k := range keys {
			m6Notify(g.lobbyWait[k].conn,
				"misc:MINIMUM_PLAYERS_MINIGAME;minimum="+strconv.Itoa(coursingMinPlayers))
		}
		return
	}

	g.started = true

	// Even split; odd player out is ignored (coursing.ts count logic).
	count := len(keys)
	if count%2 != 0 {
		count--
	}
	hunters := keys[:count/2]
	prey := keys[count/2 : count]

	players := map[string]*m8Member{}
	for i, hk := range hunters {
		h := g.lobbyWait[hk]
		p := g.lobbyWait[prey[i]]
		h.team, p.team = coursingTeamHunter, coursingTeamPrey
		h.target, p.target = prey[i], hk
		h.score, p.score = 0, 0
		players[hk], players[prey[i]] = h, p
	}
	// Odd player out: stays in the lobby for the next round.
	for i := count; i < len(keys); i++ {
		delete(g.lobbyWait, keys[i])
	}
	g.inGame = players
	g.mu.Unlock()

	// Drop everyone from the lobby registry and teleport to spawns.
	for k, mem := range players {
		g.mu.Lock()
		delete(g.lobbyWait, k)
		g.mu.Unlock()

		mem.conn.m8Game = g.key
		mem.conn.m8Team = mem.team
		mem.conn.m8Score = 0
		mem.conn.m8Target = mem.target

		g.mu.Lock()
		var x, y int
		if mem.team == coursingTeamPrey && len(g.preySpawns) > 0 {
			x, y = m8RandomPointIn(g.preySpawns[rand.Intn(len(g.preySpawns))])
		} else {
			x, y = m8RandomPointIn(g.hunterSpawn)
		}
		g.mu.Unlock()
		m8Teleport(mem.conn, x, y)
	}

	// sendPointers after 1.5s (coursing.ts setTimeout).
	time.AfterFunc(coursingPointerDelay*time.Millisecond, func() {
		g.mu.Lock()
		players := g.inGame
		g.mu.Unlock()
		for k, mem := range players {
			if mem.target == "" {
				continue
			}
			// player.pointer: Remove-then-Entity (pointer.ts default remove).
			// Remove carries no data → 2-element frame like Node's serialize.
			_ = send(mem.conn.conn, []any{PacketPointer, PointerRemove})
			_ = send(mem.conn.conn, pktOp(PacketPointer, PointerEntity, map[string]any{
				"instance": mem.target, "type": PointerEntity,
			}))
			_ = k
		}
	})
}

// m8StartTeamWar ports TeamWar.start: shuffle, split red/blue, teleport.
func (g *m8Game) startTeamWar() {
	g.mu.Lock()
	keys := make([]string, 0, len(g.lobbyWait))
	for k := range g.lobbyWait {
		keys = append(keys, k)
	}
	rand.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })

	if len(keys) < teamWarMinPlayers {
		g.mu.Unlock()
		for _, k := range keys {
			m6Notify(g.lobbyWait[k].conn,
				"misc:MINIMUM_PLAYERS_MINIGAME;minimum="+strconv.Itoa(teamWarMinPlayers))
		}
		return
	}

	g.started = true
	split := len(keys) / 2
	players := map[string]*m8Member{}
	for i, k := range keys {
		mem := g.lobbyWait[k]
		if i < split {
			mem.team = teamWarTeamBlue
		} else {
			mem.team = teamWarTeamRed
		}
		players[k] = mem
	}
	g.inGame = players
	g.mu.Unlock()

	for k, mem := range players {
		g.mu.Lock()
		delete(g.lobbyWait, k)
		var x, y int
		if mem.team == teamWarTeamRed {
			x, y = m8RandomPointIn(g.redSpawn)
		} else {
			x, y = m8RandomPointIn(g.blueSpawn)
		}
		g.mu.Unlock()

		mem.conn.m8Game = g.key
		mem.conn.m8Team = mem.team
		m8Teleport(mem.conn, x, y)
	}
}

// m8Tick is the shared 1s tick (minigame.ts setInterval + subclass ticks).
func (g *m8Game) tick() {
	g.mu.Lock()
	if g.countdown <= 0 {
		if g.key == "coursing" {
			g.countdown = coursingCountdown
		} else {
			g.countdown = teamWarCountdown
		}
		wasStarted := g.started
		g.mu.Unlock()
		if wasStarted {
			g.stop()
		} else if g.key == "coursing" {
			g.startCoursing()
		} else {
			g.startTeamWar()
		}
		return
	}
	g.countdown--
	countdown := g.countdown
	started := g.started
	lobby := map[string]*m8Member{}
	for k, v := range g.lobbyWait {
		lobby[k] = v
	}
	inGame := map[string]*m8Member{}
	for k, v := range g.inGame {
		inGame[k] = v
	}
	centreX, centreY := g.centre.x, g.centre.y
	redKills, blueKills := g.redKills, g.blueKills
	scoreTick := countdown%coursingScoreTick == 0
	g.mu.Unlock()

	// Lobby countdown packets every tick (both games).
	if len(lobby) > 0 {
		m8SendPacket(lobby, g.opcode, map[string]any{
			"action":    MinigameActionLobby,
			"countdown": countdown,
			"started":   started,
		})
	}

	// Coursing: update prey scores every 4 ticks from distance-to-centre.
	if g.key == "coursing" && started && scoreTick {
		for k, mem := range inGame {
			if mem.team != coursingTeamPrey {
				continue
			}
			px, py, _ := entityPos(k)
			distance := abs(px-centreX) + abs(py-centreY) // Utils.getDistance
			score := distance / coursingScoreDivisor
			mem.conn.m8Score += score
			mem.score = mem.conn.m8Score
			m8SendPacket(map[string]*m8Member{k: mem}, g.opcode, map[string]any{
				"action": MinigameActionScore,
				"score":  mem.conn.m8Score,
			})
		}
	}

	// TeamWar: in-game score packets every tick.
	if g.key == "teamwar" && len(inGame) > 0 {
		m8SendPacket(inGame, g.opcode, map[string]any{
			"action":        MinigameActionScore,
			"countdown":     countdown,
			"redTeamKills":  redKills,
			"blueTeamKills": blueKills,
		})
	}
}

// m8RecordKill ports TeamWar.kill (the coursing kill callback has no
// behaviour in Node — only teamwar overrides it).
func (g *m8Game) recordKill(killer *playerConn) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch killer.m8Team {
	case teamWarTeamBlue:
		g.blueKills += killPointsPerKill
	case teamWarTeamRed:
		g.redKills += killPointsPerKill
	}
}

// ---------------------------------------------------------------------------
// Area linking + lifecycle (minigames.ts constructor + linkAreas).
// ---------------------------------------------------------------------------

// m8LoadGames parses world.json areas.minigame, links each area to its
// minigame (linkAreas), and starts the 1s tick goroutine per game.
func m8LoadGames() {
	m8Mu.Lock()
	defer m8Mu.Unlock()
	if len(m8Games) > 0 {
		return
	}
	loadWorld()
	if world == nil {
		return
	}
	raw, err := os.ReadFile(worldPath())
	if err != nil {
		return
	}
	var doc struct {
		Areas struct {
			Minigame []m8Area `json:"minigame"`
		} `json:"areas"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		log.Printf("m8: parse areas: %v", err)
		return
	}

	newGame := func(key string, opcode int, name string, countdown int) *m8Game {
		return &m8Game{
			key: key, opcode: opcode, name: name, countdown: countdown,
			lobbyWait: map[string]*m8Member{}, inGame: map[string]*m8Member{},
		}
	}
	coursing := newGame("coursing", MinigameCoursing, "Coursing", coursingCountdown)
	teamwar := newGame("teamwar", MinigameTeamWar, "TeamWar", teamWarCountdown)

	for i := range doc.Areas.Minigame {
		area := doc.Areas.Minigame[i]
		switch area.Minigame {
		case "coursing":
			switch area.MObjectType {
			case "lobby":
				coursing.lobbyAreas = append(coursing.lobbyAreas, &area)
				coursing.lobby = &area // last one wins (this.lobby field)
			case "hunterspawn":
				coursing.hunterSpawn = &area
			case "preyspawn":
				coursing.preySpawns = append(coursing.preySpawns, &area)
			case "centre":
				coursing.centre.x, coursing.centre.y = area.X, area.Y
			}
		case "teamwar":
			switch area.MObjectType {
			case "lobby":
				teamwar.lobbyAreas = append(teamwar.lobbyAreas, &area)
				teamwar.lobby = &area
			case "redteamspawn":
				teamwar.redSpawn = &area
			case "blueteamspawn":
				teamwar.blueSpawn = &area
			}
		}
	}

	coursing.countdown = m8EnvCountdown("M8_COURSING_COUNTDOWN", coursingCountdown)
	teamwar.countdown = m8EnvCountdown("M8_TEAMWAR_COUNTDOWN", teamWarCountdown)

	m8Games["coursing"] = coursing
	m8Games["teamwar"] = teamwar

	for _, g := range m8Games {
		gg := g
		go func() {
			t := time.NewTicker(minigameTickInterval * time.Millisecond)
			defer t.Stop()
			for range t.C {
				gg.tick()
			}
		}()
	}
	log.Printf("m8: minigames loaded (coursing lobby=%v teamwar lobby=%v)",
		coursing.lobby != nil, teamwar.lobby != nil)
}

// m8GameFor returns the named game (nil when missing).
func m8GameFor(key string) *m8Game {
	m8Mu.Lock()
	defer m8Mu.Unlock()
	return m8Games[key]
}

// ---------------------------------------------------------------------------
// Player-side hooks (called from the movement/disconnect paths).
// ---------------------------------------------------------------------------

// m8OnPositionUpdate mirrors areas onEnter/onExit: on each confirmed position
// change, move the player between lobby membership and nothing (enter lobby
// -> addPlayer, leave lobby area -> removePlayer). In-game players use the
// exit hook only to detect leaving via the lobby (Node tracks exits through
// the area callbacks; the stub keeps membership until stop()).
func m8OnPositionUpdate(c *playerConn) {
	// Coursing lobby enter/exit (area onEnter/onExit parity). Membership
	// checks run over EVERY lobby area (each fires its own callbacks).
	g := m8GameFor("coursing")
	if g != nil && len(g.lobbyAreas) > 0 {
		inside := false
		for _, a := range g.lobbyAreas {
			if a.inside(c.sess.playerX, c.sess.playerY) {
				inside = true
				break
			}
		}

		g.mu.Lock()
		_, inLobby := g.lobbyWait[c.instance]
		g.mu.Unlock()

		switch {
		case inside && !inLobby && c.m8Game == "":
			g.addPlayer(c)
		case !inside && inLobby:
			g.removePlayer(c)
		}
	}

	// TeamWar lobby (same base-class addPlayer/removePlayer flow).
	tw := m8GameFor("teamwar")
	if tw != nil && len(tw.lobbyAreas) > 0 {
		inside := false
		for _, a := range tw.lobbyAreas {
			if a.inside(c.sess.playerX, c.sess.playerY) {
				inside = true
				break
			}
		}

		tw.mu.Lock()
		_, inLobby := tw.lobbyWait[c.instance]
		tw.mu.Unlock()

		switch {
		case inside && !inLobby && c.m8Game == "":
			tw.addPlayer(c)
		case !inside && inLobby:
			tw.removePlayer(c)
		}
	}
}

// m8OnDisconnect ports Minigame.disconnect: teleport to a random lobby
// position, drop from the game, stop when fewer than 2 remain.
func m8OnDisconnect(c *playerConn) {
	if c.m8Game == "" {
		return
	}
	g := m8GameFor(c.m8Game)
	if g == nil {
		return
	}
	g.mu.Lock()
	delete(g.inGame, c.instance)
	delete(g.lobbyWait, c.instance)
	remaining := len(g.inGame)
	lobby := g.lobby
	g.mu.Unlock()

	c.m8Game = ""
	c.m8Team = 0
	c.m8Score = 0
	c.m8Target = ""

	x, y := m8LobbyPoint(lobby)
	// The conn is dying; update the session position so the disconnect
	// persist path saves the lobby tile and the relogin lands back in the
	// lobby area (disconnect() setPosition parity). Persist goes through
	// m5TrackPos (never hold pstateMu outside it).
	c.sess.playerX, c.sess.playerY = x, y
	m5TrackPos(c)

	// minigame.ts disconnect: stop when fewer than 2 players remain.
	if remaining < 2 {
		g.stop()
	}
}

// ---------------------------------------------------------------------------
// M8TEST debug frame (TESTMAP-only): deterministic kill/score injection so
// the e2e can exercise the kill path without real PVP deaths (the stub has
// none). Shape: C->S [46,{"m8test":"kill"|"score"}].
// ---------------------------------------------------------------------------

func m8HandleTest(c *playerConn, frame clientFrame) {
	if !testMode || cleanMode || combatMode || len(frame) < 2 {
		return
	}
	var data struct {
		M8Test string `json:"m8test"`
	}
	if err := json.Unmarshal(frame[1], &data); err != nil {
		return
	}
	switch data.M8Test {
	case "kill":
		if c.m8Game == "" {
			return
		}
		if g := m8GameFor(c.m8Game); g != nil {
			g.recordKill(c)
		}
	case "kills":
		// Echo the teamwar kill counters (game state introspection).
		g := m8GameFor("teamwar")
		if g == nil {
			return
		}
		g.mu.Lock()
		r, b := g.redKills, g.blueKills
		g.mu.Unlock()
		m6Notify(c, fmt.Sprintf("m8:kills red=%d blue=%d", r, b))
	case "pointers":
		// Re-fire coursing sendPointers (idempotent, TESTMAP-only) so the
		// harness gets a deterministic window for the Pointer frames.
		g := m8GameFor("coursing")
		if g == nil {
			return
		}
		g.mu.Lock()
		players := map[string]*m8Member{}
		for k, v := range g.inGame {
			players[k] = v
		}
		g.mu.Unlock()
		for _, mem := range players {
			if mem.target == "" {
				continue
			}
			_ = send(mem.conn.conn, []any{PacketPointer, PointerRemove})
			_ = send(mem.conn.conn, pktOp(PacketPointer, PointerEntity, map[string]any{
				"instance": mem.target, "type": PointerEntity,
			}))
		}
	case "state":
		// Full session introspection for the harness (positions, team,
		// score, target, game key).
		m6Notify(c, fmt.Sprintf("m8:state game=%s team=%d score=%d target=%s x=%d y=%d inst=%s",
			c.m8Game, c.m8Team, c.m8Score, c.m8Target, c.sess.playerX, c.sess.playerY, c.instance))
	case "score":
		if c.m8Game != "coursing" {
			return
		}
		// Dead-prey per-tick penalty (coursing.ts incrementCoursingScore(-10)
		// with the clamp at 0). Echo the clamped score for the e2e.
		c.m8Score += coursingDeadPenalty
		if c.m8Score < 0 {
			c.m8Score = 0
		}
		m6Notify(c, fmt.Sprintf("m8:score=%d", c.m8Score))
	}
}
