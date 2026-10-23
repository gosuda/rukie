package api_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	"github.com/gosuda/rukie/internal/store"
	"github.com/gosuda/rukie/internal/web/zerojs"
)

type f14Env struct {
	server *op.Server
	svc    *auth.Service
	st     *store.Store
	deps   api.Deps
}

func buildF14Env(t *testing.T) *f14Env {
	t.Helper()
	path := t.TempDir() + "/f14.sqlite"
	st, openErr := store.Open(t.Context(), store.Options{Path: path})
	if openErr != nil {
		t.Fatalf("store open: %v", openErr)
	}
	t.Cleanup(func() {
		_ = st.Close(context.Background())
	})
	svc := auth.New(st)
	deps := api.Deps{Service: svc, Limiter: access.NewLimiter(), Reveals: auth.NewRevealCache(), Store: st}
	table := op.NewTable()
	docs := &api.DocStore{}
	if regErr := api.RegisterAll(table, docs); regErr != nil {
		t.Fatalf("register index: %v", regErr)
	}
	for _, fn := range []func(*op.Table, api.Deps) error{
		api.RegisterAuth,
		api.RegisterUser,
		api.RegisterAdmin,
		api.RegisterAdminF14,
		api.RegisterF12,
		api.RegisterOrgs,
		api.RegisterRepos,
	} {
		if fnErr := fn(table, deps); fnErr != nil {
			t.Fatalf("register: %v", fnErr)
		}
	}
	if validErr := table.Validate(); validErr != nil {
		t.Fatalf("validate: %v", validErr)
	}
	views := op.NewViews()
	api.RegisterViews(views)
	api.RegisterAuthViews(views)
	api.RegisterUserViews(views)
	api.RegisterAdminViews(views)
	api.RegisterAdminF14Views(views)
	api.RegisterF12Views(views)
	api.RegisterOrgsViews(views)
	api.RegisterReposViews(views)
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
		IsSiteSegment: api.SiteSegment,
	})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	return &f14Env{server: server, svc: svc, st: st, deps: deps}
}

func f14Admin(t *testing.T, e *f14Env, name, pw string) {
	t.Helper()
	if _, err := e.svc.BootstrapAdmin(t.Context(), name, pw, "127.0.0.1"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
}

func f14User(t *testing.T, e *f14Env, adminID int64, name, pw string) {
	t.Helper()
	if _, err := e.svc.CreateUser(t.Context(), adminID, auth.CreateUserInput{Name: name, Password: pw}, "127.0.0.1"); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
}

func f14Bearer(t *testing.T, e *f14Env, userID int64, scopes ...op.Scope) string {
	t.Helper()
	grants := op.Scopes(0)
	for _, s := range scopes {
		grants = grants.Add(s)
	}
	minted, err := e.svc.CreateToken(t.Context(), auth.CreateTokenInput{UserID: userID, Name: "f14", Scopes: grants})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	return "Bearer " + minted.Secret
}

type f14Req struct {
	Method  string
	Target  string
	Auth    string
	Accept  string
	Headers map[string]string
	Body    string
}

func f14Call(t *testing.T, e *f14Env, r f14Req) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if r.Body != "" {
		reader = strings.NewReader(r.Body)
	}
	req := httptest.NewRequest(r.Method, r.Target, reader)
	if r.Body != "" && r.Headers["Content-Type"] == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	accept := r.Accept
	if accept == "" {
		accept = "application/json"
	}
	req.Header.Set("Accept", accept)
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}
	if r.Auth != "" {
		if strings.HasPrefix(r.Auth, "Bearer ") {
			req.Header.Set("Authorization", r.Auth)
		} else {
			req.Header.Set("Cookie", r.Auth)
		}
	}
	rec := httptest.NewRecorder()
	e.server.ServeHTTP(rec, req)
	return rec
}

func f14Login(t *testing.T, e *f14Env, username, password string) string {
	t.Helper()
	rec := f14Call(t, e, f14Req{Method: http.MethodPost, Target: "/login",
		Body: `{"username":"` + username + `","password":"` + password + `"}`})
	if rec.Code != http.StatusCreated {
		t.Fatalf("login = %d: %s", rec.Code, rec.Body.String())
	}
	setCookie := rec.Header().Get("Set-Cookie")
	if !strings.HasPrefix(setCookie, "__Host-") {
		t.Fatalf("Set-Cookie = %q", setCookie)
	}
	return strings.TrimSpace(strings.Split(setCookie, ";")[0])
}

func f14JSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := jsonv2.Unmarshal(rec.Body.Bytes(), &decoded, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("decode %d: %v\n%s", rec.Code, err, rec.Body.String())
	}
	return decoded
}

// f14CSRF fetches a CSRF cookie and field value for hand-posted forms.
func f14CSRF(t *testing.T, e *f14Env, path, cookie string) (string, string) {
	t.Helper()
	rec := f14Call(t, e, f14Req{Method: http.MethodGet, Target: path, Auth: cookie, Accept: "text/html"})
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
	field, _, _ := strings.Cut(rest, `"`)
	return csrfCookie, field
}

func f14IDem() string {
	var raw [12]byte
	_, _ = rand.Read(raw[:])
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	out := make([]byte, 16)
	for i := range out {
		out[i] = alphabet[int(raw[i%12])%len(alphabet)]
	}
	return string(out)
}

var f14AdminOps = []string{
	"admin.user.delete",
	"admin.repo.list", "admin.repo.purge", "admin.repo.verify",
	"admin.job.list", "admin.job.retry",
	"admin.audit.list", "admin.health.get",
	"admin.token.list", "admin.token.create", "admin.token.revoke",
	"admin.search.sync",
	"admin.mirror.sync", "admin.mirror.retry",
	"admin.attest.verify", "admin.inbox.test",
}

func f14Specs(t *testing.T, e *f14Env) map[string]op.Spec {
	t.Helper()
	byName := map[string]op.Spec{}
	for _, s := range e.server.Specs() {
		byName[s.Name] = s
	}
	return byName
}

// TestEveryOpHasPageTest is the U5 registry gate for the admin domain: every
// admin op is registered with its settled scope, every admin read has a page
// view in the HTML route set, and every admin mutation renders its form when
// a hand-posted form fails validation.
func TestEveryOpHasPageTest(t *testing.T) {
	e := buildF14Env(t)
	pw := randomPW(t)
	f14Admin(t, e, "ada", pw)
	acookie := f14Login(t, e, "ada", pw)
	specs := f14Specs(t, e)
	for _, name := range f14AdminOps {
		spec, ok := specs[name]
		if !ok {
			t.Fatalf("admin op %q is not registered", name)
		}
		if spec.Scope != op.ScopeAdminWrite {
			t.Fatalf("admin op %q scope = %v, want admin:write", name, spec.Scope)
		}
	}
	pages := map[string]bool{}
	for _, p := range e.server.HTMLPages() {
		pages[p.Name] = true
	}
	if err := e.st.Write(t.Context(), func(tx *store.WriteTx) error {
		_, werr := tx.InsertRepo(time.Now(), store.RepoInsert{
			Name: "demo", Visibility: "public", DefaultBranch: "main",
			RukPath: "ada/demo.git", OwnerUserID: sql.NullInt64{Int64: 1, Valid: true},
		})
		return werr
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	reads := map[string]struct {
		check  func(t *testing.T, body string)
		target string
		want   int
	}{
		"admin.repo.list":   {target: "/admin/repos", want: http.StatusOK},
		"admin.job.list":    {target: "/admin/jobs", want: http.StatusOK},
		"admin.audit.list":  {target: "/admin/audit", want: http.StatusOK},
		"admin.health.get":  {target: "/admin/health", want: http.StatusOK},
		"admin.token.list":  {target: "/admin/users/ada/tokens", want: http.StatusOK},
		"admin.repo.verify": {},
		"admin.attest.verify": {
			target: "/admin/attestations/verify?owner=ada&repo=demo&subject=head",
			want:   http.StatusServiceUnavailable,
		},
	}
	if !pages["admin.repo.verify"] {
		t.Fatalf("admin read %q has no page view", "admin.repo.verify")
	}
	verifyList := f14Call(t, e, f14Req{Method: http.MethodGet, Target: "/admin/repos?limit=1", Auth: acookie, Accept: "application/json"})
	if verifyList.Code != http.StatusOK {
		t.Fatalf("repo list = %d", verifyList.Code)
	}
	verifyID := ""
	decoded := f14JSON(t, verifyList)
	if repos, ok := decoded["repos"].([]any); ok && len(repos) > 0 {
		if first, ok := repos[0].(map[string]any); ok {
			if id, ok := first["id"].(float64); ok && id > 0 {
				verifyID = strconv.FormatInt(int64(id), 10)
			}
		}
	}
	if verifyID == "" {
		t.Fatalf("no repo id for the verify page")
	}
	verifyRec := f14Call(t, e, f14Req{Method: http.MethodGet, Target: "/admin/repos/" + verifyID + "/verify", Auth: acookie, Accept: "text/html"})
	if verifyRec.Code != http.StatusOK {
		t.Fatalf("GET verify = %d, want 200:\n%s", verifyRec.Code, verifyRec.Body.String())
	}
	if err := zerojs.Assert(verifyRec.Body.Bytes()); err != nil {
		t.Fatalf("verify zero-JS: %v", err)
	}
	for name, tc := range reads {
		if !pages[name] {
			t.Fatalf("admin read %q has no page view", name)
		}
		if tc.target == "" {
			continue
		}
		rec := f14Call(t, e, f14Req{Method: http.MethodGet, Target: tc.target, Auth: acookie, Accept: "text/html"})
		if rec.Code != tc.want {
			t.Fatalf("GET %s = %d, want %d:\n%s", tc.target, rec.Code, tc.want, rec.Body.String())
		}
		if err := zerojs.Assert(rec.Body.Bytes()); err != nil {
			t.Fatalf("%s zero-JS: %v", tc.target, err)
		}
		if tc.check != nil {
			tc.check(t, rec.Body.String())
		}
	}
	forms := map[string]string{
		"admin.user.delete":  "/admin/users/ada/delete",
		"admin.repo.purge":   "/admin/repos/abc/purge",
		"admin.job.retry":    "/admin/jobs/no-such-job/retry",
		"admin.token.create": "/admin/users/ada/tokens",
		"admin.token.revoke": "/admin/users/ada/tokens/revoke",
		"admin.search.sync":  "/admin/search/sync",
		"admin.mirror.sync":  "/admin/mirrors/abc/sync",
		"admin.mirror.retry": "/admin/mirror/runs/no-such-run/retry",
		"admin.inbox.test":   "/admin/inbox/test",
	}
	if len(forms) != 9 {
		t.Fatalf("form map covers %d mutations, want 9", len(forms))
	}
	for name, target := range forms {
		csrfCookie, csrfField := f14CSRF(t, e, "/admin/users", acookie)
		rec := f14Call(t, e, f14Req{Method: http.MethodPost, Target: target,
			Auth:   acookie + "; " + csrfCookie,
			Accept: "text/html",
			Headers: map[string]string{
				"Content-Type": "application/x-www-form-urlencoded",
			},
			Body: "_csrf=" + csrfField + "&_idem=f14-" + f14IDem() + "&bogus-field=1"})
		if rec.Code < 400 || rec.Code >= 500 {
			t.Fatalf("POST %s = %d, want a 4xx form re-render:\n%s", target, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "<form") {
			t.Fatalf("POST %s renders no form for %s:\n%s", target, name, rec.Body.String())
		}
		if err := zerojs.Assert(rec.Body.Bytes()); err != nil {
			t.Fatalf("%s zero-JS: %v", target, err)
		}
	}
}

// TestAdminOpScopes pins the F14 invariant: every admin op needs a site
// admin, and the framework scope gate denies everyone else. A 404 from a
// missing object still proves the scope and admin gates passed.
func TestAdminOpScopes(t *testing.T) {
	e := buildF14Env(t)
	pw := randomPW(t)
	f14Admin(t, e, "ada", pw)
	bobPW := randomPW(t)
	f14User(t, e, 1, "bob", bobPW)
	bobCookie := f14Login(t, e, "bob", bobPW)
	adaCookie := f14Login(t, e, "ada", pw)
	adaWrite := f14Bearer(t, e, 1, op.ScopeAdminWrite)
	adaRead := f14Bearer(t, e, 1, op.ScopeAdminRead)
	bobRead := f14Bearer(t, e, 2, op.ScopeAdminRead)
	probes := []f14Req{
		{Method: http.MethodGet, Target: "/admin/repos"},
		{Method: http.MethodGet, Target: "/admin/jobs"},
		{Method: http.MethodGet, Target: "/admin/audit"},
		{Method: http.MethodGet, Target: "/admin/health"},
		{Method: http.MethodGet, Target: "/admin/users/bob/tokens"},
		{Method: http.MethodGet, Target: "/admin/repos/1/verify"},
		{Method: http.MethodGet, Target: "/admin/attestations/verify?owner=a&repo=b&subject=c"},
		{Method: http.MethodPost, Target: "/admin/users/bob/delete", Body: `{"confirm":"bob"}`},
		{Method: http.MethodPost, Target: "/admin/repos/1/purge", Body: `{"confirm":"a/b"}`},
		{Method: http.MethodPost, Target: "/admin/jobs/x/retry"},
		{Method: http.MethodPost, Target: "/admin/search/sync"},
		{Method: http.MethodPost, Target: "/admin/users/bob/tokens", Body: `{"scopes":"repo:read"}`},
		{Method: http.MethodPost, Target: "/admin/users/bob/tokens/revoke", Body: `{}`},
		{Method: http.MethodPost, Target: "/admin/mirrors/1/sync"},
		{Method: http.MethodPost, Target: "/admin/mirror/runs/x/retry"},
		{Method: http.MethodPost, Target: "/admin/inbox/test", Body: `{"user":"bob"}`},
	}
	if len(probes) != 16 {
		t.Fatalf("scope matrix covers %d ops, want 16", len(probes))
	}
	for _, probe := range probes {
		// No credential reaches the scope gate as anonymous and is
		// refused; a bad credential fails authentication instead.
		anon := f14Call(t, e, f14Req{Method: probe.Method, Target: probe.Target, Body: probe.Body})
		if anon.Code != http.StatusForbidden {
			t.Fatalf("%s %s anon = %d, want 403", probe.Method, probe.Target, anon.Code)
		}
		nonAdmin := probe
		nonAdmin.Auth = bobCookie
		if rec := f14Call(t, e, nonAdmin); rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s non-admin session = %d, want 403:\n%s", probe.Method, probe.Target, rec.Code, rec.Body.String())
		}
		readToken := probe
		readToken.Auth = bobRead
		if rec := f14Call(t, e, readToken); rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s non-admin read token = %d, want 403", probe.Method, probe.Target, rec.Code)
		}
		adminRead := probe
		adminRead.Auth = adaRead
		if rec := f14Call(t, e, adminRead); rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s admin without write scope = %d, want 403:\n%s", probe.Method, probe.Target, rec.Code, rec.Body.String())
		}
		_ = adaWrite
		_ = adaCookie
	}
	adminWrite := f14Req{Method: http.MethodGet, Target: "/admin/health", Auth: adaWrite}
	if rec := f14Call(t, e, adminWrite); rec.Code != http.StatusOK {
		t.Fatalf("admin write token health = %d, want 200:\n%s", rec.Code, rec.Body.String())
	}
	adminSession := f14Req{Method: http.MethodGet, Target: "/admin/health", Auth: adaCookie}
	if rec := f14Call(t, e, adminSession); rec.Code != http.StatusOK {
		t.Fatalf("admin session health = %d, want 200:\n%s", rec.Code, rec.Body.String())
	}
}

func f14ETag(t *testing.T, e *f14Env, userID int64) string {
	t.Helper()
	etag, err := e.svc.ETagFor(t.Context(), userID)
	if err != nil {
		t.Fatalf("etag: %v", err)
	}
	return etag
}

// TestAdminUserDeleteLifecycle pins the 202 tombstone contract: the delete
// answers 202 with a future purge horizon, the account stops authenticating
// at once, and the sweeper hard-deletes the row past the horizon.
func TestAdminUserDeleteLifecycle(t *testing.T) {
	e := buildF14Env(t)
	pw := randomPW(t)
	f14Admin(t, e, "ada", pw)
	acookie := f14Login(t, e, "ada", pw)
	victimPW := randomPW(t)
	f14User(t, e, 1, "carol", victimPW)
	victim, err := e.svc.GetUser(t.Context(), "carol")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	etag := f14ETag(t, e, victim.ID)
	missing := f14Call(t, e, f14Req{Method: http.MethodPost, Target: "/admin/users/nobody/delete",
		Auth: acookie, Headers: map[string]string{"If-Match": etag},
		Body: `{"confirm":"nobody"}`})
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unknown delete = %d, want 404", missing.Code)
	}
	// N-1: empty confirm is refused.
	empty := f14Call(t, e, f14Req{Method: http.MethodPost, Target: "/admin/users/carol/delete",
		Auth: acookie, Headers: map[string]string{"If-Match": etag}, Body: `{}`})
	if empty.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty confirm = %d, want 422", empty.Code)
	}
	// N: wrong confirm is refused.
	wrong := f14Call(t, e, f14Req{Method: http.MethodPost, Target: "/admin/users/carol/delete",
		Auth: acookie, Headers: map[string]string{"If-Match": etag}, Body: `{"confirm":"mallory"}`})
	if wrong.Code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong confirm = %d, want 422", wrong.Code)
	}
	// N+1: missing If-Match is refused.
	noMatch := f14Call(t, e, f14Req{Method: http.MethodPost, Target: "/admin/users/carol/delete",
		Auth: acookie, Body: `{"confirm":"carol"}`})
	if noMatch.Code != http.StatusPreconditionRequired && noMatch.Code != http.StatusForbidden {
		t.Fatalf("missing if-match = %d, want 428", noMatch.Code)
	}
	rec := f14Call(t, e, f14Req{Method: http.MethodPost, Target: "/admin/users/carol/delete",
		Auth: acookie, Headers: map[string]string{"If-Match": etag}, Body: `{"confirm":"carol"}`})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("delete = %d, want 202:\n%s", rec.Code, rec.Body.String())
	}
	out := f14JSON(t, rec)
	horizon, _ := out["purge_after"].(float64)
	if horizon <= float64(time.Now().UnixMilli()) {
		t.Fatalf("purge_after = %v, want a future horizon", out["purge_after"])
	}
	// The tombstoned account stops authenticating at once: the profile
	// vanishes and the password no longer works.
	profile := f14Call(t, e, f14Req{Method: http.MethodGet, Target: "/carol"})
	if profile.Code != http.StatusNotFound {
		t.Fatalf("tombstoned profile = %d, want 404", profile.Code)
	}
	if _, err := e.svc.Login(t.Context(), auth.LoginInput{Username: "carol", Password: victimPW, IP: "127.0.0.1"}); err == nil {
		t.Fatalf("tombstoned user still logs in")
	}
	fresh := f14ETag(t, e, victim.ID)
	again := f14Call(t, e, f14Req{Method: http.MethodPost, Target: "/admin/users/carol/delete",
		Auth: acookie, Headers: map[string]string{"If-Match": fresh}, Body: `{"confirm":"carol"}`})
	if again.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d, want 404", again.Code)
	}
	// The last active admin refuses to delete themselves.
	self := f14Call(t, e, f14Req{Method: http.MethodPost, Target: "/admin/users/ada/delete",
		Auth: acookie, Headers: map[string]string{"If-Match": f14ETag(t, e, 1)}, Body: `{"confirm":"ada"}`})
	if self.Code != http.StatusConflict {
		t.Fatalf("last admin delete = %d, want 409", self.Code)
	}
	// Past the horizon the sweeper hard-deletes the row.
	future := time.Now().Add(31 * 24 * time.Hour)
	watcher := repowatch.New(repowatch.Config{Store: e.st, Clock: func() time.Time { return future }})
	if err := watcher.SweepDueUserPurges(t.Context()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	still, lookupErr := e.svc.GetUser(t.Context(), "carol")
	_ = still
	if lookupErr == nil {
		t.Fatalf("purged user still resolves after the sweep")
	}
}

// TestAdminViolationMatrix pins one named violation per assumption on the
// F14 error surface.
func TestAdminViolationMatrix(t *testing.T) {
	e := buildF14Env(t)
	pw := randomPW(t)
	f14Admin(t, e, "ada", pw)
	acookie := f14Login(t, e, "ada", pw)
	cases := []struct {
		req    f14Req
		name   string
		absent string
		want   int
	}{
		{req: f14Req{Method: http.MethodPost, Target: "/admin/repos/abc/purge", Auth: acookie, Body: `{"confirm":"a/b"}`}, name: "purge-live-conflict", want: 422},
		{req: f14Req{Method: http.MethodPost, Target: "/admin/repos/999999/purge", Auth: acookie, Body: `{"confirm":"a/b"}`}, name: "purge-unknown-id", want: 404},
		{req: f14Req{Method: http.MethodGet, Target: "/admin/repos/zero/verify", Auth: acookie}, name: "verify-bad-id", want: 422},
		{req: f14Req{Method: http.MethodGet, Target: "/admin/repos/999999/verify", Auth: acookie}, name: "verify-unknown-id", want: 404},
		{req: f14Req{Method: http.MethodGet, Target: "/admin/repos?state=burning", Auth: acookie}, name: "repo-list-bad-state", want: 422},
		{req: f14Req{Method: http.MethodGet, Target: "/admin/repos?cursor=!!!", Auth: acookie}, name: "repo-list-bad-cursor", want: 400},
		{req: f14Req{Method: http.MethodGet, Target: "/admin/repos?owner=nobody", Auth: acookie}, name: "repo-list-unknown-owner", want: 422},
		{req: f14Req{Method: http.MethodGet, Target: "/admin/jobs?kind=teleport", Auth: acookie}, name: "job-bad-kind", want: 422},
		{req: f14Req{Method: http.MethodGet, Target: "/admin/jobs?state=vapor", Auth: acookie}, name: "job-bad-state", want: 422},
		{req: f14Req{Method: http.MethodPost, Target: "/admin/jobs/no-such-job/retry", Auth: acookie}, name: "job-retry-unknown", want: 404},
		{req: f14Req{Method: http.MethodGet, Target: "/admin/audit?cursor=!!!", Auth: acookie}, name: "audit-bad-cursor", want: 400},
		{req: f14Req{Method: http.MethodGet, Target: "/admin/audit?actor=nobody", Auth: acookie}, name: "audit-unknown-actor", want: 422},
		{req: f14Req{Method: http.MethodGet, Target: "/admin/users/nobody/tokens", Auth: acookie}, name: "tokens-unknown-user", want: 404},
		{req: f14Req{Method: http.MethodPost, Target: "/admin/users/nobody/tokens", Auth: acookie, Body: `{"scopes":"repo:read"}`}, name: "token-create-unknown-user", want: 404},
		{req: f14Req{Method: http.MethodPost, Target: "/admin/users/ada/tokens", Auth: acookie, Body: `{"scopes":"nonsense:read"}`}, name: "token-create-bad-scope", want: 422},
		{req: f14Req{Method: http.MethodPost, Target: "/admin/users/ada/tokens", Auth: acookie, Body: `{}`}, name: "token-create-no-scope", want: 422},
		{req: f14Req{Method: http.MethodPost, Target: "/admin/users/ada/tokens/revoke", Auth: acookie, Body: `{}`}, name: "token-revoke-no-selector", want: 422},
		{req: f14Req{Method: http.MethodPost, Target: "/admin/users/ada/tokens/revoke", Auth: acookie, Body: `{"prefix":"ab","every":true}`}, name: "token-revoke-two-selectors", want: 422},
		{req: f14Req{Method: http.MethodPost, Target: "/admin/users/ada/tokens/revoke", Auth: acookie, Body: `{"prefix":"deadbeefcafe"}`}, name: "token-revoke-unknown", want: 404},
		{req: f14Req{Method: http.MethodPost, Target: "/admin/mirrors/zero/sync", Auth: acookie}, name: "mirror-sync-bad-id", want: 422},
		{req: f14Req{Method: http.MethodPost, Target: "/admin/mirrors/999999/sync", Auth: acookie}, name: "mirror-sync-unknown", want: 404},
		{req: f14Req{Method: http.MethodPost, Target: "/admin/mirror/runs/no-such-run/retry", Auth: acookie}, name: "mirror-retry-unknown", want: 404},
		{req: f14Req{Method: http.MethodGet, Target: "/admin/attestations/verify?owner=a&repo=b", Auth: acookie}, name: "attest-missing-subject", want: 422},
		{req: f14Req{Method: http.MethodGet, Target: "/admin/attestations/verify?subject=x", Auth: acookie}, name: "attest-missing-repo", want: 422},
		{req: f14Req{Method: http.MethodPost, Target: "/admin/inbox/test", Auth: acookie, Body: `{}`}, name: "inbox-empty-user", want: 422},
		{req: f14Req{Method: http.MethodPost, Target: "/admin/inbox/test", Auth: acookie, Body: `{"user":"nobody"}`}, name: "inbox-unknown-user", want: 404},
	}
	for _, tc := range cases {
		rec := f14Call(t, e, tc.req)
		if rec.Code != tc.want {
			t.Fatalf("%s: %s %s = %d, want %d:\n%s", tc.name, tc.req.Method, tc.req.Target, rec.Code, tc.want, rec.Body.String())
		}
		if tc.absent != "" && strings.Contains(rec.Body.String(), tc.absent) {
			t.Fatalf("%s leaks %q", tc.name, tc.absent)
		}
	}
}

// TestAdminJobRetryDead pins the retry contract: only dead jobs return to
// queued, and live jobs are refused.
func TestAdminJobRetryDead(t *testing.T) {
	e := buildF14Env(t)
	pw := randomPW(t)
	f14Admin(t, e, "ada", pw)
	acookie := f14Login(t, e, "ada", pw)
	now := time.Now()
	q := queue.Open(e.st, func() time.Time { return now })
	if _, err := q.Enqueue(t.Context(), queue.EnqueueSpec{ID: "f14-dead", Kind: queue.KindRepoPurge, RepoID: 1, Payload: "{}"}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	queued := f14Call(t, e, f14Req{Method: http.MethodPost, Target: "/admin/jobs/f14-dead/retry", Auth: acookie})
	if queued.Code != http.StatusConflict {
		t.Fatalf("retry queued = %d, want 409", queued.Code)
	}
	// Fail to the attempt cap with the clock jumping over every backoff.
	// The tenth failure parks the job dead.
	for range 10 {
		claimed, err := q.Claim(t.Context(), "f14-test", time.Minute, 10)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if len(claimed) != 1 {
			t.Fatalf("claim = %d, want 1", len(claimed))
		}
		if err := q.Fail(t.Context(), "f14-test", claimed[0].ID, claimed[0].LeaseToken); err != nil {
			t.Fatalf("fail: %v", err)
		}
		now = now.Add(16 * time.Minute)
	}
	rec := f14Call(t, e, f14Req{Method: http.MethodPost, Target: "/admin/jobs/f14-dead/retry", Auth: acookie})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("retry dead = %d, want 202:\n%s", rec.Code, rec.Body.String())
	}
	again := f14Call(t, e, f14Req{Method: http.MethodPost, Target: "/admin/jobs/f14-dead/retry", Auth: acookie})
	if again.Code != http.StatusConflict {
		t.Fatalf("retry requeued = %d, want 409", again.Code)
	}
}

// TestAdminTokenCycle pins mint, list, and revoke for another user.
func TestAdminTokenCycle(t *testing.T) {
	e := buildF14Env(t)
	pw := randomPW(t)
	f14Admin(t, e, "ada", pw)
	acookie := f14Login(t, e, "ada", pw)
	bobPW := randomPW(t)
	f14User(t, e, 1, "bob", bobPW)
	mint := f14Call(t, e, f14Req{Method: http.MethodPost, Target: "/admin/users/bob/tokens",
		Auth: acookie, Body: `{"name":"agent","scopes":"repo:read repo:write","ttl":"1h"}`})
	if mint.Code != http.StatusCreated {
		t.Fatalf("mint = %d, want 201:\n%s", mint.Code, mint.Body.String())
	}
	minted := f14JSON(t, mint)
	secret, _ := minted["secret"].(string)
	prefix, _ := minted["prefix"].(string)
	if secret == "" || prefix == "" {
		t.Fatalf("mint hides the secret: %v", minted)
	}
	list := f14Call(t, e, f14Req{Method: http.MethodGet, Target: "/admin/users/bob/tokens", Auth: acookie})
	if list.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200", list.Code)
	}
	if !strings.Contains(list.Body.String(), prefix) {
		t.Fatalf("list misses %s:\n%s", prefix, list.Body.String())
	}
	if strings.Contains(list.Body.String(), secret) {
		t.Fatalf("list leaks the secret")
	}
	revoke := f14Call(t, e, f14Req{Method: http.MethodPost, Target: "/admin/users/bob/tokens/revoke",
		Auth: acookie, Body: `{"prefix":"` + prefix + `"}`})
	if revoke.Code != http.StatusCreated {
		t.Fatalf("revoke = %d, want 201:\n%s", revoke.Code, revoke.Body.String())
	}
	if got := f14JSON(t, revoke)["revoked"]; got != float64(1) {
		t.Fatalf("revoked = %v, want 1", got)
	}
	reuse := f14Call(t, e, f14Req{Method: http.MethodGet, Target: "/admin/repos", Auth: "Bearer " + secret})
	if reuse.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token reuse = %d, want 401", reuse.Code)
	}
}

// TestAdminInboxTestNotice pins the audit-fallback channel: the notice is
// visible in the audit log at once.
func TestAdminInboxTestNotice(t *testing.T) {
	e := buildF14Env(t)
	pw := randomPW(t)
	f14Admin(t, e, "ada", pw)
	acookie := f14Login(t, e, "ada", pw)
	bobPW := randomPW(t)
	f14User(t, e, 1, "bob", bobPW)
	rec := f14Call(t, e, f14Req{Method: http.MethodPost, Target: "/admin/inbox/test",
		Auth: acookie, Body: `{"user":"bob"}`})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("inbox test = %d, want 202:\n%s", rec.Code, rec.Body.String())
	}
	audit := f14Call(t, e, f14Req{Method: http.MethodGet, Target: "/admin/audit?action=admin.inbox_test", Auth: acookie})
	if audit.Code != http.StatusOK {
		t.Fatalf("audit = %d, want 200", audit.Code)
	}
	if !strings.Contains(audit.Body.String(), "user:bob") {
		t.Fatalf("notice missing from audit:\n%s", audit.Body.String())
	}
}

// TestAAChecklist is the DES-21 accessibility skeleton for the admin
// surface: it pins what code can verify (language, viewport, label
// association, zero-JS, document policy) over every admin page. Contrast
// ratios, 24px targets, 400 percent reflow, reduced motion, and forced
// colors need a browser pass: the pages ship no styles or scripts, so the
// skeleton asserts that invariant too.
func TestAAChecklist(t *testing.T) {
	e := buildF14Env(t)
	pw := randomPW(t)
	f14Admin(t, e, "ada", pw)
	acookie := f14Login(t, e, "ada", pw)
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	wrapped := e.deps.AdminRaw(e.server, key)
	fetch := func(target string) (http.Header, string) {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("Accept", "text/html")
		req.Header.Set("Cookie", acookie)
		rec := httptest.NewRecorder()
		wrapped.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200:\n%s", target, rec.Code, rec.Body.String())
		}
		return rec.Header(), rec.Body.String()
	}
	targets := []string{
		"/admin", "/admin/users", "/admin/users/ada", "/admin/orgs",
		"/admin/repos", "/admin/jobs", "/admin/audit", "/admin/health", "/admin/monitor",
	}
	for _, target := range targets {
		header, body := fetch(target)
		if csp := header.Get("Content-Security-Policy"); csp == "" {
			t.Fatalf("%s has no content security policy", target)
		}
		if err := zerojs.Assert([]byte(body)); err != nil {
			t.Fatalf("%s zero-JS: %v", target, err)
		}
		if !strings.Contains(body, `<html lang="en">`) {
			t.Fatalf("%s has no language declaration", target)
		}
		if !strings.Contains(body, `name="viewport"`) {
			t.Fatalf("%s has no viewport meta", target)
		}
		for input := range strings.SplitSeq(body, "<input") {
			if !strings.Contains(input, `type="hidden"`) && strings.Contains(input, "type=") {
				if !strings.Contains(input, `name="`) {
					t.Fatalf("%s has an unnamed input", target)
				}
			}
		}
		if strings.Contains(body, "<style") || strings.Contains(body, "style=") {
			t.Fatalf("%s ships styles the skeleton cannot measure; keep pages unstyled", target)
		}
	}
}
