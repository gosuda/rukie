package supervisor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func runSupervised(t *testing.T, proc *fakeProc) *Supervisor {
	t.Helper()
	s, err := New(Config{
		Binary:       "ruk",
		DaemonConfig: "/tmp/ruk.toml",
		Clock:        newFakeClock(),
		Starter: starterFunc(func(string, string, []string, io.Writer, io.Writer) (Proc, error) {
			return proc, nil
		}),
		Probe: okProbe,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error("supervised Run after cancel:", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("supervisor did not stop")
		}
	})
	waitFor(t, "readiness", s.Ready)
	return s
}

func TestReadyzFalseWhileChildDown(t *testing.T) {
	first := blockingProc()
	var mu sync.Mutex
	secondGate := make(chan struct{})
	calls := 0
	s, err := New(Config{
		Binary:       "ruk",
		DaemonConfig: "/tmp/ruk.toml",
		Clock:        newFakeClock(),
		Starter: starterFunc(func(string, string, []string, io.Writer, io.Writer) (Proc, error) {
			mu.Lock()
			calls++
			n := calls
			mu.Unlock()
			if n == 1 {
				return first, nil
			}
			<-secondGate
			return blockingProc(), nil
		}),
		Probe: okProbe,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if s.Ready() {
		t.Fatal("Ready is true before the child starts")
	}
	if ready, _ := s.DaemonProbe(); ready {
		t.Fatal("DaemonProbe is true before the child starts")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	waitFor(t, "first readiness", s.Ready)
	if ready, reason := s.DaemonProbe(); !ready || reason != "" {
		t.Fatalf("DaemonProbe = (%v, %q), want (true, \"\")", ready, reason)
	}
	// Crash the child; the restart must wait behind the gate.
	first.waitGate <- context.DeadlineExceeded
	waitFor(t, "unreadiness during restart", func() bool { return !s.Ready() })
	if ready, reason := s.DaemonProbe(); ready || reason == "" {
		t.Fatalf("DaemonProbe = (%v, %q) while down, want (false, non-empty)", ready, reason)
	}
	close(secondGate)
	waitFor(t, "second readiness", s.Ready)
	if got := s.Restarts(); got != 1 {
		t.Fatalf("restarts = %d, want 1", got)
	}
}

func TestDaemonCallsFailFast503(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteUnavailable(rec)
	res := rec.Result()
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", res.StatusCode)
	}
	if got := res.Header.Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After = %q, want %q", got, "5")
	}
	if got := res.Header.Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json", got)
	}

	down, err := New(Config{ExternalSocket: "/run/ruk.sock", Clock: newFakeClock(), Probe: okProbe})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := down.Gate(); err == nil {
		t.Fatal("Gate on a down daemon returned nil")
	}
	called := false
	handler := down.FailFast(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		called = true
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))
	if called {
		t.Fatal("fail-fast handler reached the backend while down")
	}

	up := runSupervised(t, blockingProc())
	if err := up.Gate(); err != nil {
		t.Fatalf("Gate on a live daemon returned %v", err)
	}
	called = false
	up.FailFast(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))
	if !called {
		t.Fatal("fail-fast handler did not reach the backend while up")
	}
}

func TestDaemonProbeContract(t *testing.T) {
	s := runSupervised(t, blockingProc())
	if ready, reason := s.DaemonProbe(); !ready || reason != "" {
		t.Fatalf("DaemonProbe = (%v, %q), want (true, \"\")", ready, reason)
	}
}
