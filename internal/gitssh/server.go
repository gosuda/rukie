package gitssh

import (
	"cmp"
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/events"
	"github.com/gosuda/rukie/internal/githttp"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
)

const (
	defaultMaxSessions       = 128
	defaultIdleTimeout       = 10 * time.Minute
	defaultKeepaliveInterval = 60 * time.Second
	handshakeTimeout         = 30 * time.Second
	keyLookupTimeout         = 5 * time.Second
	deniedPublickey          = "Permission denied (publickey)"
)

// Config carries every dependency of the SSH git listener.
type Config struct {
	Auth              *auth.Service
	Limiter           *access.Limiter
	Client            *ruk.Client
	Feed              *events.HookSink
	Clock             func() time.Time
	Barrier           func() (release func(), ok bool)
	SocketPath        string
	RepoRoot          string
	HostKeys          []ssh.Signer
	Limits            wire.GitLimits
	HookTimeout       time.Duration
	MaxSessions       int
	IdleTimeout       time.Duration
	KeepaliveInterval time.Duration
}

// Server speaks git-upload-pack and git-receive-pack over SSH exec.
type Server struct {
	limiter   *access.Limiter
	client    *ruk.Client
	feed      *events.HookSink
	git       *githttp.Server
	resolver  access.Resolver
	conns     map[net.Conn]struct{}
	clock     func() time.Time
	auth      *auth.Service
	listeners map[net.Listener]struct{}
	barrier   func() (release func(), ok bool)
	repoRoot  string
	sock      string
	hostKeys  []ssh.Signer
	limits    wire.GitLimits
	idleAfter time.Duration
	keepEvery time.Duration
	maxSess   int
	hookAfter time.Duration
	sessions  int
	mu        sync.Mutex
	closed    atomic.Bool
}

type configError struct {
	msg string
}

// Error reports the listener configuration failure.
func (e *configError) Error() string {
	return "gitssh: " + e.msg
}

// New validates its config and returns a listener that Serve drives.
func New(cfg Config) (*Server, error) {
	if cfg.Auth == nil {
		return nil, &configError{msg: "auth service is required"}
	}
	if cfg.Limiter == nil {
		return nil, &configError{msg: "rate limiter is required"}
	}
	if cfg.Client == nil && cfg.SocketPath == "" {
		return nil, &configError{msg: "ruk client or socket path is required"}
	}
	if cfg.RepoRoot == "" {
		return nil, &configError{msg: "repo root is required"}
	}
	if cfg.Limits.MaxStdinBytes == 0 {
		return nil, &configError{msg: "max stdin bytes must be positive"}
	}
	if len(cfg.HostKeys) == 0 {
		return nil, &configError{msg: "at least one host key is required"}
	}
	hookTimeout := cmp.Or(cfg.HookTimeout, 10*time.Second)
	if hookTimeout <= 0 {
		hookTimeout = 10 * time.Second
	}
	maxSess := cmp.Or(cfg.MaxSessions, defaultMaxSessions)
	if maxSess <= 0 {
		maxSess = defaultMaxSessions
	}
	idleAfter := cmp.Or(cfg.IdleTimeout, defaultIdleTimeout)
	if idleAfter <= 0 {
		idleAfter = defaultIdleTimeout
	}
	keepEvery := cmp.Or(cfg.KeepaliveInterval, defaultKeepaliveInterval)
	if keepEvery <= 0 {
		keepEvery = defaultKeepaliveInterval
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	git, err := githttp.New(githttp.Config{
		Auth:       cfg.Auth,
		Limiter:    cfg.Limiter,
		Client:     cfg.Client,
		SocketPath: cfg.SocketPath,
		RepoRoot:   cfg.RepoRoot,
		Limits:     cfg.Limits,
		Feed:       cfg.Feed,
		Clock:      clock,
	})
	if err != nil {
		return nil, err
	}
	return &Server{
		auth:      cfg.Auth,
		limiter:   cfg.Limiter,
		client:    cfg.Client,
		feed:      cfg.Feed,
		git:       git,
		resolver:  cfg.Auth.Store().Resolver(),
		clock:     clock,
		repoRoot:  cfg.RepoRoot,
		sock:      cfg.SocketPath,
		limits:    cfg.Limits,
		hookAfter: hookTimeout,
		barrier:   cfg.Barrier,
		hostKeys:  cfg.HostKeys,
		maxSess:   maxSess,
		idleAfter: idleAfter,
		keepEvery: keepEvery,
		conns:     map[net.Conn]struct{}{},
		listeners: map[net.Listener]struct{}{},
	}, nil
}

func (s *Server) serverConfig() *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: s.authorizeKey,
	}
	for _, key := range s.hostKeys {
		cfg.AddHostKey(key)
	}
	return cfg
}

func (s *Server) authorizeKey(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	fingerprint := ssh.FingerprintSHA256(key)
	ip := remoteIP(meta)
	ctx, stop := context.WithTimeout(context.Background(), keyLookupTimeout)
	defer stop()
	found, user, err := s.auth.LookupSSHKey(ctx, fingerprint)
	if err != nil {
		s.auth.AuditSSHUnknownKey(ctx, fingerprint, ip)
		_ = s.limiter.Admit(ctx, op.RateKey{Kind: op.RateIPGit, ID: ip})
		return nil, fmt.Errorf("%s", deniedPublickey)
	}
	return &ssh.Permissions{
		Extensions: map[string]string{
			"user-id":     fmt.Sprintf("%d", user.ID),
			"key-id":      fmt.Sprintf("%d", found.ID),
			"fingerprint": fingerprint,
			"admin":       fmt.Sprintf("%v", user.IsAdmin),
		},
	}, nil
}

func remoteIP(meta ssh.ConnMetadata) string {
	host, _, err := net.SplitHostPort(meta.RemoteAddr().String())
	if err != nil {
		return meta.RemoteAddr().String()
	}
	return host
}

func (s *Server) clientFor(ctx context.Context) (*ruk.Client, error) {
	s.mu.Lock()
	if s.client != nil {
		client := s.client
		s.mu.Unlock()
		return client, nil
	}
	s.mu.Unlock()
	if s.sock == "" {
		return nil, fmt.Errorf("gitssh: no daemon client")
	}
	client, err := ruk.Dial(ctx, s.sock)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		late := client
		client = s.client
		go func() { _ = late.Close() }()
		return client, nil
	}
	s.client = client
	return client, nil
}

// Serve accepts connections until ctx ends or the listener closes.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	s.mu.Lock()
	s.listeners[ln] = struct{}{}
	s.mu.Unlock()
	defer func() {
		_ = ln.Close()
		s.mu.Lock()
		delete(s.listeners, ln)
		s.mu.Unlock()
	}()
	conf := s.serverConfig()
	for {
		raw, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			if s.closed.Load() {
				return nil
			}
			continue
		}
		go s.handle(ctx, conf, raw)
	}
}

func (s *Server) handle(ctx context.Context, conf *ssh.ServerConfig, raw net.Conn) {
	s.trackAdd(raw)
	defer s.trackDrop(raw)
	_ = raw.SetDeadline(time.Now().Add(handshakeTimeout))
	conn, chans, reqs, err := ssh.NewServerConn(raw, conf)
	_ = raw.SetDeadline(time.Time{})
	if err != nil {
		_ = raw.Close()
		return
	}
	defer func() { _ = conn.Close() }()
	done := make(chan struct{})
	defer close(done)
	go ssh.DiscardRequests(reqs)
	live := &sessionLive{}
	live.touch()
	go s.keepalive(conn, done, raw)
	go s.watchIdle(done, live, raw)
	for ch := range chans {
		if ch.ChannelType() != "session" {
			_ = ch.Reject(ssh.UnknownChannelType, "unknown channel")
			continue
		}
		if !s.tryAcquire() {
			_ = ch.Reject(ssh.ResourceShortage, "too many sessions")
			continue
		}
		channel, reqs, err := ch.Accept()
		if err != nil {
			s.releaseSession()
			continue
		}
		live.touch()
		perms := conn.Permissions
		go func() {
			defer s.releaseSession()
			s.serveSessionGuarded(ctx, perms, channel, reqs, live)
		}()
	}
}

func (s *Server) trackAdd(raw net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conns[raw] = struct{}{}
}

func (s *Server) trackDrop(raw net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, raw)
}

func (s *Server) tryAcquire() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions >= s.maxSess {
		return false
	}
	s.sessions++
	return true
}

func (s *Server) releaseSession() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions > 0 {
		s.sessions--
	}
}

// Close stops every listener and drops every live connection.
func (s *Server) Close() error {
	s.closed.Store(true)
	s.mu.Lock()
	defer s.mu.Unlock()
	for ln := range s.listeners {
		_ = ln.Close()
	}
	for raw := range s.conns {
		_ = raw.Close()
	}
	return nil
}

type sessionLive struct {
	at atomic.Int64
}

func (l *sessionLive) touch() {
	l.at.Store(time.Now().UnixNano())
}

func (s *Server) keepalive(conn *ssh.ServerConn, done <-chan struct{}, raw net.Conn) {
	ticker := time.NewTicker(s.keepEvery)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
		}
		if _, _, err := conn.SendRequest("keepalive@openssh.com", false, nil); err != nil {
			_ = raw.Close()
			return
		}
	}
}

func (s *Server) idleTick() time.Duration {
	return min(max(s.idleAfter/4, 100*time.Millisecond), 30*time.Second)
}

func (s *Server) watchIdle(done <-chan struct{}, live *sessionLive, raw net.Conn) {
	ticker := time.NewTicker(s.idleTick())
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
		}
		if time.Since(time.Unix(0, live.at.Load())) > s.idleAfter {
			_ = raw.Close()
			return
		}
	}
}
