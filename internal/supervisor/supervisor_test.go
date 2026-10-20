package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	now    time.Time
	sleeps []time.Duration
	mu     sync.Mutex
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1_700_000_000, 0)}
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sleeps = append(f.sleeps, d)
	f.now = f.now.Add(d)
	return nil
}

func (f *fakeClock) slept() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.sleeps...)
}

type fakeProc struct {
	waitErr  error
	waitGate chan error
	waited   chan struct{}
}

func crashingProc(err error) *fakeProc {
	return &fakeProc{waitErr: err, waited: make(chan struct{})}
}

func blockingProc() *fakeProc {
	return &fakeProc{waitGate: make(chan error, 1), waited: make(chan struct{})}
}

func (p *fakeProc) Wait() error {
	close(p.waited)
	if p.waitGate != nil {
		return <-p.waitGate
	}
	return p.waitErr
}

func (p *fakeProc) Signal(sig os.Signal) error {
	if p.waitGate != nil {
		select {
		case p.waitGate <- fmt.Errorf("signaled %v", sig):
		default:
		}
	}
	return nil
}

func (p *fakeProc) Kill() error {
	if p.waitGate != nil {
		select {
		case p.waitGate <- fmt.Errorf("killed"):
		default:
		}
	}
	return nil
}

func okProbe(context.Context) error { return nil }

type starterFunc func(bin, configPath string, env []string, stdout, stderr io.Writer) (Proc, error)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSupervisorRestartCap(t *testing.T) {
	clock := newFakeClock()
	var mu sync.Mutex
	var starts int
	s, err := New(Config{
		Binary:       "ruk",
		DaemonConfig: "/tmp/ruk.toml",
		Clock:        clock,
		Starter: starterFunc(func(string, string, []string, io.Writer, io.Writer) (Proc, error) {
			mu.Lock()
			starts++
			mu.Unlock()
			return crashingProc(fmt.Errorf("boom")), nil
		}),
		Probe: okProbe,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Run(context.Background()); err == nil {
		t.Fatal("Run with a perpetually crashing child returned nil, want budget-exhausted error")
	}
	if starts != 4 {
		t.Fatalf("starts = %d, want 4 (1 initial + 3 restarts)", starts)
	}
	if got := s.Restarts(); got != 3 {
		t.Fatalf("restarts = %d, want 3", got)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	if got := clock.slept(); !equalDurations(got, want) {
		t.Fatalf("backoffs = %v, want %v", got, want)
	}
	if s.Ready() {
		t.Fatal("Ready is true after the budget is exhausted")
	}
}

func TestSupervisorRestartCapBoundary(t *testing.T) {
	for _, tc := range []struct {
		delays []time.Duration
		max    int
		starts int
	}{
		{max: 1, starts: 2, delays: []time.Duration{time.Second}},
		{max: 2, starts: 3, delays: []time.Duration{time.Second, 2 * time.Second}},
		{max: 4, starts: 5, delays: []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second}},
	} {
		clock := newFakeClock()
		var starts int
		s, err := New(Config{
			Binary:       "ruk",
			DaemonConfig: "/tmp/ruk.toml",
			MaxRestarts:  tc.max,
			Clock:        clock,
			Starter: starterFunc(func(string, string, []string, io.Writer, io.Writer) (Proc, error) {
				starts++
				return crashingProc(errors.New("boom")), nil
			}),
			Probe: okProbe,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := s.Run(context.Background()); err == nil {
			t.Fatalf("max=%d: Run returned nil", tc.max)
		}
		if starts != tc.starts {
			t.Fatalf("max=%d: starts = %d, want %d", tc.max, starts, tc.starts)
		}
		if got := clock.slept(); !equalDurations(got, tc.delays) {
			t.Fatalf("max=%d: backoffs = %v, want %v", tc.max, got, tc.delays)
		}
	}
}

func TestSupervisorCleanCancel(t *testing.T) {
	proc := blockingProc()
	clock := newFakeClock()
	s, err := New(Config{
		Binary:       "ruk",
		DaemonConfig: "/tmp/ruk.toml",
		Clock:        clock,
		Starter: starterFunc(func(string, string, []string, io.Writer, io.Writer) (Proc, error) {
			return proc, nil
		}),
		Probe: okProbe,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	waitFor(t, "readiness", s.Ready)
	if got := s.Starts(); got != 1 {
		t.Fatalf("starts = %d, want 1", got)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cancelled Run returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if s.Ready() {
		t.Fatal("Ready is true after shutdown")
	}
	if got := s.Restarts(); got != 0 {
		t.Fatalf("restarts = %d, want 0", got)
	}
}

func TestSupervisorStartFailureDoesNotRestart(t *testing.T) {
	var starts int
	s, err := New(Config{
		Binary:       "ruk",
		DaemonConfig: "/tmp/ruk.toml",
		Clock:        newFakeClock(),
		Starter: starterFunc(func(string, string, []string, io.Writer, io.Writer) (Proc, error) {
			starts++
			return nil, errors.New("exec missing")
		}),
		Probe: okProbe,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Run(context.Background()); err == nil {
		t.Fatal("Run with a failing starter returned nil")
	}
	if starts != 1 {
		t.Fatalf("starts = %d, want 1: a start failure must not consume the restart budget", starts)
	}
}

func TestSupervisorStartupProbeTimeout(t *testing.T) {
	clock := newFakeClock()
	s, err := New(Config{
		Binary:       "ruk",
		DaemonConfig: "/tmp/ruk.toml",
		Clock:        clock,
		Starter: starterFunc(func(string, string, []string, io.Writer, io.Writer) (Proc, error) {
			return blockingProc(), nil
		}),
		Probe: func(context.Context) error { return errors.New("connection refused") },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Run(context.Background()); err == nil {
		t.Fatal("Run with a silent socket returned nil, want startup-timeout error")
	}
	if s.Ready() {
		t.Fatal("Ready is true after a startup timeout")
	}
}

func TestSupervisorExternalSocketStartsNoChild(t *testing.T) {
	s, err := New(Config{
		ExternalSocket: "/run/ruk.sock",
		Clock:          newFakeClock(),
		Starter: starterFunc(func(string, string, []string, io.Writer, io.Writer) (Proc, error) {
			t.Error("starter ran in external mode; no child may start")
			return crashingProc(errors.New("must not start")), nil
		}),
		Probe: okProbe,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	waitFor(t, "external readiness", s.Ready)
	if got := s.Starts(); got != 0 {
		t.Fatalf("starts = %d, want 0 in external mode", got)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cancelled external Run returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("external Run did not return after cancel")
	}
}

func TestSupervisorRequiresBinaryOrExternal(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New without binary or external socket returned nil, want a config error")
	}
	if _, err := New(Config{Binary: "ruk"}); err == nil {
		t.Fatal("New without daemon config returned nil, want a config error")
	}
}

func equalDurations(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSupervisorStartupDeathSpendsBudget(t *testing.T) {
	clock := newFakeClock()
	var mu sync.Mutex
	var starts int
	var first *fakeProc
	s, err := New(Config{
		Binary:       "ruk",
		DaemonConfig: "/tmp/ruk.toml",
		Clock:        clock,
		Starter: starterFunc(func(string, string, []string, io.Writer, io.Writer) (Proc, error) {
			mu.Lock()
			defer mu.Unlock()
			starts++
			if starts == 1 {
				first = blockingProc()
				return first, nil
			}
			return crashingProc(fmt.Errorf("boot boom")), nil
		}),
		Probe: func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			if starts == 1 {
				return nil
			}
			return fmt.Errorf("connection refused")
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background()) }()
	waitFor(t, "first readiness", s.Ready)
	mu.Lock()
	proc := first
	mu.Unlock()
	proc.waitGate <- fmt.Errorf("segfault")
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run returned nil after repeated boot deaths, want budget exhaustion")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the restart budget was spent")
	}
	mu.Lock()
	n := starts
	mu.Unlock()
	if n != 4 {
		t.Fatalf("starts = %d, want 4 (1 initial + 3 restarts): a boot death must spend budget, not end the run", n)
	}
	if got := s.Restarts(); got != 3 {
		t.Fatalf("restarts = %d, want 3", got)
	}
	if s.Ready() {
		t.Fatal("Ready is true after the budget is exhausted")
	}
}

func TestSupervisorResetsBudgetAfterStableReady(t *testing.T) {
	clock := newFakeClock()
	var mu sync.Mutex
	var procs []*fakeProc
	s, err := New(Config{
		Binary:       "ruk",
		DaemonConfig: "/tmp/ruk.toml",
		Clock:        clock,
		Starter: starterFunc(func(string, string, []string, io.Writer, io.Writer) (Proc, error) {
			mu.Lock()
			defer mu.Unlock()
			proc := blockingProc()
			procs = append(procs, proc)
			return proc, nil
		}),
		Probe: okProbe,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("supervisor did not stop after cancel")
		}
	})
	for crash := range 4 {
		waitFor(t, "readiness", s.Ready)
		mu.Lock()
		have := len(procs)
		mu.Unlock()
		if serr := clock.Sleep(context.Background(), 61*time.Second); serr != nil {
			t.Fatalf("advance past the stable window: %v", serr)
		}
		mu.Lock()
		victim := procs[len(procs)-1]
		mu.Unlock()
		victim.waitGate <- fmt.Errorf("crash %d", crash)
		waitFor(t, "restart", func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(procs) == have+1
		})
	}
	waitFor(t, "readiness after the fourth crash", s.Ready)
	mu.Lock()
	n := len(procs)
	mu.Unlock()
	if n != 5 {
		t.Fatalf("starts = %d, want 5: stable service must reset the consecutive counter", n)
	}
	if got := s.Restarts(); got != 1 {
		t.Fatalf("restarts = %d, want 1: every crash followed 61s of serving", got)
	}
}

func TestSupervisorExternalSocketReprobes(t *testing.T) {
	var mu sync.Mutex
	healthy := true
	s, err := New(Config{
		ExternalSocket: "/run/ruk.sock",
		Clock:          newFakeClock(),
		Probe: func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			if healthy {
				return nil
			}
			return fmt.Errorf("dial refused")
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("external Run did not return after cancel")
		}
	})
	waitFor(t, "external readiness", s.Ready)
	mu.Lock()
	healthy = false
	mu.Unlock()
	waitFor(t, "unreadiness after the external daemon dies", func() bool { return !s.Ready() })
	if ready, reason := s.DaemonProbe(); ready || reason == "" {
		t.Fatalf("DaemonProbe = (%v, %q) while the external daemon is down, want (false, non-empty)", ready, reason)
	}
	mu.Lock()
	healthy = true
	mu.Unlock()
	waitFor(t, "re-readiness after the external daemon recovers", s.Ready)
	if got := s.Starts(); got != 0 {
		t.Fatalf("starts = %d, want 0 in external mode", got)
	}
}
