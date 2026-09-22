// gVer gate (GO-PLAN §12 R1 / REWRITE-V2 V2-M1): the version contract.
//
// The stock client sends Handshake{gVer} with gVer = config.version =
// GVER ('0.5.5-beta' in .env.defaults); the TS game server rejects any
// mismatch (incoming.ts -> connection.reject('updated'), socket close
// 1010). This file enforces the same contract on the Go stub: a strict
// Handshake{gVer} check in handleConn with GVER_STRICT=0 as the dev escape
// hatch.
//
// Reject uses NO new opcode: one Notification-Text frame ([25, 2,
// {message}]) carrying the hub redirect payload, then close — the same
// shape as the TS close-with-reason, plus a human-readable pointer at the
// correct build. Documented payload:
//
//	"version mismatch: server gVer=<GVer> buildID=<BuildID> (client gVer=<gv>).
//	 Refresh the client, or reconnect via <hubAddr>."
//
// (<hubAddr> is HUB_ADDR when set; otherwise the refresh clause stands
// alone. In all-in-one mode there is no hub, so the message is purely the
// refresh pointer.)
package server

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/gorilla/websocket"

	gnet "rpg-world-server/internal/net"
	"rpg-world-server/internal/version"
)

// handshakeDataElem picks the Handshake data element: the stock client
// sends [1, data] (socket.ts send), hub-style register frames send
// [1, null, data]. Anything else has no readable gVer.
func handshakeDataElem(frame clientFrame) json.RawMessage {
	switch {
	case len(frame) == 2:
		return frame[1]
	case len(frame) >= 3:
		return frame[2]
	default:
		return nil
	}
}

// gverGatePass reports whether a Handshake frame satisfies the version
// contract, returning the client gVer for the reject payload / logs.
// GVER_STRICT=0 accepts everything (dev escape hatch).
func gverGatePass(frame clientFrame) (clientGVer string, pass bool) {
	clientGVer = version.ExtractGVer(handshakeDataElem(frame))
	return clientGVer, version.Pass(clientGVer)
}

// gverRejectMessage builds the hub-redirect notice for a mismatched client.
func gverRejectMessage(clientGVer string) string {
	msg := fmt.Sprintf("version mismatch: server gVer=%s buildID=%s (client gVer=%s). Refresh the client",
		version.GVer, version.BuildID, clientGVer)
	if hub := os.Getenv("HUB_ADDR"); hub != "" {
		msg += fmt.Sprintf(", or reconnect via %s", hub)
	}
	return msg + "."
}

// sendGVerReject delivers the reject notice on an existing opcode
// (Notification Text, no wire change) and logs the mismatch. The caller
// closes the conn (RemoveClient + return, ban-path parity).
func sendGVerReject(conn *websocket.Conn, clientGVer string) {
	msg := gverRejectMessage(clientGVer)
	log.Printf("gver: reject gVer=%q (want %q) buildID=%s", clientGVer, version.GVer, version.BuildID)
	_ = gnet.SendDirect(conn, pktOp(PacketNotification, NotificationText, notificationPacketData{
		Message: msg,
	}))
}
