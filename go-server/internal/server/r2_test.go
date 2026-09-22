package server

import (
	"testing"
)

// Banner decision matrix: banner only when the hub knows a preferred version
// that is neither ours nor already announced to this session.
func TestShouldBanner(t *testing.T) {
	cases := []struct {
		name      string
		own       string
		preferred string
		announced string
		want      bool
	}{
		{"behind, never announced", "v1", "v2", "", true},
		{"behind, announced older", "v1", "v3", "v2", true},
		{"already announced", "v1", "v2", "v2", false},
		{"up to date", "v2", "v2", "", false},
		{"up to date announced", "v2", "v2", "v2", false},
		{"preferred unknown", "v1", "", "", false},
		{"own unknown", "", "v2", "", false},
	}
	for _, tc := range cases {
		if got := shouldBanner(tc.own, tc.preferred, tc.announced); got != tc.want {
			t.Errorf("%s: shouldBanner(%q,%q,%q) = %v, want %v",
				tc.name, tc.own, tc.preferred, tc.announced, got, tc.want)
		}
	}
}

// Once-per-session-per-version: announcing records the version (no re-send),
// a newer preferred version re-arms, and disconnect forgets the session.
func TestBannerSessionRecord(t *testing.T) {
	bannerMu.Lock()
	bannerSent["r2probe"] = "v2"
	bannerMu.Unlock()
	defer bannerForget("r2probe")

	bannerMu.Lock()
	announced := bannerSent["r2probe"]
	bannerMu.Unlock()
	if shouldBanner("v1", "v2", announced) {
		t.Fatal("must not re-banner the announced version")
	}
	if !shouldBanner("v1", "v3", announced) {
		t.Fatal("a newer preferred version must re-arm the banner")
	}
	bannerForget("r2probe")
	bannerMu.Lock()
	_, still := bannerSent["r2probe"]
	bannerMu.Unlock()
	if still {
		t.Fatal("bannerForget must drop the session record")
	}
}

// Own version honors the explicit VERSION tag, else derives the pair.
func TestOwnVersion(t *testing.T) {
	t.Setenv("VERSION", "live-tag")
	if got := ownVersion(); got != "live-tag" {
		t.Fatalf("ownVersion with tag = %q, want live-tag", got)
	}
}
