//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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
)

func mountSearchAPI(t *testing.T, s *stack) *httptest.Server {
	t.Helper()
	var sealKey [32]byte
	_, _ = rand.Read(sealKey[:])
	mirrorQueue := queue.Open(s.st, time.Now)
	deps := api.Deps{
		Service: s.auth, Limiter: access.NewLimiter(), Reveals: auth.NewRevealCache(),
		Store: s.st, Ruk: s.client, Queue: mirrorQueue,
		RepoRoot: s.roots, RukBin: rukBin(t),
		SealKey: sealKey[:],
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
		func() error { return api.RegisterSearch(table, deps) },
		func() error { return api.RegisterCode(table, deps) },
		func() error { return api.RegisterRefs(table, deps) },
	} {
		if serr := step(); serr != nil {
			t.Fatalf("register: %v", serr)
		}
	}
	if verr := table.Validate(); verr != nil {
		t.Fatalf("validate: %v", verr)
	}
	views := op.NewViews()
	api.RegisterViews(views)
	api.RegisterAuthViews(views)
	api.RegisterUserViews(views)
	api.RegisterAdminViews(views)
	api.RegisterReposViews(views)
	api.RegisterSearchViews(views)
	api.RegisterCodeViews(views)
	api.RegisterRefsViews(views)
	var key [32]byte
	_, _ = rand.Read(key[:])
	mux := http.NewServeMux()
	mounted, merr := op.Mount(mux, table, op.Config{
		Authenticator: auth.NewAuthenticator(s.auth),
		Idempotency:   op.NewMemoryIdempotency(),
		Audit:         op.FuncAuditor(func(context.Context, op.AuditRow) error { return nil }),
		Limiter:       deps.Limiter,
		CSRFKey:       key[:],
		Views:         views,
		Title:         "e2e-search",
		Version:       "0",
		IsSiteSegment: api.SiteSegment,
	})
	if merr != nil {
		t.Fatalf("mount: %v", merr)
	}
	front := api.RewriteSearchPaths(api.RawBytes{Deps: deps}.Wrap(mounted))
	ts := httptest.NewServer(front)
	t.Cleanup(ts.Close)
	return ts
}

func searchSetup(t *testing.T) (*stack, *httptest.Server, userRec, userRec) {
	t.Helper()
	s := newStack(t, nil)
	ada := s.addAdmin(t, "ada", "repo:read", "repo:write")
	bob := s.addUser(t, "bob", "repo:read")
	s.addRepo(t, "ada", "codepub", "public")
	s.addRepo(t, "ada", "codepub2", "public")
	s.addRepo(t, "ada", "codepriv", "private")
	files := map[string]string{}
	for i := range 25 {
		name := "f" + itoa2(i) + ".txt"
		files[name] = "needle-f9 line one\nfiller text here\nneedle-f9 line three\n"
	}
	big := strings.Repeat("x", 600*1024) + "\nbigmarker-f9\n"
	files["big.txt"] = big
	oid := pushMain(t, s, "ada", "codepub", files)
	ensureBranch(t, s, "ada", "codepub", oid)
	oid2 := pushMain(t, s, "ada", "codepub2", map[string]string{
		"g00.txt": "needle-f9 second repo\n",
		"g01.txt": "needle-f9 second repo again\n",
	})
	ensureBranch(t, s, "ada", "codepub2", oid2)
	return s, mountSearchAPI(t, s), ada, bob
}

func ensureBranch(t *testing.T, s *stack, owner, repo, oid string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	handle, err := s.client.OpenRepo(ctx, filepath.Join(s.roots, owner, repo+".git", ".ruk"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	if _, err := ruk.Call[map[string]any, map[string]any](ctx, s.client, handle, wire.MethodBookmarkSet, map[string]any{
		"name":     "refs/heads/main",
		"target":   map[string]any{"commit": map[string]any{"oid": oid}},
		"expected": map[string]any{"present": map[string]any{"direct": oid}},
	}); err != nil {
		t.Fatalf("bookmark.set: %v", err)
	}
}

func itoa2(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

func searchGet(t *testing.T, srv *httptest.Server, target, token string) (int, map[string]any, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+target, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		t.Fatalf("body: %v", rerr)
	}
	var decoded map[string]any
	if len(raw) > 0 {
		if uerr := jsonv2.Unmarshal(raw, &decoded, jsonv1.DefaultOptionsV1()); uerr != nil {
			t.Fatalf("decode %d: %v\n%s", resp.StatusCode, uerr, raw)
		}
	}
	return resp.StatusCode, decoded, resp.Header
}

func hitsOf(t *testing.T, body map[string]any) []any {
	t.Helper()
	hits, _ := body["hits"].([]any)
	if hits == nil {
		t.Fatalf("body has no hits list: %v", body)
	}
	return hits
}

func nextOf(body map[string]any) string {
	next, _ := body["next"].(string)
	return next
}

func TestSearchCodeSingleRepoCursorVerbatim(t *testing.T) {
	_, srv, ada, _ := searchSetup(t)
	first, body, _ := searchGet(t, srv, "/ada/codepub/search?q=needle-f9&per_page=5", ada.token)
	if first != http.StatusOK {
		t.Fatalf("page 1 = %d (%v)", first, body)
	}
	hits := hitsOf(t, body)
	if len(hits) != 5 {
		t.Fatalf("page 1 hits = %d, want 5", len(hits))
	}
	cursor := nextOf(body)
	if cursor == "" {
		t.Fatal("page 1 has no continuation")
	}
	if !strings.HasPrefix(cursor, "{") {
		t.Fatalf("single-repo cursor is not a verbatim Ruk cursor: %q", cursor)
	}
	for _, key := range []string{`"store"`, `"view"`, `"query"`, `"lease"`, `"position"`} {
		if !strings.Contains(cursor, key) {
			t.Fatalf("single-repo cursor lost daemon key %s: %q", key, cursor)
		}
	}
	for _, key := range []string{`"v"`, `"fp"`, `"cursors"`} {
		if strings.Contains(cursor, key+":") {
			t.Fatalf("single-repo cursor carries composite envelope key %s", key)
		}
	}
	second, body2, _ := searchGet(t, srv, "/ada/codepub/search?q=needle-f9&per_page=5&next="+url.QueryEscape(cursor), ada.token)
	if second != http.StatusOK {
		t.Fatalf("page 2 = %d (%v)", second, body2)
	}
	hits2 := hitsOf(t, body2)
	if len(hits2) == 0 {
		t.Fatal("page 2 has no hits")
	}
	firstHit := hits[0].(map[string]any)
	secondHit := hits2[0].(map[string]any)
	if firstHit["path"] == secondHit["path"] && firstHit["line"] == secondHit["line"] {
		t.Fatal("page 2 repeats page 1 instead of continuing")
	}
	again, body3, _ := searchGet(t, srv, "/ada/codepub/search?q=needle-f9&per_page=5&next="+url.QueryEscape(cursor), ada.token)
	if again != http.StatusOK {
		t.Fatalf("page 2 replay = %d", again)
	}
	hits3 := hitsOf(t, body3)
	if len(hits3) != len(hits2) {
		t.Fatal("cursor replay is not idempotent")
	}
}

func TestSearchExpiredLeaseProblem(t *testing.T) {
	_, srv, ada, _ := searchSetup(t)
	first, body, _ := searchGet(t, srv, "/ada/codepub/search?q=needle-f9&per_page=1", ada.token)
	if first != http.StatusOK {
		t.Fatalf("page 1 = %d (%v)", first, body)
	}
	cursor := nextOf(body)
	if cursor == "" {
		t.Fatal("page 1 has no continuation")
	}
	t.Log("waiting 32s for the 30s Ruk read lease to expire")
	time.Sleep(32 * time.Second)
	code, expired, _ := searchGet(t, srv, "/ada/codepub/search?q=needle-f9&per_page=1&next="+url.QueryEscape(cursor), ada.token)
	if code != http.StatusConflict {
		t.Fatalf("expired lease = %d, want 409 (%v)", code, expired)
	}
	typ, _ := expired["type"].(string)
	if !strings.Contains(typ, "lease-conflict") {
		t.Fatalf("expired lease problem = %q, want the lease-conflict catalogue entry", typ)
	}
}

func TestSearchNoLinkHeader(t *testing.T) {
	_, srv, ada, _ := searchSetup(t)
	sawNext := false
	for _, target := range []string{
		"/search?q=needle-f9&per_page=5",
		"/ada/codepub/search?q=needle-f9&per_page=5",
		"/search/repos?q=codepub&per_page=1",
		"/search/users?q=ada",
	} {
		code, body, header := searchGet(t, srv, target, ada.token)
		if code != http.StatusOK {
			t.Fatalf("GET %s = %d (%v)", target, code, body)
		}
		if nextOf(body) != "" {
			sawNext = true
		}
		if link := header.Get("Link"); strings.Contains(link, `rel="next"`) {
			t.Fatalf("GET %s emitted pagination Link: %q", target, link)
		}
	}
	if !sawNext {
		t.Fatal("no page carried a continuation to pin the header against")
	}
}

func TestSearchFanOutCompositeCursor(t *testing.T) {
	_, srv, ada, _ := searchSetup(t)
	first, body, _ := searchGet(t, srv, "/search?q=needle-f9&per_page=5", ada.token)
	if first != http.StatusOK {
		t.Fatalf("multi page 1 = %d (%v)", first, body)
	}
	seen := map[string]bool{}
	for _, h := range hitsOf(t, body) {
		m, _ := h.(map[string]any)
		repo, _ := m["repo"].(string)
		seen[repo] = true
	}
	if !seen["ada/codepub"] {
		t.Fatalf("fan-out missed ada/codepub: %v", seen)
	}
	cursor := nextOf(body)
	if cursor == "" {
		t.Fatal("multi page 1 has no continuation")
	}
	if strings.HasPrefix(cursor, "{") {
		t.Fatalf("multi cursor looks verbatim, want a composite envelope: %.60q", cursor)
	}
	second, body2, _ := searchGet(t, srv, "/search?q=needle-f9&per_page=5&next="+url.QueryEscape(cursor), ada.token)
	if second != http.StatusOK {
		t.Fatalf("multi page 2 = %d (%v)", second, body2)
	}
	if !seen["ada/codepub2"] {
		for _, h := range hitsOf(t, body2) {
			m, _ := h.(map[string]any)
			repo, _ := m["repo"].(string)
			seen[repo] = true
		}
		if !seen["ada/codepub2"] {
			t.Fatalf("fan-out missed ada/codepub2 across two pages: %v", seen)
		}
	}
	tampered := cursor[:len(cursor)-2] + "xx"
	if code, _, _ := searchGet(t, srv, "/search?q=needle-f9&per_page=5&next="+url.QueryEscape(tampered), ada.token); code != http.StatusBadRequest {
		t.Fatalf("tampered composite = %d, want 400", code)
	}
	other, otherBody, _ := searchGet(t, srv, "/search?q=zzz-no-such-needle-zzz&per_page=5", ada.token)
	if other != http.StatusOK {
		t.Fatalf("other query = %d", other)
	}
	_ = otherBody
	single, singleBody, _ := searchGet(t, srv, "/ada/codepub/search?q=needle-f9&per_page=5", ada.token)
	if single != http.StatusOK {
		t.Fatalf("single page 1 = %d", single)
	}
	singleCursor := nextOf(singleBody)
	if code, _, _ := searchGet(t, srv, "/search?q=needle-f9&per_page=5&next="+url.QueryEscape(singleCursor), ada.token); code != http.StatusBadRequest {
		t.Fatalf("verbatim cursor on multi endpoint = %d, want 400", code)
	}
}

func TestSearchSkipsLargeBlobs(t *testing.T) {
	_, srv, ada, _ := searchSetup(t)
	code, body, _ := searchGet(t, srv, "/ada/codepub/search?q=bigmarker-f9", ada.token)
	if code != http.StatusOK {
		t.Fatalf("search = %d (%v)", code, body)
	}
	for _, h := range hitsOf(t, body) {
		m, _ := h.(map[string]any)
		if path, _ := m["path"].(string); path == "big.txt" {
			t.Fatal("over-512 KiB blob was searched")
		}
	}
	skipped, _ := body["skipped_blobs"].(float64)
	if skipped < 1 {
		t.Fatalf("skipped_blobs = %v, want at least the 600 KiB blob", body["skipped_blobs"])
	}
}

func TestSearchInvalidRegex(t *testing.T) {
	_, srv, ada, _ := searchSetup(t)
	code, body, _ := searchGet(t, srv, "/ada/codepub/search?q=%28unclosed&regex=true", ada.token)
	if code != http.StatusBadRequest {
		t.Fatalf("invalid regex = %d, want 400 (%v)", code, body)
	}
}

func TestSearchAliasRoutes(t *testing.T) {
	_, srv, ada, _ := searchSetup(t)
	for _, target := range []string{
		"/ada/codepub/search?q=needle-f9&per_page=2",
		"/explore/code?q=needle-f9&per_page=2",
	} {
		code, body, _ := searchGet(t, srv, target, ada.token)
		if code != http.StatusOK {
			t.Fatalf("GET %s = %d (%v)", target, code, body)
		}
		if body["type"] != "code" {
			t.Fatalf("GET %s type = %v, want code", target, body["type"])
		}
		if len(hitsOf(t, body)) == 0 {
			t.Fatalf("GET %s has no hits", target)
		}
	}
}

func TestSearchUnifiedTypes(t *testing.T) {
	_, srv, ada, _ := searchSetup(t)
	code, body, _ := searchGet(t, srv, "/search?type=repos&q=codepub", ada.token)
	if code != http.StatusOK || body["type"] != "repos" {
		t.Fatalf("unified repos = %d %v", code, body)
	}
	if items, _ := body["repos"].([]any); len(items) == 0 {
		t.Fatal("unified repos has no rows")
	}
	code, body, _ = searchGet(t, srv, "/search?type=users&q=ada", ada.token)
	if code != http.StatusOK || body["type"] != "users" {
		t.Fatalf("unified users = %d %v", code, body)
	}
	if items, _ := body["users"].([]any); len(items) == 0 {
		t.Fatal("unified users has no rows")
	}
}
