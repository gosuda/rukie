package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/supervisor"
)

// TestDaemonCallsFailFast503 guards the production git gate: daemon-backed
// git routes answer 503 through sup.FailFast while the child is down, and
// pass through once it serves. Deleting the FailFast mount in setupApp
// lets the backend run while down and fails this test. Non-git routes
// (/healthz, /readyz, pages) sit behind the fallback and are never gated.
func TestDaemonCallsFailFast503(t *testing.T) {
	newDown := func(t *testing.T) *supervisor.Supervisor {
		t.Helper()
		sup, err := supervisor.New(supervisor.Config{
			ExternalSocket: "/run/ruk.sock",
			Probe:          func(context.Context) error { return context.DeadlineExceeded },
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := sup.Gate(); err == nil {
			t.Fatal("Gate on a down daemon returned nil")
		}
		return sup
	}

	backendCalled := false
	fallbackCalled := false
	build := func(sup *supervisor.Supervisor) gitFallback {
		backendCalled = false
		fallbackCalled = false
		backend := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			backendCalled = true
			w.WriteHeader(http.StatusOK)
		})
		fallback := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fallbackCalled = true
			w.WriteHeader(http.StatusOK)
		})
		return gitFallback{git: sup.FailFast(backend), fallback: fallback}
	}

	down := build(newDown(t))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/ada/hello.git/git-receive-pack", nil)
	down.ServeHTTP(rec, req)
	res := rec.Result()
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("down git status = %d, want 503", res.StatusCode)
	}
	if got := res.Header.Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After = %q, want 5", got)
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read 503 body: %v", err)
	}
	var decoded map[string]any
	if err := jsonv2.Unmarshal(raw, &decoded, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("decode 503 body: %v", err)
	}
	if decoded["type"] != "/-/problems/unavailable" {
		t.Fatalf("type = %v, want the catalogue unavailable problem", decoded["type"])
	}
	if backendCalled {
		t.Fatal("backend ran while the daemon was down; the FailFast mount is missing")
	}
	if fallbackCalled {
		t.Fatal("fallback ran for a git path while down")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	down.ServeHTTP(rec, req)
	if res := rec.Result(); res.StatusCode != http.StatusOK {
		_ = res.Body.Close()
		t.Fatalf("non-git status while down = %d, want 200: liveness must not sit behind the gate", res.StatusCode)
	} else {
		_ = res.Body.Close()
	}
	if !fallbackCalled {
		t.Fatal("fallback did not run for a non-git path while down")
	}

	upSup, err := supervisor.New(supervisor.Config{
		ExternalSocket: "/run/ruk.sock",
		Probe:          func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- upSup.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for !upSup.Ready() {
		if time.Now().After(deadline) {
			t.Fatal("supervisor never became ready")
		}
		time.Sleep(time.Millisecond)
	}
	up := build(upSup)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/ada/hello.git/git-receive-pack", nil)
	up.ServeHTTP(rec, req)
	if res := rec.Result(); res.StatusCode != http.StatusOK {
		_ = res.Body.Close()
		t.Fatalf("up git status = %d, want 200", res.StatusCode)
	} else {
		_ = res.Body.Close()
	}
	if !backendCalled {
		t.Fatal("backend did not run while the daemon was up")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cancelled Run returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not stop after cancel")
	}
}
