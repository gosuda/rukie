package view

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/api"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/store"
)

type renderEnv struct {
	server *op.Server
	svc    *auth.Service
	st     *store.Store
}

type htmlPage struct {
	Check  func(t *testing.T, body string)
	Header http.Header
	Op     string
	File   string
	Body   string
}

func buildRenderEnv(t *testing.T) *renderEnv {
	t.Helper()
	path := t.TempDir() + "/render.sqlite"
	st, serr := store.Open(t.Context(), store.Options{Path: path})
	if serr != nil {
		t.Fatalf("store open: %v", serr)
	}
	svc := auth.New(st)
	t.Cleanup(func() {
		_ = st.Close(context.Background())
	})
	deps := api.Deps{Service: svc, Limiter: access.NewLimiter(), Reveals: auth.NewRevealCache(), Store: st}
	table := op.NewTable()
	docs := &api.DocStore{}
	if err := api.RegisterAll(table, docs); err != nil {
		t.Fatalf("register index: %v", err)
	}
	if err := api.RegisterAuth(table, deps); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	if err := api.RegisterUser(table, deps); err != nil {
		t.Fatalf("register user: %v", err)
	}
	if err := api.RegisterAdmin(table, deps); err != nil {
		t.Fatalf("register admin: %v", err)
	}
	if err := api.RegisterAdminF14(table, deps); err != nil {
		t.Fatalf("register admin f14: %v", err)
	}
	if err := api.RegisterSearch(table, deps); err != nil {
		t.Fatalf("register search: %v", err)
	}
	if err := api.RegisterF12(table, deps); err != nil {
		t.Fatalf("register f12: %v", err)
	}
	if err := api.RegisterSocial(table, deps); err != nil {
		t.Fatalf("register social: %v", err)
	}
	fill, err := api.RegisterLLMS(table, "http://example.com")
	if err != nil {
		t.Fatalf("register llms: %v", err)
	}
	views := op.NewViews()
	api.RegisterViews(views)
	api.RegisterAuthViews(views)
	api.RegisterUserViews(views)
	api.RegisterAdminViews(views)
	api.RegisterAdminF14Views(views)
	api.RegisterSearchViews(views)
	api.RegisterF12Views(views)
	api.RegisterSocialViews(views)
	api.RegisterLLMSViews(views)
	var key [32]byte
	if _, rerr := rand.Read(key[:]); rerr != nil {
		t.Fatalf("rand: %v", rerr)
	}
	mux := http.NewServeMux()
	server, err := op.Mount(mux, table, op.Config{
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
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	fill(server.Document())
	return &renderEnv{server: server, svc: svc, st: st}
}

func renderFirstAdminRepoID(t *testing.T, e *renderEnv, cookie string) string {
	t.Helper()
	rec := renderDoJSON(t, e, "GET", "/admin/repos?limit=1", cookie, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin repo list = %d: %s", rec.Code, rec.Body.String())
	}
	decoded := renderDecode(t, rec)
	repos, _ := decoded["repos"].([]any)
	if len(repos) == 0 {
		t.Fatalf("no repos for repo verify fixture")
	}
	first, _ := repos[0].(map[string]any)
	id, _ := first["id"].(float64)
	if id <= 0 {
		t.Fatalf("repo id = %v", first["id"])
	}
	return strconv.FormatInt(int64(id), 10)
}

func renderSeedSocial(t *testing.T, e *renderEnv) {
	t.Helper()
	ada, aerr := e.st.LookupUserByName(t.Context(), "ada")
	if aerr != nil {
		t.Fatalf("lookup ada: %v", aerr)
	}
	bob, berr := e.st.LookupUserByName(t.Context(), "bob")
	if berr != nil {
		t.Fatalf("lookup bob: %v", berr)
	}
	now := time.Now()
	err := e.st.Write(t.Context(), func(tx *store.WriteTx) error {
		id, ierr := tx.InsertRepo(now, store.RepoInsert{
			OwnerUserID:   sql.NullInt64{Int64: ada.ID, Valid: true},
			Name:          "demo",
			Visibility:    "public",
			DefaultBranch: "main",
			RukPath:       "ada/demo.git",
		})
		if ierr != nil {
			return ierr
		}
		if serr := tx.AddStar(id, bob.ID, now); serr != nil {
			return serr
		}
		_, ierr = tx.InsertOrg(now, store.OrgInsert{Name: "acme", Visibility: "public"})
		return ierr
	})
	if err != nil {
		t.Fatalf("seed social: %v", err)
	}
}

func renderDoJSON(t *testing.T, e *renderEnv, method, target, cookie, body string) *httptest.ResponseRecorder {
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

type renderRequest struct {
	Body   string
	Method string
	Target string
	Cookie string
}

func renderDoHTML(t *testing.T, e *renderEnv, p renderRequest) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if p.Body != "" {
		reader = strings.NewReader(p.Body)
	}
	req := httptest.NewRequest(p.Method, p.Target, reader)
	if p.Body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	if p.Cookie != "" {
		req.Header.Set("Cookie", p.Cookie)
	}
	rec := httptest.NewRecorder()
	e.server.ServeHTTP(rec, req)
	return rec
}

func captureGET(t *testing.T, e *renderEnv, pages *[]htmlPage, op, file, target, cookie string, want int, check func(t *testing.T, body string)) {
	t.Helper()
	rec := renderDoHTML(t, e, renderRequest{Method: "GET", Target: target, Cookie: cookie})
	if rec.Code != want {
		t.Fatalf("GET %s = %d, want %d\n%s", target, rec.Code, want, rec.Body.String())
	}
	*pages = append(*pages, htmlPage{Check: check, Header: rec.Header(), Op: op, File: file, Body: rec.Body.String()})
}

func capturePOSTForm(t *testing.T, e *renderEnv, pages *[]htmlPage, op, file, target, csrfPath, cookie, body string, want int, check func(t *testing.T, body string)) {
	t.Helper()
	rec := renderPostForm(t, e, target, csrfPath, cookie, body)
	if rec.Code != want {
		t.Fatalf("POST %s = %d, want %d\n%s", target, rec.Code, want, rec.Body.String())
	}
	*pages = append(*pages, htmlPage{Check: check, Header: rec.Header(), Op: op, File: file, Body: rec.Body.String()})
}

func renderDecode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := jsonv2.Unmarshal(rec.Body.Bytes(), &decoded, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("decode %d: %v\n%s", rec.Code, err, rec.Body.String())
	}
	return decoded
}

func renderRandomPW(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return "Aa1!" + base64.RawURLEncoding.EncodeToString(raw)
}

func renderCreateAdmin(t *testing.T, e *renderEnv, name, pw string) {
	t.Helper()
	if _, err := e.svc.BootstrapAdmin(t.Context(), name, pw, "127.0.0.1"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
}

func renderLoginCookie(t *testing.T, e *renderEnv, username, password string) string {
	t.Helper()
	rec := renderDoJSON(t, e, "POST", "/login", "", `{"username":"`+username+`","password":"`+password+`"}`)
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

func renderGetCSRF(t *testing.T, e *renderEnv, path, cookie string) (string, string) {
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
	_, rest, found := strings.Cut(body, `name="_csrf" value="`)
	if !found {
		t.Fatalf("no csrf field on %s", path)
	}
	field, _, found := strings.Cut(rest, `"`)
	if !found {
		t.Fatalf("unterminated csrf field on %s", path)
	}
	return csrfCookie, field
}

func renderPostForm(t *testing.T, e *renderEnv, postPath, csrfPath, cookie, body string) *httptest.ResponseRecorder {
	t.Helper()
	csrfCookie, csrfField := renderGetCSRF(t, e, csrfPath, cookie)
	full := "_csrf=" + csrfField + "&_idem=form-" + csrfField[:8] + "&" + body
	cookies := csrfCookie
	if cookie != "" {
		cookies = cookie + "; " + csrfCookie
	}
	req := httptest.NewRequest("POST", postPath, strings.NewReader(full))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Cookie", cookies)
	rec := httptest.NewRecorder()
	e.server.ServeHTTP(rec, req)
	return rec
}

func renderSealKey() []byte {
	return []byte("render-test-seal-key-0123456789a")
}

func renderTOTPCode(t *testing.T, secretBase32 string, at time.Time) string {
	t.Helper()
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secretBase32)
	if err != nil {
		t.Fatalf("decode secret: %v", err)
	}
	return auth.TOTPCode(raw, auth.TOTPCounter(at))
}

func renderEnroll(t *testing.T, e *renderEnv, cookie string) (string, string) {
	t.Helper()
	e.svc.SetSealKey(renderSealKey())
	rec := renderDoJSON(t, e, "POST", "/settings/security/totp", cookie, `{}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("enroll = %d: %s", rec.Code, rec.Body.String())
	}
	secret, _ := renderDecode(t, rec)["secret"].(string)
	if secret == "" {
		t.Fatalf("enroll returned no secret: %s", rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/settings/security/totp?reveal=") {
		t.Fatalf("enroll location = %q", loc)
	}
	return secret, loc
}

func renderCodesCheck(t *testing.T, body string) {
	t.Helper()
	if !strings.Contains(body, "<code>") {
		t.Fatal("regen page shows no recovery codes")
	}
}

type renderStubCode struct {
	challenge string
	nonce     string
	sub       string
	username  string
}

type renderStub struct {
	t      *testing.T
	server *httptest.Server
	priv   *rsa.PrivateKey
	codes  map[string]renderStubCode
	issuer string
	mu     sync.Mutex
}

func newRenderStub(t *testing.T) *renderStub {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	stub := &renderStub{t: t, priv: priv, codes: map[string]renderStubCode{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/op/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		raw, _ := jsonv2.Marshal(map[string]any{
			"issuer": stub.issuer, "authorization_endpoint": stub.issuer + "/auth",
			"token_endpoint": stub.issuer + "/token", "jwks_uri": stub.issuer + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		}, jsonv1.DefaultOptionsV1())
		_, _ = w.Write(raw)
	})
	mux.HandleFunc("/op/jwks", func(w http.ResponseWriter, _ *http.Request) {
		pub := stub.priv.Public().(*rsa.PublicKey)
		w.Header().Set("Content-Type", "application/json")
		raw, _ := jsonv2.Marshal(map[string]any{"keys": []any{map[string]any{
			"kty": "RSA", "kid": "k", "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}),
		}}}, jsonv1.DefaultOptionsV1())
		_, _ = w.Write(raw)
	})
	mux.HandleFunc("/op/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_ = r.ParseForm()
		user, _, basic := r.BasicAuth()
		clientID := r.Form.Get("client_id")
		if basic {
			clientID = user
		}
		code := r.Form.Get("code")
		stub.mu.Lock()
		binding, ok := stub.codes[code]
		stub.mu.Unlock()
		if !ok || clientID != "rukie-test" {
			w.WriteHeader(http.StatusBadRequest)
			raw, _ := jsonv2.Marshal(map[string]string{"error": "invalid_grant"}, jsonv1.DefaultOptionsV1())
			_, _ = w.Write(raw)
			return
		}
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != binding.challenge {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		stub.mu.Lock()
		delete(stub.codes, code)
		stub.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		header, _ := jsonv2.Marshal(map[string]string{"alg": "RS256", "kid": "k", "typ": "JWT"}, jsonv1.DefaultOptionsV1())
		payload, _ := jsonv2.Marshal(map[string]any{
			"iss": stub.issuer, "sub": binding.sub, "aud": "rukie-test",
			"exp": time.Now().Add(5 * time.Minute).Unix(), "iat": time.Now().Unix(),
			"nonce": binding.nonce, "preferred_username": binding.username,
			"email": binding.username + "@example.com", "email_verified": true,
		}, jsonv1.DefaultOptionsV1())
		segments := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
		digest := sha256.Sum256([]byte(segments))
		sig, serr := rsa.SignPKCS1v15(rand.Reader, stub.priv, crypto.SHA256, digest[:])
		if serr != nil {
			t.Errorf("sign: %v", serr)
			return
		}
		raw, _ := jsonv2.Marshal(map[string]any{
			"access_token": "x", "token_type": "Bearer",
			"id_token": segments + "." + base64.RawURLEncoding.EncodeToString(sig),
		}, jsonv1.DefaultOptionsV1())
		_, _ = w.Write(raw)
	})
	stub.server = httptest.NewServer(mux)
	t.Cleanup(stub.server.Close)
	stub.issuer = stub.server.URL + "/op"
	return stub
}

func (s *renderStub) issue(challenge, nonce, sub, username string) string {
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	code := base64.RawURLEncoding.EncodeToString(raw)
	s.mu.Lock()
	s.codes[code] = renderStubCode{challenge: challenge, nonce: nonce, sub: sub, username: username}
	s.mu.Unlock()
	return code
}

func renderTOTPRevealCheck(t *testing.T, body string) {
	t.Helper()
	lowered := strings.ToLower(body)
	if strings.Contains(body, "<img") || strings.Contains(lowered, "qrcode") ||
		strings.Contains(lowered, "qr-code") || strings.Contains(body, "<svg") ||
		strings.Contains(body, "<canvas") {
		t.Fatal("enrollment page carries a QR image")
	}
	if !strings.Contains(body, "Manual key: <code>") {
		t.Fatal("no manual key on enrollment page")
	}
	if !strings.Contains(body, "otpauth://totp/") {
		t.Fatal("no otpauth URI on enrollment page")
	}
}

func collectHTMLPages(t *testing.T, e *renderEnv) []htmlPage {
	t.Helper()
	pw := renderRandomPW(t)
	renderCreateAdmin(t, e, "ada", pw)
	acookie := renderLoginCookie(t, e, "ada", pw)
	bobPW := renderRandomPW(t)
	if _, err := e.svc.CreateUser(t.Context(), 1, auth.CreateUserInput{Name: "bob", Password: bobPW}, "127.0.0.1"); err != nil {
		t.Fatalf("create bob: %v", err)
	}
	bcookie := renderLoginCookie(t, e, "bob", bobPW)
	mint := renderDoJSON(t, e, "POST", "/settings/tokens", acookie, `{"name":"agent","scopes":"repo:read repo:write"}`)
	if mint.Code != http.StatusCreated {
		t.Fatalf("mint = %d: %s", mint.Code, mint.Body.String())
	}
	var pages []htmlPage
	captureGET(t, e, &pages, "api.index", "api-index.html", "/-/api", "", http.StatusOK, nil)
	captureGET(t, e, &pages, "search.code", "search.html", "/search?q=x", acookie, http.StatusOK, nil)
	captureGET(t, e, &pages, "search.code", "search-empty.html", "/search", acookie, http.StatusUnprocessableEntity, nil)
	captureGET(t, e, &pages, "search.repos", "search-repos.html", "/search/repos?q=x", "", http.StatusOK, nil)
	captureGET(t, e, &pages, "search.users", "search-users.html", "/search/users?q=x", "", http.StatusOK, nil)
	captureGET(t, e, &pages, "api.llms.get", "llms.html", "/llms.txt", "", http.StatusOK, nil)
	captureGET(t, e, &pages, "api.llmsfull.get", "llms-full.html", "/llms-full.txt", "", http.StatusOK, nil)
	captureGET(t, e, &pages, "auth.login.page", "login.html", "/login", "", http.StatusOK, nil)
	capturePOSTForm(t, e, &pages, "auth.login.page", "login-error.html", "/login", "/login", "", "username=&password=", http.StatusUnprocessableEntity, nil)
	captureGET(t, e, &pages, "auth.logout.page", "logout.html", "/logout", "", http.StatusOK, nil)
	captureGET(t, e, &pages, "user.get", "profile-ada.html", "/ada", "", http.StatusOK, nil)
	renderSeedSocial(t, e)
	captureGET(t, e, &pages, "explore.repos", "explore-repos-api.html", "/api/explore/repos", "", http.StatusOK, nil)
	captureGET(t, e, &pages, "explore.users", "explore-users-api.html", "/api/explore/users", "", http.StatusOK, nil)
	captureGET(t, e, &pages, "explore.orgs", "explore-orgs-api.html", "/api/explore/orgs", "", http.StatusOK, nil)
	captureGET(t, e, &pages, "star.list", "stargazers-api.html", "/api/stars?repo=ada/demo", "", http.StatusOK, nil)
	captureGET(t, e, &pages, "repo.fork.list", "forks-api.html", "/api/repos/ada/demo/forks", "", http.StatusOK, nil)
	captureGET(t, e, &pages, "explore.repos.page", "explore-repos.html", "/-/explore/repos", "", http.StatusOK, nil)
	captureGET(t, e, &pages, "explore.users.page", "explore-users.html", "/-/explore/users", "", http.StatusOK, nil)
	captureGET(t, e, &pages, "explore.orgs.page", "explore-orgs.html", "/-/explore/orgs", "", http.StatusOK, nil)
	captureGET(t, e, &pages, "star.list.page", "stargazers.html", "/ada/demo/stargazers", "", http.StatusOK, nil)
	captureGET(t, e, &pages, "repo.forks.page", "forks.html", "/ada/demo/forks", "", http.StatusOK, nil)
	captureGET(t, e, &pages, "repo.fork.page", "fork.html", "/ada/demo/fork", acookie, http.StatusOK, nil)
	captureGET(t, e, &pages, "", "unknown-user.html", "/no-such-user-xyz", "", http.StatusNotFound, nil)
	captureGET(t, e, &pages, "settings.page", "settings.html", "/settings", acookie, http.StatusOK, nil)
	captureGET(t, e, &pages, "auth.session.list", "security.html", "/settings/security", acookie, http.StatusOK, nil)
	captureGET(t, e, &pages, "auth.token.list", "tokens.html", "/settings/tokens", acookie, http.StatusOK, nil)
	captureGET(t, e, &pages, "admin.user.list", "admin-users.html", "/admin/users", acookie, http.StatusOK, nil)
	captureGET(t, e, &pages, "admin.repo.list", "admin-repos.html", "/admin/repos", acookie, http.StatusOK, nil)
	captureGET(t, e, &pages, "admin.job.list", "admin-jobs.html", "/admin/jobs", acookie, http.StatusOK, nil)
	captureGET(t, e, &pages, "admin.audit.list", "admin-audit.html", "/admin/audit", acookie, http.StatusOK, nil)
	captureGET(t, e, &pages, "admin.health.get", "admin-health.html", "/admin/health", acookie, http.StatusOK, nil)
	captureGET(t, e, &pages, "admin.token.list", "admin-tokens-bob.html", "/admin/users/bob/tokens", acookie, http.StatusOK, nil)
	captureGET(t, e, &pages, "admin.attest.verify", "admin-attest-verify.html", "/admin/attestations/verify?owner=ada&repo=demo&subject=head", acookie, http.StatusServiceUnavailable, nil)
	repoID := renderFirstAdminRepoID(t, e, acookie)
	captureGET(t, e, &pages, "admin.repo.verify", "admin-repo-verify.html", "/admin/repos/"+repoID+"/verify", acookie, http.StatusOK, func(t *testing.T, body string) {
		t.Helper()
		if !strings.Contains(body, "daemon-registered: FAILED") {
			t.Fatalf("verify page shows no failed daemon check:\n%s", body)
		}
	})
	captureGET(t, e, &pages, "auth.totpverify.page", "totp-challenge.html", "/login/totp", "", http.StatusOK, nil)
	captureGET(t, e, &pages, "auth.totp.page", "totp.html", "/settings/security/totp", bcookie, http.StatusOK, nil)
	formMint := renderPostForm(t, e, "/settings/tokens", "/settings/tokens", acookie, "name=formagent&scopes=repo%3Aread")
	if formMint.Code != http.StatusSeeOther {
		t.Fatalf("form mint = %d: %s", formMint.Code, formMint.Body.String())
	}
	formLoc := formMint.Header().Get("Location")
	if !strings.HasPrefix(formLoc, "/settings/tokens?reveal=") {
		t.Fatalf("mint location = %q", formLoc)
	}
	captureGET(t, e, &pages, "auth.token.list", "tokens-reveal.html", formLoc, acookie, http.StatusOK, nil)
	secret, enrollReveal := renderEnroll(t, e, bcookie)
	captureGET(t, e, &pages, "auth.totp.page", "totp-reveal.html", enrollReveal, bcookie, http.StatusOK, renderTOTPRevealCheck)
	captureGET(t, e, &pages, "auth.totp.page", "totp-pending.html", "/settings/security/totp", bcookie, http.StatusOK, nil)
	verify := renderPostForm(t, e, "/login/totp", "/login/totp", bcookie, "code="+renderTOTPCode(t, secret, time.Now()))
	if verify.Code != http.StatusSeeOther {
		t.Fatalf("verify = %d: %s", verify.Code, verify.Body.String())
	}
	if rotated := verify.Header().Get("Set-Cookie"); strings.HasPrefix(rotated, "__Host-") {
		bcookie = strings.TrimSpace(strings.Split(rotated, ";")[0])
	}
	captureGET(t, e, &pages, "auth.totp.page", "totp-enabled.html", "/settings/security/totp", bcookie, http.StatusOK, nil)
	captureGET(t, e, &pages, "auth.session.list", "security-totp.html", "/settings/security", bcookie, http.StatusOK, nil)
	regen := renderDoJSON(t, e, "POST", "/settings/security/recovery", bcookie, `{}`)
	if regen.Code != http.StatusCreated {
		t.Fatalf("regen = %d: %s", regen.Code, regen.Body.String())
	}
	regenLoc := regen.Header().Get("Location")
	if !strings.HasPrefix(regenLoc, "/settings/security/totp?reveal=") {
		t.Fatalf("regen location = %q", regenLoc)
	}
	captureGET(t, e, &pages, "auth.totp.page", "totp-codes.html", regenLoc, bcookie, http.StatusOK, renderCodesCheck)
	stub := newRenderStub(t)
	if err := e.svc.SetOIDCProviders(t.Context(), []auth.OIDCProviderConfig{{
		ID: "stub", Issuer: stub.issuer, ClientID: "rukie-test",
		RedirectURL: "http://localhost/cb", AllowSignup: true,
	}}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	captureGET(t, e, &pages, "auth.oidc.start", "oidc-start.html", "/login/oidc/stub", "", http.StatusOK, nil)
	started := renderDoJSON(t, e, "GET", "/login/oidc/stub", "", "")
	if started.Code != http.StatusOK {
		t.Fatalf("start = %d: %s", started.Code, started.Body.String())
	}
	authURL, _ := renderDecode(t, started)["auth_url"].(string)
	browserCookie := ""
	for _, h := range started.Header().Values("Set-Cookie") {
		if strings.HasPrefix(h, auth.OIDCBrowserCookieName()+"=") {
			browserCookie = strings.TrimSpace(strings.Split(h, ";")[0])
		}
	}
	if browserCookie == "" {
		t.Fatalf("start returned no browser cookie")
	}
	parsed, _ := url.Parse(authURL)
	q := parsed.Query()
	code := stub.issue(q.Get("code_challenge"), q.Get("nonce"), "render-1", "renderuser")
	captureGET(t, e, &pages, "auth.oidc.callback", "oidc-callback.html", "/login/oidc/stub/callback?code="+code+"&state="+q.Get("state"), browserCookie, http.StatusOK, nil)
	return pages
}

func assertHTMLCoverage(t *testing.T, e *renderEnv, pages []htmlPage) {
	t.Helper()
	covered := map[string]bool{}
	for _, p := range pages {
		if p.Op != "" {
			covered[p.Op] = true
		}
	}
	for _, spec := range e.server.HTMLPages() {
		if !covered[spec.Name] {
			t.Fatalf("registered HTML route %s %s (%s) has no renderable fixture", spec.Method, spec.Path, spec.Name)
		}
	}
}
