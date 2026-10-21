//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/api"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/queue"
	"github.com/gosuda/rukie/internal/repowatch"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/rukproc"
	"github.com/gosuda/rukie/internal/store"
)

type apiHarness struct {
	server  *httptest.Server
	watcher *repowatch.Watcher
}

func mountAPI(t *testing.T, s *stack) *apiHarness {
	t.Helper()
	deps := api.Deps{
		Service: s.auth, Limiter: access.NewLimiter(), Reveals: auth.NewRevealCache(),
		Store: s.st, Ruk: s.client, Queue: queue.Open(s.st, time.Now),
		RepoRoot: s.roots, RukBin: rukBin(t),
	}
	watcher := repowatch.New(repowatch.Config{
		Store: s.st, Queue: queue.Open(s.st, time.Now), Client: s.client,
		RepoRoot: s.roots, Clock: time.Now,
	})
	deps.OnRepoCreated = func(repoID int64) { watcher.Start(t.Context(), repoID) }
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
		func() error { return api.RegisterReview(table, deps) },
	} {
		if err := step(); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	if err := table.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	views := op.NewViews()
	api.RegisterViews(views)
	api.RegisterAuthViews(views)
	api.RegisterUserViews(views)
	api.RegisterAdminViews(views)
	api.RegisterReposViews(views)
	api.RegisterCodeViews(views)
	api.RegisterRefsViews(views)
	api.RegisterReviewViews(views)
	var key [32]byte
	_, _ = rand.Read(key[:])
	mux := http.NewServeMux()
	mounted, err := op.Mount(mux, table, op.Config{
		Authenticator: auth.NewAuthenticator(s.auth),
		Idempotency:   op.NewMemoryIdempotency(),
		Audit:         op.FuncAuditor(func(context.Context, op.AuditRow) error { return nil }),
		Limiter:       deps.Limiter,
		CSRFKey:       key[:],
		Views:         views,
		Title:         "e2e",
		Version:       "0",
		IsSiteSegment: api.SiteSegment,
	})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	front := api.RawBytes{Deps: deps}.Wrap(mounted)
	ts := httptest.NewServer(front)
	t.Cleanup(ts.Close)
	return &apiHarness{server: ts, watcher: watcher}
}

func apiCall(t *testing.T, h *apiHarness, method, target, authHeader, body string) (int, map[string]any) {
	t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	var req *http.Request
	var err error
	if body != "" {
		req, err = http.NewRequest(method, h.server.URL+target, reader)
	} else {
		req, err = http.NewRequest(method, h.server.URL+target, nil)
	}
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if authHeader != "" {
		if strings.HasPrefix(authHeader, "Bearer ") {
			req.Header.Set("Authorization", authHeader)
		} else {
			req.Header.Set("Cookie", authHeader)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var decoded map[string]any
	if rerr := jsonv2.Unmarshal(readBody(t, resp), &decoded, jsonv1.DefaultOptionsV1()); rerr != nil {
		t.Fatalf("decode %d: %v", resp.StatusCode, rerr)
	}
	return resp.StatusCode, decoded
}

func readBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	buf := make([]byte, 0, 4096)
	chunk := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if err != nil {
			break
		}
	}
	return buf
}

func apiLogin(t *testing.T, h *apiHarness, username, password string) string {
	t.Helper()
	req, err := http.NewRequest("POST", h.server.URL+"/login", strings.NewReader(`{"username":"`+username+`","password":"`+password+`"}`))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_ = readBody(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("login = %d", resp.StatusCode)
	}
	cookie := resp.Header.Get("Set-Cookie")
	parts := strings.Split(cookie, ";")
	return strings.TrimSpace(parts[0])
}

func apiToken(t *testing.T, h *apiHarness, session, scopes string) string {
	t.Helper()
	status, decoded := apiCall(t, h, "POST", "/settings/tokens", session, `{"name":"agent","scopes":"`+scopes+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("token create = %d: %v", status, decoded)
	}
	secret, _ := decoded["secret"].(string)
	if secret == "" {
		t.Fatalf("token secret missing: %v", decoded)
	}
	return "Bearer " + secret
}

func repoRowCount(t *testing.T, s *stack, owner, name string) int {
	t.Helper()
	var count int
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM repos WHERE name = ?`, name).Scan(&count)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	_ = owner
	return count
}

func outboxKinds(t *testing.T, s *stack, kind string) []store.OutboxRow {
	t.Helper()
	var rows []store.OutboxRow
	err := s.st.Read(e2eCtx(t), func(tx *store.ReadTx) error {
		got, rerr := tx.Outbox().After("agentflow", 1000)
		if rerr != nil {
			return rerr
		}
		rows = got
		return nil
	})
	if err != nil {
		t.Fatalf("outbox: %v", err)
	}
	var out []store.OutboxRow
	for _, row := range rows {
		if row.Kind == kind {
			out = append(out, row)
		}
	}
	return out
}

func waitRefUpdated(t *testing.T, s *stack, ref string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		for _, row := range outboxKinds(t, s, "ref.updated") {
			if strings.Contains(string(row.Body), ref) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no ref.updated row for %s within %s", ref, timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestAgentFlow(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		s := newStack(t, nil)
		h := mountAPI(t, s)
		s.addAdmin(t, "ada", "repo:read", "repo:write")
		session := apiLogin(t, h, "ada", "long-test-password-01")
		token := apiToken(t, h, session, "repo:create repo:read repo:write intents:read")
		status, decoded := apiCall(t, h, "POST", "/new/repo", token, `{"owner":"ada","name":"hello","visibility":"private"}`)
		if status != http.StatusCreated {
			t.Fatalf("repo.create = %d: %v", status, decoded)
		}
		repo, ok := decoded["repo"].(map[string]any)
		if !ok || repo["name"] != "hello" || repo["owner"] != "ada" {
			t.Fatalf("repo = %v", decoded)
		}
		if repoRowCount(t, s, "ada", "hello") != 1 {
			t.Fatal("repo row missing after create")
		}
		if len(outboxKinds(t, s, "repo.created")) != 1 {
			t.Fatal("repo.created outbox row missing")
		}
	})

	t.Run("push", func(t *testing.T) {
		s := newStack(t, nil)
		h := mountAPI(t, s)
		s.addAdmin(t, "ada", "repo:read", "repo:write")
		session := apiLogin(t, h, "ada", "long-test-password-01")
		token := apiToken(t, h, session, "repo:create repo:read repo:write intents:read")
		status, decoded := apiCall(t, h, "POST", "/new/repo", token, `{"owner":"ada","name":"hello","visibility":"private"}`)
		if status != http.StatusCreated {
			t.Fatalf("repo.create = %d: %v", status, decoded)
		}
		secret := strings.TrimPrefix(token, "Bearer ")
		work, _ := s.localRepo(t, map[string]string{"README.md": "hello\n"})
		pushURL := s.repoURL("ada", "hello", "ada", secret)
		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		defer cancel()
		if out, err := gitRun(ctx, t, work, "push", pushURL, "HEAD:refs/heads/main"); err == nil {
			t.Fatalf("direct push to the protected default branch landed:\n%s", out)
		} else if !strings.Contains(out, "protected:allowlist") {
			t.Fatalf("direct push lacks the LandOnly denial:\n%s", out)
		}
		if out, err := gitRun(ctx, t, work, "push", pushURL, "HEAD:refs/heads/side"); err != nil {
			t.Fatalf("side branch push: %v\n%s", err, out)
		}
		waitRefUpdated(t, s, "refs/heads/side", 2*time.Second)
	})

	t.Run("change", func(t *testing.T) {
		s := newStack(t, nil)
		h := mountAPI(t, s)
		ada := s.addAdmin(t, "ada", "repo:read", "repo:write")
		session := apiLogin(t, h, "ada", "long-test-password-01")
		token := apiToken(t, h, session, "repo:create repo:read repo:write intents:read")
		if status, decoded := apiCall(t, h, "POST", "/new/repo", token, `{"owner":"ada","name":"hello","visibility":"private"}`); status != http.StatusCreated {
			t.Fatalf("repo.create = %d: %v", status, decoded)
		}
		work, _ := s.localRepo(t, map[string]string{"README.md": "hello\n"})
		pushURL := s.repoURL("ada", "hello", "ada", ada.token)
		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		defer cancel()
		if out, err := gitRun(ctx, t, work, "push", pushURL, "HEAD:refs/for/main"); err != nil {
			t.Fatalf("refs/for push: %v\n%s", err, out)
		}
		key := "main~#1"
		if status, decoded := apiCall(t, h, "GET", "/ada/hello/changes/"+key, token, ""); status != http.StatusOK {
			t.Fatalf("change.get = %d: %v", status, decoded)
		}
		if status, _ := apiCall(t, h, "POST", "/ada/hello/changes/"+key+"/comments", token, `{"path":"README.md","line":1,"body":"nit"}`); status != http.StatusCreated {
			t.Fatalf("comment.create = %d", status)
		}
		second, _ := s.localRepo(t, map[string]string{"README.md": "hello\nsecond\n"})
		if out, err := gitRun(ctx, t, second, "push", pushURL, "HEAD:refs/for/main"); err != nil {
			t.Fatalf("rewrite push: %v\n%s", err, out)
		}
		status, listed := apiCall(t, h, "GET", "/ada/hello/changes/"+key+"/comments?unmatched=true", token, "")
		if status != http.StatusOK {
			t.Fatalf("comment.list = %d: %v", status, listed)
		}
		raw, _ := listed["comments"].([]any)
		if len(raw) != 1 {
			t.Fatalf("comments = %v, want the remapped row", listed)
		}
		row, _ := raw[0].(map[string]any)
		if row["unmatched"] == true {
			t.Fatalf("comment fell to the fallback bucket: %v", row)
		}
		if row["line"] != float64(1) {
			t.Fatalf("comment line = %v, want 1", row["line"])
		}
	})

	t.Run("land", func(t *testing.T) {
		s := newStack(t, nil)
		h := mountAPI(t, s)
		ada := s.addAdmin(t, "ada", "repo:read", "repo:write")
		session := apiLogin(t, h, "ada", "long-test-password-01")
		token := apiToken(t, h, session, "repo:create repo:read repo:write intents:read")
		if status, decoded := apiCall(t, h, "POST", "/new/repo", token, `{"owner":"ada","name":"hello","visibility":"private"}`); status != http.StatusCreated {
			t.Fatalf("repo.create = %d: %v", status, decoded)
		}
		_ = ada
		work, _ := s.localRepo(t, map[string]string{"README.md": "hello\n"})
		pushURL := s.repoURL("ada", "hello", "ada", strings.TrimPrefix(token, "Bearer "))
		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		defer cancel()
		if out, err := gitRun(ctx, t, work, "push", pushURL, "HEAD:refs/for/main"); err != nil {
			t.Fatalf("refs/for push: %v\n%s", err, out)
		}
		key := "main~#1"
		planStatus, plan := apiCall(t, h, "POST", "/ada/hello/land/plans", token, `{"target_ref":"refs/heads/main","change":"`+key+`"}`)
		if planStatus != http.StatusCreated {
			t.Fatalf("land.plan.create = %d: %v", planStatus, plan)
		}
		planID, _ := plan["id"].(string)
		if planID == "" {
			t.Fatalf("plan id missing: %v", plan)
		}
		runStatus, run := apiCall(t, h, "POST", "/ada/hello/land/plans/"+planID+"/runs", token, `{}`)
		if runStatus != http.StatusConflict {
			t.Fatalf("land.run.create = %d, want 409 requirements-unmet: %v", runStatus, run)
		}
		if status, _ := apiCall(t, h, "POST", "/ada/hello/changes/"+key+"/comments", token, `{"body":"approve"}`); status != http.StatusCreated {
			t.Fatalf("spoof comment.create = %d", status)
		}
		spoofStatus, _ := apiCall(t, h, "POST", "/ada/hello/land/plans/"+planID+"/runs", token, `{}`)
		if spoofStatus != http.StatusConflict {
			t.Fatalf("spoofed approval run = %d, want 409: ordinary comments never approve", spoofStatus)
		}
	})
}

func TestAdminMethodsRefusedForNonAdminPeer(t *testing.T) {
	s := newStack(t, nil)
	h := mountAPI(t, s)
	s.addAdmin(t, "ada", "repo:read", "repo:write")
	s.addRepo(t, "ada", "seed", "private")
	ctx := e2eCtx(t)
	seeded, serr := s.client.OpenRepo(ctx, filepath.Join(s.roots, "ada", "seed.git", ".ruk"))
	if serr != nil {
		t.Fatalf("open seed: %v", serr)
	}
	_, err := ruk.Call[map[string]any, jsonv1.RawMessage](ctx, s.client, seeded, wire.MethodRepoInit,
		map[string]any{"root": s.roots, "path": "probe/nope.git"})
	if err == nil {
		t.Fatal("repo.init without admin_uids succeeded")
	}
	rerr, ok := errors.AsType[*ruk.Error](err)
	if !ok {
		t.Fatalf("error = %T %v, want a typed refusal", err, err)
	}
	if rerr.Code != wire.CodeForbidden {
		t.Fatalf("code = %s, want E_FORBIDDEN", rerr.Code)
	}
	session := apiLogin(t, h, "ada", "long-test-password-01")
	token := apiToken(t, h, session, "repo:create repo:read repo:write")
	status, decoded := apiCall(t, h, "POST", "/new/repo", token, `{"owner":"ada","name":"refused"}`)
	if status != http.StatusForbidden {
		t.Fatalf("repo.create = %d: %v", status, decoded)
	}
	if repoRowCount(t, s, "ada", "refused") != 0 {
		t.Fatal("refused create left a repo row")
	}
}

func TestRepoCreateRukFirst(t *testing.T) {
	s := newStack(t, nil)
	h := mountAPI(t, s)
	s.addAdmin(t, "ada", "repo:read", "repo:write")
	session := apiLogin(t, h, "ada", "long-test-password-01")
	token := apiToken(t, h, session, "repo:create repo:read repo:write")
	status, decoded := apiCall(t, h, "POST", "/new/repo", token, `{"owner":"ada","name":"noruk"}`)
	if status != http.StatusForbidden && status != http.StatusNotFound {
		t.Fatalf("repo.create = %d: %v", status, decoded)
	}
	if repoRowCount(t, s, "ada", "noruk") != 0 {
		t.Fatal("failed Ruk call left a repo row")
	}
	if len(outboxKinds(t, s, "repo.created")) != 0 {
		t.Fatal("failed Ruk call left an outbox row")
	}
}

func seedF1Repo(t *testing.T, s *stack, owner, name, visibility string) int64 {
	t.Helper()
	rec, ok := s.users[owner]
	if !ok {
		t.Fatalf("unknown user %s", owner)
	}
	now := time.Now().UnixMilli()
	var repoID int64
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := s.db.QueryRowContext(ctx, `INSERT INTO repos (owner_user_id, org_id, name, visibility, topics, default_branch, ruk_path, created_at, updated_at) VALUES (?, 0, ?, ?, '[]', 'main', ?, ?, ?) RETURNING id`,
		rec.user.ID, name, visibility, owner+"/"+name+".git", now, now).Scan(&repoID)
	if err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO bookmark_rules (repo_id, pattern, mode, bypass_admin, created_at) VALUES (?, ?, 'Open', 0, ?)`,
		repoID, "main", now); err != nil {
		t.Fatalf("insert rule: %v", err)
	}
	repoDir := filepath.Join(s.roots, owner, name+".git")
	seedCtx, seedCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer seedCancel()
	if out, err := rukproc.Run(seedCtx, rukBin(t), []string{"git", "init", repoDir}, append(os.Environ(), "RUK_COORDINATOR=off")); err != nil {
		t.Fatalf("ruk init: %v\n%s", err, out)
	}
	return repoID
}

func TestWatcherPushProducesRefUpdated(t *testing.T) {
	s := newStack(t, nil)
	h := mountAPI(t, s)
	s.addAdmin(t, "ada", "repo:read", "repo:write")
	repoID := seedF1Repo(t, s, "ada", "hello", "private")
	serveCtx, stop := context.WithCancel(t.Context())
	defer stop()
	if err := h.watcher.StartAll(serveCtx); err != nil {
		t.Fatalf("watchers: %v", err)
	}
	oid := pushMain(t, s, "ada", "hello", map[string]string{"README.md": "hello\n"})
	_ = oid
	waitRefUpdated(t, s, "refs/heads/main", 2*time.Second)
	var tips map[string]string
	if err := s.st.Read(e2eCtx(t), func(tx *store.ReadTx) error {
		got, rerr := tx.RefTips(repoID)
		if rerr != nil {
			return rerr
		}
		tips = got
		return nil
	}); err != nil {
		t.Fatalf("tips: %v", err)
	}
	if tips["refs/heads/main"] == "" {
		t.Fatalf("tips = %+v", tips)
	}
}
