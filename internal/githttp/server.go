package githttp

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/api"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/events"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
	"github.com/gosuda/rukie/internal/supervisor"
)

const (
	uploadServiceName  = "upload-pack"
	receiveServiceName = "receive-pack"
	refsForPrefix      = "refs/for/"
	maxReasonBytes     = 512
	defaultUploadCap   = 10 * time.Minute
	defaultReceiveCap  = 5 * time.Minute
	defaultRetryAfter  = 5 * time.Second
)

type Config struct {
	Auth        *auth.Service
	Limiter     *access.Limiter
	Client      *ruk.Client
	Feed        *events.HookSink
	Clock       func() time.Time
	Barrier     func() (release func(), ok bool)
	RepoRoot    string
	SocketPath  string
	Limits      wire.GitLimits
	HookTimeout time.Duration
}

type Server struct {
	auth        *auth.Service
	limiter     *access.Limiter
	client      *ruk.Client
	feed        *events.HookSink
	resolver    *access.SQLResolver
	db          *sql.DB
	clock       func() time.Time
	barrier     func() (func(), bool)
	wants       map[string]map[string]string
	repoRoot    string
	socketPath  string
	limits      wire.GitLimits
	hookTimeout time.Duration
	mu          sync.Mutex
}

func (s *Server) stashWant(invocation string, want map[string]string) {
	if invocation == "" || len(want) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wants == nil {
		s.wants = map[string]map[string]string{}
	}
	s.wants[invocation] = want
}

func (s *Server) takeWant(invocation string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := s.wants[invocation]
	delete(s.wants, invocation)
	return want
}

func (s *Server) currentDaemonTips(ctx context.Context, client *ruk.Client, path string) (map[string]string, error) {
	repo, oerr := client.OpenRepo(ctx, path)
	if oerr != nil {
		return nil, oerr
	}
	reply, lerr := ruk.RefsList(ctx, client, repo, ruk.RefsListArgs{})
	if lerr != nil {
		return nil, lerr
	}
	out := make(map[string]string, len(reply.Refs.Items))
	for _, ref := range reply.Refs.Items {
		out[ref.Name] = ref.Target.Hex()
	}
	return out, nil
}

func (s *Server) reconcileUnknownPush(ctx context.Context, client *ruk.Client, path, requestID string) (supervisor.Verdict, error) {
	return supervisor.ReconcileUnknownOutcome(ctx,
		func(context.Context) (supervisor.LookupResult, error) {
			return supervisor.LookupResult{}, nil
		},
		func(tctx context.Context) (map[string]string, error) {
			return s.currentDaemonTips(tctx, client, path)
		},
		s.takeWant(requestID))
}

func New(cfg Config) (*Server, error) {
	if cfg.Auth == nil {
		return nil, errConfig("auth service is required")
	}
	if cfg.Limiter == nil {
		return nil, errConfig("rate limiter is required")
	}
	if cfg.Client == nil && cfg.SocketPath == "" {
		return nil, errConfig("ruk client or socket path is required")
	}
	if !filepath.IsAbs(cfg.RepoRoot) {
		return nil, errConfig("repo root must be absolute")
	}
	if cfg.Limits.MaxStdinBytes == 0 {
		return nil, errConfig("max stdin bytes must be positive")
	}
	hookTimeout := cmp.Or(cfg.HookTimeout, 10*time.Second)
	if hookTimeout <= 0 {
		hookTimeout = 10 * time.Second
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Server{
		auth:        cfg.Auth,
		limiter:     cfg.Limiter,
		client:      cfg.Client,
		feed:        cfg.Feed,
		resolver:    access.NewSQLResolver(cfg.Auth.DB(), access.NewStoreTeams(cfg.Auth.DB(), nil)),
		db:          cfg.Auth.DB(),
		repoRoot:    filepath.Clean(cfg.RepoRoot),
		socketPath:  cfg.SocketPath,
		limits:      cfg.Limits,
		hookTimeout: hookTimeout,
		clock:       clock,
		barrier:     cfg.Barrier,
	}, nil
}

func (s *Server) clientFor(ctx context.Context) (*ruk.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		return s.client, nil
	}
	if s.socketPath == "" {
		return nil, errors.New("githttp: no daemon client")
	}
	client, err := ruk.Dial(ctx, s.socketPath)
	if err != nil {
		return nil, err
	}
	s.client = client
	return client, nil
}

func errConfig(msg string) error {
	return &configError{msg: msg}
}

type configError struct {
	msg string
}

func (e *configError) Error() string {
	return "githttp: " + e.msg
}

type routeKind uint8

const (
	routeNone routeKind = iota
	routeBare
	routeInfoRefs
	routeUploadPack
	routeReceivePack
)

type route struct {
	owner string
	repo  string
	kind  routeKind
}

var ownerPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,38}$`)

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

var reservedOwners = func() map[string]bool {
	out := map[string]bool{}
	for _, name := range api.Reserved() {
		out[name] = true
	}
	return out
}()

func validOwner(name string) bool {
	if !ownerPattern.MatchString(name) {
		return false
	}
	if auth.ReservedName(name) {
		return false
	}
	return !reservedOwners[name]
}

func validRepo(name string) bool {
	if name == "." || name == ".." {
		return false
	}
	return repoPattern.MatchString(name)
}

func parseRoute(path string) route {
	if !strings.HasPrefix(path, "/") {
		return route{}
	}
	rest := strings.TrimPrefix(path, "/")
	parts := strings.Split(rest, "/")
	if len(parts) > 2 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) < 2 {
		return route{}
	}
	owner, repoGit := parts[0], parts[1]
	if !validOwner(owner) {
		return route{}
	}
	repo, ok := api.StripGitSuffix(repoGit)
	if !ok || !validRepo(repo) {
		return route{}
	}
	base := route{owner: owner, repo: repo}
	tail := parts[2:]
	if len(tail) == 0 {
		base.kind = routeBare
		return base
	}
	if len(tail) == 1 && tail[0] == "git-upload-pack" {
		base.kind = routeUploadPack
		return base
	}
	if len(tail) == 1 && tail[0] == "git-receive-pack" {
		base.kind = routeReceivePack
		return base
	}
	if len(tail) == 2 && tail[0] == "info" && tail[1] == "refs" {
		base.kind = routeInfoRefs
		return base
	}
	return route{}
}

func RouteGitPath(path string) (string, string, bool) {
	rt := parseRoute(path)
	if rt.kind == routeNone {
		return "", "", false
	}
	return rt.owner, rt.repo, true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rt := parseRoute(r.URL.Path)
	if rt.kind == routeNone {
		writeText(w, http.StatusNotFound, "not found")
		return
	}
	switch rt.kind {
	case routeBare:
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, []string{http.MethodGet})
			return
		}
		http.Redirect(w, r, "/"+rt.owner+"/"+rt.repo, http.StatusMovedPermanently)
	case routeInfoRefs:
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, []string{http.MethodGet})
			return
		}
		s.serveInfoRefs(w, r, rt)
	case routeUploadPack:
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, []string{http.MethodPost})
			return
		}
		s.serveRound(w, r, rt, uploadServiceName)
	case routeReceivePack:
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, []string{http.MethodPost})
			return
		}
		s.serveRound(w, r, rt, receiveServiceName)
	case routeNone:
		writeText(w, http.StatusNotFound, "not found")
	}
}

func writeText(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(msg + "\n"))
}

func writeMethodNotAllowed(w http.ResponseWriter, allow []string) {
	w.Header().Set("Allow", strings.Join(allow, ", "))
	writeText(w, http.StatusMethodNotAllowed, "method not allowed")
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="rukie"`)
	writeText(w, http.StatusUnauthorized, "authentication is required")
}

func writeRateLimited(w http.ResponseWriter, after time.Duration) {
	secs := int64(after / time.Second)
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(secs, 10))
	writeText(w, http.StatusTooManyRequests, "rate limit exceeded")
}

func (s *Server) storePath(rt route) string {
	return filepath.Join(s.repoRoot, rt.owner, rt.repo+".git", ".ruk")
}

func (s *Server) newRequestID() string {
	return "push-" + string(store.NewID(s.clock()))
}
