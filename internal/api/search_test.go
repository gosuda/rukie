package api_test

import (
	"context"
	"database/sql"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/api"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/store"
	"github.com/gosuda/rukie/internal/web/zerojs"
)

type searchEnv struct {
	server http.Handler
	svc    *auth.Service
	st     *store.Store
	adaTok string
	bobTok string
}

func buildSearchEnv(t *testing.T) *searchEnv {
	t.Helper()
	path := t.TempDir() + "/search.sqlite"
	st, err := store.Open(t.Context(), store.Options{Path: path})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	svc := auth.New(st)
	deps := api.Deps{Service: svc, Limiter: access.NewLimiter(), Reveals: auth.NewRevealCache(), Store: st}
	table := op.NewTable()
	for _, step := range []func() error{
		func() error { return api.RegisterAll(table, &api.DocStore{}) },
		func() error { return api.RegisterAuth(table, deps) },
		func() error { return api.RegisterUser(table, deps) },
		func() error { return api.RegisterAdmin(table, deps) },
		func() error { return api.RegisterSearch(table, deps) },
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
	api.RegisterSearchViews(views)
	var key [32]byte
	mux := http.NewServeMux()
	server, merr := op.Mount(mux, table, op.Config{
		Authenticator: auth.NewAuthenticator(svc),
		IsSiteSegment: api.SiteSegment,
		Idempotency:   op.NewMemoryIdempotency(),
		Audit:         op.FuncAuditor(func(context.Context, op.AuditRow) error { return nil }),
		Limiter:       deps.Limiter,
		CSRFKey:       key[:],
		Views:         views,
		Title:         "test",
		Version:       "0",
	})
	if merr != nil {
		t.Fatalf("mount: %v", merr)
	}
	wrapped := api.RewriteSearchPaths(server)
	return &searchEnv{server: wrapped, svc: svc, st: st}
}

func seedSearchUsers(t *testing.T, e *searchEnv) (ada, bob, cara auth.User) {
	t.Helper()
	ctx := t.Context()
	var err error
	ada, err = e.svc.BootstrapAdmin(ctx, "ada", "Aa1!test-password-01", "127.0.0.1")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	bob, err = e.svc.CreateUser(ctx, ada.ID, auth.CreateUserInput{
		Name: "bob", Email: "bob-secret@example.test", FullName: "Bob Builder",
		Password: "Aa1!test-password-01",
	}, "127.0.0.1")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	cara, err = e.svc.CreateUser(ctx, ada.ID, auth.CreateUserInput{
		Name: "cara", Email: "cara@example.test", FullName: "Cara Coder",
		Password: "Aa1!test-password-01",
	}, "127.0.0.1")
	if err != nil {
		t.Fatalf("create cara: %v", err)
	}
	if derr := e.st.UpdateAuthUserAdmin(ctx, cara.ID, cara.FullName, cara.Email, false, time.Now().UnixMilli()); derr != nil {
		t.Fatalf("deactivate cara: %v", derr)
	}
	mint := func(u auth.User, scopes ...string) string {
		parsed, perr := auth.ParseScopeSet(scopes)
		if perr != nil {
			t.Fatalf("scopes: %v", perr)
		}
		secret, serr := e.svc.CreateToken(ctx, auth.CreateTokenInput{
			Name: "test", Scopes: parsed, UserID: u.ID,
		})
		if serr != nil {
			t.Fatalf("mint: %v", serr)
		}
		return secret.Secret
	}
	e.adaTok = mint(ada, "repo:read", "repo:write")
	e.bobTok = mint(bob, "repo:read")
	return ada, bob, cara
}

func seedSearchRepo(t *testing.T, e *searchEnv, ownerID int64, name, visibility, desc string) {
	t.Helper()
	err := e.st.Write(t.Context(), func(tx *store.WriteTx) error {
		_, ierr := tx.InsertRepo(time.Now(), store.RepoInsert{
			OwnerUserID:   sql.NullInt64{Int64: ownerID, Valid: true},
			Name:          name,
			Visibility:    visibility,
			Description:   desc,
			Topics:        `["search"]`,
			DefaultBranch: "main",
			RukPath:       "dummy/" + name + ".git",
		})
		return ierr
	})
	if err != nil {
		t.Fatalf("insert repo %s: %v", name, err)
	}
}

func searchJSON(t *testing.T, e *searchEnv, method, target, authHeader string) (int, map[string]any, http.Header) {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.Header.Set("Accept", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", "Bearer "+authHeader)
	}
	rec := httptest.NewRecorder()
	e.server.ServeHTTP(rec, req)
	var decoded map[string]any
	if rec.Body.Len() > 0 {
		decoded = decodeBody(t, rec)
	}
	return rec.Code, decoded, rec.Header()
}

func searchHTML(t *testing.T, e *searchEnv, target, authHeader string) (int, string, http.Header) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	if authHeader != "" {
		req.Header.Set("Authorization", "Bearer "+authHeader)
	}
	rec := httptest.NewRecorder()
	e.server.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String(), rec.Header()
}

func TestSearchValidationTable(t *testing.T) {
	e := buildSearchEnv(t)
	ada, _, _ := seedSearchUsers(t, e)
	seedSearchRepo(t, e, ada.ID, "hello", "public", "hello world")
	long := strings.Repeat("x", 4097)
	globs33 := strings.Repeat("a,", 32) + "b"
	bigGlob := strings.Repeat("g", 1025)
	_ = ada
	for _, tc := range []struct {
		name   string
		target string
		slug   string
		auth   string
		code   int
	}{
		{"code empty q", "/search?q=", "validation", "ada", 422},
		{"code q too long", "/search?q=" + long, "validation", "ada", 422},
		{"code bad type", "/search?q=x&type=bogus", "invalid", "ada", 400},
		{"code too many globs", "/search?q=x&globs=" + globs33, "validation", "ada", 422},
		{"code big glob", "/search?q=x&globs=" + bigGlob, "validation", "ada", 422},
		{"code owner without repo", "/search?q=x&owner=ada", "validation", "ada", 422},
		{"code rev too long", "/search?q=x&rev=" + long, "validation", "ada", 422},
		{"code bad path", "/search?q=x&path=../evil", "validation", "ada", 422},
		{"code garbage cursor multi", "/search?q=x&next=!!!", "invalid", "ada", 400},
		{"code single-shaped cursor on multi", "/search?q=x&next=%7B%22a%22%3A1%7D", "invalid", "ada", 400},
		{"code wrong-fingerprint cursor", "/search?q=x&next=" + fakeComposite(t, "ff"), "invalid", "ada", 400},
		{"code garbage cursor single", "/search?q=x&owner=ada&repo=hello&next=!!!", "invalid", "ada", 400},
		{"repos empty q", "/search/repos?q=", "validation", "", 422},
		{"repos q too long", "/search/repos?q=" + long, "validation", "", 422},
		{"repos garbage cursor", "/search/repos?q=x&next=!!!", "invalid", "", 400},
		{"users empty q", "/search/users?q=", "validation", "", 422},
		{"users q too long", "/search/users?q=" + long, "validation", "", 422},
		{"users garbage cursor", "/search/users?q=x&next=!!!", "invalid", "", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := ""
			if tc.auth == "ada" {
				token = e.adaTok
			}
			code, body, _ := searchJSON(t, e, http.MethodGet, tc.target, token)
			if code != tc.code {
				t.Fatalf("GET %s = %d, want %d (%v)", tc.target, code, tc.code, body)
			}
			typ, _ := body["type"].(string)
			_ = typ
			if !strings.Contains(recProblemSlug(body), tc.slug) {
				t.Fatalf("GET %s problem = %v, want slug containing %q", tc.target, body, tc.slug)
			}
		})
	}
}

func recProblemSlug(body map[string]any) string {
	if body == nil {
		return ""
	}
	typ, _ := body["type"].(string)
	return typ
}

func fakeComposite(t *testing.T, fp string) string {
	t.Helper()
	raw := `{"v":1,"fp":"` + fp + `","cursors":{},"after_name":"","after_id":0}`
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func TestSearchEmptyQueryInvalid(t *testing.T) {
	e := buildSearchEnv(t)
	ada, _, _ := seedSearchUsers(t, e)
	seedSearchRepo(t, e, ada.ID, "hello", "public", "hello world")
	for _, target := range []struct {
		url  string
		auth string
	}{
		{"/search", "ada"},
		{"/search/repos", ""},
		{"/search/users", ""},
		{"/explore/code", "ada"},
		{"/ada/hello/search", "ada"},
	} {
		token := ""
		if target.auth == "ada" {
			token = e.adaTok
		}
		code, decoded, _ := searchJSON(t, e, http.MethodGet, target.url, token)
		if code != http.StatusUnprocessableEntity {
			t.Fatalf("GET %s json = %d, want 422 (%v)", target.url, code, decoded)
		}
		code, body, _ := searchHTML(t, e, target.url, token)
		if code != http.StatusUnprocessableEntity {
			t.Fatalf("GET %s html = %d, want 422", target.url, code)
		}
		if err := zerojs.Assert([]byte(body)); err != nil {
			t.Fatalf("GET %s failed zero-JS: %v", target.url, err)
		}
		if strings.Contains(body, "<script") {
			t.Fatalf("GET %s contains script", target.url)
		}
	}
	code, _, _ := searchJSON(t, e, http.MethodGet, "/search", "")
	if code != http.StatusForbidden {
		t.Fatalf("GET /search anonymous json = %d, want 403", code)
	}
	code, decoded, _ := searchJSON(t, e, http.MethodGet, "/search?type=repos&q=x", e.adaTok)
	if code != http.StatusOK {
		t.Fatalf("GET unified repos json = %d", code)
	}
	if decoded["type"] != "repos" {
		t.Fatalf("unified type = %v, want repos", decoded["type"])
	}
}

func TestSearchCodeScopeHonored(t *testing.T) {
	e := buildSearchEnv(t)
	ada, bob, _ := seedSearchUsers(t, e)
	ctx := t.Context()
	seedSearchRepo(t, e, ada.ID, "hello", "public", "hello world")
	code, _, _ := searchJSON(t, e, http.MethodGet, "/search?q=x", "")
	if code != http.StatusForbidden {
		t.Fatalf("anonymous code search = %d, want 403", code)
	}
	code, body, _ := searchJSON(t, e, http.MethodGet, "/search/repos?q=hello", "")
	if code != http.StatusOK {
		t.Fatalf("anonymous repos search = %d, want 200 (%v)", code, body)
	}
	parsed, _ := auth.ParseScopeSet([]string{"repo:create"})
	secret, _ := e.svc.CreateToken(ctx, auth.CreateTokenInput{Name: "noread", Scopes: parsed, UserID: bob.ID})
	code, _, _ = searchJSON(t, e, http.MethodGet, "/search?q=x", secret.Secret)
	if code != http.StatusForbidden {
		t.Fatalf("token without repo:read = %d, want 403", code)
	}
}

func TestSearchReposFTS(t *testing.T) {
	e := buildSearchEnv(t)
	ada, bob, _ := seedSearchUsers(t, e)
	seedSearchRepo(t, e, ada.ID, "hello", "public", "Hello world service")
	seedSearchRepo(t, e, ada.ID, "secret", "private", "needle-project vault")
	seedSearchRepo(t, e, bob.ID, "world", "public", "WORLD explorer")
	for _, tc := range []struct {
		q    string
		want []string
	}{
		{"hello", []string{"ada/hello"}},
		{"HELLO", []string{"ada/hello"}},
		{"world", []string{"ada/hello", "bob/world"}},
		{"vault", []string{}},
		{"o", []string{"ada/hello", "bob/world"}},
		{"ll", []string{"ada/hello"}},
	} {
		code, body, _ := searchJSON(t, e, http.MethodGet, "/search/repos?q="+tc.q, "")
		if code != http.StatusOK {
			t.Fatalf("q=%s code = %d (%v)", tc.q, code, body)
		}
		got := repoNames(body)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("q=%s got %v, want %v", tc.q, got, tc.want)
		}
	}
	code, body, _ := searchJSON(t, e, http.MethodGet, "/search/repos?q=needle", "")
	if code != http.StatusOK || len(repoNames(body)) != 0 {
		t.Fatalf("anon private search = %d %v, want empty", code, body)
	}
	code, body, _ = searchJSON(t, e, http.MethodGet, "/search/repos?q=needle", e.adaTok)
	if code != http.StatusOK || strings.Join(repoNames(body), ",") != "ada/secret" {
		t.Fatalf("owner private search = %d %v, want ada/secret", code, body)
	}
	code, body, _ = searchJSON(t, e, http.MethodGet, "/search/repos?q=needle", e.bobTok)
	if code != http.StatusOK || len(repoNames(body)) != 0 {
		t.Fatalf("bob private search = %d %v, want empty", code, body)
	}
	code, body, _ = searchJSON(t, e, http.MethodGet, "/search/repos?q=hello+OR+world", "")
	if code != http.StatusOK {
		t.Fatalf("operator query code = %d (%v)", code, body)
	}
}

func repoNames(body map[string]any) []string {
	items, _ := body["repos"].([]any)
	var out []string
	for _, item := range items {
		m, _ := item.(map[string]any)
		owner, _ := m["owner"].(string)
		name, _ := m["name"].(string)
		out = append(out, owner+"/"+name)
	}
	return out
}

func TestSearchUsersDirectory(t *testing.T) {
	e := buildSearchEnv(t)
	ada, bob, cara := seedSearchUsers(t, e)
	_ = ada
	_ = cara
	code, body, _ := searchJSON(t, e, http.MethodGet, "/search/users?q=builder", "")
	if code != http.StatusOK {
		t.Fatalf("code = %d (%v)", code, body)
	}
	if got := userNames(body); strings.Join(got, ",") != "bob" {
		t.Fatalf("q=builder got %v, want [bob]", got)
	}
	code, body, _ = searchJSON(t, e, http.MethodGet, "/search/users?q=a", "")
	if code != http.StatusOK {
		t.Fatalf("code = %d (%v)", code, body)
	}
	if got := userNames(body); strings.Join(got, ",") != "ada" {
		t.Fatalf("q=a got %v, want [ada]", got)
	}
	code, body, _ = searchJSON(t, e, http.MethodGet, "/search/users?q=bob-secret", "")
	if code != http.StatusOK || len(userNames(body)) != 0 {
		t.Fatalf("email probe = %d %v, want empty", code, body)
	}
	code, body, _ = searchJSON(t, e, http.MethodGet, "/search/users?q=cara", "")
	if code != http.StatusOK || len(userNames(body)) != 0 {
		t.Fatalf("inactive search = %d %v, want empty", code, body)
	}
	_ = bob
}

func userNames(body map[string]any) []string {
	items, _ := body["users"].([]any)
	var out []string
	for _, item := range items {
		m, _ := item.(map[string]any)
		name, _ := m["name"].(string)
		out = append(out, name)
	}
	return out
}

func TestSearchReposPaging(t *testing.T) {
	e := buildSearchEnv(t)
	ada, _, _ := seedSearchUsers(t, e)
	for i := range 105 {
		seedSearchRepo(t, e, ada.ID, padName(i), "public", "padding")
	}
	for _, tc := range []struct {
		perPage string
		want    int
	}{
		{"-1", 30}, {"0", 30}, {"1", 1}, {"99", 99}, {"100", 100}, {"101", 100},
	} {
		code, body, _ := searchJSON(t, e, http.MethodGet, "/search/repos?q=pad-&per_page="+tc.perPage, "")
		if code != http.StatusOK {
			t.Fatalf("per_page=%s code = %d", tc.perPage, code)
		}
		if got := len(repoNames(body)); got != tc.want {
			t.Fatalf("per_page=%s got %d rows, want %d", tc.perPage, got, tc.want)
		}
	}
	var seen []string
	next := ""
	for {
		target := "/search/repos?q=pad-&per_page=40"
		if next != "" {
			target += "&next=" + next
		}
		code, body, _ := searchJSON(t, e, http.MethodGet, target, "")
		if code != http.StatusOK {
			t.Fatalf("walk code = %d", code)
		}
		seen = append(seen, repoNames(body)...)
		n, _ := body["next"].(string)
		if n == "" {
			break
		}
		next = n
	}
	if len(seen) != 105 {
		t.Fatalf("walk covered %d rows, want 105", len(seen))
	}
	for i := 1; i < len(seen); i++ {
		if seen[i-1] >= seen[i] {
			t.Fatalf("walk out of order at %d: %q >= %q", i, seen[i-1], seen[i])
		}
	}
}

func padName(i int) string {
	return "pad-" + strings.Repeat("0", 3-len(itoaPad(i))) + itoaPad(i)
}

func itoaPad(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func TestSearchNoLinkHeader(t *testing.T) {
	e := buildSearchEnv(t)
	ada, _, _ := seedSearchUsers(t, e)
	for i := range 5 {
		seedSearchRepo(t, e, ada.ID, padName(i), "public", "padding")
	}
	for _, target := range []struct {
		url  string
		auth string
	}{
		{"/search?type=repos&q=pad-&per_page=2", "ada"},
		{"/search?type=users&q=zzz-no-such-user", "ada"},
		{"/search/repos?q=pad-&per_page=2", ""},
		{"/search/users?q=zzz-no-such-user", ""},
		{"/explore/code?type=repos&q=pad-&per_page=2", "ada"},
	} {
		token := ""
		if target.auth == "ada" {
			token = e.adaTok
		}
		code, _, header := searchJSON(t, e, http.MethodGet, target.url, token)
		if code != http.StatusOK {
			t.Fatalf("GET %s = %d", target.url, code)
		}
		if link := header.Get("Link"); strings.Contains(link, `rel="next"`) {
			t.Fatalf("GET %s emitted pagination Link: %q", target, link)
		}
	}
}

func TestSearchBadBearer(t *testing.T) {
	e := buildSearchEnv(t)
	code, _, _ := searchJSON(t, e, http.MethodGet, "/search/repos?q=x", "bogus-token")
	if code != http.StatusUnauthorized {
		t.Fatalf("bad bearer = %d, want 401", code)
	}
}
