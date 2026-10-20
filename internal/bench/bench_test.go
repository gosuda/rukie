//go:build perf

package bench

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/api"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/githttp"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/queue"
	"github.com/gosuda/rukie/internal/repowatch"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/rukproc"
	"github.com/gosuda/rukie/internal/store"
)

const (
	warmups = 100
	samples = 1000
)

func nearestRank(durations []time.Duration, quantile float64) time.Duration {
	sorted := slices.Clone(durations)
	slices.SortFunc(sorted, func(a, b time.Duration) int {
		switch {
		case a < b:
			return -1
		case a > b:
			return 1
		default:
			return 0
		}
	})
	index := int(float64(len(sorted))*quantile + 0.999999999)
	if index < 1 {
		index = 1
	}
	if index > len(sorted) {
		index = len(sorted)
	}
	return sorted[index-1]
}

func measure(t *testing.T, name string, budget time.Duration, run func()) {
	t.Helper()
	for range warmups {
		run()
	}
	durations := make([]time.Duration, 0, samples)
	for range samples {
		start := time.Now()
		run()
		durations = append(durations, time.Since(start))
	}
	p95 := nearestRank(durations, 0.95)
	p50 := nearestRank(durations, 0.5)
	t.Logf("%s: p50=%s p95=%s budget=%s", name, p50, p95, budget)
	if p95 > budget {
		t.Fatalf("%s: p95 %s exceeds budget %s", name, p95, budget)
	}
}

func TestPerfBudget(t *testing.T) {
	bin := os.Getenv("RUK_BIN")
	if bin == "" {
		t.Skip("RUK_BIN is not set")
	}
	h := liveStack(t, bin)
	measure(t, "repo-get-json", 100*time.Millisecond, func() {
		h.get(t, "/ada/hello", "application/json", http.StatusOK)
	})
	measure(t, "repo-get-html", 200*time.Millisecond, func() {
		h.get(t, "/ada/hello", "text/html", http.StatusOK)
	})
	measure(t, "tree-json", 100*time.Millisecond, func() {
		h.get(t, "/ada/hello/tree/main", "application/json", http.StatusOK)
	})
	measure(t, "blob-html", 200*time.Millisecond, func() {
		h.get(t, "/ada/hello/blob/main/README.md", "text/html", http.StatusOK)
	})
	measure(t, "info-refs", 150*time.Millisecond, func() {
		h.infoRefs(t)
	})
}

type liveHarness struct {
	api string
	git string
}

func (h *liveHarness) get(t *testing.T, target, accept string, want int) {
	t.Helper()
	req, err := http.NewRequest("GET", h.api+target, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Accept", accept)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("GET %s %s = %d, want %d", target, accept, resp.StatusCode, want)
	}
}

func (h *liveHarness) infoRefs(t *testing.T) {
	t.Helper()
	req, err := http.NewRequest("GET", h.git+"/ada/hello.git/info/refs?service=git-upload-pack", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("info/refs = %d", resp.StatusCode)
	}
}

func liveStack(t *testing.T, bin string) *liveHarness {
	t.Helper()
	ctx := t.Context()
	dir := t.TempDir()
	roots := filepath.Join(dir, "roots")
	if err := os.MkdirAll(filepath.Join(roots, "ada"), 0o755); err != nil {
		t.Fatalf("roots: %v", err)
	}
	socket := filepath.Join(dir, "ruk.sock")
	config := fmt.Sprintf("socket_path = %q\nrepo_roots = [%q]\nallowed_uids = [%d]\nadmin_uids = [%d]\nframe_timeout_ms = 5000\nhook_timeout_ms = 5000\n",
		socket, roots, os.Getuid(), os.Getuid())
	configPath := filepath.Join(dir, "daemon.toml")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("daemon config: %v", err)
	}
	var logs bytes.Buffer
	child, err := rukproc.StartServe(bin, configPath, os.Environ(), &logs, &logs)
	if err != nil {
		t.Fatalf("daemon start: %v\n%s", err, logs.String())
	}
	t.Cleanup(func() { _ = child.Kill() })
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, serr := os.Stat(socket); serr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("socket never appeared\n%s", logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	client, err := ruk.Dial(dialCtx, socket)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	sqlite := filepath.Join(dir, "bench.sqlite")
	st, err := store.Open(ctx, store.Options{Path: sqlite})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	svc, err := auth.Open(sqlite)
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	admin, err := svc.BootstrapAdmin(ctx, "ada", "long-test-password-01", "127.0.0.1")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	seedBenchRepo(t, ctx, st, client, seedConfig{bin: bin, roots: roots, ownerID: admin.ID})
	deps := api.Deps{
		Service: svc, Limiter: access.NewLimiter(), Reveals: auth.NewRevealCache(),
		Store: st, Ruk: client, Queue: queue.Open(st, time.Now),
		RepoRoot: roots, RukBin: bin,
	}
	watcher := repowatch.New(repowatch.Config{Store: st, Client: client, RepoRoot: roots, Clock: time.Now})
	deps.OnRepoCreated = func(repoID int64) { watcher.Start(ctx, repoID) }
	deps.OnRepoDeleted = watcher.Stop
	table := op.NewTable()
	docs := &api.DocStore{}
	for _, step := range []func() error{
		func() error { return api.RegisterAll(table, docs) },
		func() error { return api.RegisterAuth(table, deps) },
		func() error { return api.RegisterUser(table, deps) },
		func() error { return api.RegisterAdmin(table, deps) },
		func() error { return api.RegisterRepos(table, deps) },
		func() error { return api.RegisterCode(table, deps) },
		func() error { return api.RegisterRefs(table, deps) },
	} {
		if err := step(); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	views := op.NewViews()
	api.RegisterViews(views)
	api.RegisterAuthViews(views)
	api.RegisterUserViews(views)
	api.RegisterAdminViews(views)
	api.RegisterReposViews(views)
	api.RegisterCodeViews(views)
	api.RegisterRefsViews(views)
	var key [32]byte
	_, _ = rand.Read(key[:])
	mux := http.NewServeMux()
	mounted, err := op.Mount(mux, table, op.Config{
		Authenticator: auth.NewAuthenticator(svc),
		Idempotency:   op.NewMemoryIdempotency(),
		Audit:         op.FuncAuditor(func(context.Context, op.AuditRow) error { return nil }),
		Limiter:       deps.Limiter,
		CSRFKey:       key[:],
		Views:         views,
		Title:         "bench",
		Version:       "0",
		IsSiteSegment: api.SiteSegment,
	})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	apiServer := httptest.NewServer(api.RawBytes{Deps: deps}.Wrap(mounted))
	t.Cleanup(apiServer.Close)
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
	gitServer := httptest.NewServer(git)
	t.Cleanup(gitServer.Close)
	return &liveHarness{api: apiServer.URL, git: gitServer.URL}
}

type seedConfig struct {
	bin     string
	roots   string
	ownerID int64
}

func seedBenchRepo(t *testing.T, ctx context.Context, st *store.Store, client *ruk.Client, cfg seedConfig) {
	t.Helper()
	bin := cfg.bin
	roots := cfg.roots
	ownerID := cfg.ownerID
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_SYSTEM=" + os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"RUK_COORDINATOR=off",
		"LANG=C",
		"GIT_AUTHOR_NAME=bench",
		"GIT_AUTHOR_EMAIL=bench@example.test",
		"GIT_COMMITTER_NAME=bench",
		"GIT_COMMITTER_EMAIL=bench@example.test",
	}
	repoDir := filepath.Join(roots, "ada", "hello.git")
	if out, err := rukproc.Run(ctx, bin, []string{"git", "init", repoDir}, env); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	blobFile := filepath.Join(t.TempDir(), "blob")
	if err := os.WriteFile(blobFile, []byte("hello\n"), 0o600); err != nil {
		t.Fatalf("blob file: %v", err)
	}
	blobOut, err := rukproc.Run(ctx, bin, []string{"git", "--git-dir=" + repoDir, "hash-object", "-w", blobFile}, env)
	if err != nil {
		t.Fatalf("hash-object: %v", err)
	}
	blob := strings.TrimSpace(string(blobOut))
	treeFile := filepath.Join(t.TempDir(), "tree")
	blobRaw := mustHex(t, blob)
	entry := append([]byte("100644 README.md\x00"), blobRaw...)
	if err := os.WriteFile(treeFile, entry, 0o600); err != nil {
		t.Fatalf("tree file: %v", err)
	}
	treeOut, err := rukproc.Run(ctx, bin, []string{"git", "--git-dir=" + repoDir, "hash-object", "-w", "-t", "tree", treeFile}, env)
	if err != nil {
		t.Fatalf("mktree: %v", err)
	}
	tree := strings.TrimSpace(string(treeOut))
	commitOut, err := rukproc.Run(ctx, bin, []string{"git", "--git-dir=" + repoDir, "commit-tree", tree, "-m", "seed"}, env)
	if err != nil {
		t.Fatalf("commit-tree: %v", err)
	}
	commit := strings.TrimSpace(string(commitOut))
	repo, err := client.OpenRepo(ctx, filepath.Join(repoDir, ".ruk"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	if _, err := ruk.Call[map[string]any, map[string]any](ctx, client, repo, wire.MethodBookmarkSet, map[string]any{
		"name":     "refs/heads/main",
		"target":   map[string]any{"commit": map[string]any{"oid": commit}},
		"expected": "absent",
	}); err != nil {
		t.Fatalf("bookmark.set: %v", err)
	}
	now := time.Now()
	var repoID int64
	if err := st.Write(ctx, func(tx *store.WriteTx) error {
		var ierr error
		repoID, ierr = tx.InsertRepo(now, store.RepoInsert{
			OwnerUserID:   int64OrNull(ownerID),
			Name:          "hello",
			Visibility:    "private",
			Topics:        "[]",
			DefaultBranch: "main",
			RukPath:       "ada/hello.git",
		})
		if ierr != nil {
			return ierr
		}
		return tx.InsertDefaultRule(repoID, now, "main")
	}); err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	_ = repoID
}

func int64OrNull(id int64) sql.NullInt64 {
	return sql.NullInt64{Int64: id, Valid: true}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	raw := make([]byte, len(s)/2)
	for i := range raw {
		hi := hexValue(s[2*i])
		lo := hexValue(s[2*i+1])
		raw[i] = hi<<4 | lo
	}
	return raw
}

func hexValue(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return 0
	}
}
