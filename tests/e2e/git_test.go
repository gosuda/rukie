//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
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
	"github.com/gosuda/rukie/internal/e2estack"
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
	shared  *e2estack.Stack
	server  *httptest.Server
	auth    *auth.Service
	st      *store.Store
	client  *ruk.Client
	child   *rukproc.Child
	handler http.Handler
	users   map[string]userRec
	dir     string
	roots   string
	socket  string
	url     string
}

var gitBin = e2estack.GitBin
var rukBin = e2estack.RukBin
var gitEnv = e2estack.GitEnv
var gitRun = e2estack.GitRun
var httpGet = e2estack.HTTPGet

func adaptStack(t *testing.T, shared *e2estack.Stack) *stack {
	t.Helper()
	return &stack{shared: shared, server: shared.Server, auth: shared.Auth,
		st: shared.Store, client: shared.Client, child: shared.Child, handler: shared.Handler,
		users: map[string]userRec{}, dir: shared.Dir, roots: shared.Roots, socket: shared.Socket, url: shared.URL}
}

func newStack(t *testing.T, limits *wire.GitLimits) *stack {
	return adaptStack(t, e2estack.New(t, limits))
}

func newStackWithHTTPCap(t *testing.T, limits *wire.GitLimits, httpCap uint64) *stack {
	return adaptStack(t, e2estack.NewWithHTTPCap(t, limits, httpCap))
}

func newStackNoAdmin(t *testing.T, limits *wire.GitLimits) *stack {
	return adaptStack(t, e2estack.NewNoAdmin(t, limits))
}

func newStackWithConfig(t *testing.T, limits *wire.GitLimits, extraConfig string, httpCap uint64) *stack {
	return adaptStack(t, e2estack.NewWithConfig(t, limits, extraConfig, httpCap))
}

func (s *stack) record(rec e2estack.User) userRec {
	user := userRec{token: rec.Token, user: rec.User}
	s.users[user.user.Name] = user
	return user
}

func (s *stack) addAdmin(t *testing.T, name string, scopes ...string) userRec {
	return s.record(s.shared.AddAdmin(t, name, scopes...))
}

func (s *stack) addUser(t *testing.T, name string, scopes ...string) userRec {
	return s.record(s.shared.AddUser(t, name, scopes...))
}

func (s *stack) addRepo(t *testing.T, owner, name, visibility string) int64 {
	return s.shared.AddRepo(t, owner, name, visibility)
}

func (s *stack) addRule(t *testing.T, repoID int64, pattern, mode string) {
	s.shared.AddRule(t, repoID, pattern, mode)
}

func (s *stack) addGrant(t *testing.T, repoID, userID int64, role string) {
	s.shared.AddGrant(t, repoID, userID, role)
}

func (s *stack) repoURL(owner, repo, username, token string) string {
	return s.shared.RepoURL(owner, repo, username, token)
}

func (s *stack) localRepo(t *testing.T, files map[string]string) (string, string) {
	return s.shared.LocalRepo(t, files)
}

func (s *stack) lsRemote(t *testing.T, url string) map[string]string {
	return s.shared.LsRemote(t, url)
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
	postPin := func(path string) {
		t.Helper()
		first := true
		var baseline string
		for _, name := range []string{"internal", "secret", "nosuchrepo"} {
			req, err := http.NewRequest(http.MethodPost, s.url+"/ada/"+name+".git/"+path, strings.NewReader("0000"))
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("post: %v", err)
			}
			var body bytes.Buffer
			_, _ = body.ReadFrom(resp.Body)
			_ = resp.Body.Close()
			fingerprint := strconv.Itoa(resp.StatusCode) + "\n" + resp.Header.Get("WWW-Authenticate") + "\n" + body.String()
			if first {
				if resp.StatusCode != http.StatusUnauthorized {
					t.Fatalf("anonymous POST %s status = %d, want 401", name, resp.StatusCode)
				}
				baseline = fingerprint
				first = false
			} else if fingerprint != baseline {
				t.Fatalf("anonymous POST %s answer differs:\n%q vs %q", name, fingerprint, baseline)
			}
		}
	}
	postPin("git-receive-pack")
	status, _, _ = httpGet(t, s.url+"/ada/secret.git/info/refs?service=git-upload-pack", "mallory", mallory.token)
	if status != http.StatusNotFound {
		t.Fatalf("stranger on private status = %d, want 404", status)
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
	s.addRule(t, repoID, "refs/heads/main", "LandOnly")
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
	svc := auth.New(st)
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
	if ierr := st.Write(ctx, func(tx *store.WriteTx) error {
		_, ierr := tx.InsertRepo(time.Now(), store.RepoInsert{
			OwnerUserID:   sql.NullInt64{Int64: admin.ID, Valid: true},
			Name:          "hello",
			Visibility:    "private",
			DefaultBranch: "main",
			RukPath:       "ada/hello.git",
		})
		return ierr
	}); ierr != nil {
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
	t.Run("push_options_one_option_at_quota_1_lands", func(t *testing.T) {
		s, ada := withLimits(t, func(l *wire.GitLimits) { l.MaxPushOptions = 1 })
		work, oid := s.localRepo(t, map[string]string{"README.md": "hello\n"})
		pushURL := s.repoURL("ada", "hello", "ada", ada.token)
		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		defer cancel()
		if out, err := gitRun(ctx, t, work, "push", "--push-option=ci.skip", pushURL, "HEAD:refs/heads/main"); err != nil {
			t.Fatalf("one option at quota 1 failed: %v\n%s", err, out)
		}
		if refs := s.lsRemote(t, pushURL); refs["refs/heads/main"] != oid {
			t.Fatalf("main = %q, want %q", refs["refs/heads/main"], oid)
		}
	})
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
	direct := exec.Command("go", "list", "-f", "{{join .Imports \" \"}}", "./internal/githttp")
	direct.Dir = root
	direct.Env = os.Environ()
	out, err := direct.Output()
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
	baseline, err := childNames(self)
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	var mu sync.Mutex
	seen := map[int]struct{}{}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if kids, err := childNames(self); err == nil {
				mu.Lock()
				for pid := range kids {
					seen[pid] = struct{}{}
				}
				mu.Unlock()
			}
			time.Sleep(time.Millisecond)
		}
	})
	var pidsMu sync.Mutex
	own := map[int]struct{}{}
	run := func(ctx context.Context, dir string, args ...string) (string, error) {
		t.Helper()
		cmd := exec.CommandContext(ctx, gitBin(t), args...)
		cmd.Dir = dir
		cmd.Env = gitEnv(t)
		var combined bytes.Buffer
		cmd.Stdout = &combined
		cmd.Stderr = &combined
		if err := cmd.Start(); err != nil {
			return combined.String(), err
		}
		pidsMu.Lock()
		own[cmd.Process.Pid] = struct{}{}
		pidsMu.Unlock()
		waitErr := cmd.Wait()
		return combined.String(), waitErr
	}
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	cloneParent := t.TempDir()
	if pout, perr := run(ctx, work, "push", pushURL, "HEAD:refs/heads/main"); perr != nil {
		t.Fatalf("push: %v\n%s", perr, pout)
	}
	if cout, cerr := run(ctx, cloneParent, "clone", pushURL, "hello"); cerr != nil {
		t.Fatalf("clone: %v\n%s", cerr, cout)
	}
	close(stop)
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	for pid := range seen {
		if _, ok := baseline[pid]; ok {
			continue
		}
		pidsMu.Lock()
		_, isOwn := own[pid]
		pidsMu.Unlock()
		if !isOwn {
			t.Fatalf("non-git child process %d appeared during clone and push", pid)
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
