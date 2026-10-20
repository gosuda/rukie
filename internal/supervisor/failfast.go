package supervisor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
)

// RetryAfterSeconds is the fail-fast retry hint daemon-backed requests
// carry while the child is down.
const RetryAfterSeconds = 5

// ErrDaemonUnavailable reports a daemon-backed request that cannot run
// because the child is down.
var ErrDaemonUnavailable = errors.New("supervisor: ruk daemon is unavailable")

// Gate returns nil when the daemon is serving and ErrDaemonUnavailable
// otherwise. Callers fail fast instead of queueing behind a dead child.
func (s *Supervisor) Gate() error {
	if s.ready.Load() {
		return nil
	}
	return ErrDaemonUnavailable
}

// WriteUnavailable answers one request with 503 and Retry-After: 5 while
// the daemon is down. The body is a minimal RFC 9457 problem.
func WriteUnavailable(w http.ResponseWriter) {
	body := `{"type":"https://rukie/problems/backend-unavailable",` +
		`"title":"backend unavailable",` +
		`"status":503,` +
		`"detail":"the ruk daemon is down; retry the request shortly"}`
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Retry-After", strconv.Itoa(RetryAfterSeconds))
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(body))
}

// FailFast wraps next so daemon-backed routes answer 503 with
// Retry-After: 5 while the child is down instead of hanging or
// queueing. Readiness and liveness routes must not sit behind it.
func (s *Supervisor) FailFast(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Gate() != nil {
			WriteUnavailable(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// WaitReady blocks until the daemon serves or ctx ends. It reports the
// bound so callers can distinguish a timeout from a manual cancel with
// context.Cause where the context carries one.
func (s *Supervisor) WaitReady(ctx context.Context) error {
	deadline := s.clock.Now().Add(s.startupTimeout())
	for s.Gate() != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("supervisor: wait ready: %w", ctx.Err())
		}
		if !s.clock.Now().Before(deadline) {
			return fmt.Errorf("supervisor: daemon not ready within %s", s.startupTimeout())
		}
		if serr := s.clock.Sleep(ctx, s.pollInterval()); serr != nil {
			return fmt.Errorf("supervisor: wait ready: %w", serr)
		}
	}
	return nil
}
