//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/githttp"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/rukproc"
	"github.com/gosuda/rukie/internal/store"
)

type userRec struct {
	token string
	user  auth.User
}

type stack struct {
	t       *testing.T
	server  *httptest.Server
	auth    *auth.Service
	db      *sql.DB
	client  *ruk.Client
	child   *rukproc.Child
	handler http.Handler
	users   map[string]userRec
	dir     string
	roots   string
	socket  string
	url     string
}

func gitBin(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("GIT_EXEC"); bin != "" {
		return bin
	}
	return "git"
}

func rukBin(t *testing.T) string {
	t.Helper()
	bin, err := rukproc.ResolveBinary("")
	if err != nil {
		t.Fatalf("ruk binary: %v", err)
	}
	return bin
}

func newStack(t *testing.T, limits *wire.GitLimits) *stack {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	dir := t.TempDir()
	roots := filepath.Join(dir, "roots")
	if err := os.MkdirAll(roots, 0o755); err != nil {
		t.Fatalf("roots: %v", err)
	}
	sqlite := filepath.Join(dir, "rukie.sqlite")
	st, err := store.Open(ctx, store.Options{Path: sqlite})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	svc, err := auth.Open(sqlite)
	if err != nil {
		t.Fatalf("auth open: %v", err)
	}
	socket := filepath.Join(dir, "ruk.sock")
	config := fmt.Sprintf("socket_path = %q\nrepo_roots = [%q]\nallowed_uids = [%d]\nframe_timeout_ms = 5000\nretention_interval_ms = 0\nhook_timeout_ms = 5000\n",
		socket, roots, os.Getuid())
	configPath := filepath.Join(dir, "daemon.toml")
	if werr := os.WriteFile(configPath, []byte(config), 0o600); werr != nil {
		t.Fatalf("daemon config: %v", werr)
	}
	var logs bytes.Buffer
	child, err := rukproc.StartServe(rukBin(t), configPath, os.Environ(), &logs, &logs)
	if err != nil {
		t.Fatalf("daemon start: %v\n%s", err, logs.String())
	}
	s := &stack{t: t, dir: dir, roots: roots, socket: socket, auth: svc, db: svc.DB(), child: child, users: map[string]userRec{}}
	t.Cleanup(func() {
		_ = child.Kill()
		_ = svc.Close()
		_ = st.Close(context.Background())
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, serr := os.Stat(socket); serr == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = child.Kill()
			t.Fatalf("daemon socket never appeared\n%s", logs.String())
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
	s.client = client
	useLimits := wire.DefaultGitLimits()
	if limits != nil {
		useLimits = *limits
	}
	git, err := githttp.New(githttp.Config{
		Auth:     svc,
		Limiter:  access.NewLimiter(),
		Client:   client,
		RepoRoot: roots,
		Limits:   useLimits,
	})
	if err != nil {
		t.Fatalf("githttp: %v", err)
	}
	s.handler = git
	s.server = httptest.NewServer(git)
	t.Cleanup(s.server.Close)
	s.url = s.server.URL
	return s
}

func (s *stack) mintToken(t *testing.T, user auth.User, scopes ...string) userRec {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	parsed, err := auth.ParseScopeSet(scopes)
	if err != nil {
		t.Fatalf("scopes: %v", err)
	}
	secret, err := s.auth.CreateToken(ctx, auth.CreateTokenInput{
		Name: "e2e", Scopes: parsed, UserID: user.ID,
	})
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	rec := userRec{user: user, token: secret.Secret}
	s.users[user.Name] = rec
	return rec
}

func (s *stack) addAdmin(t *testing.T, name string, scopes ...string) userRec {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	user, err := s.auth.BootstrapAdmin(ctx, name, "long-test-password-01", "127.0.0.1")
	if err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}
	return s.mintToken(t, user, scopes...)
}

func (s *stack) addUser(t *testing.T, name string, scopes ...string) userRec {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var adminID int64
	for _, rec := range s.users {
		adminID = rec.user.ID
		break
	}
	if adminID == 0 {
		t.Fatal("bootstrap an admin before adding users")
	}
	user, err := s.auth.CreateUser(ctx, adminID, auth.CreateUserInput{
		Name: name, Email: name + "@example.test", Password: "long-test-password-01",
	}, "127.0.0.1")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return s.mintToken(t, user, scopes...)
}

func (s *stack) addRepo(t *testing.T, owner, name, visibility string) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	rec, ok := s.users[owner]
	if !ok {
		t.Fatalf("unknown user %s", owner)
	}
	now := time.Now().UnixMilli()
	var repoID int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO repos (owner_user_id, org_id, name, visibility, created_at, updated_at) VALUES (?, 0, ?, ?, ?, ?) RETURNING id`,
		rec.user.ID, name, visibility, now, now).Scan(&repoID)
	if err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	repoDir := filepath.Join(s.roots, owner, name+".git")
	seedCtx, seedCancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer seedCancel()
	if out, err := rukproc.Run(seedCtx, rukBin(t), []string{"git", "init", repoDir}, append(os.Environ(), "RUK_COORDINATOR=off")); err != nil {
		t.Fatalf("ruk init: %v\n%s", err, out)
	}
	return repoID
}

func (s *stack) addRule(t *testing.T, repoID int64, pattern, mode string, bypass bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	bypassInt := 0
	if bypass {
		bypassInt = 1
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO bookmark_rules (repo_id, pattern, mode, bypass_admin, created_at) VALUES (?, ?, ?, ?, ?)`,
		repoID, pattern, mode, bypassInt, time.Now().UnixMilli()); err != nil {
		t.Fatalf("insert rule: %v", err)
	}
}

func (s *stack) repoURL(owner, repo, username, token string) string {
	creds := ""
	if username != "" {
		creds = username + ":" + token + "@"
	}
	plain, _ := strings.CutPrefix(s.url, "http://")
	return "http://" + creds + plain + "/" + owner + "/" + repo + ".git"
}

func gitEnv(t *testing.T) []string {
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

func gitRun(ctx context.Context, t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(ctx, gitBin(t), args...)
	cmd.Dir = dir
	cmd.Env = gitEnv(t)
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	err := cmd.Run()
	return combined.String(), err
}

func (s *stack) localRepo(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	dir := t.TempDir()
	if out, err := gitRun(ctx, t, dir, "init", "-b", "main", "."); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if out, err := gitRun(ctx, t, dir, "add", "-A"); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	if out, err := gitRun(ctx, t, dir, "commit", "-m", "seed"); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	out, err := gitRun(ctx, t, dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v\n%s", err, out)
	}
	return dir, strings.TrimSpace(out)
}

func (s *stack) lsRemote(t *testing.T, repoURL string) map[string]string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	out, err := gitRun(ctx, t, t.TempDir(), "ls-remote", repoURL)
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

func httpGet(t *testing.T, url, username, token string) (int, http.Header, string) {
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

func TestGitPushFetchRoundTrip(t *testing.T) {
	s := newStack(t, nil)
	ada := s.addAdmin(t, "ada", "repo:read", "repo:write")
	s.addRepo(t, "ada", "hello", "private")
	work, oid := s.localRepo(t, map[string]string{"README.md": "hello\n"})
	pushURL := s.repoURL("ada", "hello", "ada", ada.token)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	if out, err := gitRun(ctx, t, work, "push", pushURL, "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	refs := s.lsRemote(t, pushURL)
	if refs["refs/heads/main"] != oid {
		t.Fatalf("main = %q, want %q", refs["refs/heads/main"], oid)
	}
	cloneDir := t.TempDir()
	if out, err := gitRun(ctx, t, cloneDir, "clone", pushURL, "hello"); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(filepath.Join(cloneDir, "hello", "README.md"))
	if err != nil {
		t.Fatalf("read clone: %v", err)
	}
	if string(raw) != "hello\n" {
		t.Fatalf("cloned content = %q", raw)
	}
}

func TestGitLargePushStreamsWithinWindows(t *testing.T) {
	s := newStack(t, nil)
	ada := s.addAdmin(t, "ada", "repo:read", "repo:write")
	s.addRepo(t, "ada", "hello", "private")
	raw := make([]byte, 1<<20)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("rand: %v", err)
	}
	work, oid := s.localRepo(t, map[string]string{"big.bin": string(raw)})
	pushURL := s.repoURL("ada", "hello", "ada", ada.token)
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()
	if out, err := gitRun(ctx, t, work, "push", pushURL, "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("large push: %v\n%s", err, out)
	}
	if refs := s.lsRemote(t, pushURL); refs["refs/heads/main"] != oid {
		t.Fatalf("main = %q, want %q", refs["refs/heads/main"], oid)
	}
}

func TestGitProtocolV0Fetch(t *testing.T) {
	s := newStack(t, nil)
	ada := s.addAdmin(t, "ada", "repo:read", "repo:write")
	s.addRepo(t, "ada", "hello", "private")
	work, oid := s.localRepo(t, map[string]string{"README.md": "hello\n"})
	pushURL := s.repoURL("ada", "hello", "ada", ada.token)
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	if out, err := gitRun(ctx, t, work, "push", pushURL, "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	out, err := gitRun(ctx, t, t.TempDir(), "-c", "protocol.version=0", "ls-remote", pushURL)
	if err != nil {
		t.Fatalf("v0 ls-remote: %v\n%s", err, out)
	}
	if !strings.Contains(out, oid) {
		t.Fatalf("v0 ls-remote lacks %s:\n%s", oid, out)
	}
}

func TestConcurrentPushesSerialize(t *testing.T) {
	s := newStack(t, nil)
	ada := s.addAdmin(t, "ada", "repo:read", "repo:write")
	s.addRepo(t, "ada", "hello", "private")
	work, oid := s.localRepo(t, map[string]string{"README.md": "hello\n"})
	pushURL := s.repoURL("ada", "hello", "ada", ada.token)
	const branches = 8
	var wg sync.WaitGroup
	errs := make([]error, branches)
	outputs := make([]string, branches)
	for i := range branches {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			ref := "refs/heads/concurrent-" + strconv.Itoa(i)
			out, err := gitRun(ctx, t, work, "push", pushURL, "HEAD:"+ref)
			outputs[i] = out
			errs[i] = err
		})
	}
	wg.Wait()
	for i := range branches {
		if errs[i] != nil {
			t.Fatalf("push %d: %v\n%s", i, errs[i], outputs[i])
		}
	}
	refs := s.lsRemote(t, pushURL)
	for i := range branches {
		name := "refs/heads/concurrent-" + strconv.Itoa(i)
		if refs[name] != oid {
			t.Fatalf("%s = %q, want %q", name, refs[name], oid)
		}
	}
}

func TestGitBasicAuthUsernameMismatch401(t *testing.T) {
	s := newStack(t, nil)
	ada := s.addAdmin(t, "ada", "repo:read", "repo:write")
	s.addRepo(t, "ada", "hello", "private")
	infoURL := s.url + "/ada/hello.git/info/refs?service=git-upload-pack"
	status, header, _ := httpGet(t, infoURL, "mallory", ada.token)
	if status != http.StatusUnauthorized {
		t.Fatalf("mismatched username status = %d, want 401", status)
	}
	if header.Get("WWW-Authenticate") == "" {
		t.Fatal("401 lacks WWW-Authenticate")
	}
	status, _, _ = httpGet(t, infoURL, "ada", "not-a-token")
	if status != http.StatusUnauthorized {
		t.Fatalf("bad token status = %d, want 401", status)
	}
	status, _, _ = httpGet(t, infoURL, "ada", ada.token)
	if status != http.StatusOK {
		t.Fatalf("matched username status = %d, want 200", status)
	}
}

func TestGitAnonymousReadInternalDenied(t *testing.T) {
	s := newStack(t, nil)
	s.addAdmin(t, "ada", "repo:read", "repo:write")
	mallory := s.addUser(t, "mallory", "repo:read", "repo:write")
	s.addRepo(t, "ada", "internal", "internal")
	s.addRepo(t, "ada", "secret", "private")
	s.addRepo(t, "ada", "public", "public")
	var baseline string
	for _, name := range []string{"internal", "secret", "nosuchrepo"} {
		status, header, body := httpGet(t, s.url+"/ada/"+name+".git/info/refs?service=git-upload-pack", "", "")
		if status != http.StatusUnauthorized {
			t.Fatalf("anonymous %s status = %d, want 401", name, status)
		}
		if header.Get("WWW-Authenticate") == "" {
			t.Fatalf("anonymous %s 401 lacks WWW-Authenticate", name)
		}
		fingerprint := strconv.Itoa(status) + "\n" + header.Get("WWW-Authenticate") + "\n" + body
		if baseline == "" {
			baseline = fingerprint
		} else if fingerprint != baseline {
			t.Fatalf("anonymous %s answer differs from the baseline:\n%q vs %q", name, fingerprint, baseline)
		}
	}
	status, _, _ := httpGet(t, s.url+"/ada/public.git/info/refs?service=git-upload-pack", "", "")
	if status != http.StatusOK {
		t.Fatalf("anonymous public status = %d, want 200", status)
	}
	status, _, _ = httpGet(t, s.url+"/ada/secret.git/info/refs?service=git-upload-pack", "mallory", mallory.token)
	if status != http.StatusNotFound {
		t.Fatalf("stranger on private status = %d, want 404", status)
	}
}

func (s *stack) addGrant(t *testing.T, repoID, userID int64, role string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO repo_grants (repo_id, user_id, role) VALUES (?, ?, ?)`, repoID, userID, role); err != nil {
		t.Fatalf("insert grant: %v", err)
	}
}

func TestGitReadOnlyTokenCannotPush(t *testing.T) {
	s := newStack(t, nil)
	ada := s.addAdmin(t, "ada", "repo:read", "repo:write")
	mallory := s.addUser(t, "mallory", "repo:read")
	repoID := s.addRepo(t, "ada", "hello", "private")
	s.addGrant(t, repoID, mallory.user.ID, "read")
	work, _ := s.localRepo(t, map[string]string{"README.md": "hello\n"})
	pushURL := s.repoURL("ada", "hello", "mallory", mallory.token)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	out, err := gitRun(ctx, t, work, "push", pushURL, "HEAD:refs/heads/main")
	if err == nil {
		t.Fatalf("read-only push succeeded\n%s", out)
	}
	adaPush := s.repoURL("ada", "hello", "ada", ada.token)
	if refs := s.lsRemote(t, adaPush); refs["refs/heads/main"] != "" {
		t.Fatalf("read-only push landed: %q", refs["refs/heads/main"])
	}
}

func TestGitLandOnlyDirectPushDenied(t *testing.T) {
	s := newStack(t, nil)
	ada := s.addAdmin(t, "ada", "repo:read", "repo:write")
	repoID := s.addRepo(t, "ada", "hello", "private")
	s.addRule(t, repoID, "refs/heads/main", "LandOnly", false)
	work, _ := s.localRepo(t, map[string]string{"README.md": "hello\n"})
	pushURL := s.repoURL("ada", "hello", "ada", ada.token)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	out, err := gitRun(ctx, t, work, "push", pushURL, "HEAD:refs/heads/main")
	if err == nil {
		t.Fatalf("direct push to a LandOnly branch succeeded\n%s", out)
	}
	if refs := s.lsRemote(t, pushURL); refs["refs/heads/main"] != "" {
		t.Fatalf("main landed despite the LandOnly rule: %q", refs["refs/heads/main"])
	}
	if out, err := gitRun(ctx, t, work, "push", pushURL, "HEAD:refs/heads/feature"); err != nil {
		t.Fatalf("unprotected branch push failed: %v\n%s", err, out)
	}
}

func TestGitAuthorizesBeforeOpeningStream(t *testing.T) {
	dir := t.TempDir()
	sqlite := filepath.Join(dir, "rukie.sqlite")
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	st, err := store.Open(ctx, store.Options{Path: sqlite})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer func() { _ = st.Close(context.Background()) }()
	svc, err := auth.Open(sqlite)
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	defer func() { _ = svc.Close() }()
	admin, err := svc.BootstrapAdmin(ctx, "ada", "long-test-password-01", "127.0.0.1")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	parsed, err := auth.ParseScopeSet([]string{"repo:read", "repo:write"})
	if err != nil {
		t.Fatalf("scopes: %v", err)
	}
	secret, err := svc.CreateToken(ctx, auth.CreateTokenInput{Name: "e2e", Scopes: parsed, UserID: admin.ID})
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	roots := filepath.Join(dir, "roots")
	repoGit := filepath.Join(roots, "ada", "hello.git", ".ruk")
	if merr := os.MkdirAll(repoGit, 0o755); merr != nil {
		t.Fatalf("mkdir: %v", merr)
	}
	now := time.Now().UnixMilli()
	if _, ierr := svc.DB().ExecContext(ctx, `INSERT INTO repos (owner_user_id, org_id, name, visibility, created_at, updated_at) VALUES (?, 0, ?, ?, ?, ?)`, admin.ID, "hello", "private", now, now); ierr != nil {
		t.Fatalf("insert repo: %v", ierr)
	}
	socket := filepath.Join(dir, "count.sock")
	ln, lerr := net.Listen("unix", socket)
	if lerr != nil {
		t.Fatalf("listen: %v", lerr)
	}
	defer func() { _ = ln.Close() }()
	fake := &countingDaemon{t: t, opens: make(chan struct{}, 64)}
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go fake.serve(conn)
		}
	}()
	dialCtx, dialCancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer dialCancel()
	client, err := ruk.Dial(dialCtx, socket)
	if err != nil {
		t.Fatalf("dial fake daemon: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	git, err := githttp.New(githttp.Config{
		Auth:     svc,
		Limiter:  access.NewLimiter(),
		Client:   client,
		RepoRoot: roots,
		Limits:   wire.DefaultGitLimits(),
	})
	if err != nil {
		t.Fatalf("githttp: %v", err)
	}
	server := httptest.NewServer(git)
	defer server.Close()
	status, _, _ := httpGet(t, server.URL+"/ada/hello.git/info/refs?service=git-upload-pack", "ada", "bad-token")
	if status != http.StatusUnauthorized {
		t.Fatalf("bad token status = %d, want 401", status)
	}
	if n := fake.count(); n != 0 {
		t.Fatalf("unauthorized request opened %d daemon streams", n)
	}
	status, _, _ = httpGet(t, server.URL+"/ada/hello.git/info/refs?service=git-upload-pack", "ada", secret.Secret)
	if status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound {
		t.Fatalf("authorized request denied with %d before any stream", status)
	}
	deadline := time.Now().Add(5 * time.Second)
	for fake.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if fake.count() == 0 {
		t.Fatal("authorized request never reached the daemon")
	}
}

type countingDaemon struct {
	t     *testing.T
	opens chan struct{}
}

func (f *countingDaemon) count() int {
	return len(f.opens)
}

func (f *countingDaemon) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return
	}
	var hdr [13]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return
	}
	total := int(hdr[0])<<24 | int(hdr[1])<<16 | int(hdr[2])<<8 | int(hdr[3])
	payload := make([]byte, total-9)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return
	}
	accept := `{"profile":"rukie","object_formats":["sha1"],"features":["credits","hooks.v1","events.v1","archive.v1"],"protocol_major":1,"protocol_minor":1}`
	frame := make([]byte, 0, 13+len(accept))
	n := 9 + len(accept)
	frame = append(frame, byte(n>>24), byte(n>>16), byte(n>>8), byte(n), 2, 0, 0, 0, 0, 0, 0, 0, 0)
	frame = append(frame, accept...)
	if _, err := conn.Write(frame); err != nil {
		return
	}
	for {
		if _, err := io.ReadFull(conn, hdr[:]); err != nil {
			return
		}
		total := int(hdr[0])<<24 | int(hdr[1])<<16 | int(hdr[2])<<8 | int(hdr[3])
		kind := hdr[4]
		payload := make([]byte, total-9)
		if _, err := io.ReadFull(conn, payload); err != nil {
			return
		}
		if kind == 1 {
			select {
			case f.opens <- struct{}{}:
			default:
			}
			return
		}
	}
}

func TestGitStreamLimitsRefuseOversizePush(t *testing.T) {
	limits := wire.DefaultGitLimits()
	limits.MaxStdinBytes = 64
	s := newStack(t, &limits)
	ada := s.addAdmin(t, "ada", "repo:read", "repo:write")
	s.addRepo(t, "ada", "hello", "private")
	work, _ := s.localRepo(t, map[string]string{"README.md": "hello\n"})
	pushURL := s.repoURL("ada", "hello", "ada", ada.token)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	out, err := gitRun(ctx, t, work, "push", pushURL, "HEAD:refs/heads/main")
	if err == nil {
		t.Fatalf("oversize push succeeded\n%s", out)
	}
	status, _, body := httpGet(t, s.url+"/ada/hello.git/info/refs?service=git-upload-pack", "ada", ada.token)
	if status != http.StatusOK {
		t.Fatalf("advertise status = %d, want 200", status)
	}
	if strings.Contains(body, " refs/heads/main\n") {
		t.Fatalf("oversize push landed: %q", body)
	}
}

func TestGitChunkedOverflowRefused413(t *testing.T) {
	limits := wire.DefaultGitLimits()
	limits.MaxStdinBytes = 64
	s := newStack(t, &limits)
	ada := s.addAdmin(t, "ada", "repo:read", "repo:write")
	s.addRepo(t, "ada", "hello", "private")
	req, err := http.NewRequest(http.MethodPost, s.url+"/ada/hello.git/git-receive-pack", io.NopCloser(strings.NewReader(strings.Repeat("a", 256))))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.ContentLength = -1
	req.SetBasicAuth("ada", ada.token)
	req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked overflow status = %d, want 413", resp.StatusCode)
	}
}

func TestGitIngestLimitsRefuseTightPush(t *testing.T) {
	withLimits := func(t *testing.T, mutate func(*wire.GitLimits)) (*stack, userRec) {
		t.Helper()
		limits := wire.DefaultGitLimits()
		mutate(&limits)
		s := newStack(t, &limits)
		ada := s.addAdmin(t, "ada", "repo:read", "repo:write")
		s.addRepo(t, "ada", "hello", "private")
		return s, ada
	}
	pushMain := func(t *testing.T, s *stack, ada userRec, work string) (string, error) {
		t.Helper()
		pushURL := s.repoURL("ada", "hello", "ada", ada.token)
		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		defer cancel()
		return gitRun(ctx, t, work, "push", pushURL, "HEAD:refs/heads/main")
	}
	refAbsent := func(t *testing.T, s *stack, ada userRec) {
		t.Helper()
		adaPush := s.repoURL("ada", "hello", "ada", ada.token)
		if refs := s.lsRemote(t, adaPush); refs["refs/heads/main"] != "" {
			t.Fatalf("tight push landed: %q", refs["refs/heads/main"])
		}
	}
	t.Run("pack_bytes", func(t *testing.T) {
		s, ada := withLimits(t, func(l *wire.GitLimits) { l.MaxPackBytes = 64 })
		work, _ := s.localRepo(t, map[string]string{"README.md": "hello\n"})
		if out, err := pushMain(t, s, ada, work); err == nil {
			t.Fatalf("pack-bytes-tight push succeeded\n%s", out)
		}
		refAbsent(t, s, ada)
	})
	t.Run("objects", func(t *testing.T) {
		s, ada := withLimits(t, func(l *wire.GitLimits) { l.MaxObjects = 2 })
		work, _ := s.localRepo(t, map[string]string{"README.md": "hello\n"})
		if out, err := pushMain(t, s, ada, work); err == nil {
			t.Fatalf("object-tight push succeeded\n%s", out)
		}
		refAbsent(t, s, ada)
	})
	t.Run("object_bytes", func(t *testing.T) {
		s, ada := withLimits(t, func(l *wire.GitLimits) { l.MaxObjectBytes = 1 })
		work, _ := s.localRepo(t, map[string]string{"README.md": "hello\n"})
		if out, err := pushMain(t, s, ada, work); err == nil {
			t.Fatalf("object-bytes-tight push succeeded\n%s", out)
		}
		refAbsent(t, s, ada)
	})
	t.Run("delta_depth", func(t *testing.T) {
		s, ada := withLimits(t, func(l *wire.GitLimits) { l.MaxDeltaDepth = 0 })
		pushURL := s.repoURL("ada", "hello", "ada", ada.token)
		ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
		defer cancel()
		work, _ := s.localRepo(t, map[string]string{"big.txt": strings.Repeat("lorem ipsum dolor sit amet\n", 800)})
		if out, err := gitRun(ctx, t, work, "push", pushURL, "HEAD:refs/heads/main"); err != nil {
			t.Fatalf("delta-free base push failed: %v\n%s", err, out)
		}
		changed := strings.Repeat("lorem ipsum dolor sit amet\n", 400) + "lorem IPSUM changed\n" + strings.Repeat("lorem ipsum dolor sit amet\n", 399)
		if err := os.WriteFile(filepath.Join(work, "big.txt"), []byte(changed), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if out, err := gitRun(ctx, t, work, "add", "-A"); err != nil {
			t.Fatalf("add: %v\n%s", err, out)
		}
		if out, err := gitRun(ctx, t, work, "commit", "-m", "tweak"); err != nil {
			t.Fatalf("commit: %v\n%s", err, out)
		}
		if out, err := gitRun(ctx, t, work, "push", pushURL, "HEAD:refs/heads/main"); err == nil {
			t.Fatalf("delta-depth-tight push succeeded\n%s", out)
		}
	})
	t.Run("push_options_zero_refuses", func(t *testing.T) {
		s, ada := withLimits(t, func(l *wire.GitLimits) { l.MaxPushOptions = 0 })
		work, _ := s.localRepo(t, map[string]string{"README.md": "hello\n"})
		pushURL := s.repoURL("ada", "hello", "ada", ada.token)
		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		defer cancel()
		if out, err := gitRun(ctx, t, work, "push", "--push-option=ci.skip", pushURL, "HEAD:refs/heads/main"); err == nil {
			t.Fatalf("zero-option-quota push succeeded\n%s", out)
		}
		refAbsent(t, s, ada)
	})
	t.Run("push_options_one_lands", func(t *testing.T) {
		s, ada := withLimits(t, func(l *wire.GitLimits) { l.MaxPushOptions = 1 })
		work, oid := s.localRepo(t, map[string]string{"README.md": "hello\n"})
		pushURL := s.repoURL("ada", "hello", "ada", ada.token)
		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		defer cancel()
		if out, err := gitRun(ctx, t, work, "push", pushURL, "HEAD:refs/heads/main"); err != nil {
			t.Fatalf("at-limit push failed: %v\n%s", err, out)
		}
		if refs := s.lsRemote(t, pushURL); refs["refs/heads/main"] != oid {
			t.Fatalf("main = %q, want %q", refs["refs/heads/main"], oid)
		}
	})
}

func TestRefsForRejectedUntilReview(t *testing.T) {
	s := newStack(t, nil)
	ada := s.addAdmin(t, "ada", "repo:read", "repo:write")
	s.addRepo(t, "ada", "hello", "private")
	work, _ := s.localRepo(t, map[string]string{"README.md": "hello\n"})
	pushURL := s.repoURL("ada", "hello", "ada", ada.token)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	out, err := gitRun(ctx, t, work, "push", pushURL, "HEAD:refs/for/main")
	if err == nil {
		t.Fatalf("refs/for push succeeded without review\n%s", out)
	}
	if !strings.Contains(out, "change review is not available") {
		t.Fatalf("push refusal lacks the pinned reason:\n%s", out)
	}
	refs := s.lsRemote(t, pushURL)
	if refs["refs/for/main"] != "" {
		t.Fatalf("refs/for/main landed as a real ref: %q", refs["refs/for/main"])
	}
}

func TestGitServiceUnknownServiceRefused(t *testing.T) {
	s := newStack(t, nil)
	s.addAdmin(t, "ada", "repo:read", "repo:write")
	s.addRepo(t, "ada", "public", "public")
	status, _, _ := httpGet(t, s.url+"/ada/public.git/info/refs?service=git-upload-archive", "", "")
	if status != http.StatusForbidden {
		t.Fatalf("unknown service status = %d, want 403", status)
	}
}

func TestNoGitProcessInBoundary(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller unavailable")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	cmd := exec.Command("go", "list", "-f", "{{join .Imports \" \"}}", "./internal/githttp")
	cmd.Dir = root
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for imp := range strings.FieldsSeq(string(out)) {
		if imp == "os/exec" {
			t.Fatal("internal/githttp imports os/exec directly")
		}
	}
	if runtime.GOOS != "linux" {
		t.Skip("process boundary check needs /proc")
	}
	s := newStack(t, nil)
	ada := s.addAdmin(t, "ada", "repo:read", "repo:write")
	s.addRepo(t, "ada", "hello", "private")
	work, _ := s.localRepo(t, map[string]string{"README.md": "hello\n"})
	pushURL := s.repoURL("ada", "hello", "ada", ada.token)
	self := os.Getpid()
	before, err := childNames(self)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	cloneParent := t.TempDir()
	if pout, perr := gitRun(ctx, t, work, "push", pushURL, "HEAD:refs/heads/main"); perr != nil {
		t.Fatalf("push: %v\n%s", perr, pout)
	}
	if cout, cerr := gitRun(ctx, t, cloneParent, "clone", pushURL, "hello"); cerr != nil {
		t.Fatalf("clone: %v\n%s", cerr, cout)
	}
	after, err := childNames(self)
	if err != nil {
		t.Fatalf("resnapshot: %v", err)
	}
	for pid, name := range after {
		if _, ok := before[pid]; !ok && name != "git" && !strings.HasSuffix(name, "/git") {
			t.Fatalf("new child process %d (%s) spawned during clone and push", pid, name)
		}
	}
}

func childNames(self int) (map[int]string, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	out := map[int]string{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == self {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}
		text := string(raw)
		_, after, ok := strings.CutLast(text, ")")
		if !ok {
			continue
		}
		fields := strings.Fields(after)
		if len(fields) < 2 {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil || ppid != self {
			continue
		}
		comm, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "comm"))
		if err != nil {
			continue
		}
		out[pid] = strings.TrimSpace(string(comm))
	}
	return out, nil
}
