package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gosuda/rukie/internal/api"
)

func TestReadyzDaemonDownIs503(t *testing.T) {
	handler := api.Readiness{Daemon: func() (bool, string) {
		return false, "ruk serve is down"
	}}.Handler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	res := rec.Result()
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readyz with a down daemon = %d, want 503", res.StatusCode)
	}
	var body struct {
		Daemon struct {
			Reason string `json:"reason"`
			Ready  bool   `json:"ready"`
		} `json:"daemon"`
		Ready bool `json:"ready"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode readyz body: %v", err)
	}
	if body.Ready {
		t.Fatal("readyz body claims ready with a down daemon")
	}
	if body.Daemon.Ready || body.Daemon.Reason == "" {
		t.Fatalf("daemon check = %+v, want down with a reason", body.Daemon)
	}
}

func TestReadyzDaemonUpWithStoreDownIs503(t *testing.T) {
	handler := api.Readiness{Daemon: func() (bool, string) {
		return true, ""
	}}.Handler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	res := rec.Result()
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readyz with no store = %d, want 503", res.StatusCode)
	}
}
