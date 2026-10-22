package e2estack

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/events"
	"github.com/gosuda/rukie/internal/githttp"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/rukproc"
	"github.com/gosuda/rukie/internal/store"
	"github.com/gosuda/rukie/internal/sysops"
)

// User pairs an authenticated user with its minted token secret.
type User struct {
	// Token is the bearer secret for the user.
	Token string
	// User is the authenticated identity.
	User auth.User
}

// Stack is one isolated daemon plus Smart HTTP test fixture. Each
// constructor starts its own daemon child and HTTP server, both closed by
// test cleanup.
type Stack struct {
	// Server serves the Smart HTTP handler.
	Server *httptest.Server
	// Auth is the token and user service.
	Auth *auth.Service
	// Store is the metadata database.
	Store *store.Store
	// Client dials the daemon socket.
	Client *ruk.Client
	// Child is the running daemon process.
	Child *rukproc.Child
	// Handler serves Smart HTTP without the test server.
	Handler http.Handler
	// LogFile writes daemon output.
	LogFile *os.File
	// Users holds minted users by name.
	Users map[string]User
	// Dir is the fixture root.
	Dir string
	// Roots holds the daemon repository roots.
	Roots string
	// Socket is the daemon socket path.
	Socket string
	// URL is the Smart HTTP server address.
	URL string
}

// GitBin returns the git binary under test.
func GitBin(t testing.TB) string {
	t.Helper()
	if bin := os.Getenv("GIT_EXEC"); bin != "" {
		return bin
	}
	return "git"
}

// RukBin resolves the ruk binary under test.
func RukBin(t testing.TB) string {
	t.Helper()
	bin, err := rukproc.ResolveBinary("")
	if err != nil {
		t.Fatalf("ruk binary: %v", err)
	}
	return bin
}

// New starts a default stack with the current user as admin.
func New(t testing.TB, limits *wire.GitLimits) *Stack {
	t.Helper()
	return NewWithConfig(t, limits, "", 0)
}

// NewWithHTTPCap starts a default stack with a capped HTTP body.
func NewWithHTTPCap(t testing.TB, limits *wire.GitLimits, httpCap uint64) *Stack {
	t.Helper()
	return NewWithConfig(t, limits, "", httpCap)
}

// NewNoAdmin starts a stack with no admin user.
func NewNoAdmin(t testing.TB, limits *wire.GitLimits) *Stack {
	t.Helper()
	return NewSetup(t, limits, "", 0, "")
}

// NewWithConfig starts a stack with extra daemon config and the current user as admin.
func NewWithConfig(t testing.TB, limits *wire.GitLimits, extraConfig string, httpCap uint64) *Stack {
	t.Helper()
	return NewSetup(t, limits, extraConfig, httpCap, fmt.Sprintf("admin_uids = [%d]\n", os.Getuid()))
}

// NewSetup starts a stack with extra daemon config and an explicit admin line.
func NewSetup(t testing.TB, limits *wire.GitLimits, extraConfig string, httpCap uint64, adminLine string) *Stack {
	t.Helper()
	return newSetup(t, limits, extraConfig, httpCap, adminLine, access.DefaultLimiterConfig())
}

// NewWithLimiter creates a test stack with the supplied rate-limit settings.
func NewWithLimiter(t testing.TB, limits *wire.GitLimits, limiterConfig access.LimiterConfig) *Stack {
	t.Helper()
	return newSetup(t, limits, "", 0, fmt.Sprintf("admin_uids = [%d]\n", os.Getuid()), limiterConfig)
}

func newSetup(t testing.TB, limits *wire.GitLimits, extraConfig string, httpCap uint64, adminLine string, limiterConfig access.LimiterConfig) *Stack {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	dir := t.TempDir()
	roots := filepath.Join(dir, "roots")
	if err := os.MkdirAll(roots, 0o750); err != nil {
		t.Fatalf("roots: %v", err)
	}
	sqlite := filepath.Join(dir, "rukie.sqlite")
	st, err := store.Open(ctx, store.Options{Path: sqlite})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	svc := auth.New(st)
	socket := filepath.Join(dir, "ruk.sock")
	config := fmt.Sprintf("socket_path = %q\nrepo_roots = [%q]\nallowed_uids = [%d]\n%sframe_timeout_ms = 60000\nretention_interval_ms = 0\nhook_timeout_ms = 5000\n%s",
		socket, roots, os.Getuid(), adminLine, extraConfig)
	if strings.Contains(extraConfig, "retention_interval_ms") {
		config = fmt.Sprintf("socket_path = %q\nrepo_roots = [%q]\nallowed_uids = [%d]\n%sframe_timeout_ms = 60000\nhook_timeout_ms = 5000\n%s",
			socket, roots, os.Getuid(), adminLine, extraConfig)
	}
	configPath := filepath.Join(dir, "daemon.toml")
	if werr := os.WriteFile(configPath, []byte(config), 0o600); werr != nil {
		t.Fatalf("daemon config: %v", werr)
	}
	logFile, err := os.CreateTemp(dir, "ruk-*.log")
	logPath := logFile.Name()
	if err != nil {
		t.Fatalf("daemon log: %v", err)
	}
	child, err := rukproc.StartServe(RukBin(t), configPath, os.Environ(), logFile, logFile)
	if err != nil {
		_ = logFile.Close()
		t.Fatalf("daemon start: %v", err)
	}
	s := &Stack{Dir: dir, Roots: roots, Socket: socket, LogFile: logFile, Auth: svc, Store: st, Child: child, Users: map[string]User{}}
	t.Cleanup(func() {
		_ = child.Kill()
		if t.Failed() {
			if _, seekErr := logFile.Seek(0, io.SeekStart); seekErr == nil {
				raw, readErr := io.ReadAll(logFile)
				if readErr == nil {
					if len(raw) > 4000 {
						raw = raw[len(raw)-4000:]
					}
					t.Logf("daemon logs (%s):\\n%s", logPath, raw)
				}
			}
		}
		_ = logFile.Close()
		_ = st.Close(context.Background())
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, serr := os.Stat(socket); serr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon socket never appeared (logs at %s)", logPath)
		}
		time.Sleep(20 * time.Millisecond)
	}
	dialCtx, dialCancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer dialCancel()
	client, err := ruk.Dial(dialCtx, socket)
	if err != nil {
		t.Fatalf("dial daemon: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	s.Client = client
	useLimits := wire.DefaultGitLimits()
	if limits != nil {
		useLimits = *limits
	}
	spool, err := events.OpenSpool(dir)
	if err != nil {
		t.Fatalf("spool: %v", err)
	}
	git, err := githttp.New(githttp.Config{
		Auth:             svc,
		Limiter:          access.NewLimiterWithConfig(limiterConfig),
		Client:           client,
		Feed:             events.OpenHookSink(st, spool, time.Now),
		Store:            st,
		RepoRoot:         roots,
		Limits:           useLimits,
		MaxHTTPBodyBytes: httpCap,
		Barrier: func() (func(), bool) {
			return sysops.HoldSharedBarrier(dir)
		},
	})
	if err != nil {
		t.Fatalf("githttp: %v", err)
	}
	s.Handler = git
	s.Server = httptest.NewServer(git)
	t.Cleanup(s.Server.Close)
	s.URL = s.Server.URL
	return s
}

// MintToken mints a token with scopes for user and records it.
func (s *Stack) MintToken(t testing.TB, user auth.User, scopes ...string) User {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	parsed, err := auth.ParseScopeSet(scopes)
	if err != nil {
		t.Fatalf("scopes: %v", err)
	}
	secret, err := s.Auth.CreateToken(ctx, auth.CreateTokenInput{
		Name: "e2e", Scopes: parsed, UserID: user.ID,
	})
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	rec := User{User: user, Token: secret.Secret}
	s.Users[user.Name] = rec
	return rec
}

// AddAdmin bootstraps the first admin and mints its token.
func (s *Stack) AddAdmin(t testing.TB, name string, scopes ...string) User {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	user, err := s.Auth.BootstrapAdmin(ctx, name, "long-test-password-01", "127.0.0.1")
	if err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}
	return s.MintToken(t, user, scopes...)
}

// AddUser creates a user after an admin exists and mints its token.
func (s *Stack) AddUser(t testing.TB, name string, scopes ...string) User {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var adminID int64
	for _, rec := range s.Users {
		adminID = rec.User.ID
		break
	}
	if adminID == 0 {
		t.Fatal("bootstrap an admin before adding users")
	}
	user, err := s.Auth.CreateUser(ctx, adminID, auth.CreateUserInput{
		Name: name, Email: name + "@example.test", Password: "long-test-password-01",
	}, "127.0.0.1")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return s.MintToken(t, user, scopes...)
}

// AddRepo inserts a repository row and initializes its daemon repository.
func (s *Stack) AddRepo(t testing.TB, owner, name, visibility string) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	rec, ok := s.Users[owner]
	if !ok {
		t.Fatalf("unknown user %s", owner)
	}
	var repoID int64
	err := s.Store.Write(ctx, func(tx *store.WriteTx) error {
		var ierr error
		repoID, ierr = tx.InsertRepo(time.Now(), store.RepoInsert{
			OwnerUserID:   sql.NullInt64{Int64: rec.User.ID, Valid: true},
			Name:          name,
			Visibility:    visibility,
			DefaultBranch: "main",
			RukPath:       owner + "/" + name + ".git",
		})
		return ierr
	})
	if err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	repoDir := filepath.Join(s.Roots, owner, name+".git")
	seedCtx, seedCancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer seedCancel()
	if out, err := rukproc.Run(seedCtx, RukBin(t), []string{"git", "init", repoDir}, append(os.Environ(), "RUK_COORDINATOR=off")); err != nil {
		t.Fatalf("ruk init: %v\n%s", err, out)
	}
	return repoID
}

// AddRule stores one bookmark rule for a repository.
func (s *Stack) AddRule(t testing.TB, repoID int64, pattern, mode string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if err := s.Store.Write(ctx, func(tx *store.WriteTx) error {
		return tx.PutBookmarkRule(repoID, pattern, mode, time.Now())
	}); err != nil {
		t.Fatalf("insert rule: %v", err)
	}
}

// RepoURL returns the Smart HTTP clone URL with credentials.
func (s *Stack) RepoURL(owner, repo, username, token string) string {
	creds := ""
	if username != "" {
		creds = username + ":" + token + "@"
	}
	plain, _ := strings.CutPrefix(s.URL, "http://")
	return "http://" + creds + plain + "/" + owner + "/" + repo + ".git"
}

// GitEnv returns an isolated environment for git child processes.
func GitEnv(t testing.TB) []string {
	t.Helper()
	home := t.TempDir()
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_SYSTEM=" + os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=e2e",
		"GIT_AUTHOR_EMAIL=e2e@example.test",
		"GIT_COMMITTER_NAME=e2e",
		"GIT_COMMITTER_EMAIL=e2e@example.test",
		"LANG=C",
	}
}

// GitRun executes git with args in dir and returns its combined output.
func GitRun(ctx context.Context, t testing.TB, dir string, args ...string) (string, error) {
	t.Helper()
	out, err := rukproc.RunInDir(ctx, GitBin(t), dir, args, GitEnv(t))
	if err != nil {
		return string(out), err
	}
	return string(out), nil
}

// LocalRepo creates a git working copy with files and returns its path and HEAD.
func (s *Stack) LocalRepo(t testing.TB, files map[string]string) (string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	dir := t.TempDir()
	if out, err := GitRun(ctx, t, dir, "init", "-b", "main", "."); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if out, err := GitRun(ctx, t, dir, "add", "-A"); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	if out, err := GitRun(ctx, t, dir, "commit", "-m", "seed"); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	out, err := GitRun(ctx, t, dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v\n%s", err, out)
	}
	return dir, strings.TrimSpace(out)
}

// LsRemote returns the advertised refs of a repository URL.
func (s *Stack) LsRemote(t testing.TB, repoURL string) map[string]string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	out, err := GitRun(ctx, t, t.TempDir(), "ls-remote", repoURL)
	if err != nil {
		t.Fatalf("ls-remote: %v\n%s", err, out)
	}
	refs := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		oid, name, _ := strings.Cut(line, "\t")
		refs[name] = oid
	}
	return refs
}

// HTTPGet issues one GET request with basic authentication.
func HTTPGet(t testing.TB, url, username, token string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if username != "" || token != "" {
		req.SetBasicAuth(username, token)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body bytes.Buffer
	_, _ = body.ReadFrom(resp.Body)
	return resp.StatusCode, resp.Header, body.String()
}

// AddGrant stores one repository grant for a user.
func (s *Stack) AddGrant(t testing.TB, repoID, userID int64, role string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if err := s.Store.Write(ctx, func(tx *store.WriteTx) error {
		return tx.PutGrant(repoID, userID, role)
	}); err != nil {
		t.Fatalf("insert grant: %v", err)
	}
}
