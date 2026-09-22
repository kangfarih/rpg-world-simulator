package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type stubHealth struct{ h Health }

func (s stubHealth) Health() Health { return s.h }

// /healthz reports the lifecycle snapshot (200 RUNNING, 503 DRAINING) and
// stays up when no provider is wired (static RUNNING stub).
func TestHealthz(t *testing.T) {
	srv := newTestServer()
	srv.Health = stubHealth{h: Health{State: "RUNNING", Load: 3, BuildID: "abc", GVer: "0.5.5-beta"}}
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health code = %d, want 200", rec.Code)
	}
	var got Health
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.State != "RUNNING" || got.Load != 3 || got.BuildID != "abc" || got.GVer != "0.5.5-beta" {
		t.Fatalf("health = %+v", got)
	}

	srv.Health = stubHealth{h: Health{State: "DRAINING", Load: 1}}
	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("draining code = %d, want 503", rec.Code)
	}

	srv.Health = nil
	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("nil-provider code = %d, want 200", rec.Code)
	}
}
