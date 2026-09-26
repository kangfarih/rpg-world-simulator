package console

import (
	"strings"
	"testing"
)

// fakeHubHandler records calls and returns canned output text.
type fakeHubHandler struct {
	calls []string
}

func (f *fakeHubHandler) record(call string) string {
	f.calls = append(f.calls, call)
	return call
}

func (f *fakeHubHandler) Server() string { return f.record("server") }
func (f *fakeHubHandler) Player(u string) string {
	return f.record("player " + u)
}

func TestHubExecDispatch(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{"/server", "server"},
		{"/player alice", "player alice"},
		{"/player Alice Smith", "player Alice Smith"},
		{"/SERVER", "server"},
		{"  /player bob  ", "player bob"},
	}
	for _, c := range cases {
		f := &fakeHubHandler{}
		got := HubExec(f, c.line)
		if got != c.want {
			t.Errorf("HubExec(%q) = %q, want %q", c.line, got, c.want)
		}
		if len(f.calls) != 1 || f.calls[0] != c.want {
			t.Errorf("HubExec(%q) calls = %v, want [%s]", c.line, f.calls, c.want)
		}
	}
}

// Bare /player returns the exact TS warning text (console.ts
// log.warning('Malformed command - Format: /player [username]')) and never
// reaches the handler.
func TestHubExecPlayerMalformed(t *testing.T) {
	for _, line := range []string{"/player", "/player   ", "/PLAYER"} {
		f := &fakeHubHandler{}
		got := HubExec(f, line)
		if got != "Malformed command - Format: /player [username]" {
			t.Errorf("HubExec(%q) = %q, want exact TS malformed text", line, got)
		}
		if len(f.calls) != 0 {
			t.Errorf("HubExec(%q) calls = %v, want no handler calls", line, f.calls)
		}
	}
}

func TestHubExecUnknown(t *testing.T) {
	f := &fakeHubHandler{}
	got := HubExec(f, "/players")
	if !strings.Contains(got, "Unknown command") || !strings.Contains(got, "players") {
		t.Fatalf("HubExec unknown = %q, want text containing 'Unknown command' and 'players'", got)
	}
	if len(f.calls) != 0 {
		t.Fatalf("HubExec unknown calls = %v, want no handler calls", f.calls)
	}
}

func TestHubExecNonSlash(t *testing.T) {
	f := &fakeHubHandler{}
	for _, line := range []string{"", "hello", "server", "/"} {
		if got := HubExec(f, line); got != "" {
			t.Errorf("HubExec(%q) = %q, want empty (ignored input)", line, got)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("HubExec non-slash calls = %v, want no handler calls", f.calls)
	}
}
