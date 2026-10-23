//go:build perf

package perf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/e2estack"
	"github.com/gosuda/rukie/internal/ruk"
)

const (
	fetchRatioBudget  = 7.5
	lookupRatioBudget = 4.0
	infoRefsBudget    = 150 * time.Millisecond
	warmupCount       = 100
	sampleCount       = 1000
	cloneWarmups      = 2
	cloneSamples      = 200
	seedCommits       = 200
	seedRefs          = 1000
	filesPerCommit    = 2
	seedBranches      = 4
	cgiUser           = "cgi-user"
	cgiToken          = "cgi-token"
	incrementalRefs   = warmupCount + sampleCount
)

type samples []time.Duration

func (s samples) p(q float64) time.Duration {
	sorted := slices.Clone(s)
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
	if len(sorted) == 0 {
		return 0
	}
	index := int(float64(len(sorted))*q + 0.999999999)
	index = min(max(index, 1), len(sorted))
	return sorted[index-1]
}

type benchWorkload struct {
	prepare func(context.Context) error
	run     func(context.Context) error
	verify  func(context.Context) error
}

type benchCase struct {
	stock      benchWorkload
	daemon     benchWorkload
	warmups    int
	total      int
	setup      func(context.Context, int) error
	after      func(context.Context) error
	name       string
	budget     string
	authority  string
	ratioMax   float64
	limitMax   time.Duration
	reportOnly bool
}

type benchRow struct {
	stock     samples
	name      string
	budget    string
	authority string
	verdict   string
	daemon    samples
	ratio     float64
}

type runMeta struct {
	rukBin     string
	rukVersion string
	rukCommit  string
	rukSchema  string
	gitBin     string
	host       string
	cpu        string
	goVersion  string
}

type tokenRing struct {
	tokens []string
	n      atomic.Uint64
}

func (r *tokenRing) next() string {
	return r.tokens[r.n.Add(1)%uint64(len(r.tokens))]
}

type gitRunner struct {
	bin string
	env []string
}

func (g gitRunner) run(ctx context.Context, dir string, args ...string) error {
	_, err := g.output(ctx, dir, args...)
	return err
}

func (g gitRunner) output(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, g.bin, args...)
	cmd.Dir = dir
	cmd.Env = g.env
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	if err := cmd.Run(); err != nil {
		return combined.String(), fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, combined.String())
	}
	return combined.String(), nil
}

type refLookup struct {
	client *ruk.Client
	repo   ruk.Repo
}

func (l refLookup) main(ctx context.Context) (string, error) {
	reply, err := ruk.RefsList(ctx, l.client, l.repo, ruk.RefsListArgs{Prefix: "refs/heads/main", Limit: 1})
	if err != nil {
		return "", fmt.Errorf("refs.list: %w", err)
	}
	if len(reply.Refs.Items) == 0 {
		return "", fmt.Errorf("refs.list: no ref under refs/heads/main")
	}
	item := reply.Refs.Items[0]
	if item.Name != "refs/heads/main" {
		return "", fmt.Errorf("refs.list: name %q, want refs/heads/main", item.Name)
	}
	return item.Target.Hex(), nil
}

func benchEnv() []string {
	return append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=bench",
		"GIT_AUTHOR_EMAIL=bench@example.test",
		"GIT_COMMITTER_NAME=bench",
		"GIT_COMMITTER_EMAIL=bench@example.test",
		"LANG=C",
	)
}

func getenvOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func seedRepo(ctx context.Context, r gitRunner, dir, pushURL string) error {
	if err := r.run(ctx, dir, "init", "-b", "main", "."); err != nil {
		return fmt.Errorf("seed init: %w", err)
	}
	oids := make([]string, 0, seedCommits)
	branchEvery := seedCommits / seedBranches
	for c := range seedCommits {
		for f := range filesPerCommit {
			size := 1024 << uint((c+f)%6)
			path := filepath.Join(dir, fmt.Sprintf("docs/file-%03d-%d.txt", c, f))
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				return fmt.Errorf("seed mkdir: %w", err)
			}
			if err := os.WriteFile(path, bytes.Repeat([]byte{byte('a' + (c+f)%6)}, size), 0o600); err != nil {
				return fmt.Errorf("seed write: %w", err)
			}
		}
		if c%10 == 0 {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("docs/big-%03d.bin", c)), bytes.Repeat([]byte{byte('A' + c%26)}, 256*1024), 0o600); err != nil {
				return fmt.Errorf("seed big write: %w", err)
			}
		}
		if err := r.run(ctx, dir, "add", "-A"); err != nil {
			return fmt.Errorf("seed add %d: %w", c, err)
		}
		if err := r.run(ctx, dir, "commit", "-m", fmt.Sprintf("seed %d", c)); err != nil {
			return fmt.Errorf("seed commit %d: %w", c, err)
		}
		out, err := r.output(ctx, dir, "rev-parse", "HEAD")
		if err != nil {
			return fmt.Errorf("seed rev-parse %d: %w", c, err)
		}
		oids = append(oids, strings.TrimSpace(out))
		if (c+1)%branchEvery == 0 {
			branch := fmt.Sprintf("feature-%d", (c+1)/branchEvery)
			if err := r.run(ctx, dir, "branch", branch); err != nil {
				return fmt.Errorf("seed branch %s: %w", branch, err)
			}
		}
	}
	for i := range seedRefs {
		if err := r.run(ctx, dir, "tag", fmt.Sprintf("perf-tag-%04d", i), oids[i%len(oids)]); err != nil {
			return fmt.Errorf("seed tag %d: %w", i, err)
		}
	}
	refs := []string{"push", pushURL, "main", "refs/tags/perf-tag-*:refs/tags/perf-tag-*"}
	for b := range seedBranches {
		refs = append(refs, fmt.Sprintf("feature-%d", b+1))
	}
	if err := r.run(ctx, dir, refs...); err != nil {
		return fmt.Errorf("seed push: %w", err)
	}
	return nil
}

type incrPlan struct {
	runner  gitRunner
	refs    []string
	oids    []string
	current int
}

func seedIncremental(ctx context.Context, runner gitRunner, donor, stockURL, daemonURL string) (*incrPlan, error) {
	plan := &incrPlan{runner: runner, refs: make([]string, 0, incrementalRefs), oids: make([]string, 0, incrementalRefs)}
	for i := range incrementalRefs {
		n := i + 1
		name := fmt.Sprintf("bench-incr/%04d.txt", n)
		path := filepath.Join(donor, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, fmt.Errorf("incremental mkdir: %w", err)
		}
		if err := os.WriteFile(path, []byte(fmt.Sprintf("incremental %d\n", n)), 0o600); err != nil {
			return nil, fmt.Errorf("incremental write: %w", err)
		}
		if err := runner.run(ctx, donor, "add", "-A"); err != nil {
			return nil, fmt.Errorf("incremental add %d: %w", n, err)
		}
		if err := runner.run(ctx, donor, "commit", "-m", fmt.Sprintf("incremental %d", n)); err != nil {
			return nil, fmt.Errorf("incremental commit %d: %w", n, err)
		}
		ref := fmt.Sprintf("refs/heads/bench-incr/%04d", n)
		if err := runner.run(ctx, donor, "branch", strings.TrimPrefix(ref, "refs/heads/")); err != nil {
			return nil, fmt.Errorf("incremental branch %d: %w", n, err)
		}
		oid, err := runner.output(ctx, donor, "rev-parse", "HEAD")
		if err != nil {
			return nil, fmt.Errorf("incremental oid %d: %w", n, err)
		}
		plan.refs = append(plan.refs, ref)
		plan.oids = append(plan.oids, strings.TrimSpace(oid))
	}
	for start := 0; start < len(plan.refs); start += 100 {
		end := min(start+100, len(plan.refs))
		refspecs := make([]string, 0, end-start)
		for _, ref := range plan.refs[start:end] {
			refspecs = append(refspecs, ref+":"+ref)
		}
		if err := runner.run(ctx, donor, append([]string{"push", stockURL}, refspecs...)...); err != nil {
			return nil, fmt.Errorf("incremental push stock: %w", err)
		}
		if err := runner.run(ctx, donor, append([]string{"push", daemonURL}, refspecs...)...); err != nil {
			return nil, fmt.Errorf("incremental push daemon: %w", err)
		}
	}
	return plan, nil
}

func (p *incrPlan) selectSample(index int) {
	p.current = index
}

func (p *incrPlan) fetch(receiver string, url string) func(context.Context) error {
	return func(ctx context.Context) error {
		ref := strings.TrimPrefix(p.refs[p.current], "refs/heads/")
		return p.runner.run(ctx, receiver, "fetch", "--quiet", url,
			p.refs[p.current]+":refs/remotes/origin/"+ref)
	}
}

func (p *incrPlan) verify(receiver string) func(context.Context) error {
	return func(ctx context.Context) error {
		ref := strings.TrimPrefix(p.refs[p.current], "refs/heads/")
		out, err := p.runner.output(ctx, receiver, "rev-parse", "refs/remotes/origin/"+ref)
		if err != nil {
			return fmt.Errorf("incremental verify: %w", err)
		}
		if got := strings.TrimSpace(out); got != p.oids[p.current] {
			return fmt.Errorf("incremental verify: got %s, want %s", got, p.oids[p.current])
		}
		return nil
	}
}

func (p *incrPlan) deleteRemoteRefs(ctx context.Context, urls ...string) error {
	refs := make([]string, len(p.refs))
	for i, ref := range p.refs {
		refs[i] = strings.TrimPrefix(ref, "refs/heads/")
	}
	for _, url := range urls {
		for start := 0; start < len(refs); start += 500 {
			end := min(start+500, len(refs))
			args := []string{"push", "--quiet", url, "--delete"}
			args = append(args, refs[start:end]...)
			if err := p.runner.run(ctx, "", args...); err != nil {
				return fmt.Errorf("delete incremental refs: %w", err)
			}
		}
	}
	return nil
}

func authedURL(raw, user, token string) string {
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return raw
	}
	return scheme + "://" + user + ":" + token + "@" + rest
}

func remoteMain(ctx context.Context, r gitRunner, url string) (string, error) {
	out, err := r.output(ctx, "", "ls-remote", url, "refs/heads/main")
	if err != nil {
		return "", fmt.Errorf("ls-remote: %w", err)
	}
	oid, _, _ := strings.Cut(strings.TrimSpace(out), "\t")
	if oid == "" {
		return "", fmt.Errorf("ls-remote: empty refs/heads/main")
	}
	return oid, nil
}

func infoRefs(ctx context.Context, url, user, token, wantRef, wantOID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("infoRefs request: %w", err)
	}
	req.SetBasicAuth(user, token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("infoRefs do: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("infoRefs body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("infoRefs status %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), wantRef) {
		return fmt.Errorf("infoRefs missing %s", wantRef)
	}
	if !strings.Contains(string(body), wantOID) {
		return fmt.Errorf("infoRefs missing %s oid", wantRef)
	}
	return nil
}

func cloneVerify(ctx context.Context, r gitRunner, dir, wantOID string) error {
	out, err := r.output(ctx, dir, "rev-parse", "refs/heads/main")
	if err != nil {
		return fmt.Errorf("clone verify rev-parse: %w", err)
	}
	if got := strings.TrimSpace(out); got != wantOID {
		return fmt.Errorf("clone verify main %s, want %s", got, wantOID)
	}
	out, err = r.output(ctx, dir, "rev-list", "--count", "HEAD")
	if err != nil {
		return fmt.Errorf("clone verify rev-list: %w", err)
	}
	var count int
	if _, err := fmt.Sscanf(strings.TrimSpace(out), "%d", &count); err != nil {
		return fmt.Errorf("clone verify count: %w", err)
	}
	if count < seedCommits {
		return fmt.Errorf("clone verify count %d, want at least %d", count, seedCommits)
	}
	return nil
}

func timeOnce(ctx context.Context, w benchWorkload) (time.Duration, error) {
	if w.prepare != nil {
		if err := w.prepare(ctx); err != nil {
			return 0, err
		}
	}
	start := time.Now()
	err := w.run(ctx)
	elapsed := time.Since(start)
	if err != nil {
		return 0, err
	}
	if w.verify != nil {
		if err := w.verify(ctx); err != nil {
			return 0, err
		}
	}
	return elapsed, nil
}

func measurePairs(ctx context.Context, setup func(context.Context, int) error, stock, daemon benchWorkload, warmups, total int) (samples, samples, error) {
	ss := make(samples, 0, total)
	ds := make(samples, 0, total)
	for i := range warmups + total {
		var sd, dd time.Duration
		var err error
		if setup != nil {
			if err = setup(ctx, i); err != nil {
				return nil, nil, fmt.Errorf("setup sample %d: %w", i, err)
			}
		}
		if i%2 == 0 {
			if sd, err = timeOnce(ctx, stock); err != nil {
				return nil, nil, fmt.Errorf("stock sample %d: %w", i, err)
			}
			if dd, err = timeOnce(ctx, daemon); err != nil {
				return nil, nil, fmt.Errorf("daemon sample %d: %w", i, err)
			}
		} else {
			if dd, err = timeOnce(ctx, daemon); err != nil {
				return nil, nil, fmt.Errorf("daemon sample %d: %w", i, err)
			}
			if sd, err = timeOnce(ctx, stock); err != nil {
				return nil, nil, fmt.Errorf("stock sample %d: %w", i, err)
			}
		}
		if i >= warmups {
			ss = append(ss, sd)
			ds = append(ds, dd)
		}
	}
	return ss, ds, nil
}

func newCGIServer(ctx context.Context, t *testing.T, root string) *httptest.Server {
	t.Helper()
	execOut, err := exec.CommandContext(ctx, e2estack.GitBin(t), "--exec-path").Output()
	if err != nil {
		t.Fatalf("git --exec-path: %v", err)
	}
	backend := filepath.Join(strings.TrimSpace(string(execOut)), "git-http-backend")
	cgiHandler := &cgi.Handler{
		Path: backend,
		Dir:  root,
		Env: []string{
			"GIT_PROJECT_ROOT=" + root,
			"GIT_HTTP_EXPORT_ALL=1",
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL=" + os.DevNull,
			"GIT_CONFIG_SYSTEM=" + os.DevNull,
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != cgiUser || p != cgiToken {
			w.Header().Set("WWW-Authenticate", `Basic realm="perf"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if len(r.TransferEncoding) > 0 && r.Body != nil {
			raw, rerr := io.ReadAll(r.Body)
			if rerr != nil {
				http.Error(w, "read body", http.StatusBadRequest)
				return
			}
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(raw))
			r.ContentLength = int64(len(raw))
			r.TransferEncoding = nil
		}
		cgiHandler.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestDaemonPathBudget compares the warm Ruk daemon Smart HTTP path with
// stock git-http-backend CGI on the same host. Both sides serve one
// repository with the same seeded history and refs. Budgets follow CEO-08
// and the §7 verification budgets; clone is reported without a threshold.
// Every run appends its dated table with binary provenance to
// internal/bench/RESULTS.md.
func TestDaemonPathBudget(t *testing.T) {
	rukBin := os.Getenv("RUK_BIN")
	if rukBin == "" {
		t.Skip("RUK_BIN is not set")
	}
	resolvedRuk, err := exec.LookPath(rukBin)
	if err != nil {
		t.Skipf("RUK_BIN %q unavailable: %v", rukBin, err)
	}
	rukBin = resolvedRuk
	gitBin, err := exec.LookPath(e2estack.GitBin(t))
	if err != nil {
		t.Skipf("stock git unavailable: %v", err)
	}
	backendOut, err := exec.CommandContext(t.Context(), gitBin, "--exec-path").Output()
	if err != nil {
		t.Skipf("stock git exec-path unavailable: %v", err)
	}
	backendPath := filepath.Join(strings.TrimSpace(string(backendOut)), "git-http-backend")
	info, statErr := os.Stat(backendPath)
	if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Skipf("stock git-http-backend unavailable at %q", backendPath)
	}
	t.Setenv("RUK_COORDINATOR", "off")
	ctx, cancel := context.WithTimeoutCause(t.Context(), 30*time.Minute, errors.New("daemonpath benchmark deadline"))
	defer cancel()

	s := e2estack.NewWithLimiter(t, nil, access.LimiterConfig{})
	ada := s.AddAdmin(t, "ada", "repo:read", "repo:write")
	tokens := []string{ada.Token}
	for i := 1; i < 16; i++ {
		tokens = append(tokens, s.MintToken(t, ada.User, "repo:read", "repo:write").Token)
	}
	ring := &tokenRing{tokens: tokens}
	daemonURLWith := func(token string) string {
		return s.RepoURL("ada", "hello", "ada", token)
	}
	s.AddRepo(t, "ada", "hello", "public")
	repo, err := s.Client.OpenRepo(ctx, filepath.Join(s.Roots, "ada", "hello.git", ".ruk"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	lookup := refLookup{client: s.Client, repo: repo}
	daemonURL := daemonURLWith(ada.Token)

	runner := gitRunner{bin: e2estack.GitBin(t), env: benchEnv()}
	if _, err = exec.LookPath(runner.bin); err != nil {
		t.Skipf("git binary %q unavailable: %v", runner.bin, err)
	}
	donorDir := t.TempDir()
	if err = seedRepo(ctx, runner, donorDir, daemonURL); err != nil {
		t.Fatalf("seed: %v", err)
	}
	expected, err := lookup.main(ctx)
	if err != nil {
		t.Fatalf("daemon main: %v", err)
	}

	cgiRoot := t.TempDir()
	bare := filepath.Join(cgiRoot, "hello.git")
	if err = runner.run(ctx, "", "clone", "--bare", daemonURL, bare); err != nil {
		t.Fatalf("bare clone: %v", err)
	}
	if err = runner.run(ctx, bare, "config", "http.receivepack", "true"); err != nil {
		t.Fatalf("cgi receivepack: %v", err)
	}
	cgiServer := newCGIServer(ctx, t, cgiRoot)
	cgiURL := authedURL(cgiServer.URL+"/hello.git", cgiUser, cgiToken)
	got, err := remoteMain(ctx, runner, cgiURL)
	if err != nil {
		t.Fatalf("cgi main: %v", err)
	}
	if got != expected {
		t.Fatalf("main mismatch: daemon %s cgi %s", expected, got)
	}
	for i, tok := range tokens {
		err = infoRefs(ctx, daemonURL+"/info/refs?service=git-upload-pack", "ada", tok, "refs/heads/main", expected)
		if err != nil {
			t.Fatalf("rotated token %d rejected before timing: %v", i, err)
		}
	}

	tmp := t.TempDir()
	stockDir := filepath.Join(tmp, "stock")
	daemonDir := filepath.Join(tmp, "daemon")
	if err = runner.run(ctx, tmp, "clone", "--quiet", cgiURL, stockDir); err != nil {
		t.Fatalf("stock clone: %v", err)
	}
	if err = runner.run(ctx, tmp, "clone", "--quiet", daemonURL, daemonDir); err != nil {
		t.Fatalf("daemon clone: %v", err)
	}
	plan, err := seedIncremental(ctx, runner, donorDir, cgiURL, daemonURLWith(ring.next()))
	if err != nil {
		t.Fatalf("incremental fixture: %v", err)
	}
	stockCloneDst := filepath.Join(tmp, "stock-clone")
	daemonCloneDst := filepath.Join(tmp, "daemon-clone")

	cases := []benchCase{
		{
			name:      "incremental-fetch",
			warmups:   warmupCount,
			total:     sampleCount,
			budget:    "<= 7.5x stock p95 (CEO-08)",
			authority: "CEO-08",
			ratioMax:  fetchRatioBudget,
			setup: func(_ context.Context, sample int) error {
				plan.selectSample(sample)
				return nil
			},
			after: func(ctx context.Context) error {
				return plan.deleteRemoteRefs(ctx, cgiURL, daemonURLWith(ring.next()))
			},
			stock: benchWorkload{
				run:    plan.fetch(stockDir, cgiURL),
				verify: plan.verify(stockDir),
			},
			daemon: benchWorkload{
				run:    plan.fetch(daemonDir, daemonURLWith(ring.next())),
				verify: plan.verify(daemonDir),
			},
		},
		{
			name:      "single-ref-lookup",
			warmups:   warmupCount,
			total:     sampleCount,
			budget:    "<= 4x stock p95 (CEO-08)",
			authority: "CEO-08",
			ratioMax:  lookupRatioBudget,
			stock: benchWorkload{
				run: func(ctx context.Context) error {
					oid, lookupErr := remoteMain(ctx, runner, cgiURL)
					if lookupErr != nil {
						return lookupErr
					}
					if oid != expected {
						return fmt.Errorf("stock main %s, want %s", oid, expected)
					}
					return nil
				},
			},
			daemon: benchWorkload{
				run: func(ctx context.Context) error {
					oid, lookupErr := remoteMain(ctx, runner, daemonURLWith(ring.next()))
					if lookupErr != nil {
						return lookupErr
					}
					if oid != expected {
						return fmt.Errorf("daemon main %s, want %s", oid, expected)
					}
					return nil
				},
			},
		},
		{
			name:       "clone",
			warmups:    cloneWarmups,
			total:      cloneSamples,
			budget:     "",
			authority:  "V5 brief",
			reportOnly: true,
			stock: benchWorkload{
				prepare: func(context.Context) error {
					return os.RemoveAll(stockCloneDst)
				},
				run: func(ctx context.Context) error {
					return runner.run(ctx, tmp, "clone", "--quiet", cgiURL, stockCloneDst)
				},
				verify: func(ctx context.Context) error {
					return cloneVerify(ctx, runner, stockCloneDst, expected)
				},
			},
			daemon: benchWorkload{
				prepare: func(context.Context) error {
					return os.RemoveAll(daemonCloneDst)
				},
				run: func(ctx context.Context) error {
					return runner.run(ctx, tmp, "clone", "--quiet", daemonURLWith(ring.next()), daemonCloneDst)
				},
				verify: func(ctx context.Context) error {
					return cloneVerify(ctx, runner, daemonCloneDst, expected)
				},
			},
		},
		{
			name:      "info-refs",
			warmups:   warmupCount,
			total:     sampleCount,
			budget:    "<= 150ms daemon p95 (verification §7)",
			authority: "verification §7",
			limitMax:  infoRefsBudget,
			stock: benchWorkload{
				run: func(ctx context.Context) error {
					return infoRefs(ctx, cgiURL+"/info/refs?service=git-upload-pack", cgiUser, cgiToken, "refs/heads/main", expected)
				},
			},
			daemon: benchWorkload{
				run: func(ctx context.Context) error {
					return infoRefs(ctx, daemonURL+"/info/refs?service=git-upload-pack", "ada", ring.next(), "refs/heads/main", expected)
				},
			},
		},
	}

	rows := make([]benchRow, 0, len(cases))
	failed := false
	var stock, daemon samples
	for _, bc := range cases {
		stock, daemon, err = measurePairs(ctx, bc.setup, bc.stock, bc.daemon, bc.warmups, bc.total)
		if err != nil {
			t.Fatalf("measurement incomplete at workload %s; no rows recorded: %v", bc.name, err)
		}
		if len(stock) == 0 || len(daemon) == 0 {
			t.Fatalf("workload %s produced no samples (stock=%d daemon=%d); nothing recorded", bc.name, len(stock), len(daemon))
		}
		ratio := float64(daemon.p(0.95)) / float64(stock.p(0.95))
		overRatio := bc.ratioMax > 0 && ratio > bc.ratioMax
		overLimit := bc.limitMax > 0 && daemon.p(0.95) > bc.limitMax
		var verdict string
		switch {
		case bc.reportOnly:
			verdict = "REPORT"
		case overRatio || overLimit:
			verdict = "FAIL"
		default:
			verdict = "PASS"
		}
		if verdict == "FAIL" {
			failed = true
		}
		rows = append(rows, benchRow{name: bc.name, stock: stock, daemon: daemon, ratio: ratio, budget: bc.budget, authority: bc.authority, verdict: verdict})
		t.Logf("%s: stock p50=%s p95=%s daemon p50=%s p95=%s ratio=%.2fx budget=%q verdict=%s",
			bc.name, stock.p(0.5), stock.p(0.95), daemon.p(0.5), daemon.p(0.95), ratio, bc.budget, verdict)
		if bc.after != nil {
			if err = bc.after(ctx); err != nil {
				t.Fatalf("post-workload cleanup %s: %v", bc.name, err)
			}
		}
	}

	host, _ := os.Hostname()
	meta := runMeta{
		rukBin:     rukBin,
		rukVersion: getenvOr("RUK_VERSION", "unset"),
		rukCommit:  getenvOr("RUK_COMMIT", "unset"),
		rukSchema:  getenvOr("RUK_SCHEMA", "unset"),
		gitBin:     e2estack.GitBin(t),
		host:       host,
		cpu:        fmt.Sprintf("%d/%s", runtime.NumCPU(), runtime.GOARCH),
		goVersion:  runtime.Version(),
	}
	if err = appendResults(ctx, meta, rows); err != nil {
		t.Fatalf("append results: %v", err)
	}
	if failed {
		t.Fatal("daemon path budget failed; see internal/bench/RESULTS.md; the fix needs a new Ruk row")
	}
}

func appendResults(ctx context.Context, meta runMeta, rows []benchRow) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("appendResults: %w", err)
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return fmt.Errorf("appendResults: caller unavailable")
	}
	path := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(file))), "internal", "bench", "RESULTS.md")
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "\n## %s\n\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&buf, "- ruk binary: %s\n- ruk version: %s\n- ruk commit: %s\n- ruk schema: %s\n- git binary: %s\n- host: %s\n- cpu: %s\n- go: %s\n- method: %d warmups, then %d paired samples with alternating stock/daemon order (fetch, lookup, info-refs); clone uses %d warmups and %d paired samples as report-only data; prepare and verify outside the timer; 16 tokens minted for ada and rotated per daemon request; nearest-rank p50/p95/p99\n\n", meta.rukBin, meta.rukVersion, meta.rukCommit, meta.rukSchema, meta.gitBin, meta.host, meta.cpu, meta.goVersion, warmupCount, sampleCount, cloneWarmups, cloneSamples)
	fmt.Fprintf(&buf, "| workload | stock p50 | stock p95 | stock p99 | daemon p50 | daemon p95 | daemon p99 | ratio p95 | budget | authority | result |\n")
	fmt.Fprintf(&buf, "|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, row := range rows {
		fmt.Fprintf(&buf, "| %s | %s | %s | %s | %s | %s | %s | %.2fx | %s | %s | %s |\n",
			row.name, row.stock.p(0.5), row.stock.p(0.95), row.stock.p(0.99),
			row.daemon.p(0.5), row.daemon.p(0.95), row.daemon.p(0.99), row.ratio, row.budget, row.authority, row.verdict)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("appendResults: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("appendResults: %w", err)
	}
	return nil
}
