// Refresh banner (GO-PLAN §12 R2): when the router's preferred version moves
// past this shard's version, every local session gets one static chatbox
// line — "new version available — refresh when ready" — on the EXISTING
// Chat-19 frame (no new opcode), rate-limited to once per session per version
// change (no spam). Hub-gated: with no HUB_ADDR the shard client never starts
// and every entry point below is a nil-safe no-op, so the default
// all-in-one path is byte-identical.
//
// Input: the hub pushes the preferred version inside every roster push;
// shardHubClient tracks it (PreferredVersion) and fires bannerOnPreferredAll
// on change. Login also checks (maybeBannerConn) so a player who joins an
// already-behind shard still sees one banner. Disconnect forgets the session
// entry (bannerForget), so the next login may banner again.
package server

import (
	"sync"

	gnet "rpg-world-server/internal/net"
	"rpg-world-server/internal/version"
	worldcore "rpg-world-server/internal/world"

	"rpg-world-server/internal/hub"
)

// bannerText is the refresh notice (static chatbox line, Chat-19 source
// frame parity — the client renders source-based Chat as a static line).
const bannerText = "new version available \u2014 refresh when ready"

var (
	bannerMu   sync.Mutex
	bannerSent = map[string]string{} // username -> version already announced this session
)

// ownVersion resolves this shard's world version: the explicit VERSION tag,
// else the buildID+gVer pair (hub.VersionOf parity with the hub rows, so the
// comparison below matches the router's preferred version exactly).
func ownVersion() string {
	return hub.VersionOf(version.BuildID, version.GVer, hub.VersionTag())
}

// shouldBanner is the pure banner decision (unit-tested): banner when the
// hub knows a preferred version that is neither ours nor already announced
// to this session.
func shouldBanner(own, preferred, announced string) bool {
	if own == "" || preferred == "" {
		return false
	}
	return preferred != own && announced != preferred
}

// maybeBannerConn sends one banner to c when due (login path + preferred
// change sweep). Nil-safe; hub-gated.
func maybeBannerConn(c *playerConn) {
	if c == nil || c.Username == "" || shardHubClient == nil {
		return
	}
	pref := shardHubClient.PreferredVersion()
	bannerMu.Lock()
	announced := bannerSent[c.Username]
	bannerMu.Unlock()
	if !shouldBanner(ownVersion(), pref, announced) {
		return
	}
	_ = gnet.Send(c.Conn, pkt(PacketChat, chatPacketData{
		Source: "server", Message: bannerText,
	}))
	bannerMu.Lock()
	bannerSent[c.Username] = pref
	bannerMu.Unlock()
}

// bannerOnPreferredAll sweeps local sessions after a preferred-version change
// (hub onPreferred callback).
func bannerOnPreferredAll() {
	for _, c := range worldcore.AllOf[*playerConn]() {
		maybeBannerConn(c)
	}
}

// bannerForget drops one session's banner record (disconnect path — the next
// login starts a fresh session and may banner again).
func bannerForget(username string) {
	if username == "" {
		return
	}
	bannerMu.Lock()
	delete(bannerSent, username)
	bannerMu.Unlock()
}
