// Package minigame holds the PURE domain logic for the M8 minigames
// (coursing + teamwar), extracted verbatim from the root m8.go.
//
// Everything in here is transport-free: no playerConn pointers (members are
// keyed by instance-ID string), no send/broadcast/setEntityPos/m6Notify
// calls. Sides that need transport take an Effects parameter implemented by
// the root adapter (m8.go), which keeps ALL wiring: m8Mu/m8Games, playerConn
// fields, tick goroutines and the M8TEST debug dispatcher.
package minigame

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
)

// ---------------------------------------------------------------------------
// Constants (Opcodes.Minigame, MinigameState/Actions, Modules.MinigameConstants).
// Values mirror root m8.go, which re-exports these (behavior-frozen).
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
	MinigameStateLobby = 0
	MinigameStateEnd   = 1
	MinigameStateExit  = 2
)

const (
	PointerLocation = 0 // Opcodes.Pointer.Location
	PointerEntity   = 1 // Opcodes.Pointer.Entity
	PointerRemove   = 3 // Opcodes.Pointer.Remove
)

const (
	TeamWarCountdown     = 240  // TEAM_WAR_COUNTDOWN (lobby + in-game seconds)
	TeamWarMinPlayers    = 2    // TEAM_WAR_MIN_PLAYERS
	CoursingCountdown    = 45   // COURSING_COUNTDOWN
	CoursingMinPlayers   = 2    // COURSING_MIN_PLAYERS
	CoursingScoreDivisor = 10   // COURSING_SCORE_DIVISOR
	CoursingTeamPrey     = 2    // Team.Prey (api/minigame.ts)
	CoursingTeamHunter   = 3    // Team.Hunter
	TeamWarTeamRed       = 0    // Team.Red
	TeamWarTeamBlue      = 1    // Team.Blue
	CoursingDeadPenalty  = -10  // coursing.ts dead-player per-tick score
	CoursingScoreTick    = 4    // score update every 4 ticks
	CoursingPointerDelay = 1500 // ms before sendPointers (coursing.ts)
	MinigameTickInterval = 1000 // minigame.ts tickInterval (1s)
	KillPointsPerKill    = 1    // teamwar.kill increments by one per kill
)

// Game keys.
const (
	KeyCoursing = "coursing"
	KeyTeamWar  = "teamwar"
)

// Winner labels returned by Game.Winner.
const (
	WinnerRed  = "red"
	WinnerBlue = "blue"
	WinnerTie  = "tie"
)

// EnvCountdown reads a countdown override for fast e2e runs (same convention
// as M4_RESPAWN_MS). Defaults are the exact Node constants; env values >0 win.
func EnvCountdown(env string, def int) int {
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

// Area is one world.json areas.minigame entry.
type Area struct {
	ID          int    `json:"id"`
	X           int    `json:"x"`
	Y           int    `json:"y"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	MObjectType string `json:"mObjectType"`
	Minigame    string `json:"minigame"`
}

// Inside reports whether (x,y) falls within the area rectangle (area.ts
// boundary math: x <= px < x+width).
func (a *Area) Inside(x, y int) bool {
	if a == nil || a.Width <= 0 || a.Height <= 0 {
		return false
	}
	return x >= a.X && x < a.X+a.Width && y >= a.Y && y < a.Y+a.Height
}

// InsideAny reports whether (x,y) falls within any of the areas.
func InsideAny(areas []*Area, x, y int) bool {
	for _, a := range areas {
		if a.Inside(x, y) {
			return true
		}
	}
	return false
}

// RandomPointIn returns a random point inside the area using the Node
// spawn formula (coursing/teamwar getSpawnPoint: x+1 .. x+width-1).
func RandomPointIn(a *Area) (int, int) {
	if a == nil || a.Width <= 2 || a.Height <= 2 {
		if a != nil {
			return a.X, a.Y
		}
		return 100, 96
	}
	return a.X + 1 + rand.Intn(a.Width-1), a.Y + 1 + rand.Intn(a.Height-1)
}

// LobbyPoint ports Minigame.getLobbyPosition: x+2 .. x+width-3 (the tighter
// inset used for teleport-backs, distinct from the spawn formula).
func LobbyPoint(a *Area) (int, int) {
	if a == nil || a.Width <= 5 || a.Height <= 5 {
		if a != nil {
			return a.X, a.Y
		}
		return 100, 96
	}
	return a.X + 2 + rand.Intn(a.Width-4), a.Y + 2 + rand.Intn(a.Height-4)
}

// areasDoc is the world.json shape for areas.minigame.
type areasDoc struct {
	Areas struct {
		Minigame []Area `json:"minigame"`
	} `json:"areas"`
}

// ParseAreasDoc parses the world.json areas.minigame list (pure counterpart
// of the unmarshal in the root m8LoadGames).
func ParseAreasDoc(raw []byte) ([]Area, error) {
	var doc areasDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse areas: %w", err)
	}
	return doc.Areas.Minigame, nil
}

// ---------------------------------------------------------------------------
// Minigame runtime (minigame.ts base class fields, transport-free).
// ---------------------------------------------------------------------------

// Member is one player inside a minigame, keyed by instance ID (no
// playerConn pointer — the root adapter joins this to the live conn).
type Member struct {
	Instance string
	Team     int // Team enum value
	// Coursing state (player.ts coursingScore/coursingTarget).
	Score  int
	Target string
}

// Game holds the shared minigame state (pure fields only; the root adapter
// owns the mutex, tick goroutines and live-conn fan-out).
type Game struct {
	Key       string // "coursing" | "teamwar"
	Opcode    int    // Opcodes.Minigame value
	Name      string
	Countdown int
	Started   bool
	// LobbyAreas = every 'lobby' mObjectType (each fires addPlayer in Node);
	// Lobby = the LAST loaded one (this.lobby field — getLobbyPosition uses
	// it for teleport-backs).
	LobbyAreas []*Area
	Lobby      *Area
	LobbyWait  map[string]*Member // playersInLobby keyed by instance
	InGame     map[string]*Member // playersInGame keyed by instance

	// Coursing extras.
	HunterSpawn *Area
	PreySpawns  []*Area
	CentreX     int
	CentreY     int

	// Teamwar extras.
	RedSpawn  *Area
	BlueSpawn *Area
	RedKills  int
	BlueKills int
}

// NewGame ports the root newGame closure (key/opcode/name/countdown plus
// empty lobby/in-game registries).
func NewGame(key string, opcode int, name string, countdown int) *Game {
	return &Game{
		Key: key, Opcode: opcode, Name: name, Countdown: countdown,
		LobbyWait: map[string]*Member{}, InGame: map[string]*Member{},
	}
}

// AddArea links one world.json minigame area to this game (minigames.ts
// linkAreas). Areas for other games are ignored; the lobby slot keeps the
// LAST loaded lobby area (this.lobby field).
func (g *Game) AddArea(a Area) {
	if a.Minigame != g.Key {
		return
	}
	cp := a
	switch cp.MObjectType {
	case "lobby":
		g.LobbyAreas = append(g.LobbyAreas, &cp)
		g.Lobby = &cp // last one wins (this.lobby field)
	}
	if g.Key != KeyCoursing && g.Key != KeyTeamWar {
		return
	}
	switch g.Key {
	case KeyCoursing:
		switch cp.MObjectType {
		case "hunterspawn":
			g.HunterSpawn = &cp
		case "preyspawn":
			g.PreySpawns = append(g.PreySpawns, &cp)
		case "centre":
			g.CentreX, g.CentreY = cp.X, cp.Y
		}
	case KeyTeamWar:
		switch cp.MObjectType {
		case "redteamspawn":
			g.RedSpawn = &cp
		case "blueteamspawn":
			g.BlueSpawn = &cp
		}
	}
}

// ---------------------------------------------------------------------------
// Transport seam: the root adapter implements Effects with send/broadcast/
// setEntityPos/m6Notify; pure methods below take it as a parameter.
// ---------------------------------------------------------------------------

// Effects is the transport seam for minigame side effects. Implementations
// live in the root adapter:
//
//	Notify   -> m6Notify(instance's conn, msg)
//	Teleport -> m8Teleport(instance's conn, x, y)
//	Broadcast -> broadcast(frames ...[]any)
type Effects interface {
	Notify(instance, msg string)
	Teleport(instance string, x, y int)
	Broadcast(frames ...[]any)
}

// NotifyMinimumPlayers ports the not-enough-players branch of startCoursing/
// startTeamWar (misc:MINIMUM_PLAYERS_MINIGAME to every lobby waiter).
// Deterministic (sorted) order for logs.
func (g *Game) NotifyMinimumPlayers(fx Effects, minimum int) {
	keys := make([]string, 0, len(g.LobbyWait))
	for k := range g.LobbyWait {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fx.Notify(k, "misc:MINIMUM_PLAYERS_MINIGAME;minimum="+strconv.Itoa(minimum))
	}
}

// TeleportToLobby computes a lobby respawn point per instance (getLobbyPosition
// parity) and fires the Teleport effect. It returns the chosen points.
func (g *Game) TeleportToLobby(fx Effects, instances []string) map[string][2]int {
	out := make(map[string][2]int, len(instances))
	for _, inst := range instances {
		x, y := LobbyPoint(g.Lobby)
		out[inst] = [2]int{x, y}
		fx.Teleport(inst, x, y)
	}
	return out
}

// ---------------------------------------------------------------------------
// Scoring rules.
// ---------------------------------------------------------------------------

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// CoursingScore ports the per-tick prey score update: distance-to-centre
// (Utils.getDistance) divided by COURSING_SCORE_DIVISOR.
func CoursingScore(px, py, centreX, centreY int) int {
	distance := absInt(px-centreX) + absInt(py-centreY)
	return distance / CoursingScoreDivisor
}

// CoursingLeader returns the in-game instance closest to the centre
// (manhattan distance). Ties break by sorted instance for determinism;
// "" when no positions are given.
func (g *Game) CoursingLeader(pos map[string][2]int) string {
	keys := make([]string, 0, len(pos))
	for k := range pos {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	best := ""
	bestDist := 0
	for i, k := range keys {
		d := absInt(pos[k][0]-g.CentreX) + absInt(pos[k][1]-g.CentreY)
		if i == 0 || d < bestDist {
			best, bestDist = k, d
		}
	}
	return best
}

// ApplyDeadPenalty ports the coursing dead-prey tick
// (incrementCoursingScore(-10) with the clamp at 0).
func ApplyDeadPenalty(score int) int {
	score += CoursingDeadPenalty
	if score < 0 {
		score = 0
	}
	return score
}

// ApplyKill ports TeamWar.kill (the coursing kill callback has no behaviour
// in Node — only teamwar overrides it).
func (g *Game) ApplyKill(team int) {
	switch team {
	case TeamWarTeamBlue:
		g.BlueKills += KillPointsPerKill
	case TeamWarTeamRed:
		g.RedKills += KillPointsPerKill
	}
}

// Winner returns the teamwar leader: most kills win, equal kills tie.
func (g *Game) Winner() string {
	switch {
	case g.RedKills > g.BlueKills:
		return WinnerRed
	case g.BlueKills > g.RedKills:
		return WinnerBlue
	default:
		return WinnerTie
	}
}

// ---------------------------------------------------------------------------
// Team splits + lifecycle predicates (shuffle stays with the caller; these
// implement the post-shuffle cut, verbatim from startCoursing/startTeamWar).
// ---------------------------------------------------------------------------

// SplitCoursingKeys splits shuffled lobby keys into hunters/prey 1:1. An odd
// player out is ignored (stays in the lobby for the next round) and returned
// as leftover ("" when even).
func SplitCoursingKeys(keys []string) (hunters, prey []string, leftover string) {
	count := len(keys)
	if count%2 != 0 {
		count--
		leftover = keys[len(keys)-1]
	}
	half := count / 2
	return keys[:half], keys[half:count], leftover
}

// SplitTeamWarKeys splits shuffled lobby keys into blue (first half) and red
// (second half).
func SplitTeamWarKeys(keys []string) (blue, red []string) {
	split := len(keys) / 2
	return keys[:split], keys[split:]
}

// RearmCountdown ports the tick's countdown<=0 re-arm (per-game default).
func RearmCountdown(key string) int {
	if key == KeyCoursing {
		return CoursingCountdown
	}
	return TeamWarCountdown
}

// ShouldStop ports minigame.ts disconnect: stop when fewer than 2 players
// remain.
func ShouldStop(remaining int) bool {
	return remaining < 2
}

// IsScoreTick reports whether this countdown value fires the coursing
// score update (every 4 ticks).
func IsScoreTick(countdown int) bool {
	return countdown%CoursingScoreTick == 0
}
