package version

import (
	"encoding/json"
	"testing"
	"time"
)

func getenvOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// The stock client sends config.version (GVER='0.5.5-beta' in .env.defaults)
// as Handshake{gVer}; the gate must accept exactly that by default.
func TestGateAcceptsStockClient(t *testing.T) {
	if GVer != "0.5.5-beta" {
		t.Fatalf("GVer = %q, want stock client gVer 0.5.5-beta", GVer)
	}
	if !PassForEnv("0.5.5-beta", true) {
		t.Fatal("strict gate must accept the stock client gVer")
	}
}

func TestGateRejectMatrix(t *testing.T) {
	cases := []struct {
		name   string
		client string
		strict bool
		want   bool
	}{
		{"exact strict", "0.5.5-beta", true, true},
		{"wrong version strict", "0.5.6-beta", true, false},
		{"empty strict", "", true, false},
		{"legacy numeric string strict", "1", true, false},
		{"wrong version lax", "0.5.6-beta", false, true},
		{"empty lax", "", false, true},
	}
	for _, tc := range cases {
		if got := PassForEnv(tc.client, tc.strict); got != tc.want {
			t.Errorf("%s: PassForEnv(%q, %v) = %v, want %v",
				tc.name, tc.client, tc.strict, got, tc.want)
		}
	}
}

func TestExtractGVer(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"stock string", `{"gVer":"0.5.5-beta"}`, "0.5.5-beta"},
		{"legacy numeric", `{"gVer":1}`, ""},
		{"missing", `{"type":"client"}`, ""},
		{"empty", ``, ""},
		{"malformed", `not-json`, ""},
		{"hub typed", `{"type":"hub","name":"s1","gVer":"0.5.5-beta"}`, "0.5.5-beta"},
	}
	for _, tc := range cases {
		if got := ExtractGVer(json.RawMessage(tc.raw)); got != tc.want {
			t.Errorf("%s: ExtractGVer = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestStrictEnv(t *testing.T) {
	if !StrictFromEnv(getenvOf(nil)) {
		t.Fatal("unset GVER_STRICT must enforce the gate")
	}
	for _, off := range []string{"0", "false", "off", "no", "OFF"} {
		if StrictFromEnv(getenvOf(map[string]string{EnvStrict: off})) {
			t.Fatalf("GVER_STRICT=%q must disable the gate", off)
		}
	}
	if !StrictFromEnv(getenvOf(map[string]string{EnvStrict: "1"})) {
		t.Fatal("GVER_STRICT=1 must enforce the gate")
	}
}

func TestDrainTimeoutEnv(t *testing.T) {
	if got := DrainTimeoutFromEnv(getenvOf(nil)); got != 30*time.Minute {
		t.Fatalf("default = %v, want 30m", got)
	}
	if got := DrainTimeoutFromEnv(getenvOf(map[string]string{EnvDrainTimeout: "5m"})); got != 5*time.Minute {
		t.Fatalf("5m = %v", got)
	}
	if got := DrainTimeoutFromEnv(getenvOf(map[string]string{EnvDrainTimeout: "60"})); got != 60*time.Second {
		t.Fatalf("bare 60 = %v, want 60s", got)
	}
	if got := DrainTimeoutFromEnv(getenvOf(map[string]string{EnvDrainTimeout: "bogus"})); got != 30*time.Minute {
		t.Fatalf("bogus = %v, want default 30m", got)
	}
}
