// Multi-server hub transport (ADDITIVE-ONLY).
//
// This file adds the socket transport for hub mode WITHOUT changing the
// all-in-one path: internal/hub/hub.go (Router/Register/Unregister/Route)
// is untouched, and nothing here runs unless HUB_ADDR is set (see Enabled).
// Importing this package starts no goroutines and opens no sockets; the
// caller drives Server/Client explicitly.
//
// Architecture (who owns what):
//
//	all-in-one (default, no HUB_ADDR): one Router is the local online map.
//	Register on login, Unregister on logout, Route every cross-player
//	message. On ErrOffline the caller falls back to local broadcast.
//	Unchanged behavior — this file is not even constructed.
//
//	multi-server (HUB_ADDR set): one hub process owns a Server (the
//	server-list: shard registrations + last-heartbeat per shard, relay
//	routing between shards, central offline mail for unreachable targets
//	drained on the owning shard's next register/heartbeat). Each world
//	process (shard) owns a Router for its local players plus a Client that
//	dials the hub, registers with a handshake frame, heartbeats every 5s,
//	and auto-reconnects with backoff.
//	On a local Route miss the shard's RouteOrForward consults the roster
//	pushed by the hub: target on a remote shard -> forward the relay frame
//	over the socket; target nowhere -> Mailer.Store for delivery on the
//	target's next Register/login.
//
//	Offline mail is shard-side: Mailer fronts a MailStore (MemoryMail by
//	default; SQLiteMail for file persistence via a hub-owned additive
//	table). Deliver is called from the login path right after
//	Router.Register (Client.Register does both).
//
// Env contract:
//
//	HUB_ADDR   hub socket address as ws(s)://host:port[/path] (shard side)
//	           or listen addr host:port (hub side, used by the hub main).
//	           Unset/empty => all-in-one mode: everything here stays idle.
//	HUB_TOKEN  shared secret. When set, the hub rejects mismatches (log +
//	           close/401); shards send it as handshake accessToken and as
//	           an Authorization: Bearer header. When empty, all shards are
//	           accepted (dev default).
//	SHARD_NAME shard identity in register/heartbeat frames (default "shard").
//
// Wire conventions (mirrors the TS reference, read-only):
//
//   - register = handshake frame: [1, null, {type:"hub",name,serverId,
//     accessToken,players}] — client.ts handleOpen sends HandshakePacket,
//     HandshakePacketData.type "hub" (packages/common/network/impl/
//     handshake.ts); Packet.Handshake == 1 (packages/common/network/
//     packets.ts, go protocol.PacketHandshake).
//   - relay envelope = [53, null, [username, innerFrame]] — RelayPacket
//     (packages/common/network/impl/relay.ts:
//     super(Packets.Relay, undefined, [username, [...packet.serialize()]]));
//     Packets.Relay == 53; hub->world delivery is verbatim
//     (controllers/incoming.ts handleRelay). Here innerFrame is the JSON
//     encoding of Message.Payload: game frames ([]any{packet,opcode,data})
//     pass through byte-identical.
//   - heartbeat cadence 5s mirrors client.ts handleError/handleClose retry
//     ("attempting again in 5 seconds"); the TS hub has no heartbeat packet,
//     so heartbeats (and roster pushes) are small JSON objects
//     {"t":"heartbeat"|"roster",...} on the same socket — new surface only,
//     no existing packet shape changes.
//   - eviction: shard idle > 3 missed beats (3 * 5s = 15s) is dropped from
//     the server-list and the roster is re-pushed.
package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	_ "modernc.org/sqlite"

	"rpg-world-server/internal/version"
)

// Env keys for the hub transport.
const (
	// EnvHubAddr gates everything in this file. Empty => all-in-one mode.
	EnvHubAddr = "HUB_ADDR"
	// EnvHubToken is the shared secret (bearer).
	EnvHubToken = "HUB_TOKEN"
	// EnvShardName is the shard identity (default "shard").
	EnvShardName = "SHARD_NAME"
)

// Transport cadence. HeartbeatInterval mirrors the TS 5s hub retry cadence;
// shards idle longer than EvictAfter (3 missed beats) are evicted.
const (
	HeartbeatInterval  = 5 * time.Second
	MissedBeatsToEvict = 3
	EvictAfter         = MissedBeatsToEvict * HeartbeatInterval

	// ReconnectBase/Max bound the shard dial backoff (1s doubling to 30s).
	ReconnectBase = 1 * time.Second
	ReconnectMax  = 30 * time.Second
)

// Frame packet ids (parity with packages/common/network/packets.ts and
// go-server/internal/protocol; kept local so hub stays dependency-free).
const (
	frameHandshake = 1
	frameRelay     = 53
)

// Enabled reports whether multi-server hub mode is on. Everything new in
// this file is gated behind it; the default (no HUB_ADDR) path never
// constructs a Server/Client and behaves exactly as before.
func Enabled() bool { return os.Getenv(EnvHubAddr) != "" }

// SharedToken returns the HUB_TOKEN secret ("" allows all — dev default).
func SharedToken() string { return os.Getenv(EnvHubToken) }

// ShardName returns the SHARD_NAME identity (default "shard").
func ShardName() string {
	if n := os.Getenv(EnvShardName); n != "" {
		return n
	}
	return "shard"
}

// ErrOfflineAuth is returned when a shard registration carries a bad token.
var ErrOfflineAuth = errors.New("hub: hub token mismatch")

// ErrForwarded is returned by RouteOrForward when a direct message missed
// locally and was handed to a remote shard over the hub socket. The caller
// must NOT fall back to local broadcast on this error (unlike ErrOffline).
var ErrForwarded = errors.New("hub: forwarded to remote shard")

// ErrNoRecipient is returned by mail Store when the message has no To.
var ErrNoRecipient = errors.New("hub: mail message has no recipient")

// ---------------------------------------------------------------------------
// Wire frames
// ---------------------------------------------------------------------------

// HubHandshake mirrors TS HubHandshakePacketData (hub variant): sent as the
// data of a [1, null, {...}] handshake frame on connect (client.ts
// handleOpen parity; remoteHost/port/maxPlayers omitted — the Go hub
// does not serve game clients or a server browser). R1 adds the additive
// routing stamps BuildID/GVer/State/Load/Addr (omitempty: pre-R1 shards
// register without them and are treated as RUNNING with load 0).
type HubHandshake struct {
	Type        string   `json:"type"` // always "hub"
	Name        string   `json:"name"`
	ServerID    int      `json:"serverId,omitempty"`
	AccessToken string   `json:"accessToken,omitempty"`
	Players     []string `json:"players,omitempty"`
	// BuildID is the shard binary stamp (version.BuildID via ldflags).
	BuildID string `json:"buildId,omitempty"`
	// GVer is the shard wire version (must equal version.GVer to take
	// matching clients; the router prefers newest RUNNING regardless).
	GVer string `json:"gVer,omitempty"`
	// State is the drain state (RUNNING/DRAINING; "" means RUNNING).
	State string `json:"state,omitempty"`
	// Load is the shard's live-conn count (router info only).
	Load int `json:"load,omitempty"`
	// Addr is the shard's game address for login redirect
	// (e.g. "127.0.0.1:9001"; "" falls back to the shard name).
	Addr string `json:"addr,omitempty"`
}

// encodeFrame serializes a TS-style [packet, opcode, data] array frame.
func encodeFrame(packet int, opcode any, data any) ([]byte, error) {
	return json.Marshal([]any{packet, opcode, data})
}

// decodeFrame parses a [packet, opcode, data] array frame.
func decodeFrame(raw []byte) (packet int, opcode, data json.RawMessage, err error) {
	var parts []json.RawMessage
	if err = json.Unmarshal(raw, &parts); err != nil {
		return 0, nil, nil, err
	}
	if len(parts) != 3 {
		return 0, nil, nil, errors.New("hub: bad frame length")
	}
	if err = json.Unmarshal(parts[0], &packet); err != nil {
		return 0, nil, nil, err
	}
	return packet, parts[1], parts[2], nil
}

// encodeRelay wraps inner (already JSON) in the [53, null, [user, inner]]
// relay envelope (RelayPacket parity).
func encodeRelay(username string, inner json.RawMessage) ([]byte, error) {
	if inner == nil {
		inner = json.RawMessage("null")
	}
	data := []any{username, json.RawMessage(inner)}
	return encodeFrame(frameRelay, nil, data)
}

// decodeRelay splits a [53, null, [user, inner]] envelope.
func decodeRelay(raw []byte) (username string, inner json.RawMessage, err error) {
	packet, _, data, err := decodeFrame(raw)
	if err != nil {
		return "", nil, err
	}
	if packet != frameRelay {
		return "", nil, errors.New("hub: not a relay frame")
	}
	var parts []json.RawMessage
	if err = json.Unmarshal(data, &parts); err != nil || len(parts) != 2 {
		return "", nil, errors.New("hub: bad relay envelope")
	}
	if err = json.Unmarshal(parts[0], &username); err != nil {
		return "", nil, err
	}
	return username, parts[1], nil
}

// isJSONObject reports whether raw is a JSON object (hub control message:
// heartbeat/roster) as opposed to an array frame.
func isJSONObject(raw []byte) bool {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case '{':
			return true
		default:
			return false
		}
	}
	return false
}

// heartbeatMsg is the shard->hub heartbeat (full local player list doubles
// as presence reconciliation, so no separate online/offline frames exist).
// State/Load are the R1 routing stamps (State "" = no change, Load always
// set: the shard reports its live-conn count every beat).
type heartbeatMsg struct {
	Type    string   `json:"t"` // "heartbeat"
	Name    string   `json:"name"`
	Players []string `json:"players,omitempty"`
	State   string   `json:"state,omitempty"`
	Load    int      `json:"load,omitempty"`
}

// rosterMsg is the hub->shard roster push: every known online player and the
// shard hosting them (drives RouteOrForward remote decisions).
type rosterMsg struct {
	Type    string            `json:"t"` // "roster"
	Players map[string]string `json:"players"`
}

// ---------------------------------------------------------------------------
// Offline mailer
// ---------------------------------------------------------------------------

// MailStore persists offline mail. Implementations must be safe for
// concurrent use. Payloads round-trip through JSON (documented lossy for Go
// values without a JSON mapping, e.g. KindGuild []string candidates decode
// as []any — the mailer preserves bytes, not Go types).
type MailStore interface {
	Store(m Message) error
	Take(username string) ([]Message, error)
}

// MemoryMail is the default in-memory MailStore (exact-match usernames,
// like Router; the caller normalizes case at the boundary).
type MemoryMail struct {
	mu  sync.Mutex
	box map[string][]Message
}

// NewMemoryMail returns an empty MemoryMail.
func NewMemoryMail() *MemoryMail { return &MemoryMail{box: make(map[string][]Message)} }

// Store queues m for m.To.
func (m *MemoryMail) Store(msg Message) error {
	if msg.To == "" {
		return ErrNoRecipient
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.box[msg.To] = append(m.box[msg.To], msg)
	return nil
}

// Take pops all queued mail for username (nil when empty).
func (m *MemoryMail) Take(username string) ([]Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.box[username]
	delete(m.box, username)
	return out, nil
}

// sqliteMailSchema is owned by the hub package and purely additive: one
// table, no changes to any existing schema.
const sqliteMailSchema = `CREATE TABLE IF NOT EXISTS hub_mail(
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	recipient TEXT NOT NULL,
	sender TEXT NOT NULL DEFAULT '',
	kind TEXT NOT NULL DEFAULT '',
	payload TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_hub_mail_recipient ON hub_mail(recipient, id);`

// SQLiteMail is a file-backed MailStore (same exact-match semantics as
// MemoryMail). Open with OpenSQLiteMail; Close releases the handle.
type SQLiteMail struct {
	db *sql.DB
}

// OpenSQLiteMail opens path (e.g. "hub_mail.db" or ":memory:" for tests)
// and ensures the additive hub_mail table exists.
func OpenSQLiteMail(path string) (*SQLiteMail, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(sqliteMailSchema); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &SQLiteMail{db: db}, nil
}

// Close releases the database handle.
func (s *SQLiteMail) Close() error { return s.db.Close() }

// Store queues m for m.To with its payload JSON-encoded.
func (s *SQLiteMail) Store(m Message) error {
	if m.To == "" {
		return ErrNoRecipient
	}
	raw, err := json.Marshal(m.Payload)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO hub_mail(recipient, sender, kind, payload, created_at) VALUES(?,?,?,?,?)`,
		m.To, m.From, string(m.Kind), string(raw), time.Now().Unix(),
	)
	return err
}

// Take pops all queued mail for username in FIFO order.
func (s *SQLiteMail) Take(username string) ([]Message, error) {
	rows, err := s.db.Query(
		`SELECT id, sender, kind, payload FROM hub_mail WHERE recipient=? ORDER BY id`, username)
	if err != nil {
		return nil, err
	}
	var out []Message
	var ids []int64
	for rows.Next() {
		var id int64
		var from, kind, payload string
		if err := rows.Scan(&id, &from, &kind, &payload); err != nil {
			_ = rows.Close()
			return nil, err
		}
		var p any
		if err := json.Unmarshal([]byte(payload), &p); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, Message{From: from, To: username, Kind: Kind(kind), Payload: p})
		ids = append(ids, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, err := s.db.Exec(`DELETE FROM hub_mail WHERE id=?`, id); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Mailer fronts a MailStore with the login-path delivery helper. The zero
// value is not usable; build with NewMailer.
type Mailer struct {
	store MailStore
}

// NewMailer returns a Mailer over store, or an in-memory store when nil.
func NewMailer(store MailStore) *Mailer {
	if store == nil {
		store = NewMemoryMail()
	}
	return &Mailer{store: store}
}

// Store queues a direct message for an offline player. Messages without To
// (guild fanout, global) are caller-routed and rejected here.
func (m *Mailer) Store(msg Message) error { return m.store.Store(msg) }

// Deliver pops all pending mail for username. Call on the login path right
// after Router.Register; the caller delivers each message locally. Returns
// nil when nothing is pending.
func (m *Mailer) Deliver(username string) []Message {
	if m == nil {
		return nil
	}
	out, err := m.store.Take(username)
	if err != nil {
		log.Printf("hub: mail deliver for %q: %v", username, err)
		return nil
	}
	return out
}

// ---------------------------------------------------------------------------
// Hub-side Server
// ---------------------------------------------------------------------------

// shardEntry is one registered world process.
type shardEntry struct {
	conn     *websocket.Conn // nil in unit tests (transport-free)
	wmu      sync.Mutex      // guards conn writes
	name     string
	players  map[string]struct{}
	lastBeat time.Time
	// R1 routing stamps (from HubHandshake/heartbeat; see Register).
	buildID   string
	gVer      string
	state     string // "" means RUNNING (pre-R1 shards)
	load      int
	addr      string
	firstSeen time.Time // registration order = build newness
}

// ShardInfo is the transport-free snapshot of one shardEntry for the
// router server-list (see Server.ListShards / NewestRunning).
type ShardInfo struct {
	Name    string
	Addr    string
	BuildID string
	GVer    string
	State   string
	Load    int
	// Newest is true on the NewestRunning pick (login target for NEW
	// sessions) when rendered via ListShards.
	Newest bool
}

// effectiveState normalizes "" (pre-R1 shard) to RUNNING.
func effectiveState(state string) string {
	if state == "" {
		return version.StateRunning
	}
	return state
}

// Server is the hub-side server-list: it accepts shard registrations,
// tracks last-heartbeat per shard, evicts shards idle longer than
// EvictAfter, routes relay frames to the owning shard, and stores mail for
// players online nowhere. Safe for concurrent use.
//
// The zero value is not usable; build with NewServer. now is a test hook
// (defaults to time.Now).
type Server struct {
	mu     sync.Mutex
	token  string
	mail   *Mailer
	shards map[string]*shardEntry
	// playerShard maps username -> hosting shard name.
	playerShard map[string]string
	now         func() time.Time

	// Upgrader handles shard sockets (CheckOrigin allow-all mirrors the
	// game stub transport; the socket is token-authenticated instead).
	Upgrader websocket.Upgrader
}

// NewServer returns a Server enforcing token ("" allows all) with mail for
// nowhere-online recipients (nil => in-memory default).
func NewServer(token string, mail *Mailer) *Server {
	if mail == nil {
		mail = NewMailer(nil)
	}
	return &Server{
		token:       token,
		mail:        mail,
		shards:      make(map[string]*shardEntry),
		playerShard: make(map[string]string),
		now:         time.Now,
		Upgrader: websocket.Upgrader{
			CheckOrigin: func(_ *http.Request) bool { return true },
		},
	}
}

// checkAuth verifies a registration: when the server token is set, either
// the Authorization bearer header (checked at upgrade) or the handshake
// accessToken must match. Mismatches are rejected and logged by the caller.
func (s *Server) checkAuth(headerToken, handshakeToken string) bool {
	if s.token == "" {
		return true
	}
	return headerToken == s.token || handshakeToken == s.token
}

// bearerToken parses an "Authorization: Bearer <token>" header value.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// Register records (or refreshes) a shard from its handshake: verifies
// auth, upserts the entry, adopts the handshake player list, records the R1
// routing stamps (buildID/gVer/state/load/addr), and stamps last-heartbeat.
// firstSeen tracks registration order (build newness for NewestRunning): a
// brand-new name sets it; a re-register keeps it unless the buildID changed
// (same-name redeploy of a new build counts as new). Transport-free
// (unit-testable); the socket is attached separately by ServeHTTP via
// Attach.
func (s *Server) Register(hs HubHandshake) error {
	if hs.Name == "" || hs.Type != "hub" {
		return errors.New("hub: bad handshake")
	}
	if !s.checkAuth("", hs.AccessToken) {
		log.Printf("hub: shard %q rejected (token mismatch)", hs.Name)
		return ErrOfflineAuth
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	e := s.shards[hs.Name]
	if e == nil {
		e = &shardEntry{
			name:      hs.Name,
			players:   make(map[string]struct{}),
			firstSeen: now,
		}
		s.shards[hs.Name] = e
	} else if hs.BuildID != "" && hs.BuildID != e.buildID {
		// Same-name redeploy of a new build: it is newer than everything
		// registered before it.
		e.firstSeen = now
	}
	e.players = make(map[string]struct{}, len(hs.Players))
	for _, p := range hs.Players {
		if p != "" {
			e.players[p] = struct{}{}
		}
	}
	if hs.BuildID != "" {
		e.buildID = hs.BuildID
	}
	if hs.GVer != "" {
		e.gVer = hs.GVer
	}
	if hs.State != "" {
		e.state = hs.State
	}
	e.load = hs.Load
	if hs.Addr != "" {
		e.addr = hs.Addr
	}
	e.lastBeat = now
	s.rebuildLocked()
	return nil
}

// Heartbeat refreshes a shard's last-heartbeat and reconciles its player
// list (full-list replace). Unknown shards are rejected so a restarted hub
// forces a clean re-register. Routing stamps are preserved (use
// HeartbeatEx to move them).
func (s *Server) Heartbeat(name string, players []string) error {
	return s.HeartbeatEx(name, players, "", -1)
}

// HeartbeatEx is Heartbeat plus the R1 routing stamps: a non-"" state moves
// the drain state (RUNNING/DRAINING), and a non-negative load replaces it.
// Heartbeats carrying neither leave the stamps untouched (pre-R1 shape).
func (s *Server) HeartbeatEx(name string, players []string, state string, load int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.shards[name]
	if e == nil {
		return errors.New("hub: unknown shard")
	}
	e.players = make(map[string]struct{}, len(players))
	for _, p := range players {
		if p != "" {
			e.players[p] = struct{}{}
		}
	}
	if state != "" {
		e.state = state
	}
	if load >= 0 {
		e.load = load
	}
	e.lastBeat = s.now()
	s.rebuildLocked()
	return nil
}

// Attach binds a live socket to a registered shard (called by ServeHTTP
// after Register). Detach unbinds on disconnect (presence is kept until
// eviction so a flapping socket does not drop the server-list entry).
func (s *Server) Attach(name string, conn *websocket.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.shards[name]; e != nil {
		e.conn = conn
	}
}

// Detach unbinds a socket from a shard (conn identity-checked).
func (s *Server) Detach(name string, conn *websocket.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.shards[name]; e != nil && e.conn == conn {
		e.conn = nil
	}
}

// FindPlayer reports the shard hosting username, if any.
func (s *Server) FindPlayer(username string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	shard, ok := s.playerShard[username]
	return shard, ok
}

// ShardCount reports the number of registered shards.
func (s *Server) ShardCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.shards)
}

// NewestRunning reports the newest healthy RUNNING shard for NEW sessions
// (login routing): among entries whose effective state is RUNNING ("" counts
// as RUNNING for pre-R1 shards), the latest firstSeen wins, ties broken by
// name ascending. Evicted (3-miss) shards are gone from the table, so they
// never win. ok is false when no RUNNING shard is registered — the caller
// must not start new sessions anywhere (clients keep current sessions).
// Transport-free.
func (s *Server) NewestRunning() (ShardInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *shardEntry
	for _, e := range s.shards {
		if effectiveState(e.state) != version.StateRunning {
			continue
		}
		if best == nil || e.firstSeen.After(best.firstSeen) ||
			(e.firstSeen.Equal(best.firstSeen) && e.name < best.name) {
			best = e
		}
	}
	if best == nil {
		return ShardInfo{}, false
	}
	return s.infoLocked(best, true), true
}

// ListShards snapshots every registered shard newest-first (same order as
// NewestRunning), flagging the login target. Transport-free; backs the
// router GET /servers server-list.
func (s *Server) ListShards() []ShardInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	var running *shardEntry
	for _, e := range s.shards {
		if effectiveState(e.state) != version.StateRunning {
			continue
		}
		if running == nil || e.firstSeen.After(running.firstSeen) ||
			(e.firstSeen.Equal(running.firstSeen) && e.name < running.name) {
			running = e
		}
	}
	out := make([]ShardInfo, 0, len(s.shards))
	for _, e := range s.shards {
		out = append(out, s.infoLocked(e, running != nil && e == running))
	}
	// Newest-first by (firstSeen desc, name asc). firstSeen is not on the
	// snapshot, so sort via the entries under the same lock.
	sort.Slice(out, func(i, j int) bool {
		ei, ej := s.shards[out[i].Name], s.shards[out[j].Name]
		if ei == nil || ej == nil {
			return out[i].Name < out[j].Name
		}
		if !ei.firstSeen.Equal(ej.firstSeen) {
			return ei.firstSeen.After(ej.firstSeen)
		}
		return ei.name < ej.name
	})
	return out
}

// infoLocked snapshots e. Caller holds mu.
func (s *Server) infoLocked(e *shardEntry, newest bool) ShardInfo {
	return ShardInfo{
		Name: e.name, Addr: e.addr, BuildID: e.buildID, GVer: e.gVer,
		State: effectiveState(e.state), Load: e.load, Newest: newest,
	}
}

// Roster snapshots username -> shard for every known online player,
// sorted keys available via SortedRoster.
func (s *Server) Roster() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.playerShard))
	for u, sh := range s.playerShard {
		out[u] = sh
	}
	return out
}

// EvictIdle drops shards idle longer than EvictAfter (3 missed 5s beats)
// and returns their names, sorted. Call it from StartSweeper or on any
// cadence the embedder owns; Register/Heartbeat also reconcile the roster
// but never evict (eviction is time-based only).
func (s *Server) EvictIdle() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.evictLocked(s.now())
}

// StartSweeper evicts idle shards plus pushes the roster on EvictAfter
// cadence until ctx ends. Optional: without it, call EvictIdle manually.
func (s *Server) StartSweeper(ctx context.Context) {
	go func() {
		t := time.NewTicker(EvictAfter)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if evicted := s.EvictIdle(); len(evicted) > 0 {
					s.pushRoster()
				}
			}
		}
	}()
}

// evictLocked removes shards idle past EvictAfter. Caller holds mu.
func (s *Server) evictLocked(now time.Time) []string {
	var evicted []string
	for name, e := range s.shards {
		if now.Sub(e.lastBeat) > EvictAfter {
			if e.conn != nil {
				_ = e.conn.Close()
				e.conn = nil
			}
			delete(s.shards, name)
			evicted = append(evicted, name)
			log.Printf("hub: shard %q evicted (3 missed heartbeats)", name)
		}
	}
	if len(evicted) > 0 {
		sort.Strings(evicted)
		s.rebuildLocked()
	}
	return evicted
}

// rebuildLocked recomputes playerShard from all entries. Caller holds mu.
func (s *Server) rebuildLocked() {
	s.playerShard = make(map[string]string)
	for name, e := range s.shards {
		for p := range e.players {
			s.playerShard[p] = name
		}
	}
}

// Relay routes one [53, ...] envelope to the shard hosting its target
// player, or stores central offline mail when the player is online nowhere
// (or the owning socket is down). raw is forwarded verbatim (handleRelay
// parity). Queued hub mail drains on the shard's next register/heartbeat
// via deliverPending. Transport-free except the live-socket write.
func (s *Server) Relay(raw []byte) {
	username, _, err := decodeRelay(raw)
	if err != nil {
		log.Printf("hub: bad relay frame: %v", err)
		return
	}
	s.mu.Lock()
	shardName, ok := s.playerShard[username]
	var target *shardEntry
	if ok {
		target = s.shards[shardName]
	}
	s.mu.Unlock()
	if !ok || target == nil {
		s.storeOffline(username, raw)
		return
	}
	target.wmu.Lock()
	connDown := target.conn == nil
	if !connDown {
		_ = target.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := target.conn.WriteMessage(websocket.TextMessage, raw); err != nil {
			log.Printf("hub: relay to shard %q: %v", shardName, err)
		}
	}
	target.wmu.Unlock()
	if connDown {
		// Owning socket down (reconnect race): keep as offline mail
		// instead of dropping; presence stays until eviction.
		s.storeOffline(username, raw)
	}
}

// storeOffline queues a relay envelope's content as central offline mail.
func (s *Server) storeOffline(username string, raw []byte) {
	var inner any
	if _, in, derr := decodeRelay(raw); derr == nil {
		_ = json.Unmarshal(in, &inner)
	}
	if err := s.mail.Store(Message{To: username, Kind: KindChat, Payload: inner}); err != nil {
		log.Printf("hub: offline store for %q: %v", username, err)
	}
}

// deliverPending pushes queued hub mail for the shard's players as relay
// envelopes. Called after attach and on every heartbeat so reconnects
// drain the queue. Shards without a live socket keep their mail queued.
func (s *Server) deliverPending(name string) {
	s.mu.Lock()
	e := s.shards[name]
	if e == nil || e.conn == nil {
		s.mu.Unlock()
		return
	}
	players := make([]string, 0, len(e.players))
	for p := range e.players {
		players = append(players, p)
	}
	s.mu.Unlock()
	for _, p := range players {
		for _, m := range s.mail.Deliver(p) {
			inner, err := json.Marshal(m.Payload)
			if err != nil {
				log.Printf("hub: mail marshal for %q: %v", p, err)
				continue
			}
			raw, err := encodeRelay(p, inner)
			if err != nil {
				continue
			}
			e.wmu.Lock()
			if e.conn != nil {
				_ = e.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				_ = e.conn.WriteMessage(websocket.TextMessage, raw)
			}
			e.wmu.Unlock()
		}
	}
}

// pushRoster broadcasts the current roster to every attached shard.
func (s *Server) pushRoster() {
	raw, err := json.Marshal(rosterMsg{Type: "roster", Players: s.Roster()})
	if err != nil {
		return
	}
	s.mu.Lock()
	entries := make([]*shardEntry, 0, len(s.shards))
	for _, e := range s.shards {
		entries = append(entries, e)
	}
	s.mu.Unlock()
	for _, e := range entries {
		e.wmu.Lock()
		if e.conn != nil {
			_ = e.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			_ = e.conn.WriteMessage(websocket.TextMessage, raw)
		}
		e.wmu.Unlock()
	}
}

// ServeHTTP upgrades shard sockets: optional bearer-header check first
// (401 on mismatch), then the first frame must be a [1, ...] handshake
// (validated token on mismatch: log + close). Afterwards it serves the
// heartbeat objects and relay frames until disconnect, pushing the roster
// on every membership change.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	headerToken := bearerToken(r)
	if s.token != "" && headerToken != "" && headerToken != s.token {
		log.Printf("hub: connection from %q rejected (bearer mismatch)", r.RemoteAddr)
		http.Error(w, "hub: token mismatch", http.StatusUnauthorized)
		return
	}
	conn, err := s.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("hub: upgrade from %q: %v", r.RemoteAddr, err)
		return
	}
	defer func() { _ = conn.Close() }()

	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		log.Printf("hub: handshake read from %q: %v", r.RemoteAddr, err)
		return
	}
	packet, _, data, err := decodeFrame(raw)
	if err != nil || packet != frameHandshake {
		log.Printf("hub: bad handshake from %q", r.RemoteAddr)
		return
	}
	var hs HubHandshake
	if err := json.Unmarshal(data, &hs); err != nil {
		log.Printf("hub: bad handshake payload from %q", r.RemoteAddr)
		return
	}
	if !s.checkAuth(headerToken, hs.AccessToken) {
		log.Printf("hub: shard %q rejected (token mismatch)", hs.Name)
		return
	}
	if err := s.Register(hs); err != nil {
		log.Printf("hub: shard %q register: %v", hs.Name, err)
		return
	}
	log.Printf("hub: shard %q registered (%d players)", hs.Name, len(hs.Players))
	s.Attach(hs.Name, conn)
	defer s.Detach(hs.Name, conn)
	s.deliverPending(hs.Name)
	s.pushRoster()
	defer s.pushRoster()

	_ = conn.SetReadDeadline(time.Time{})
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if isJSONObject(raw) {
			var hb heartbeatMsg
			if err := json.Unmarshal(raw, &hb); err != nil || hb.Type != "heartbeat" {
				continue
			}
			if hb.Name == "" {
				hb.Name = hs.Name
			}
			if err := s.HeartbeatEx(hb.Name, hb.Players, hb.State, hb.Load); err != nil {
				log.Printf("hub: heartbeat: %v", err)
				continue
			}
			s.deliverPending(hb.Name)
			s.pushRoster()
			continue
		}
		packet, _, _, err := decodeFrame(raw)
		if err != nil || packet != frameRelay {
			continue
		}
		s.Relay(raw)
	}
}

// ---------------------------------------------------------------------------
// Shard-side Client
// ---------------------------------------------------------------------------

// RelayHandler receives hub->world relay deliveries: to is the local
// player, inner is the verbatim inner frame (handleRelay parity — the
// caller passes it to the player conn untouched).
type RelayHandler func(to string, inner json.RawMessage)

// Client is the shard side of the hub transport: dials the hub, registers
// with a handshake frame, heartbeats, auto-reconnects with backoff, and
// forwards local Route misses to remote shards. A nil *Client is valid and
// means all-in-one (every method is a documented no-op returning the
// all-in-one answer), so callers can hold one unconditionally and stay
// byte-identical when HUB_ADDR is unset.
type Client struct {
	addr  string
	token string
	name  string

	router  *Router
	mail    *Mailer
	onRelay RelayHandler

	hbInterval time.Duration

	mu      sync.Mutex
	conn    *websocket.Conn
	wmu     sync.Mutex
	remote  map[string]string // username -> shard (hub roster)
	local   map[string]struct{}
	stop    chan struct{}
	stopped bool
	wg      sync.WaitGroup

	// R1 shard stamps, reported in the register frame and every
	// heartbeat (router server-list + login routing). players, when set,
	// supplies the presence list instead of the Register/Unregister-tracked
	// set (shard role: the game owns login, the client only reports).
	buildID string
	gVer    string
	game    string // shard game addr for login redirect
	state   string // drain state override ("" = RUNNING)
	players func() []string
}

// NewClient builds a shard client for addr (ws://host:port/path). router is
// the shard's local Router (must be non-nil when the client runs); mail
// defaults to in-memory; onRelay may be nil (deliveries are then dropped
// with a debug log).
func NewClient(addr, token, name string, router *Router, mail *Mailer, onRelay RelayHandler) *Client {
	if mail == nil {
		mail = NewMailer(nil)
	}
	return &Client{
		addr: addr, token: token, name: name,
		router: router, mail: mail, onRelay: onRelay,
		hbInterval: HeartbeatInterval,
		remote:     make(map[string]string),
		local:      make(map[string]struct{}),
		stop:       make(chan struct{}),
	}
}

// ClientFromEnv builds a Client from HUB_ADDR/HUB_TOKEN/SHARD_NAME, or
// returns nil in all-in-one mode (no HUB_ADDR) so the default path stays
// exactly as today.
func ClientFromEnv(router *Router, mail *Mailer, onRelay RelayHandler) *Client {
	addr := os.Getenv(EnvHubAddr)
	if addr == "" {
		return nil
	}
	return NewClient(addr, SharedToken(), ShardName(), router, mail, onRelay)
}

// SetHeartbeatInterval overrides the 5s heartbeat cadence (tests).
func (c *Client) SetHeartbeatInterval(d time.Duration) {
	if c == nil || d <= 0 {
		return
	}
	c.hbInterval = d
}

// SetBuild records the shard routing stamps reported in the register frame
// and every heartbeat (buildID via version.BuildID, gVer via version.GVer,
// game = shard game addr for login redirect). Nil-safe.
func (c *Client) SetBuild(buildID, gVer, game string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buildID, c.gVer, c.game = buildID, gVer, game
}

// SetState overrides the drain state reported in heartbeats ("" = RUNNING).
// The shard role flips this to DRAINING on SIGTERM so the router stops
// sending NEW sessions while existing ones play on. Nil-safe.
func (c *Client) SetState(state string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = state
}

// SetPlayersProvider supplies the presence list for register/heartbeat
// frames. When set, the game owns login (shard role) and the client only
// reports; otherwise presence comes from Register/Unregister. Nil-safe.
func (c *Client) SetPlayersProvider(players func() []string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.players = players
}

// Register marks username online locally and returns its pending offline
// mail for the caller to deliver. Login-path hook (Router.Register +
// Mailer.Deliver in one step).
func (c *Client) Register(username string) []Message {
	if c == nil || c.router == nil {
		return nil
	}
	c.router.Register(username)
	c.mu.Lock()
	if c.local == nil {
		c.local = make(map[string]struct{})
	}
	c.local[username] = struct{}{}
	c.mu.Unlock()
	return c.mail.Deliver(username)
}

// Unregister marks username offline locally.
func (c *Client) Unregister(username string) {
	if c == nil || c.router == nil {
		return
	}
	c.router.Unregister(username)
	c.mu.Lock()
	delete(c.local, username)
	c.mu.Unlock()
}

// Connected reports whether the hub socket is up. Nil-safe (false).
func (c *Client) Connected() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil
}

// WaitConnected polls for a live socket until timeout. Nil-safe (false).
func (c *Client) WaitConnected(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c.Connected() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return c.Connected()
}

// RemoteOf reports the hub-known shard hosting username, per the last
// roster push. Nil-safe (false). Lets the caller distinguish
// remote-vs-nowhere before routing (the future social_wire branch).
func (c *Client) RemoteOf(username string) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	shard, ok := c.remote[username]
	return shard, ok
}

// RouteOrForward resolves m locally first (Route parity). On a local miss
// of a direct message it forwards the [53, ...] relay envelope over the
// socket when the hub roster places the target on a remote shard
// (returning ErrForwarded — no local fallback), and otherwise stores
// offline mail and returns ErrOffline (caller falls back exactly as in
// all-in-one). Guild/global kinds resolve locally only. Nil-client (or
// offline-socket) behavior is exactly the all-in-one answer: local Route,
// else store + ErrOffline.
func (c *Client) RouteOrForward(m Message) ([]string, error) {
	if c == nil || c.router == nil {
		return nil, ErrOffline
	}
	if recips, err := c.router.Route(m); err == nil {
		return recips, nil
	} else if !errors.Is(err, ErrOffline) {
		return nil, err
	}
	if m.To == "" || m.Kind == KindGuild || m.Kind == KindGlobal {
		return nil, ErrOffline
	}
	if c.forwardRemote(m) {
		return nil, ErrForwarded
	}
	if err := c.mail.Store(m); err != nil {
		return nil, err
	}
	return nil, ErrOffline
}

// forwardRemote sends m to the shard hosting m.To per the hub roster.
// Reports whether the target is remote AND the socket is up (only then is
// the message handed off).
func (c *Client) forwardRemote(m Message) bool {
	c.mu.Lock()
	_, remote := c.remote[m.To]
	conn := c.conn
	c.mu.Unlock()
	if !remote || conn == nil {
		return false
	}
	inner, err := json.Marshal(m.Payload)
	if err != nil {
		log.Printf("hub: relay marshal for %q: %v", m.To, err)
		return false
	}
	raw, err := encodeRelay(m.To, inner)
	if err != nil {
		return false
	}
	c.wmu.Lock()
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	err = conn.WriteMessage(websocket.TextMessage, raw)
	c.wmu.Unlock()
	if err != nil {
		log.Printf("hub: relay forward for %q: %v", m.To, err)
		return false
	}
	return true
}

// localPlayers snapshots the shard's online set: the provider list when
// SetPlayersProvider is used (shard role), else the set observed through
// Client.Register/Unregister.
func (c *Client) localPlayers() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.players != nil {
		out := append([]string(nil), c.players()...)
		sort.Strings(out)
		return out
	}
	out := make([]string, 0, len(c.local))
	for u := range c.local {
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}

// clientStamps snapshots the R1 routing stamps under lock.
func (c *Client) clientStamps() (buildID, gVer, game, state string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buildID, c.gVer, c.game, c.state
}

// handshakeFrame builds the [1, null, {...}] register frame.
func (c *Client) handshakeFrame() ([]byte, error) {
	players := c.localPlayers()
	buildID, gVer, game, state := c.clientStamps()
	hs := HubHandshake{
		Type: "hub", Name: c.name, AccessToken: c.token, Players: players,
		BuildID: buildID, GVer: gVer, State: state, Load: len(players), Addr: game,
	}
	return encodeFrame(frameHandshake, nil, hs)
}

// heartbeatFrame builds the {"t":"heartbeat",...} frame.
func (c *Client) heartbeatFrame() ([]byte, error) {
	players := c.localPlayers()
	_, _, _, state := c.clientStamps()
	return json.Marshal(heartbeatMsg{
		Type: "heartbeat", Name: c.name, Players: players,
		State: state, Load: len(players),
	})
}

// Start dials the hub and serves register/heartbeat/read until ctx ends or
// Stop is called. Nil-safe (no-op). It blocks; run it in a goroutine.
func (c *Client) Start(ctx context.Context) {
	if c == nil {
		return
	}
	backoff := ReconnectBase
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stop:
			return
		default:
		}
		if c.dialOnce(ctx) {
			backoff = ReconnectBase
		} else {
			select {
			case <-ctx.Done():
				return
			case <-c.stop:
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > ReconnectMax {
				backoff = ReconnectMax
			}
		}
	}
}

// Stop ends a running Start loop and closes the socket. Nil-safe.
func (c *Client) Stop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if !c.stopped {
		c.stopped = true
		close(c.stop)
	}
	conn := c.conn
	c.conn = nil
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	c.wg.Wait()
}

// dialOnce connects, registers, and serves until the socket drops.
// Reports false when the caller should back off before retrying.
func (c *Client) dialOnce(ctx context.Context) bool {
	header := http.Header{}
	if c.token != "" {
		header.Set("Authorization", "Bearer "+c.token)
	}
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(ctx, c.addr, header)
	if err != nil {
		log.Printf("hub: dial %q: %v (retrying)", c.addr, err)
		return false
	}
	hs, err := c.handshakeFrame()
	if err != nil {
		_ = conn.Close()
		return false
	}
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, hs); err != nil {
		log.Printf("hub: register: %v (retrying)", err)
		_ = conn.Close()
		return false
	}
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	done := make(chan struct{})
	c.wg.Add(1)
	go c.readLoop(conn, done)
	defer func() {
		<-done
		c.wg.Done()
	}()

	t := time.NewTicker(c.hbInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			c.detach(conn)
			_ = conn.Close()
			return true
		case <-c.stop:
			c.detach(conn)
			_ = conn.Close()
			return true
		case <-t.C:
			hb, err := c.heartbeatFrame()
			if err != nil {
				continue
			}
			c.wmu.Lock()
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			err = conn.WriteMessage(websocket.TextMessage, hb)
			c.wmu.Unlock()
			if err != nil {
				log.Printf("hub: heartbeat: %v (reconnecting)", err)
				c.detach(conn)
				_ = conn.Close()
				return true
			}
		}
	}
}

// detach clears the socket if it is still conn.
func (c *Client) detach(conn *websocket.Conn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == conn {
		c.conn = nil
	}
}

// readLoop serves inbound hub frames until the socket drops, then closes
// done. Relay envelopes addressed to local players go to onRelay verbatim
// (handleRelay parity); roster pushes refresh the remote map.
func (c *Client) readLoop(conn *websocket.Conn, done chan struct{}) {
	defer close(done)
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			c.detach(conn)
			_ = conn.Close()
			return
		}
		if isJSONObject(raw) {
			var rs rosterMsg
			if err := json.Unmarshal(raw, &rs); err != nil || rs.Type != "roster" {
				continue
			}
			remote := make(map[string]string, len(rs.Players))
			for u, sh := range rs.Players {
				if u != "" && sh != "" {
					remote[u] = sh
				}
			}
			c.mu.Lock()
			c.remote = remote
			c.mu.Unlock()
			continue
		}
		packet, _, _, err := decodeFrame(raw)
		if err != nil || packet != frameRelay {
			continue
		}
		to, inner, err := decodeRelay(raw)
		if err != nil {
			continue
		}
		if _, rerr := c.router.Route(Message{Kind: KindChat, To: to}); rerr != nil {
			// Target left between hub routing and delivery: keep it as
			// offline mail instead of dropping it.
			var payload any
			_ = json.Unmarshal(inner, &payload)
			if serr := c.mail.Store(Message{To: to, Kind: KindChat, Payload: payload}); serr != nil {
				log.Printf("hub: relay for offline %q dropped: %v", to, serr)
			}
			continue
		}
		if c.onRelay != nil {
			c.onRelay(to, inner)
		} else {
			log.Printf("hub: relay for %q dropped (no handler)", to)
		}
	}
}
