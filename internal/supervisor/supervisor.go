package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"time"

	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/rukproc"
)

// Timing bounds the supervisor against the ops contract: startup waits
// 15 s for the socket and handshake, a crashed child restarts at most
// 3 times with backoff 1 s, 2 s, 4 s, then the supervisor exits non-zero.
const (
	StartupTimeout = 15 * time.Second
	MaxRestarts    = 3
	StopGrace      = 10 * time.Second
)

// stableResetAfter clears the consecutive-restart count once the child
// has served this long without dying, so one crash a week never exhausts
// the O35 budget. The MaxRestarts doc ("caps consecutive child restarts")
// describes this counter.
const stableResetAfter = 60 * time.Second

// externalReprobeInterval re-checks an externally managed daemon, so a
// dead external socket flips /readyz false and the fail-fast 503 gates
// engage until it answers again.
const externalReprobeInterval = 5 * time.Second

// childExitError reports a child that died before its startup probe
// succeeded. Unlike a probe timeout on a live child, the waiter already
// reaped it, so the caller must not Kill it again.
type childExitError struct{ err error }

func (e *childExitError) Error() string { return e.err.Error() }

func (e *childExitError) Unwrap() error { return e.err }

// RestartBackoffs holds the O35 backoff schedule: attempt n sleeps
// RestartBackoffs[n-1]. The test suite pins this table through an
// injectable clock, never by shortening these constants.
var RestartBackoffs = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

// Proc abstracts one running ruk serve child. *rukproc.Child satisfies it;
// tests substitute a fake.
type Proc interface {
	Wait() error
	Signal(os.Signal) error
	Kill() error
}

func soleDaemonStarter(bin, configPath string, env []string, stdout, stderr io.Writer) (Proc, error) {
	return rukproc.StartServe(bin, configPath, env, stdout, stderr)
}

// Probe verifies the daemon answers. The production probe dials the
// socket and handshakes; tests substitute a fake.
type Probe func(ctx context.Context) error

// Clock abstracts time so restart-cap and backoff-boundary tests run
// without shortened production constants.
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) Sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// SystemClock is the production clock.
func SystemClock() Clock { return systemClock{} }

// Config wires one supervised daemon. Pointer-word fields pack first
// so the GC prefix stays tight; pure scalars sit last.
type Config struct {
	// Starter launches the child; nil selects the rukproc starter.
	Starter func(bin, configPath string, env []string, stdout, stderr io.Writer) (Proc, error)
	// Probe checks daemon liveness; nil selects a socket dial probe.
	Probe Probe
	// Clock abstracts time; nil selects the system clock.
	Clock Clock
	// Stdout and Stderr receive child logs; nil discards.
	Stdout io.Writer
	Stderr io.Writer
	// Env is the full child environment (see HermeticEnv).
	Env []string
	// Binary is the ruk binary path (resolved through rukproc).
	Binary string
	// DaemonConfig is the rendered daemon TOML path passed as --config.
	DaemonConfig string
	// Socket is probed at startup. In external mode it names the
	// externally managed daemon and no child is started.
	ExternalSocket string
	// SocketPath is the child socket to probe. It defaults to the
	// socket rendered into DaemonConfig when Probe is custom; the
	// default probe needs it set.
	SocketPath string
	// Backoffs overrides RestartBackoffs; nil selects it.
	Backoffs []time.Duration
	// MaxRestarts caps consecutive child restarts; zero selects MaxRestarts.
	MaxRestarts int
	// StartupTimeout bounds the socket-plus-handshake wait; zero selects StartupTimeout.
	StartupTimeout time.Duration
	// PollInterval spaces startup probes; zero selects 100 ms.
	PollInterval time.Duration
	// StopGrace bounds SIGTERM-to-SIGKILL on shutdown; zero selects StopGrace.
	StopGrace time.Duration
}

// Supervisor runs one ruk serve child within the O35 restart budget and
// reports daemon readiness for /readyz and fail-fast gates. The 64-bit
// counters sit at 8-aligned offsets (224, 232: multiples of 8 past an
// 8-aligned base with all preceding sizes multiple of 8), which keeps
// them safe for 32-bit atomic access while satisfying the packing
// order below.
type Supervisor struct {
	clock     Clock
	cfg       Config
	restarts  atomic.Int64
	starts    atomic.Int64
	ready     atomic.Bool
	everReady atomic.Bool
}

// New validates the config and returns a supervisor. It starts nothing.
func New(cfg Config) (*Supervisor, error) {
	if cfg.ExternalSocket == "" && cfg.Binary == "" {
		return nil, fmt.Errorf("supervisor: ruk binary path is required")
	}
	if cfg.ExternalSocket == "" && cfg.DaemonConfig == "" {
		return nil, fmt.Errorf("supervisor: rendered daemon config path is required")
	}
	s := &Supervisor{cfg: cfg, clock: cfg.Clock}
	if s.clock == nil {
		s.clock = systemClock{}
	}
	if s.cfg.Probe == nil {
		target := s.cfg.ExternalSocket
		if target == "" {
			target = s.cfg.SocketPath
		}
		if target != "" {
			socket := target
			s.cfg.Probe = func(ctx context.Context) error {
				client, err := ruk.Dial(ctx, socket)
				if err != nil {
					return err
				}
				return client.Close()
			}
		}
	}
	return s, nil
}

func (s *Supervisor) maxRestarts() int {
	if s.cfg.MaxRestarts > 0 {
		return s.cfg.MaxRestarts
	}
	return MaxRestarts
}

func (s *Supervisor) backoffs() []time.Duration {
	if len(s.cfg.Backoffs) > 0 {
		return s.cfg.Backoffs
	}
	return RestartBackoffs
}

func (s *Supervisor) startupTimeout() time.Duration {
	if s.cfg.StartupTimeout > 0 {
		return s.cfg.StartupTimeout
	}
	return StartupTimeout
}

func (s *Supervisor) pollInterval() time.Duration {
	if s.cfg.PollInterval > 0 {
		return s.cfg.PollInterval
	}
	return 100 * time.Millisecond
}

func (s *Supervisor) stopGrace() time.Duration {
	if s.cfg.StopGrace > 0 {
		return s.cfg.StopGrace
	}
	return StopGrace
}

func (s *Supervisor) starter() func(string, string, []string, io.Writer, io.Writer) (Proc, error) {
	if s.cfg.Starter != nil {
		return s.cfg.Starter
	}
	return soleDaemonStarter
}

// Ready reports whether the daemon is currently serving. It is false
// before the first successful probe and while the child is down.
func (s *Supervisor) Ready() bool { return s.ready.Load() }

// Restarts counts child restarts so far.
func (s *Supervisor) Restarts() int64 { return s.restarts.Load() }

// Starts counts child launches so far.
func (s *Supervisor) Starts() int64 { return s.starts.Load() }

// DaemonProbe adapts Ready to the readyz handler contract.
func (s *Supervisor) DaemonProbe() (bool, string) {
	if s.ready.Load() {
		return true, ""
	}
	return false, "ruk serve is down"
}

func (s *Supervisor) stdout() io.Writer {
	if s.cfg.Stdout != nil {
		return s.cfg.Stdout
	}
	return io.Discard
}

func (s *Supervisor) stderr() io.Writer {
	if s.cfg.Stderr != nil {
		return s.cfg.Stderr
	}
	return io.Discard
}

func (s *Supervisor) pollDaemonProbeUntilTimeout(ctx context.Context, exit <-chan error) error {
	probe := s.cfg.Probe
	if probe == nil {
		return fmt.Errorf("supervisor: no daemon probe configured")
	}
	deadline := s.clock.Now().Add(s.startupTimeout())
	for {
		select {
		case werr := <-exit:
			return &childExitError{err: fmt.Errorf("supervisor: child exited during startup: %w", werr)}
		default:
		}
		if perr := probe(ctx); perr == nil {
			return nil
		} else if ctx.Err() != nil {
			return fmt.Errorf("supervisor: startup wait: %w", ctx.Err())
		}
		if !s.clock.Now().Before(deadline) {
			select {
			case werr := <-exit:
				return &childExitError{err: fmt.Errorf("supervisor: child exited during startup: %w", werr)}
			default:
			}
			return fmt.Errorf("supervisor: daemon did not answer within %s", s.startupTimeout())
		}
		if serr := s.clock.Sleep(ctx, s.pollInterval()); serr != nil {
			return fmt.Errorf("supervisor: startup wait: %w", serr)
		}
	}
}

// Run supervises until ctx ends or the restart budget is exhausted.
// A cancelled context is a clean shutdown and returns nil; an exhausted
// budget returns a non-nil error so the process exits non-zero.
//
// A child that dies before its first probe succeeds still spends one
// budget unit (with the O35 backoff) once the supervisor has ever been
// ready or restarted before; only a stillborn first start fails fast.
// A crash after stableResetAfter of serving resets the consecutive
// counter first, so an occasional crash never exhausts the budget.
func (s *Supervisor) Run(ctx context.Context) error {
	if s.cfg.ExternalSocket != "" {
		if err := s.pollDaemonProbeUntilTimeout(ctx, nil); err != nil {
			return err
		}
		s.ready.Store(true)
		s.everReady.Store(true)
		for {
			if serr := s.clock.Sleep(ctx, externalReprobeInterval); serr != nil {
				s.ready.Store(false)
				return nil
			}
			if perr := s.probeOnce(ctx); perr != nil {
				s.ready.Store(false)
			} else {
				s.ready.Store(true)
			}
		}
	}
	backoffs := s.backoffs()
	for {
		child, err := s.starter()(s.cfg.Binary, s.cfg.DaemonConfig, s.cfg.Env, s.stdout(), s.stderr())
		if err != nil {
			s.ready.Store(false)
			return fmt.Errorf("supervisor: start ruk serve: %w", err)
		}
		s.starts.Add(1)
		exit := make(chan error, 1)
		go func() { exit <- child.Wait() }()
		if perr := s.pollDaemonProbeUntilTimeout(ctx, exit); perr != nil {
			s.ready.Store(false)
			_, exited := errors.AsType[*childExitError](perr)
			if !exited {
				// The child may still be alive (probe timeout on a
				// live child): SIGKILL it and reap it through the
				// single waiter before spending the restart, or the
				// orphan keeps the socket and every later probe fails.
				_ = child.Signal(killSignal())
				<-exit
			}
			if exited || s.restarts.Load() > 0 || s.everReady.Load() {
				if bend := s.spendRestart(ctx, backoffs, perr); bend != nil {
					return bend
				}
				continue
			}
			return perr
		}
		s.ready.Store(true)
		s.everReady.Store(true)
		readyAt := s.clock.Now()
		select {
		case <-ctx.Done():
			s.ready.Store(false)
			_ = s.terminateChildWithSingleWaiter(child, exit)
			return nil
		case werr := <-exit:
			s.ready.Store(false)
			if ctx.Err() != nil {
				return nil
			}
			if s.clock.Now().Sub(readyAt) >= stableResetAfter {
				s.restarts.Store(0)
			}
			if bend := s.spendRestart(ctx, backoffs, daemonLossAgainstBudget(werr)); bend != nil {
				return bend
			}
		}
	}
}

// spendRestart sleeps one O35 backoff and consumes one restart-budget
// unit. It returns non-nil when the run must end: an exhausted budget
// carries the cause as an error, and a cancelled context returns nil
// for a clean shutdown.
func (s *Supervisor) spendRestart(ctx context.Context, backoffs []time.Duration, cause error) error {
	n := s.restarts.Load()
	if int(n) >= s.maxRestarts() {
		return fmt.Errorf("supervisor: ruk serve failed after %d restarts: %w", n, cause)
	}
	delay := backoffs[len(backoffs)-1]
	if int(n) < len(backoffs) {
		delay = backoffs[n]
	}
	if serr := s.clock.Sleep(ctx, delay); serr != nil {
		return nil
	}
	s.restarts.Add(1)
	return nil
}

func (s *Supervisor) probeOnce(ctx context.Context) error {
	probe := s.cfg.Probe
	if probe == nil {
		return fmt.Errorf("supervisor: no daemon probe configured")
	}
	return probe(ctx)
}

func daemonLossAgainstBudget(werr error) error {
	if werr == nil {
		return fmt.Errorf("ruk serve exited 0 outside shutdown")
	}
	return werr
}

func (s *Supervisor) terminateChildWithSingleWaiter(child Proc, exit <-chan error) error {
	_ = child.Signal(terminateSignal())
	grace, cancel := context.WithTimeout(context.Background(), s.stopGrace())
	defer cancel()
	select {
	case werr := <-exit:
		return werr
	case <-grace.Done():
	}
	_ = child.Signal(killSignal())
	reap, stop := context.WithTimeout(context.Background(), s.stopGrace())
	defer stop()
	select {
	case werr := <-exit:
		return werr
	case <-reap.Done():
		return fmt.Errorf("supervisor: child did not exit after SIGKILL")
	}
}
