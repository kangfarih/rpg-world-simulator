package app

import (
	"testing"
	"time"

	"rpg-world-server/internal/version"
)

func TestParseRole(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{"default", nil, nil, RoleAllInOne},
		{"env router", map[string]string{EnvRole: "router"}, nil, RoleRouter},
		{"env shard", map[string]string{EnvRole: "shard"}, nil, RoleShard},
		{"env case", map[string]string{EnvRole: "Router"}, nil, RoleRouter},
		{"flag wins over env", map[string]string{EnvRole: "router"}, []string{"--role=shard"}, RoleShard},
		{"flag separate arg", nil, []string{"--role", "router"}, RoleRouter},
		{"unknown falls back", map[string]string{EnvRole: "bogus"}, nil, RoleAllInOne},
		{"unknown flag falls back", nil, []string{"--role=bogus"}, RoleAllInOne},
		{"other flags ignored", nil, []string{"--clean"}, RoleAllInOne},
	}
	for _, tc := range cases {
		if got := ParseRole(getenvOf(tc.env), tc.args); got != tc.want {
			t.Errorf("%s: ParseRole = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestLifecycleTransitions(t *testing.T) {
	l := &Lifecycle{}
	if got := l.State(); got != version.StateRunning {
		t.Fatalf("initial = %q, want RUNNING", got)
	}
	if !l.BeginDraining() {
		t.Fatal("first BeginDraining must flip")
	}
	if l.BeginDraining() {
		t.Fatal("second BeginDraining must be a no-op")
	}
	if got := l.State(); got != version.StateDraining {
		t.Fatalf("after drain = %q, want DRAINING", got)
	}
	l.SetLoad(7)
	if got := l.Health(); got.Load != 7 || got.State != version.StateDraining {
		t.Fatalf("health = %+v", got)
	}
	if l.Health().BuildID == "" || l.Health().GVer == "" {
		t.Fatalf("health must carry stamps: %+v", l.Health())
	}
	l.MarkShutdown()
	if got := l.State(); got != version.StateShutdown {
		t.Fatalf("after shutdown = %q, want SHUTDOWN", got)
	}
}

func TestWaitEmptyOrTimeout(t *testing.T) {
	l := &Lifecycle{}
	if !l.WaitEmptyOrTimeout(func() bool { return true }, time.Minute) {
		t.Fatal("already-empty must return true")
	}
	if l.WaitEmptyOrTimeout(func() bool { return false }, 300*time.Millisecond) {
		t.Fatal("never-empty must time out false")
	}
	n := 0
	empty := func() bool { n++; return n >= 3 }
	if !l.WaitEmptyOrTimeout(empty, 10*time.Second) {
		t.Fatal("empty-on-third-poll must return true")
	}
}
