// Package hub is an ADDITIVE-ONLY, transport-free relay for hub->world
// packets. It defines the Message envelope and the Router that resolves
// recipients against a local online map. It opens no sockets, spawns no
// goroutines, and changes no root behavior.
//
// TS sources mirrored here (read-only, DO NOT import):
//   - packages/server/src/network/client.ts — Client.relay(username, packet)
//     wraps the inner packet in a RelayPacket ([Packets.Relay, [username,
//     innerFrame]]) and sends it over the hub WebSocket; handleOpen sends
//     the HandshakePacket, handleError/handleClose retry every 5s.
//   - packages/server/src/controllers/incoming.ts — hub->world handling:
//     handleRelay delivers the inner frame verbatim to the named player
//     (player.send(new Packet(info[0], info[1], info[2])), debug-log when
//     absent); handleChat DMs the target, broadcasts globally when no
//     target, and notifies the source on notFound/success; handleGuild
//     Update unicasts the member list to the requester; handleFriends
//     applies activeFriends to the named player.
//   - packages/common/network/impl/relay.ts — RelayPacket shape:
//     super(Packets.Relay, undefined, [username, [...packet.serialize()]]).
//   - go-server/main.go — the stub listens on 127.0.0.1:9001 with the HUB
//     disabled; go-server/internal/net/bus.go — Bus fan-out seam
//     (Broadcast/SendTo/RegionOf/Interested) whose conventions this package
//     follows (transport-free interface, caller owns delivery).
//
// Modes:
//
//	all-in-one (current go-server, single process): keep one Router as the
//	local online map. Register on login, Unregister on logout/disconnect,
//	and Route every cross-player message through it. Route resolves only
//	what is local; on ErrOffline the caller falls back to local broadcast
//	(the single process owns every player, so "offline" just means unknown
//	name — same position as TS handleRelay's "could not find player" log).
//
//	multi-server (out of scope): run one Router per world process for its
//	local players and plug a socket transport in the CALLER. On ErrOffline
//	the caller forwards the Message over the hub socket using the
//	client.ts relay/send frame shape; frames arriving from the hub map back
//	into Message values and Route locally. Handshake, auth, reconnect, and
//	wire serialization all belong to that transport, not here.
//
// Divergences from the TS hub (deliberate, transport-free scope):
//   - No sockets or wire format: TS ships [53, undefined, [username,
//     innerFrame]] JSON over WebSocket; here Payload is opaque (the caller
//     passes the frame/payload through untouched) and delivery is the
//     caller's job — Router only resolves recipient usernames.
//   - No side effects: TS handleChat emits notify/success/globalMessage on
//     the world and handleGuild/handleFriends mutate player state. Route
//     has no world hooks; the caller delivers to each returned username and
//     owns acks (notFound/success) itself.
//   - Exact-match usernames, like entities.getPlayer behind TS
//     world.getPlayerByName — no case folding or formatName normalization.
//   - Guild roster is caller-supplied: the TS hub owns cross-server guild /
//     friends state, and Player login/logout sync (syncGuildMembers /
//     syncFriendsList) is out of scope here. For KindGuild the caller puts
//     the candidate member list in Payload and Route filters it to online.
//   - Handshake/auth/reconnect (client.ts handleOpen/handleError/
//     handleClose) do not exist here; see multi-server mode above.
package hub

import (
	"errors"
	"sort"
	"sync"
)

// Kind classifies a Message for recipient resolution.
type Kind string

// Message kinds. Chat is a direct message to To; guild fans out to the
// online subset of the Payload candidate list; global addresses every
// online username.
const (
	KindChat   Kind = "chat"
	KindGuild  Kind = "guild"
	KindGlobal Kind = "global"
)

// Message is one transport-free relay unit.
//
//	From    source username (informational; used by the caller for
//	        notFound/success acks, never for routing).
//	To      target username for KindChat (mirrors RelayPacketData[0] and
//	        ChatPacketData.target).
//	Kind    chat (direct), guild (fanout), or global (broadcast).
//	Payload opaque caller payload delivered verbatim to each recipient
//	        (mirrors RelayPacketData[1], the inner [packet, opcode, data]
//	        frame). For KindGuild the caller passes the candidate member
//	        usernames as []string and Route intersects them with online.
type Message struct {
	From    string
	To      string
	Kind    Kind
	Payload any
}

// ErrOffline is returned by Route when a direct (chat) target is not
// registered. The caller falls back to local broadcast on this error
// (all-in-one) or forwards the Message to the hub socket (multi-server).
var ErrOffline = errors.New("hub: target offline")

// Router resolves Message recipients against a local online set. The zero
// value is not usable; build with NewRouter. Safe for concurrent use.
type Router struct {
	mu     sync.RWMutex
	online map[string]struct{}
}

// NewRouter returns an empty Router.
func NewRouter() *Router {
	return &Router{online: make(map[string]struct{})}
}

// Register marks username online. Empty names are ignored. Registering an
// already-online name is a no-op.
func (r *Router) Register(username string) {
	if username == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.online[username] = struct{}{}
}

// Unregister marks username offline. Unknown names are a no-op.
func (r *Router) Unregister(username string) {
	if username == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.online, username)
}

// Route resolves m to the usernames the caller must deliver to:
//
//	KindChat (and any other non-guild/non-global kind): [m.To] when To is
//	  online, else nil + ErrOffline (caller falls back to local broadcast).
//	KindGuild: the online subset of the Payload []string candidate members,
//	  sorted, possibly empty (empty is NOT an error — there is simply
//	  nobody to deliver to). A non-[]string payload resolves to empty.
//	KindGlobal: every online username, sorted (possibly empty, nil error).
func (r *Router) Route(m Message) ([]string, error) {
	switch m.Kind {
	case KindGlobal:
		return r.snapshot(), nil
	case KindGuild:
		members, _ := m.Payload.([]string)
		return r.intersect(members), nil
	default: // KindChat + unknown kinds route as a direct message to To.
		r.mu.RLock()
		_, ok := r.online[m.To]
		r.mu.RUnlock()
		if !ok {
			return nil, ErrOffline
		}
		return []string{m.To}, nil
	}
}

// snapshot returns all online usernames, sorted.
func (r *Router) snapshot() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.online))
	for name := range r.online {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// intersect returns the sorted online subset of members.
func (r *Router) intersect(members []string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []string{}
	for _, name := range members {
		if _, ok := r.online[name]; ok {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
