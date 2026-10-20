//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/ruk/wire"
)

func capturePush(t *testing.T, s *stack, ada userRec, work, ref string) []byte {
	t.Helper()
	var bodies [][]byte
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "tee failed", http.StatusBadGateway)
				return
			}
			bodies = append(bodies, raw)
			r.Body = io.NopCloser(bytes.NewReader(raw))
			r.ContentLength = int64(len(raw))
		}
		target := s.url + r.URL.Path
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		fwd, err := http.NewRequest(r.Method, target, r.Body)
		if err != nil {
			http.Error(w, "forward failed", http.StatusBadGateway)
			return
		}
		fwd.Header = r.Header.Clone()
		fwd.ContentLength = r.ContentLength
		resp, err := http.DefaultTransport.RoundTrip(fwd)
		if err != nil {
			http.Error(w, "upstream failed", http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		for key, values := range resp.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer proxy.Close()
	proxyCred, _ := strings.CutPrefix(proxy.URL, "http://")
	proxyPush := "http://" + ada.user.Name + ":" + ada.token + "@" + proxyCred + "/ada/hello.git"
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	out, err := gitRun(ctx, t, work, "push", proxyPush, "HEAD:"+ref)
	if len(bodies) == 0 {
		t.Fatalf("proxy recorded no push body: %v\n%s", err, out)
	}
	return bodies[len(bodies)-1]
}

func spliceRef(t *testing.T, body []byte, oldRef, newRef string) []byte {
	t.Helper()
	if len(body) < 4 {
		t.Fatal("body too short for a pkt-line")
	}
	var length int
	if _, err := fmt.Sscanf(string(body[:4]), "%04x", &length); err != nil || length > len(body) {
		t.Fatalf("bad first pkt-line length: %q", body[:4])
	}
	line := string(body[4:length])
	fields := strings.SplitN(line, " ", 3)
	if len(fields) != 3 {
		t.Fatalf("first command line has no ref: %q", line)
	}
	rest := fields[2]
	ref := rest
	caps := ""
	if cut, after, found := strings.Cut(rest, "\x00"); found {
		ref = cut
		caps = "\x00" + after
	} else {
		ref = strings.TrimRight(rest, "\n")
	}
	if ref != oldRef {
		t.Fatalf("first command ref = %q, want %q", ref, oldRef)
	}
	rebuilt := fields[0] + " " + fields[1] + " " + newRef + caps
	out := append([]byte(fmt.Sprintf("%04x", len(rebuilt)+4)), rebuilt...)
	return append(out, body[length:]...)
}

func TestGitReadOnlyPushShowsPolicyReason(t *testing.T) {
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
	if !strings.Contains(out, "is not allowed by policy") {
		t.Fatalf("read-only push lacks the per-ref policy reason:\n%s", out)
	}
	adaPush := s.repoURL("ada", "hello", "ada", ada.token)
	if refs := s.lsRemote(t, adaPush); refs["refs/heads/main"] != "" {
		t.Fatalf("read-only push landed: %q", refs["refs/heads/main"])
	}
}

func TestGitReadOnlyRawPushRejectedNg(t *testing.T) {
	s := newStack(t, nil)
	ada := s.addAdmin(t, "ada", "repo:read", "repo:write")
	mallory := s.addUser(t, "mallory", "repo:read")
	repoID := s.addRepo(t, "ada", "hello", "private")
	s.addGrant(t, repoID, mallory.user.ID, "read")
	work, _ := s.localRepo(t, map[string]string{"README.md": "hello\n"})
	recorded := capturePush(t, s, ada, work, "refs/heads/side")
	replayed := spliceRef(t, recorded, "refs/heads/side", "refs/heads/replayed")
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url+"/ada/hello.git/git-receive-pack", bytes.NewReader(replayed))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.SetBasicAuth("mallory", mallory.token)
	req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("raw push status = %d, want 200 with an ng line:\n%s", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), "ng refs/heads/replayed") {
		t.Fatalf("raw push lacks the ng line:\n%s", raw)
	}
	if !strings.Contains(string(raw), "is not allowed by policy") {
		t.Fatalf("raw push lacks the policy reason:\n%s", raw)
	}
	adaPush := s.repoURL("ada", "hello", "ada", ada.token)
	if refs := s.lsRemote(t, adaPush); refs["refs/heads/replayed"] != "" {
		t.Fatalf("raw push landed: %q", refs["refs/heads/replayed"])
	}
}

func TestGitRefsForRawPushRejectedPinned(t *testing.T) {
	s := newStack(t, nil)
	ada := s.addAdmin(t, "ada", "repo:read", "repo:write")
	mallory := s.addUser(t, "mallory", "repo:read", "repo:write")
	repoID := s.addRepo(t, "ada", "hello", "private")
	s.addGrant(t, repoID, mallory.user.ID, "read")
	work, _ := s.localRepo(t, map[string]string{"README.md": "hello\n"})
	recorded := capturePush(t, s, ada, work, "refs/for/probe")
	pushURL := s.repoURL("ada", "hello", "ada", ada.token)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url+"/ada/hello.git/git-receive-pack", bytes.NewReader(recorded))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.SetBasicAuth("mallory", mallory.token)
	req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "change review is not available") {
		t.Fatalf("refs/for push lacks the pinned reason:\n%s", raw)
	}
	if refs := s.lsRemote(t, pushURL); refs["refs/for/probe"] != "" {
		t.Fatalf("refs/for/probe landed as a real ref: %q", refs["refs/for/probe"])
	}
}

func TestGitOversizePushIs413(t *testing.T) {
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
	if !strings.Contains(out, "413") {
		t.Fatalf("oversize push lacks the 413 refusal:\n%s", out)
	}
	status, _, body := httpGet(t, s.url+"/ada/hello.git/info/refs?service=git-upload-pack", "ada", ada.token)
	if status != http.StatusOK {
		t.Fatalf("advertise status = %d, want 200", status)
	}
	if strings.Contains(body, " refs/heads/main\x00") || strings.Contains(body, " refs/heads/main\n") {
		t.Fatalf("oversize push landed: %q", body)
	}
}

func TestConcurrentPushesSameRefSerializes(t *testing.T) {
	s := newStack(t, nil)
	ada := s.addAdmin(t, "ada", "repo:read", "repo:write")
	s.addRepo(t, "ada", "hello", "private")
	base, _ := s.localRepo(t, map[string]string{"README.md": "base\n"})
	pushURL := s.repoURL("ada", "hello", "ada", ada.token)
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	if out, err := gitRun(ctx, t, base, "push", pushURL, "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("seed push: %v\n%s", err, out)
	}
	mkSide := func(t *testing.T, name, content string) (string, string) {
		t.Helper()
		dir := t.TempDir()
		if out, err := gitRun(ctx, t, dir, "clone", pushURL, "side"); err != nil {
			t.Fatalf("clone: %v\n%s", err, out)
		}
		work := dir + "/side"
		if err := os.WriteFile(work+"/"+name, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if out, err := gitRun(ctx, t, work, "add", "-A"); err != nil {
			t.Fatalf("add: %v\n%s", err, out)
		}
		if out, err := gitRun(ctx, t, work, "commit", "-m", "side"); err != nil {
			t.Fatalf("commit: %v\n%s", err, out)
		}
		out, err := gitRun(ctx, t, work, "rev-parse", "HEAD")
		if err != nil {
			t.Fatalf("rev-parse: %v\n%s", err, out)
		}
		return work, strings.TrimSpace(out)
	}
	dirA, oidA := mkSide(t, "f-a.txt", "side-a\n")
	dirB, oidB := mkSide(t, "f-b.txt", "side-b\n")
	var wg sync.WaitGroup
	type result struct {
		out string
		err error
	}
	results := make([]result, 2)
	dirs := []string{dirA, dirB}
	for i := range 2 {
		wg.Go(func() {
			pctx, pcancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer pcancel()
			out, err := gitRun(pctx, t, dirs[i], "push", pushURL, "HEAD:refs/heads/x")
			results[i] = result{out: out, err: err}
		})
	}
	wg.Wait()
	wins := 0
	for _, r := range results {
		if r.err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("same-ref race: %d pushes succeeded, want exactly 1:\n%+v", wins, results)
	}
	loser := 0
	if results[0].err == nil {
		loser = 1
	}
	if out := results[loser].out; !strings.Contains(out, "non-fast-forward") && !strings.Contains(out, "rejected") && !strings.Contains(out, "stale") {
		t.Fatalf("loser lacks a contention refusal:\n%s", out)
	}
	refs := s.lsRemote(t, pushURL)
	want := oidA
	if results[1].err == nil {
		want = oidB
	}
	if refs["refs/heads/x"] != want {
		t.Fatalf("refs/heads/x = %q, want winner %q", refs["refs/heads/x"], want)
	}
}

func TestGitCooldownBlocksValidToken(t *testing.T) {
	s := newStack(t, nil)
	ada := s.addAdmin(t, "ada", "repo:read", "repo:write")
	s.addRepo(t, "ada", "hello", "private")
	infoURL := s.url + "/ada/hello.git/info/refs?service=git-upload-pack"
	for i := range 65 {
		status, _, _ := httpGet(t, infoURL, "ada", "wrong-token")
		if i < 60 && status != http.StatusUnauthorized {
			t.Fatalf("probe %d status = %d, want 401", i, status)
		}
		if i >= 60 && status != http.StatusTooManyRequests {
			t.Fatalf("probe %d status = %d, want 429 once the trip line is crossed", i, status)
		}
	}
	status, header, _ := httpGet(t, infoURL, "ada", ada.token)
	if status != http.StatusTooManyRequests {
		t.Fatalf("cooldown status = %d, want 429", status)
	}
	if header.Get("Retry-After") == "" {
		t.Fatal("cooldown lacks Retry-After")
	}
}

func TestGitAuthCaseInsensitiveAndHint(t *testing.T) {
	s := newStack(t, nil)
	s.addAdmin(t, "ada", "repo:read", "repo:write")
	seedCtx, seedCancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer seedCancel()
	seedNow := time.Now().UnixMilli()
	res, err := s.db.ExecContext(seedCtx, `INSERT INTO users
		(name, lower_name, email, full_name, password_hash, password_algo, password_changed_at, is_admin, created_at, updated_at)
		VALUES ('Mallory', 'mallory', 'mallory@example.test', '', 'x', 'argon2id-v19', ?, 0, ?, ?)`, seedNow, seedNow, seedNow)
	if err != nil {
		t.Fatalf("seed mixed-case user: %v", err)
	}
	malloryID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("mixed-case user id: %v", err)
	}
	malloryScopes, err := auth.ParseScopeSet([]string{"repo:read", "repo:write"})
	if err != nil {
		t.Fatalf("scopes: %v", err)
	}
	mallorySecret, err := s.auth.CreateToken(seedCtx, auth.CreateTokenInput{Name: "e2e", Scopes: malloryScopes, UserID: malloryID})
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	caps := userRec{token: mallorySecret.Secret}
	repoID := s.addRepo(t, "ada", "hello", "private")
	s.addGrant(t, repoID, malloryID, "read")
	status, _, _ := httpGet(t, s.url+"/ada/hello.git/info/refs?service=git-upload-pack", "mallory", caps.token)
	if status != http.StatusOK {
		t.Fatalf("lowercase login for Mallory got %d, want the case-insensitive match", status)
	}
	status, header, body := httpGet(t, s.url+"/ada/hello.git/info/refs?service=git-upload-pack", "ada", "not-a-token-password")
	if status != http.StatusUnauthorized {
		t.Fatalf("password status = %d, want 401", status)
	}
	if got := header.Get("WWW-Authenticate"); got != `Basic realm="rukie git"` {
		t.Fatalf("WWW-Authenticate = %q, want the rukie git realm", got)
	}
	if !strings.Contains(body, "use a scoped token as password") {
		t.Fatalf("password 401 lacks the scoped-token hint: %q", body)
	}
	status, _, tokenBody := httpGet(t, s.url+"/ada/hello.git/info/refs?service=git-upload-pack", "ada", caps.token)
	if status != http.StatusUnauthorized {
		t.Fatalf("mismatched token status = %d, want 401", status)
	}
	if strings.Contains(tokenBody, "use a scoped token as password") {
		t.Fatalf("token-shaped 401 names a password hint it should not: %q", tokenBody)
	}
}

func TestGitDaemonLimitRefusesChunkedBody(t *testing.T) {
	limits := wire.DefaultGitLimits()
	limits.MaxStdinBytes = 64
	s := newStackWithHTTPCap(t, &limits, 1<<20)
	ada := s.addAdmin(t, "ada", "repo:read", "repo:write")
	s.addRepo(t, "ada", "hello", "private")
	body := strings.Repeat("a", 256)
	req, err := http.NewRequest(http.MethodPost, s.url+"/ada/hello.git/git-receive-pack", io.NopCloser(strings.NewReader(body)))
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
		t.Fatalf("chunked status = %d, want 413 from the daemon envelope cap", resp.StatusCode)
	}
	status, _, body := httpGet(t, s.url+"/ada/hello.git/info/refs?service=git-upload-pack", "ada", ada.token)
	if status != http.StatusOK {
		t.Fatalf("advertise status = %d, want 200", status)
	}
	if strings.Contains(body, " refs/heads/main\x00") || strings.Contains(body, " refs/heads/main\n") {
		t.Fatalf("ref moved on a refused body: %q", body)
	}
}
