package api_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/api"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/store"
	"github.com/gosuda/rukie/internal/web/zerojs"
)

type testEnv struct {
	server *op.Server
	svc    *auth.Service
	deps   api.Deps
}

func buildEnv(t *testing.T) *testEnv {
	t.Helper()
	path := t.TempDir() + "/k5.sqlite"
	st, err := store.Open(t.Context(), store.Options{Path: path})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	svc, err := auth.Open(path)
	if err != nil {
		t.Fatalf("auth open: %v", err)
	}
	t.Cleanup(func() {
		_ = svc.Close()
		_ = st.Close(context.Background())
	})
	deps := api.Deps{Service: svc, Limiter: access.NewLimiter(), Reveals: auth.NewRevealCache()}
	table := op.NewTable()
	if regErr := api.RegisterAuth(table, deps); regErr != nil {
		t.Fatalf("register auth: %v", regErr)
	}
	if regErr := api.RegisterUser(table, deps); regErr != nil {
		t.Fatalf("register user: %v", regErr)
	}
	if regErr := api.RegisterAdmin(table, deps); regErr != nil {
		t.Fatalf("register admin: %v", regErr)
	}
	if regErr := api.RegisterF12(table, deps); regErr != nil {
		t.Fatalf("register f12: %v", regErr)
	}
	if validErr := table.Validate(); validErr != nil {
		t.Fatalf("validate: %v", validErr)
	}
	views := op.NewViews()
	api.RegisterAuthViews(views)
	api.RegisterUserViews(views)
	api.RegisterAdminViews(views)
	api.RegisterF12Views(views)
	var key [32]byte
	_, _ = rand.Read(key[:])
	mux := http.NewServeMux()
	server, err := op.Mount(mux, table, op.Config{
		Authenticator: auth.NewAuthenticator(svc),
		Idempotency:   op.NewMemoryIdempotency(),
		Audit:         op.FuncAuditor(func(context.Context, op.AuditRow) error { return nil }),
		Limiter:       deps.Limiter,
		CSRFKey:       key[:],
		Views:         views,
		Title:         "test",
		Version:       "0",
	})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	return &testEnv{server: server, svc: svc, deps: deps}
}

func doJSON(t *testing.T, e *testEnv, method, target, cookie, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	rec := httptest.NewRecorder()
	e.server.ServeHTTP(rec, req)
	return rec
}

func doForm(t *testing.T, e *testEnv, method, target, cookie, accept, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", accept)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	rec := httptest.NewRecorder()
	e.server.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := jsonv2.Unmarshal(rec.Body.Bytes(), &decoded, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("decode %d: %v\n%s", rec.Code, err, rec.Body.String())
	}
	return decoded
}

func randomPW(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return "Aa1!" + base64.RawURLEncoding.EncodeToString(raw)
}

func createAdmin(t *testing.T, e *testEnv, name, pw string) {
	t.Helper()
	if _, err := e.svc.BootstrapAdmin(t.Context(), name, pw, "127.0.0.1"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
}

func loginCookie(t *testing.T, e *testEnv, username, password string) string {
	t.Helper()
	rec := doJSON(t, e, "POST", "/login", "", `{"username":"`+username+`","password":"`+password+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("login = %d: %s", rec.Code, rec.Body.String())
	}
	setCookie := rec.Header().Get("Set-Cookie")
	if !strings.HasPrefix(setCookie, "__Host-") {
		t.Fatalf("Set-Cookie = %q", setCookie)
	}
	parts := strings.Split(setCookie, ";")
	return strings.TrimSpace(parts[0])
}

func TestAuthOpRegistration(t *testing.T) {
	e := buildEnv(t)
	specs := e.server.Specs()
	names := map[string]bool{}
	hidden := map[string]bool{}
	for _, s := range specs {
		names[s.Name] = true
		if s.Hidden {
			hidden[s.Name] = true
		}
	}
	for _, want := range []string{
		"auth.login", "auth.logout", "auth.password.change",
		"auth.token.list", "auth.token.create", "auth.token.revoke",
		"auth.session.list", "auth.session.revoke",
		"auth.totp.verify", "auth.totp.enroll", "auth.totp.disable",
		"auth.recovery.regenerate", "auth.oidc.start", "auth.oidc.callback",
		"auth.oidc.link", "admin.user.totp-reset",
		"user.get", "user.self.update", "user.self.delete",
		"admin.user.create", "admin.user.reset-password", "admin.user.unlock",
		"admin.user.update", "admin.user.list",
	} {
		if !names[want] {
			t.Fatalf("op %q not registered", want)
		}
	}
	for _, want := range []string{"auth.login.page", "auth.logout.page", "settings.page", "auth.totpverify.page", "auth.totp.page"} {
		if !hidden[want] {
			t.Fatalf("page op %q missing or not hidden", want)
		}
	}
	if len(specs) != 29 {
		t.Fatalf("ops = %d, want 29", len(specs))
	}
	tools, err := op.BuildTools(specs, e.server.Document(), op.ToolFilter{Principal: op.Principal{Kind: op.PrincipalHuman, Grants: adminGrants()}})
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	for _, tool := range tools {
		if strings.HasSuffix(tool.Name, "_page") {
			t.Fatalf("page op leaked into MCP: %q", tool.Name)
		}
	}
	root, err := op.BuildCommands(specs, e.server.Document())
	if err != nil {
		t.Fatalf("commands: %v", err)
	}
	_ = root
}

func adminGrants() op.Scopes {
	var out op.Scopes
	for _, s := range op.AllScopes() {
		out = out.Add(s)
	}
	return out
}

func TestLoginLogoutFlow(t *testing.T) {
	e := buildEnv(t)
	pw := randomPW(t)
	createAdmin(t, e, "ada", pw)
	rec := doJSON(t, e, "POST", "/login", "", `{"username":"ada","password":"wrongpassword1!"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad login = %d", rec.Code)
	}
	if body := decodeBody(t, rec); body["detail"] != "Invalid username or password." {
		t.Fatalf("bad login detail = %v", body)
	}
	cookie := loginCookie(t, e, "ada", pw)
	rec = doJSON(t, e, "GET", "/settings", cookie, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("settings = %d: %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, e, "POST", "/logout", cookie, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("logout = %d", rec.Code)
	}
	if got := rec.Header().Get("Set-Cookie"); !strings.HasPrefix(got, "__Host-") {
		t.Fatalf("logout clear = %q", got)
	}
	rec = doJSON(t, e, "GET", "/settings", cookie, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("settings after logout = %d", rec.Code)
	}
	cookie2 := loginCookie(t, e, "ada", pw)
	csrfCookie, csrfField := getCSRF(t, e, "/logout", cookie2)
	rec = doForm(t, e, "POST", "/logout", cookie2+"; "+csrfCookie, "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", "_csrf="+csrfField+"&_idem=x")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("form logout = %d: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Fatalf("logout location = %q", loc)
	}
}

func getCSRF(t *testing.T, e *testEnv, path, cookie string) (string, string) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	rec := httptest.NewRecorder()
	e.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("csrf page %s = %d", path, rec.Code)
	}
	var csrfCookie string
	for _, h := range rec.Header().Values("Set-Cookie") {
		if strings.HasPrefix(h, "__Host-rukie_csrf=") {
			csrfCookie = strings.TrimSpace(strings.Split(h, ";")[0])
		}
	}
	if csrfCookie == "" {
		t.Fatalf("no csrf cookie on %s", path)
	}
	body := rec.Body.String()
	idx := strings.Index(body, `name="_csrf" value="`)
	if idx < 0 {
		t.Fatalf("no csrf field on %s", path)
	}
	rest := body[idx+len(`name="_csrf" value="`):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		t.Fatalf("unterminated csrf field on %s", path)
	}
	return csrfCookie, rest[:end]
}

func TestLoginRateLimit429(t *testing.T) {
	e := buildEnv(t)
	pw := randomPW(t)
	createAdmin(t, e, "ada", pw)
	bad := `{"username":"ada","password":"wrongpassword1!"}`
	for i := range 5 {
		if rec := doJSON(t, e, "POST", "/login", "", bad); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d", i+1, rec.Code)
		}
	}
	if rec := doJSON(t, e, "POST", "/login", "", bad); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("attempt 6 = %d, want 429", rec.Code)
	}
}

func TestFreshGatesTokenCreate(t *testing.T) {
	e := buildEnv(t)
	pw := randomPW(t)
	createAdmin(t, e, "ada", pw)
	cookie := loginCookie(t, e, "ada", pw)
	rec := doJSON(t, e, "POST", "/settings/tokens", cookie, `{"name":"agent","scopes":"repo:read repo:write"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("token create = %d: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	secret, _ := body["secret"].(string)
	prefix, _ := body["prefix"].(string)
	if secret == "" || prefix == "" || !strings.HasPrefix(secret, "rk_") {
		t.Fatalf("token body = %v", body)
	}
	rec = doJSON(t, e, "GET", "/settings/tokens?reveal="+prefix, cookie, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("reveal = %d", rec.Code)
	}
	shown := decodeBody(t, rec)
	if shown["secret"] != secret {
		t.Fatalf("reveal mismatch: %v", shown)
	}
	again := doJSON(t, e, "GET", "/settings/tokens?reveal="+prefix, cookie, "")
	if shown2 := decodeBody(t, again); shown2["secret"] != nil {
		t.Fatalf("secret shown twice: %v", shown2)
	}
	bearer := "Bearer " + secret
	req := httptest.NewRequest("POST", "/settings/tokens", strings.NewReader(`{"name":"x","scopes":"repo:read"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", bearer)
	rec2 := httptest.NewRecorder()
	e.server.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("token mint by token = %d", rec2.Code)
	}
	req = httptest.NewRequest("POST", "/admin/users", strings.NewReader(`{"name":"bob","password":"`+randomPW(t)+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", bearer)
	rec3 := httptest.NewRecorder()
	e.server.ServeHTTP(rec3, req)
	if rec3.Code != http.StatusForbidden {
		t.Fatalf("admin create by read token = %d", rec3.Code)
	}
}

func TestUserProfile404(t *testing.T) {
	e := buildEnv(t)
	pw := randomPW(t)
	createAdmin(t, e, "ada", pw)
	if rec := doJSON(t, e, "GET", "/nobody", "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown user = %d", rec.Code)
	}
	if rec := doJSON(t, e, "GET", "/ada", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("profile = %d", rec.Code)
	}
	req := httptest.NewRequest("GET", "/ada", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	rec := httptest.NewRecorder()
	e.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("profile html = %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	if err := zerojs.Assert(rec.Body.Bytes()); err != nil {
		t.Fatalf("profile zero-js: %v", err)
	}
}

func TestSelfUpdateETag(t *testing.T) {
	e := buildEnv(t)
	pw := randomPW(t)
	createAdmin(t, e, "ada", pw)
	cookie := loginCookie(t, e, "ada", pw)
	rec := doJSON(t, e, "GET", "/settings", cookie, "")
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("settings has no etag")
	}
	if rec := doJSON(t, e, "POST", "/settings", cookie, `{"full_name":"Ada"}`); rec.Code != 428 {
		t.Fatalf("missing if-match = %d", rec.Code)
	}
	req := httptest.NewRequest("POST", "/settings", strings.NewReader(`{"full_name":"Ada"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("If-Match", `"1-2"`)
	rec2 := httptest.NewRecorder()
	e.server.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale etag = %d: %s", rec2.Code, rec2.Body.String())
	}
	req = httptest.NewRequest("POST", "/settings", strings.NewReader(`{"full_name":"Ada Lovelace"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("If-Match", etag)
	rec3 := httptest.NewRecorder()
	e.server.ServeHTTP(rec3, req)
	if rec3.Code != http.StatusCreated {
		t.Fatalf("update = %d: %s", rec3.Code, rec3.Body.String())
	}
}

func TestAdminLastAdmin(t *testing.T) {
	e := buildEnv(t)
	pw := randomPW(t)
	createAdmin(t, e, "ada", pw)
	cookie := loginCookie(t, e, "ada", pw)
	rec := doJSON(t, e, "GET", "/admin/users", cookie, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin list = %d: %s", rec.Code, rec.Body.String())
	}
	bobPW := randomPW(t)
	rec = doJSON(t, e, "POST", "/admin/users", cookie, `{"name":"bob","password":"`+bobPW+`","is_admin":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create bob = %d: %s", rec.Code, rec.Body.String())
	}
	bobCookie := loginCookie(t, e, "bob", bobPW)
	rec = doJSON(t, e, "POST", "/admin/users", cookie, `{"name":"ada2","password":"`+randomPW(t)+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create ada2 = %d", rec.Code)
	}
	deactivate := func(target, actorCookie string) int {
		tag := adminETagFor(t, e, target)
		req := httptest.NewRequest("POST", "/admin/users/"+target, strings.NewReader(`{"is_active":false}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Cookie", actorCookie)
		req.Header.Set("If-Match", tag)
		r := httptest.NewRecorder()
		e.server.ServeHTTP(r, req)
		return r.Code
	}
	if code := deactivate("bob", cookie); code != http.StatusCreated {
		t.Fatalf("deactivate bob = %d", code)
	}
	if rec := doJSON(t, e, "GET", "/admin/users", bobCookie, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("inactive admin list = %d", rec.Code)
	}
	if code := deactivate("ada", cookie); code != http.StatusConflict {
		t.Fatalf("deactivate last admin = %d, want 409", code)
	}
}

func TestAdminUserListPagination(t *testing.T) {
	e := buildEnv(t)
	pw := randomPW(t)
	createAdmin(t, e, "ada", pw)
	cookie := loginCookie(t, e, "ada", pw)
	for _, name := range []string{"bob", "carol"} {
		rec := doJSON(t, e, "POST", "/admin/users", cookie, `{"name":"`+name+`","password":"`+randomPW(t)+`"}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create %s = %d: %s", name, rec.Code, rec.Body.String())
		}
	}
	rec := doJSON(t, e, "GET", "/admin/users?limit=1", cookie, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("limit=1 = %d: %s", rec.Code, rec.Body.String())
	}
	if users, _ := decodeBody(t, rec)["users"].([]any); len(users) != 1 {
		t.Fatalf("limit=1 users = %d, want 1", len(users))
	}
	link := rec.Header().Get("Link")
	if !strings.Contains(link, `rel="next"`) {
		t.Fatalf("limit=1 link = %q, want rel=next", link)
	}
	cursor := linkCursor(t, link)
	rec = doJSON(t, e, "GET", "/admin/users?limit=1&cursor="+cursor, cookie, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("page two = %d: %s", rec.Code, rec.Body.String())
	}
	if users, _ := decodeBody(t, rec)["users"].([]any); len(users) != 1 {
		t.Fatalf("page two users = %d, want 1", len(users))
	}
	rec = doJSON(t, e, "GET", "/admin/users?limit=100", cookie, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("limit=100 = %d: %s", rec.Code, rec.Body.String())
	}
	if users, _ := decodeBody(t, rec)["users"].([]any); len(users) != 3 {
		t.Fatalf("limit=100 users = %d, want 3", len(users))
	}
	if link := rec.Header().Get("Link"); strings.Contains(link, `rel="next"`) {
		t.Fatalf("full page link = %q, want no rel=next", link)
	}
	rec = doJSON(t, e, "GET", "/admin/users?limit=101", cookie, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("limit=101 = %d, want 400", rec.Code)
	}
	if typ := decodeBody(t, rec)["type"]; typ != "/-/problems/invalid" {
		t.Fatalf("limit=101 type = %v, want /-/problems/invalid", typ)
	}
}

func linkCursor(t *testing.T, link string) string {
	t.Helper()
	start := strings.Index(link, "cursor=")
	if start < 0 {
		t.Fatalf("link %q has no cursor", link)
	}
	rest := link[start+len("cursor="):]
	end := strings.IndexAny(rest, "&>")
	if end < 0 {
		t.Fatalf("link %q has unterminated cursor", link)
	}
	return rest[:end]
}

func adminETagFor(t *testing.T, e *testEnv, name string) string {
	t.Helper()
	u, err := e.svc.GetUser(t.Context(), name)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	tag, err := e.svc.ETagFor(u.ID)
	if err != nil {
		t.Fatalf("etag: %v", err)
	}
	return tag
}

func postForm(t *testing.T, e *testEnv, postPath, csrfPath, cookie, body string) *httptest.ResponseRecorder {
	t.Helper()
	csrfCookie, csrfField := getCSRF(t, e, csrfPath, cookie)
	full := "_csrf=" + csrfField + "&_idem=form-" + csrfField[:8] + "&" + body
	cookies := csrfCookie
	if cookie != "" {
		cookies = cookie + "; " + csrfCookie
	}
	return doForm(t, e, "POST", postPath, cookies, "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", full)
}

func TestFormFlows(t *testing.T) {
	e := buildEnv(t)
	pw := randomPW(t)
	createAdmin(t, e, "ada", pw)
	cookie := loginCookie(t, e, "ada", pw)
	rec := postForm(t, e, "/admin/users", "/admin/users", cookie, "name=bob&password="+randomPW(t)+"&is_admin=1")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("form create = %d: %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, e, "GET", "/admin/users", cookie, "")
	users, _ := decodeBody(t, rec)["users"].([]any)
	foundAdmin := false
	for _, u := range users {
		m, _ := u.(map[string]any)
		if m["name"] == "bob" && m["is_admin"] == true {
			foundAdmin = true
		}
	}
	if !foundAdmin {
		t.Fatalf("form-created bob missing or not admin: %s", rec.Body.String())
	}
	rec = postForm(t, e, "/settings/tokens", "/settings/tokens", cookie, "name=agent&scopes=repo%3Aread+repo%3Awrite")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("form mint = %d: %s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/settings/tokens?reveal=") {
		t.Fatalf("mint location = %q", loc)
	}
	newPW := randomPW(t)
	rec = postForm(t, e, "/settings/security/password", "/settings/security", cookie, "current="+pw+"&next="+newPW)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("form password change = %d: %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, e, "POST", "/login", "", `{"username":"ada","password":"`+newPW+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("login with new password = %d: %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, e, "POST", "/login", "", `{"username":"ada","password":"`+pw+`"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("login with old password = %d", rec.Code)
	}
}

func TestNoSignup(t *testing.T) {
	e := buildEnv(t)
	if rec := doJSON(t, e, "GET", "/signup", "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("signup = %d", rec.Code)
	}
}

func TestHTMLPagesZeroJS(t *testing.T) {
	e := buildEnv(t)
	pw := randomPW(t)
	createAdmin(t, e, "ada", pw)
	cookie := loginCookie(t, e, "ada", pw)
	targets := []struct{ method, path, cookie string }{
		{"GET", "/login", ""},
		{"GET", "/logout", ""},
		{"GET", "/settings", cookie},
		{"GET", "/settings/security", cookie},
		{"GET", "/settings/tokens", cookie},
		{"GET", "/admin/users", cookie},
		{"GET", "/ada", ""},
	}
	for _, tg := range targets {
		req := httptest.NewRequest(tg.method, tg.path, nil)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		if tg.cookie != "" {
			req.Header.Set("Cookie", tg.cookie)
		}
		rec := httptest.NewRecorder()
		e.server.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d", tg.path, rec.Code)
		}
		if err := zerojs.Assert(rec.Body.Bytes()); err != nil {
			t.Fatalf("%s zero-js: %v", tg.path, err)
		}
		for _, banned := range []string{"<script", "onclick", "onload", "javascript:"} {
			if strings.Contains(strings.ToLower(rec.Body.String()), banned) {
				t.Fatalf("%s contains %q", tg.path, banned)
			}
		}
		if !strings.Contains(rec.Body.String(), "fonts.bunny.net") {
			t.Fatalf("%s missing font link", tg.path)
		}
	}
}
