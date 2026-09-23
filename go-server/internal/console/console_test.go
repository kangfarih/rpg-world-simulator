package console

import (
	"strings"
	"testing"
)

// fakeHandler records calls and returns canned output text.
type fakeHandler struct {
	calls []string
}

func (f *fakeHandler) record(call string) string {
	f.calls = append(f.calls, call)
	return call
}

func (f *fakeHandler) Players() string { return f.record("players") }
func (f *fakeHandler) Total() string   { return f.record("total") }
func (f *fakeHandler) Update() string  { return f.record("update") }
func (f *fakeHandler) Kill(u string) string {
	return f.record("kill " + u)
}
func (f *fakeHandler) Kick(u string) string {
	return f.record("kick " + u)
}
func (f *fakeHandler) Timeout(u string) string {
	return f.record("timeout " + u)
}
func (f *fakeHandler) SetAdmin(u string) string {
	return f.record("setadmin " + u)
}
func (f *fakeHandler) SetMod(u string) string {
	return f.record("setmod " + u)
}
func (f *fakeHandler) RemoveAdmin(u string) string {
	return f.record("removeadmin " + u)
}
func (f *fakeHandler) RemoveMod(u string) string {
	return f.record("removemod " + u)
}
func (f *fakeHandler) IPBan(ip string) string {
	return f.record("ipban " + ip)
}
func (f *fakeHandler) UnbanIP(ip string) string {
	return f.record("unbanip " + ip)
}
func (f *fakeHandler) Save() string { return f.record("save") }

func TestParse(t *testing.T) {
	cases := []struct {
		line string
		name string
		args []string
	}{
		{"/players", "players", nil},
		{"/total", "total", nil},
		{"/update", "update", nil},
		{"/save", "save", nil},
		{"/kill Bob", "kill", []string{"Bob"}},
		{"/kill Bob Smith", "kill", []string{"Bob", "Smith"}},
		{"/kick alice", "kick", []string{"alice"}},
		{"/timeout alice", "timeout", []string{"alice"}},
		{"/setadmin alice", "setadmin", []string{"alice"}},
		{"/setmod alice", "setmod", []string{"alice"}},
		{"/removeadmin alice", "removeadmin", []string{"alice"}},
		{"/removemod alice", "removemod", []string{"alice"}},
		{"/unbanip 1.2.3.4", "unbanip", []string{"1.2.3.4"}},
		{"/UNBANIP 1.2.3.4", "unbanip", []string{"1.2.3.4"}},
		{"/ipban 1.2.3.4", "ipban", []string{"1.2.3.4"}},
		{"/IPBAN 1.2.3.4", "ipban", []string{"1.2.3.4"}},
		{"/PLAYERS", "players", nil},
		{"  /total  ", "total", nil},
		{"/save\r\n", "save", nil},
		{"", "", nil},
		{"   ", "", nil},
		{"/", "", nil},
		{"/   ", "", nil},
		{"hello", "", nil},
		{"players", "", nil},
	}
	for _, c := range cases {
		name, args := Parse(c.line)
		if name != c.name {
			t.Errorf("Parse(%q) name = %q, want %q", c.line, name, c.name)
			continue
		}
		if len(args) != len(c.args) {
			t.Errorf("Parse(%q) args = %v, want %v", c.line, args, c.args)
			continue
		}
		for i := range args {
			if args[i] != c.args[i] {
				t.Errorf("Parse(%q) args = %v, want %v", c.line, args, c.args)
				break
			}
		}
	}
}

func TestExecDispatch(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{"/players", "players"},
		{"/total", "total"},
		{"/update", "update"},
		{"/save", "save"},
		{"/kill Bob", "kill Bob"},
		{"/kill Bob Smith", "kill Bob Smith"},
		{"/kick alice", "kick alice"},
		{"/timeout alice", "timeout alice"},
		{"/setadmin alice", "setadmin alice"},
		{"/setmod alice", "setmod alice"},
		{"/removeadmin alice", "removeadmin alice"},
		{"/removeadmin Alice Smith", "removeadmin Alice Smith"},
		{"/removemod alice", "removemod alice"},
		{"/ipban 1.2.3.4", "ipban 1.2.3.4"},
		{"/unbanip 1.2.3.4", "unbanip 1.2.3.4"},
	}
	for _, c := range cases {
		f := &fakeHandler{}
		got := Exec(f, c.line)
		if got != c.want {
			t.Errorf("Exec(%q) = %q, want %q", c.line, got, c.want)
		}
		if len(f.calls) != 1 || f.calls[0] != c.want {
			t.Errorf("Exec(%q) calls = %v, want [%s]", c.line, f.calls, c.want)
		}
	}
}

func TestExecUnknown(t *testing.T) {
	f := &fakeHandler{}
	got := Exec(f, "/frobnicate arg")
	if !strings.Contains(got, "Unknown command") || !strings.Contains(got, "frobnicate") {
		t.Fatalf("Exec unknown = %q, want text containing 'Unknown command' and 'frobnicate'", got)
	}
	if len(f.calls) != 0 {
		t.Fatalf("Exec unknown calls = %v, want no handler calls", f.calls)
	}
}

func TestExecMissingArg(t *testing.T) {
	for _, line := range []string{"/kill", "/kick", "/timeout", "/setadmin", "/setmod",
		"/removeadmin", "/removemod", "/ipban", "/unbanip"} {
		f := &fakeHandler{}
		got := Exec(f, line)
		if got == "" {
			t.Errorf("Exec(%q) = empty, want usage error text", line)
		}
		if len(f.calls) != 0 {
			t.Errorf("Exec(%q) calls = %v, want no handler calls", line, f.calls)
		}
	}
}

func TestExecNonSlash(t *testing.T) {
	f := &fakeHandler{}
	for _, line := range []string{"", "hello", "players"} {
		if got := Exec(f, line); got != "" {
			t.Errorf("Exec(%q) = %q, want empty (ignored input)", line, got)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("Exec non-slash calls = %v, want no handler calls", f.calls)
	}
}
