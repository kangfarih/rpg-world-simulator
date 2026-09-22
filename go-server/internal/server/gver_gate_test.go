package server

import (
	"encoding/json"
	"strings"
	"testing"

	"rpg-world-server/internal/version"
)

func frameOf(t *testing.T, elems ...any) clientFrame {
	t.Helper()
	raw, err := json.Marshal(elems)
	if err != nil {
		t.Fatal(err)
	}
	var f clientFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// Accept/reject matrix for the Handshake{gVer} gate.
func TestGVerGateMatrix(t *testing.T) {
	t.Setenv(version.EnvStrict, "")
	cases := []struct {
		name  string
		frame []any
		want  bool
		gv    string
	}{
		{"stock client", []any{1, map[string]any{"gVer": "0.5.5-beta"}}, true, "0.5.5-beta"},
		{"wrong version", []any{1, map[string]any{"gVer": "9.9.9"}}, false, "9.9.9"},
		{"missing gVer", []any{1, map[string]any{"type": "client"}}, false, ""},
		{"legacy numeric", []any{1, map[string]any{"gVer": 1}}, false, ""},
		{"hub-style 3-elem", []any{1, nil, map[string]any{"type": "hub", "gVer": "0.5.5-beta"}}, true, "0.5.5-beta"},
		{"empty frame", []any{}, false, ""},
	}
	for _, tc := range cases {
		gv, ok := gverGatePass(frameOf(t, tc.frame...))
		if ok != tc.want || gv != tc.gv {
			t.Errorf("%s: gverGatePass = (%q, %v), want (%q, %v)",
				tc.name, gv, ok, tc.gv, tc.want)
		}
	}
}

// GVER_STRICT=0 accepts everything (dev escape hatch).
func TestGVerGateLax(t *testing.T) {
	t.Setenv(version.EnvStrict, "0")
	gv, ok := gverGatePass(frameOf(t, 1, map[string]any{"gVer": "bogus"}))
	if !ok || gv != "bogus" {
		t.Fatalf("lax gate = (%q, %v), want (bogus, true)", gv, ok)
	}
}

// The reject payload names the server build + redirect on the existing
// Notification-Text shape (no new opcode).
func TestGVerRejectMessage(t *testing.T) {
	t.Setenv("HUB_ADDR", "ws://127.0.0.1:9101/")
	msg := gverRejectMessage("9.9.9")
	for _, want := range []string{version.GVer, version.BuildID, "9.9.9", "ws://127.0.0.1:9101/"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("reject %q missing %q", msg, want)
		}
	}
	t.Setenv("HUB_ADDR", "")
	if strings.Contains(gverRejectMessage("x"), "reconnect via") {
		t.Fatal("all-in-one reject must not name a hub")
	}
}
