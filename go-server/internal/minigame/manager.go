// Package minigame — Manager owns the M8 state machine (lobby/queue/team/
// instance lifecycle). It is transport-free: all wire/position side effects
// go through Effects (implemented by the root m8.go adapter). Pure rules
// (scoring/splits/countdowns) are reused verbatim.
package minigame

import (
	"math/rand"
	"sort"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Transport seam.
// ---------------------------------------------------------------------------

// Effects is the transport seam for minigame side effects. Implementations
// live in the root adapter:
//
//	Notify       -> m6Notify(instance's conn, msg)
//	Teleport     -> m8Teleport(instance's conn, x, y)
//	Broadcast    -> broadcast(frames ...[]any)
//	SendMinigame -> send(conn, pktOp(PacketMinigame, opcode, data))
//	SendPointer  -> send(conn, Remove) + send(conn, pktOp(PointerEntity,...))
//	EntityPos    -> entityPos(instance)
type Effects interface {
	Notify(instance, msg string)
	Teleport(instance string, x, y int)
	Broadcast(frames ...[]any)
	SendMinigame(instance string, opcode int, data map[string]any)
	SendPointer(instance string, target string)
	EntityPos(instance string) (x, y int, ok bool)
}

// ---------------------------------------------------------------------------
// Manager.
// ---------------------------------------------------------------------------

// Manager owns the mutex + games map. Members are instance-ID-keyed (no
// playerConn pointers). One mutex guards all games (the tick cadence is 1s
// and operations are tiny; per-game locks add risk without benefit).
type Manager struct {
	mu    sync.Mutex
	games map[string]*Game
}

// TickResult reports the session-mirror changes a Tick made so the root
// adapter can sync playerConn fields (m8Game/m8Team/m8Score/m8Target).
type TickResult struct {
	Stopped []string
	Started map[string]Member // game-key context in caller (assignments)
	Scored  map[string]int    // instance -> new coursing score
}

// NewManager returns an empty Manager.
func NewManager() *Manager {
	return &Manager{games: map[string]*Game{}}
}

// HasGames reports whether any games are loaded.
func (m *Manager) HasGames() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.games) > 0
}

// EnsureGame creates the named game when missing (root newGame closure).
func (m *Manager) EnsureGame(key string, opcode int, name string, countdown int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.games[key]; ok {
		return
	}
	m.games[key] = NewGame(key, opcode, name, countdown)
}

// AddArea links one world.json minigame area to its game (linkAreas).
func (m *Manager) AddArea(a Area) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.games[a.Minigame]
	if !ok {
		return
	}
	g.AddArea(a)
}

// SetCountdown overrides a game's countdown (env overrides + tests).
func (m *Manager) SetCountdown(key string, n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if g, ok := m.games[key]; ok {
		g.Countdown = n
	}
}

// SnapshotCountdown reads the current countdown.
func (m *Manager) SnapshotCountdown(key string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if g, ok := m.games[key]; ok {
		return g.Countdown
	}
	return 0
}

// GameFor returns the named game pointer (nil when missing). Callers must
// not mutate it directly; use Manager methods (lock-guarded).
func (m *Manager) GameFor(key string) *Game {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.games[key]
}

// GameKeys returns the sorted game keys (deterministic tick/position order).
func (m *Manager) GameKeys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.games))
	for k := range m.games {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// HasLobby reports whether the game has a lobby area.
func (m *Manager) HasLobby(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.games[key]
	return ok && g.Lobby != nil
}

// IsInLobby reports lobby membership.
func (m *Manager) IsInLobby(key, instance string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.games[key]
	if !ok {
		return false
	}
	_, ok = g.LobbyWait[instance]
	return ok
}

// IsInGame reports in-game membership.
func (m *Manager) IsInGame(key, instance string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.games[key]
	if !ok {
		return false
	}
	_, ok = g.InGame[instance]
	return ok
}

// ActiveGame returns the game key whose InGame holds the instance ("" none).
func (m *Manager) ActiveGame(instance string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.games))
	for k := range m.games {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, ok := m.games[k].InGame[instance]; ok {
			return k
		}
	}
	return ""
}

// KillCounts returns the teamwar kill counters.
func (m *Manager) KillCounts(key string) (red, blue int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if g, ok := m.games[key]; ok {
		return g.RedKills, g.BlueKills
	}
	return 0, 0
}

// InGameCount returns the in-game population.
func (m *Manager) InGameCount(key string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if g, ok := m.games[key]; ok {
		return len(g.InGame)
	}
	return 0
}

// MemberCopy returns a copy of one member (team/score/target mirror).
func (m *Manager) MemberCopy(key, instance string) (Member, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.games[key]
	if !ok {
		return Member{}, false
	}
	if mem, ok := g.InGame[instance]; ok {
		return *mem, true
	}
	if mem, ok := g.LobbyWait[instance]; ok {
		return *mem, true
	}
	return Member{}, false
}

// Winner returns the teamwar leader (Winner seam).
func (m *Manager) Winner(key string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if g, ok := m.games[key]; ok {
		return g.Winner()
	}
	return WinnerTie
}

// Leader returns the coursing instance closest to the centre (CoursingLeader
// seam).
func (m *Manager) Leader(key string, pos map[string][2]int) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if g, ok := m.games[key]; ok {
		return g.CoursingLeader(pos)
	}
	return ""
}

// AddPlayer ports the private addPlayer: register in lobby, Lobby packet,
// ENTERED_LOBBY notify. Sends MinigameState.Lobby (0), not MinigameActions.
func (m *Manager) AddPlayer(key, instance string, fx Effects) bool {
	m.mu.Lock()
	g, ok := m.games[key]
	if !ok {
		m.mu.Unlock()
		return false
	}
	if _, dup := g.LobbyWait[instance]; dup {
		m.mu.Unlock()
		return false
	}
	g.LobbyWait[instance] = &Member{Instance: instance}
	opcode, name := g.Opcode, g.Name
	m.mu.Unlock()

	fx.SendMinigame(instance, opcode, map[string]any{
		"action": MinigameStateLobby,
	})
	fx.Notify(instance, "misc:ENTERED_LOBBY;name="+name)
	return true
}

// RemovePlayer ports the private removePlayer: drop from lobby, Exit packet,
// EXITED_LOBBY notify.
func (m *Manager) RemovePlayer(key, instance string, fx Effects) bool {
	m.mu.Lock()
	g, ok := m.games[key]
	if !ok {
		m.mu.Unlock()
		return false
	}
	if _, dup := g.LobbyWait[instance]; !dup {
		m.mu.Unlock()
		return false
	}
	delete(g.LobbyWait, instance)
	opcode, name := g.Opcode, g.Name
	m.mu.Unlock()

	fx.SendMinigame(instance, opcode, map[string]any{"action": MinigameStateExit})
	fx.Notify(instance, "misc:EXITED_LOBBY;name="+name)
	return true
}

// Stop ports Minigame.stop: End packet to in-game players, clear state,
// teleport everyone back to a random lobby position. Returns stopped
// instances (sorted) so the adapter can clear session mirrors.
func (m *Manager) Stop(key string, fx Effects) []string {
	m.mu.Lock()
	g, ok := m.games[key]
	if !ok {
		m.mu.Unlock()
		return nil
	}
	instances := make([]string, 0, len(g.InGame))
	for k := range g.InGame {
		instances = append(instances, k)
	}
	sort.Strings(instances)
	opcode := g.Opcode
	lobby := g.Lobby
	g.InGame = map[string]*Member{}
	g.Started = false
	if g.Key == KeyTeamWar {
		g.RedKills = 0
		g.BlueKills = 0
	}
	// Node re-arms the countdown inside the NEXT tick (countdown <= 0 branch
	// only fires when the tick runs), so 0 keeps that exact behavior.
	g.Countdown = 0
	m.mu.Unlock()

	for _, inst := range instances {
		fx.SendMinigame(inst, opcode, map[string]any{"action": MinigameActionEnd})
	}
	// getLobbyPosition parity per instance (TeleportToLobby seam).
	g2 := &Game{Lobby: lobby}
	g2.TeleportToLobby(fx, instances)
	return instances
}

// StartCoursing ports Coursing.start: shuffle, split hunters/prey 1:1,
// assign targets, teleport to team spawns, schedule pointers. Returns the
// started assignments (copy) so the adapter can sync session mirrors.
func (m *Manager) StartCoursing(fx Effects) map[string]Member {
	m.mu.Lock()
	g, ok := m.games[KeyCoursing]
	if !ok {
		m.mu.Unlock()
		return nil
	}
	keys := make([]string, 0, len(g.LobbyWait))
	for k := range g.LobbyWait {
		keys = append(keys, k)
	}
	rand.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })

	if len(keys) < CoursingMinPlayers {
		m.mu.Unlock()
		// NotifyMinimumPlayers seam (sorted order).
		m.mu.Lock()
		gg := m.games[KeyCoursing]
		m.mu.Unlock()
		if gg != nil {
			gg.NotifyMinimumPlayers(fx, CoursingMinPlayers)
		}
		return nil
	}

	g.Started = true

	// Even split; odd player out is ignored (SplitCoursingKeys seam).
	hunters, prey, leftover := SplitCoursingKeys(keys)
	players := map[string]*Member{}
	for i, hk := range hunters {
		h := g.LobbyWait[hk]
		p := g.LobbyWait[prey[i]]
		h.Team, p.Team = CoursingTeamHunter, CoursingTeamPrey
		h.Target, p.Target = prey[i], hk
		h.Score, p.Score = 0, 0
		players[hk], players[prey[i]] = h, p
	}
	// Odd player out: dropped from the lobby registry (root verbatim).
	if leftover != "" {
		delete(g.LobbyWait, leftover)
	}
	g.InGame = players

	type spawn struct {
		inst string
		x, y int
	}
	spawns := make([]spawn, 0, len(players))
	order := make([]string, 0, len(players))
	for k := range players {
		order = append(order, k)
	}
	sort.Strings(order)
	for _, k := range order {
		mem := players[k]
		var x, y int
		if mem.Team == CoursingTeamPrey && len(g.PreySpawns) > 0 {
			x, y = RandomPointIn(g.PreySpawns[rand.Intn(len(g.PreySpawns))])
		} else {
			x, y = RandomPointIn(g.HunterSpawn)
		}
		spawns = append(spawns, spawn{inst: k, x: x, y: y})
	}
	assignments := make(map[string]Member, len(players))
	for k, mem := range players {
		assignments[k] = *mem
	}
	for k := range players {
		delete(g.LobbyWait, k)
	}
	m.mu.Unlock()

	for _, s := range spawns {
		fx.Teleport(s.inst, s.x, s.y)
	}

	// sendPointers after 1.5s (coursing.ts setTimeout).
	time.AfterFunc(CoursingPointerDelay*time.Millisecond, func() {
		m.SendPointers(KeyCoursing, fx)
	})
	return assignments
}

// StartTeamWar ports TeamWar.start: shuffle, split red/blue, teleport.
// Returns the started assignments (copy) for session-mirror sync.
func (m *Manager) StartTeamWar(fx Effects) map[string]Member {
	m.mu.Lock()
	g, ok := m.games[KeyTeamWar]
	if !ok {
		m.mu.Unlock()
		return nil
	}
	keys := make([]string, 0, len(g.LobbyWait))
	for k := range g.LobbyWait {
		keys = append(keys, k)
	}
	rand.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })

	if len(keys) < TeamWarMinPlayers {
		m.mu.Unlock()
		m.mu.Lock()
		gg := m.games[KeyTeamWar]
		m.mu.Unlock()
		if gg != nil {
			gg.NotifyMinimumPlayers(fx, TeamWarMinPlayers)
		}
		return nil
	}

	g.Started = true
	blue, red := SplitTeamWarKeys(keys) // SplitTeamWarKeys seam
	players := map[string]*Member{}
	for _, k := range blue {
		mem := g.LobbyWait[k]
		mem.Team = TeamWarTeamBlue
		players[k] = mem
	}
	for _, k := range red {
		mem := g.LobbyWait[k]
		mem.Team = TeamWarTeamRed
		players[k] = mem
	}
	g.InGame = players

	type spawn struct {
		inst string
		x, y int
	}
	order := make([]string, 0, len(players))
	for k := range players {
		order = append(order, k)
	}
	sort.Strings(order)
	spawns := make([]spawn, 0, len(players))
	for _, k := range order {
		mem := players[k]
		var x, y int
		if mem.Team == TeamWarTeamRed {
			x, y = RandomPointIn(g.RedSpawn)
		} else {
			x, y = RandomPointIn(g.BlueSpawn)
		}
		spawns = append(spawns, spawn{inst: k, x: x, y: y})
	}
	assignments := make(map[string]Member, len(players))
	for k, mem := range players {
		assignments[k] = *mem
	}
	for k := range players {
		delete(g.LobbyWait, k)
	}
	m.mu.Unlock()

	for _, s := range spawns {
		fx.Teleport(s.inst, s.x, s.y)
	}
	return assignments
}

// SendPointers re-fires coursing sendPointers (player.pointer: Remove-then-
// Entity, idempotent). Used by the start delay and the M8TEST debug frame.
func (m *Manager) SendPointers(key string, fx Effects) {
	m.mu.Lock()
	g, ok := m.games[key]
	if !ok {
		m.mu.Unlock()
		return
	}
	type ptr struct {
		inst, target string
	}
	pts := []ptr{}
	for k, mem := range g.InGame {
		if mem.Target == "" {
			continue
		}
		pts = append(pts, ptr{inst: k, target: mem.Target})
	}
	m.mu.Unlock()
	sort.Slice(pts, func(i, j int) bool { return pts[i].inst < pts[j].inst })
	for _, p := range pts {
		fx.SendPointer(p.inst, p.target)
	}
}

// Tick is the shared 1s tick (minigame.ts setInterval + subclass ticks).
func (m *Manager) Tick(key string, fx Effects) TickResult {
	var res TickResult
	res.Started = map[string]Member{}
	res.Scored = map[string]int{}

	m.mu.Lock()
	g, ok := m.games[key]
	if !ok {
		m.mu.Unlock()
		return res
	}
	if g.Countdown <= 0 {
		g.Countdown = RearmCountdown(g.Key) // RearmCountdown seam
		wasStarted := g.Started
		m.mu.Unlock()
		if wasStarted {
			res.Stopped = m.Stop(key, fx)
		} else if key == KeyCoursing {
			if started := m.StartCoursing(fx); len(started) > 0 {
				res.Started = started
			}
		} else {
			if started := m.StartTeamWar(fx); len(started) > 0 {
				res.Started = started
			}
		}
		return res
	}
	g.Countdown--
	countdown := g.Countdown
	started := g.Started
	opcode := g.Opcode
	lobby := map[string]*Member{}
	for k, v := range g.LobbyWait {
		lobby[k] = v
	}
	inGame := map[string]*Member{}
	for k, v := range g.InGame {
		inGame[k] = v
	}
	centreX, centreY := g.CentreX, g.CentreY
	redKills, blueKills := g.RedKills, g.BlueKills
	scoreTick := IsScoreTick(countdown)
	m.mu.Unlock()

	// Lobby countdown packets every tick (both games).
	if len(lobby) > 0 {
		lobbyKeys := make([]string, 0, len(lobby))
		for k := range lobby {
			lobbyKeys = append(lobbyKeys, k)
		}
		sort.Strings(lobbyKeys)
		for _, k := range lobbyKeys {
			fx.SendMinigame(k, opcode, map[string]any{
				"action":    MinigameActionLobby,
				"countdown": countdown,
				"started":   started,
			})
		}
	}

	// Coursing: update prey scores every 4 ticks from distance-to-centre.
	if key == KeyCoursing && started && scoreTick {
		preyKeys := []string{}
		for k, mem := range inGame {
			if mem.Team != CoursingTeamPrey {
				continue
			}
			preyKeys = append(preyKeys, k)
		}
		sort.Strings(preyKeys)
		for _, k := range preyKeys {
			px, py, ok := fx.EntityPos(k)
			if !ok {
				continue
			}
			delta := CoursingScore(px, py, centreX, centreY)
			m.mu.Lock()
			gg, ok := m.games[key]
			newScore := 0
			if ok {
				if mem, ok := gg.InGame[k]; ok {
					mem.Score += delta
					newScore = mem.Score
				}
			}
			m.mu.Unlock()
			fx.SendMinigame(k, opcode, map[string]any{
				"action": MinigameActionScore,
				"score":  newScore,
			})
			res.Scored[k] = newScore
		}
	}

	// TeamWar: in-game score packets every tick.
	if key == KeyTeamWar && len(inGame) > 0 {
		twKeys := make([]string, 0, len(inGame))
		for k := range inGame {
			twKeys = append(twKeys, k)
		}
		sort.Strings(twKeys)
		for _, k := range twKeys {
			fx.SendMinigame(k, opcode, map[string]any{
				"action":        MinigameActionScore,
				"countdown":     countdown,
				"redTeamKills":  redKills,
				"blueTeamKills": blueKills,
			})
		}
	}
	return res
}

// RecordKill ports TeamWar.kill by instance lookup (ApplyKill seam).
func (m *Manager) RecordKill(gameKey, instance string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.games[gameKey]
	if !ok {
		return
	}
	team := -1
	if mem, ok := g.InGame[instance]; ok {
		team = mem.Team
	} else if mem, ok := g.LobbyWait[instance]; ok {
		team = mem.Team
	} else {
		return
	}
	g.ApplyKill(team)
}

// RecordKillTeam ports TeamWar.kill by explicit team (adapter kill path uses
// the live conn team, verbatim with the root recordKill).
func (m *Manager) RecordKillTeam(gameKey string, team int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if g, ok := m.games[gameKey]; ok {
		g.ApplyKill(team)
	}
}

// Disconnect ports Minigame.disconnect membership drop. Returns the lobby
// respawn point (getLobbyPosition parity) and the remaining in-game count;
// the adapter persists the session point and stops via ShouldStop.
func (m *Manager) Disconnect(gameKey, instance string) (x, y, remaining int) {
	m.mu.Lock()
	g, ok := m.games[gameKey]
	if !ok {
		m.mu.Unlock()
		lx, ly := LobbyPoint(nil)
		return lx, ly, 0
	}
	delete(g.InGame, instance)
	delete(g.LobbyWait, instance)
	remaining = len(g.InGame)
	lobby := g.Lobby
	m.mu.Unlock()
	lx, ly := LobbyPoint(lobby)
	return lx, ly, remaining
}

// OnPositionUpdate mirrors areas onEnter/onExit across every lobby area.
func (m *Manager) OnPositionUpdate(instance string, x, y int, activeGame string, fx Effects) {
	for _, key := range m.GameKeys() {
		m.mu.Lock()
		g, ok := m.games[key]
		if !ok {
			m.mu.Unlock()
			continue
		}
		areas := g.LobbyAreas
		_, inLobby := g.LobbyWait[instance]
		m.mu.Unlock()
		if len(areas) == 0 {
			continue
		}
		inside := InsideAny(areas, x, y)
		switch {
		case inside && !inLobby && activeGame == "":
			m.AddPlayer(key, instance, fx)
		case !inside && inLobby:
			m.RemovePlayer(key, instance, fx)
		}
	}
}
