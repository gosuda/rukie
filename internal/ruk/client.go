package ruk

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"path/filepath"
	"sync"
	"time"

	"github.com/gosuda/rukie/internal/ruk/wire"
)

// Timeouts bounds each client wait.
type Timeouts struct {
	Dial         time.Duration
	Handshake    time.Duration
	Write        time.Duration
	CancelGrace  time.Duration
	LookupBudget time.Duration
	IdleTTL      time.Duration
}

// DefaultTimeouts holds the design values.
func DefaultTimeouts() Timeouts {
	return Timeouts{
		Dial:         2 * time.Second,
		Handshake:    5 * time.Second,
		Write:        10 * time.Second,
		CancelGrace:  2 * time.Second,
		LookupBudget: 5 * time.Second,
		IdleTTL:      30 * time.Second,
	}
}

// CallEvent reports one finished call.
type CallEvent struct {
	Method   string
	Code     string
	Duration time.Duration
	OutBytes int64
	Attempts uint8
	Outcome  Outcome
}

// AcceptEvent reports one admission.
type AcceptEvent struct {
	Wait   time.Duration
	Conn   uint64
	Queued bool
}

// ReconnectEvent reports one redial.
type ReconnectEvent struct {
	Conn     uint64
	Attempts uint8
}

// Hooks observes client behavior and defaults to silent.
type Hooks struct {
	OnCall              func(CallEvent)
	OnAccept            func(AcceptEvent)
	OnReconnect         func(ReconnectEvent)
	OnCreditWait        func(stream uint64, wait time.Duration)
	OnProtocolViolation func(err error)
}

type config struct {
	hooks          Hooks
	profile        string
	timeouts       Timeouts
	maxConns       int
	streamsPerConn int
}

// Option tunes one client.
type Option func(*config)

// WithProfile sets the handshake profile.
func WithProfile(name string) Option {
	return func(c *config) {
		c.profile = name
	}
}

// WithPool sets connection and stream bounds.
func WithPool(maxConns, streamsPerConn int) Option {
	return func(c *config) {
		c.maxConns = maxConns
		c.streamsPerConn = streamsPerConn
	}
}

// WithIdleTTL sets connection reuse lifetime.
func WithIdleTTL(d time.Duration) Option {
	return func(c *config) {
		c.timeouts.IdleTTL = d
	}
}

// WithTimeouts replaces the timeout set.
func WithTimeouts(t Timeouts) Option {
	return func(c *config) {
		c.timeouts = t
	}
}

// WithHooks installs observers.
func WithHooks(h Hooks) Option {
	return func(c *config) {
		c.hooks = h
	}
}

// Repo names one opened repository.
type Repo struct {
	Path   string
	Handle string
}

// Client speaks the daemon over pooled connections.
type Client struct {
	hooks     Hooks
	pool      *pool
	handles   *handleCache
	sessions  *sessionCache
	closed    chan struct{}
	socket    string
	profile   string
	timeouts  Timeouts
	closeOnce sync.Once
}

// Dial opens one client and handshakes eagerly.
func Dial(ctx context.Context, socketPath string, opts ...Option) (*Client, error) {
	cfg := config{
		profile:        "rukie",
		maxConns:       8,
		streamsPerConn: 8,
		timeouts:       DefaultTimeouts(),
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.profile == "" {
		return nil, fmt.Errorf("Dial: %w: empty profile", ErrHandshake)
	}
	if cfg.maxConns < 1 {
		cfg.maxConns = 1
	}
	if cfg.streamsPerConn < 1 {
		cfg.streamsPerConn = 1
	}
	c := &Client{
		socket:   socketPath,
		profile:  cfg.profile,
		timeouts: cfg.timeouts,
		hooks:    cfg.hooks,
		handles:  newHandleCache(),
		sessions: newSessionCache(),
		closed:   make(chan struct{}),
	}
	c.pool = newPool(c, cfg.maxConns, cfg.streamsPerConn)
	first, err := dialBackoff(ctx, c, cfg.timeouts)
	if err != nil {
		return nil, err
	}
	c.pool.add(first)
	return c, nil
}

func dialJitter(base time.Duration) time.Duration {
	half := int64(base / 2)
	if half < 1 {
		return 0
	}
	drawn, err := rand.Int(rand.Reader, big.NewInt(half))
	if err != nil {
		return 0
	}
	return time.Duration(drawn.Int64())
}

func dialBackoff(ctx context.Context, c *Client, timeouts Timeouts) (*conn, error) {
	backoff := 50 * time.Millisecond
	var err error
	var pc *conn
	for attempt := range 3 {
		if attempt > 0 {
			timer := time.NewTimer(backoff/2 + dialJitter(backoff))
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, fmt.Errorf("Dial: %w", ctx.Err())
			case <-timer.C:
			}
			backoff *= 2
			if backoff > time.Second {
				backoff = time.Second
			}
		}
		pc, err = dialConn(ctx, c.pool.nextID(), c.socket, c.profile, timeouts, c.hooks)
		if err == nil {
			return pc, nil
		}
	}
	return nil, err
}

// Close drains every connection.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
	})
	c.pool.close()
	return nil
}

// OpenRepo resolves one absolute path to a handle.
func (c *Client) OpenRepo(ctx context.Context, absPath string) (Repo, error) {
	if !filepath.IsAbs(absPath) {
		return Repo{}, fmt.Errorf("OpenRepo: %w: relative path %q", ErrInvalidPath, absPath)
	}
	clean := filepath.Clean(absPath)
	if handle, ok := c.handles.get(clean); ok {
		return Repo{Path: clean, Handle: handle}, nil
	}
	if _, err := Schema(ctx, c, Repo{Path: clean}, wire.SchemaArgs{}); err != nil {
		return Repo{}, err
	}
	handle, ok := c.handles.get(clean)
	if !ok {
		return Repo{}, fmt.Errorf("OpenRepo: %w: missing handle", ErrProtocol)
	}
	return Repo{Path: clean, Handle: handle}, nil
}
