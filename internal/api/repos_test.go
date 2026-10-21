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
	"github.com/gosuda/rukie/internal/store"
	"github.com/gosuda/rukie/internal/web/zerojs"
)

var _ = zerojs.Assert

type repoEnv struct {
	server *op.Server
	svc    *auth.Service
	st     *store.Store
	deps   api.Deps
}

func buildRepoEnv(t *testing.T) *repoEnv {
	t.Helper()
	path := t.TempDir() + "/f1.sqlite"
	st, err := store.Open(t.Context(), store.Options{Path: path})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	svc := auth.New(st)
	t.Cleanup(func() {
		_ = st.Close(context.Background())
	})
	deps := api.Deps{
		Service: svc, Limiter: access.NewLimiter(), Reveals: auth.NewRevealCache(),
		Store: st, Ruk: nil, RepoRoot: t.TempDir(),
	}
	table := op.NewTable()
	for _, step := range []func() error{
		func() error { return api.RegisterAll(table, &api.DocStore{}) },
		func() error { return api.RegisterAuth(table, deps) },
		func() error { return api.RegisterUser(table, deps) },
		func() error { return api.RegisterAdmin(table, deps) },
		func() error { return api.RegisterRepos(table, deps) },
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
	api.RegisterCodeViews(views)
	api.RegisterRefsViews(views)
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
	return &repoEnv{server: server, svc: svc, st: st, deps: deps}
}

type rcall struct {
	method  string
	target  string
	auth    string
	accept  string
	headers map[string]string
	body    string
}

func rdo(t *testing.T, e *repoEnv, method, target, authHeader, body string) *httptest.ResponseRecorder {
	t.Helper()
	return rdoCall(t, e, rcall{method: method, target: target, auth: authHeader, accept: "application/json", body: body})
}

func rdoFull(t *testing.T, e *repoEnv, method, target, authHeader, accept string, headers map[string]string, body string) *httptest.ResponseRecorder {
	t.Helper()
	return rdoCall(t, e, rcall{method: method, target: target, auth: authHeader, accept: accept, headers: headers, body: body})
}

func rdoCall(t *testing.T, e *repoEnv, call rcall) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if call.body != "" {
		reader = strings.NewReader(call.body)
	}
	req := httptest.NewRequest(call.method, call.target, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", call.accept)
	for key, value := range call.headers {
		req.Header.Set(key, value)
	}
	if call.auth != "" {
		if strings.HasPrefix(call.auth, "Bearer ") {
			req.Header.Set("Authorization", call.auth)
		} else {
			req.Header.Set("Cookie", call.auth)
		}
	}
	out := httptest.NewRecorder()
	e.server.ServeHTTP(out, req)
	return out
}

func rassertProblem(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d: %s", rec.Code, status, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), code) {
		t.Fatalf("body misses problem %q: %s", code, rec.Body.String())
	}
}

func rlogin(t *testing.T, e *repoEnv, username, password string) string {
	t.Helper()
	rec := rdo(t, e, "POST", "/login", "", `{"username":"`+username+`","password":"`+password+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("login = %d: %s", rec.Code, rec.Body.String())
	}
	parts := strings.Split(rec.Header().Get("Set-Cookie"), ";")
	return strings.TrimSpace(parts[0])
}

func rbootstrap(t *testing.T, e *repoEnv, name, pw string) {
	t.Helper()
	if _, err := e.svc.BootstrapAdmin(t.Context(), name, pw, "127.0.0.1"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
}

func ruser(t *testing.T, e *repoEnv, adminID int64, name string) auth.User {
	t.Helper()
	user, err := e.svc.CreateUser(t.Context(), adminID, auth.CreateUserInput{
		Name: name, Email: name + "@example.test", Password: "long-test-password-01",
	}, "127.0.0.1")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

func rtoken(t *testing.T, e *repoEnv, user auth.User, scopes ...string) string {
	t.Helper()
	parsed, err := auth.ParseScopeSet(scopes)
	if err != nil {
		t.Fatalf("scopes: %v", err)
	}
	secret, err := e.svc.CreateToken(t.Context(), auth.CreateTokenInput{
		Name: "f1", Scopes: parsed, UserID: user.ID,
	})
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	return "Bearer " + secret.Secret
}

func rseedRepo(t *testing.T, e *repoEnv, ownerID int64, name, visibility string) int64 {
	t.Helper()
	var id int64
	err := e.st.Write(t.Context(), func(tx *store.WriteTx) error {
		var ierr error
		id, ierr = tx.InsertRepo(time.Now(), store.RepoInsert{
			OwnerUserID:   nullID(ownerID),
			Name:          name,
			Visibility:    visibility,
			Topics:        "[]",
			DefaultBranch: "main",
			RukPath:       "seed/" + name + ".git",
		})
		if ierr != nil {
			return ierr
		}
		return tx.InsertDefaultRule(id, time.Now(), "main")
	})
	if err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	return id
}

func nullID(id int64) sql.NullInt64 {
	return sql.NullInt64{Int64: id, Valid: true}
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}

func rdecode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := jsonv2.Unmarshal(rec.Body.Bytes(), &decoded, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("decode %d: %v\n%s", rec.Code, err, rec.Body.String())
	}
	return decoded
}

func TestRepoCreateValidation(t *testing.T) {
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	adaCookie := rlogin(t, e, "ada", "long-test-password-01")
	rassertProblem(t, rdo(t, e, "POST", "/new/repo", "", `{"owner":"ada","name":"x"}`), 403, "forbidden")
	for _, body := range []string{
		`{"owner":"ada","name":""}`,
		`{"owner":"ada","name":".git"}`,
		`{"owner":"ada","name":"a..b"}`,
		`{"owner":"new","name":"x"}`,
		`{"owner":"repos","name":"x"}`,
		`{"owner":"ada","name":"has space"}`,
		`{"owner":"ada","name":"x","visibility":"world"}`,
		`{"owner":"ada","name":"x","website":"ftp://example.test"}`,
		`{"owner":"ada","name":"x","topics":["a","A"]}`,
		`{"owner":"ada","name":"x","default_branch":"-bad"}`,
		`{"owner":"ada","name":"has space"}`,
	} {
		rec := rdo(t, e, "POST", "/new/repo", adaCookie, body)
		if rec.Code != 400 && rec.Code != 422 {
			t.Fatalf("body %s: status = %d, want 400 or 422", body, rec.Code)
		}
	}
	rassertProblem(t, rdo(t, e, "POST", "/new/repo", adaCookie, `{"owner":"nope","name":"x"}`), 403, "forbidden")
	topics := `{"owner":"ada","name":"x","topics":[`
	for i := range 21 {
		if i > 0 {
			topics += ","
		}
		topics += `"t` + strings.Repeat("o", i) + `"`
	}
	topics += `]}`
	rassertProblem(t, rdo(t, e, "POST", "/new/repo", adaCookie, topics), 422, "validation")
	rec := rdo(t, e, "POST", "/new/repo", adaCookie, `{"owner":"ada","name":"hello"}`)
	rassertProblem(t, rec, 503, "unavailable")
}

func TestRepoCreateTokenNamespaceOnly(t *testing.T) {
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	ada, err := e.svc.GetUser(t.Context(), "ada")
	if err != nil {
		t.Fatal(err)
	}
	bob := ruser(t, e, ada.ID, "bob")
	full := rtoken(t, e, ada, "repo:create", "repo:read", "repo:write")
	rec := rdo(t, e, "POST", "/new/repo", full, `{"owner":"ada","name":"hello"}`)
	rassertProblem(t, rec, 503, "unavailable")
	rassertProblem(t, rdo(t, e, "POST", "/new/repo", full, `{"owner":"bob","name":"hello"}`), 403, "forbidden")
	narrow := rtoken(t, e, ada, "repo:read")
	rassertProblem(t, rdo(t, e, "POST", "/new/repo", narrow, `{"owner":"ada","name":"hello"}`), 403, "forbidden")
	bobToken := rtoken(t, e, bob, "repo:create")
	rassertProblem(t, rdo(t, e, "POST", "/new/repo", bobToken, `{"owner":"bob","name":"frombob"}`), 503, "unavailable")
}

func TestRepoLifecycle(t *testing.T) {
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	ada, _ := e.svc.GetUser(t.Context(), "ada")
	ruser(t, e, ada.ID, "bob")
	adaCookie := rlogin(t, e, "ada", "long-test-password-01")
	bobCookie := rlogin(t, e, "bob", "long-test-password-01")
	privID := rseedRepo(t, e, ada.ID, "priv", "private")
	rseedRepo(t, e, ada.ID, "pub", "public")

	rec := rdo(t, e, "GET", "/ada/pub", "", "")
	if rec.Code != 200 {
		t.Fatalf("anon public get = %d: %s", rec.Code, rec.Body.String())
	}
	rassertProblem(t, rdo(t, e, "GET", "/ada/priv", "", ""), 404, "not-found")
	rassertProblem(t, rdo(t, e, "GET", "/ada/priv", bobCookie, ""), 404, "not-found")

	owned := rdo(t, e, "GET", "/ada/priv", adaCookie, "")
	if owned.Code != 200 {
		t.Fatalf("owner get = %d: %s", owned.Code, owned.Body.String())
	}
	etag := owned.Header().Get("ETag")
	if etag == "" {
		t.Fatal("repo get misses ETag")
	}
	cached := rdoFull(t, e, "GET", "/ada/priv", adaCookie, "application/json", map[string]string{"If-None-Match": etag}, "")
	if cached.Code != 304 {
		t.Fatalf("conditional get = %d, want 304", cached.Code)
	}
	rassertProblem(t, rdo(t, e, "POST", "/ada/priv/settings/general", adaCookie, `{"description":"x"}`), 428, "precondition-required")
	rassertProblem(t, rdoFull(t, e, "POST", "/ada/priv/settings/general", adaCookie, "application/json", map[string]string{"If-Match": `"repo-0-0"`}, `{"description":"x"}`), 412, "stale")
	updated := rdoFull(t, e, "POST", "/ada/priv/settings/general", adaCookie, "application/json", map[string]string{"If-Match": etag}, `{"description":"hello world","topics":["go","forge"]}`)
	if updated.Code != 200 {
		t.Fatalf("update = %d: %s", updated.Code, updated.Body.String())
	}
	fresh := rdecode(t, updated)["repo"].(map[string]any)
	if fresh["description"] != "hello world" {
		t.Fatalf("description = %v", fresh["description"])
	}
	rassertProblem(t, rdoFull(t, e, "POST", "/ada/priv/settings/general", adaCookie, "application/json", map[string]string{"If-Match": etag}, `{"description":"again"}`), 412, "stale")

	rassertProblem(t, rdoFull(t, e, "POST", "/ada/priv/settings/visibility", adaCookie, "application/json", map[string]string{"If-Match": updated.Header().Get("ETag")}, `{"visibility":"public","confirm":"wrong"}`), 422, "validation")
	rassertProblem(t, rdo(t, e, "POST", "/ada/priv/settings/visibility", bobCookie, `{"visibility":"public","confirm":"ada/priv"}`), 404, "not-found")

	del := rdoFull(t, e, "POST", "/ada/priv/settings/delete", adaCookie, "application/json", map[string]string{"If-Match": updated.Header().Get("ETag")}, `{"confirm":"ada/priv"}`)
	if del.Code != 202 {
		t.Fatalf("delete = %d: %s", del.Code, del.Body.String())
	}
	deleted := rdecode(t, del)
	if deleted["state"] != "deleted" {
		t.Fatalf("state = %v", deleted["state"])
	}
	var job store.Job
	if jerr := e.st.Read(t.Context(), func(tx *store.ReadTx) error {
		var gerr error
		job, gerr = tx.GetJob("repo-purge:" + itoa(privID))
		return gerr
	}); jerr != nil {
		t.Fatalf("purge job missing: %v", jerr)
	} else if job.State != "queued" {
		t.Fatalf("purge job = %+v", job)
	}
	rassertProblem(t, rdo(t, e, "GET", "/ada/priv", "", ""), 404, "not-found")
	gone := rdo(t, e, "GET", "/ada/priv", adaCookie, "")
	if gone.Code != 200 {
		t.Fatalf("owner deleted get = %d: %s", gone.Code, gone.Body.String())
	}
	if rdecode(t, gone)["repo"].(map[string]any)["state"] != "deleted" {
		t.Fatalf("deleted state missing: %s", gone.Body.String())
	}
	goneETag := gone.Header().Get("ETag")
	rassertProblem(t, rdo(t, e, "POST", "/ada/priv/settings/restore", adaCookie, ""), 428, "precondition-required")
	restored := rdoFull(t, e, "POST", "/ada/priv/settings/restore", adaCookie, "application/json", map[string]string{"If-Match": goneETag}, "")
	if restored.Code != 200 {
		t.Fatalf("restore = %d: %s", restored.Code, restored.Body.String())
	}
	rassertProblem(t, rdoFull(t, e, "POST", "/ada/priv/settings/restore", adaCookie, "application/json", map[string]string{"If-Match": restored.Header().Get("ETag")}, ""), 409, "conflict")
}

func TestRepoOpRegistration(t *testing.T) {
	e := buildRepoEnv(t)
	names := map[string]bool{}
	for _, s := range e.server.Specs() {
		names[s.Name] = true
	}
	for _, want := range []string{
		"repo.create", "repo.get", "repo.list", "repo.update",
		"repo.visibility.set", "repo.rename", "repo.delete", "repo.restore",
		"repo.collaborator.list", "repo.collaborator.add", "repo.collaborator.remove",
		"repo.create.page", "repo.settings.page",
		"code.rev.resolve", "code.tree.get", "code.blob.get", "code.raw.get",
		"code.blame.get", "code.log.list", "code.commit.get", "code.diff.get",
		"branch.list", "branch.get", "branch.create", "branch.update", "branch.delete",
		"tag.list", "tag.get", "tag.create", "tag.delete",
	} {
		if !names[want] {
			t.Fatalf("op %q not registered", want)
		}
	}
}

func TestCollaborators(t *testing.T) {
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	ada, _ := e.svc.GetUser(t.Context(), "ada")
	ruser(t, e, ada.ID, "bob")
	adaCookie := rlogin(t, e, "ada", "long-test-password-01")
	bobCookie := rlogin(t, e, "bob", "long-test-password-01")
	rseedRepo(t, e, ada.ID, "team", "private")

	rassertProblem(t, rdo(t, e, "POST", "/ada/team/settings/collaborators/bob", adaCookie, `{"permission":"super"}`), 422, "validation")
	rassertProblem(t, rdo(t, e, "POST", "/ada/team/settings/collaborators/ghost", adaCookie, `{"permission":"read"}`), 404, "not-found")
	rassertProblem(t, rdo(t, e, "POST", "/ada/team/settings/collaborators/ada", adaCookie, `{"permission":"read"}`), 409, "conflict")
	rassertProblem(t, rdo(t, e, "POST", "/ada/team/settings/collaborators/bob", bobCookie, `{"permission":"read"}`), 404, "not-found")
	if rec := rdo(t, e, "POST", "/ada/team/settings/collaborators/bob", adaCookie, `{"permission":"read"}`); rec.Code != 204 {
		t.Fatalf("grant read = %d: %s", rec.Code, rec.Body.String())
	}
	rassertProblem(t, rdo(t, e, "POST", "/ada/team/settings/collaborators/bob", bobCookie, `{"permission":"read"}`), 403, "forbidden")
	if rec := rdo(t, e, "POST", "/ada/team/settings/collaborators/bob", adaCookie, `{"permission":"write"}`); rec.Code != 204 {
		t.Fatalf("grant = %d: %s", rec.Code, rec.Body.String())
	}
	listed := rdo(t, e, "GET", "/ada/team/settings/collaborators", adaCookie, "")
	if listed.Code != 200 {
		t.Fatalf("list = %d: %s", listed.Code, listed.Body.String())
	}
	grants := rdecode(t, listed)["collaborators"].([]any)
	if len(grants) != 1 || grants[0].(map[string]any)["user"] != "bob" {
		t.Fatalf("grants = %v", grants)
	}
	if rec := rdo(t, e, "POST", "/ada/team/settings/collaborators/bob/remove", adaCookie, ""); rec.Code != 204 {
		t.Fatalf("remove = %d: %s", rec.Code, rec.Body.String())
	}
	rassertProblem(t, rdo(t, e, "POST", "/ada/team/settings/collaborators/bob/remove", adaCookie, ""), 404, "not-found")
}

func TestRepoListVisibility(t *testing.T) {
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	ada, _ := e.svc.GetUser(t.Context(), "ada")
	bob := ruser(t, e, ada.ID, "bob")
	adaCookie := rlogin(t, e, "ada", "long-test-password-01")
	bobCookie := rlogin(t, e, "bob", "long-test-password-01")
	rseedRepo(t, e, ada.ID, "pub", "public")
	rseedRepo(t, e, ada.ID, "inner", "internal")
	rseedRepo(t, e, ada.ID, "priv", "private")
	rseedRepo(t, e, bob.ID, "bpub", "public")

	anon := rdecode(t, rdo(t, e, "GET", "/repos", "", ""))
	if len(anon["repos"].([]any)) != 2 {
		t.Fatalf("anon sees %v", anon["repos"])
	}
	bobList := rdecode(t, rdo(t, e, "GET", "/repos", bobCookie, ""))
	if len(bobList["repos"].([]any)) != 3 {
		t.Fatalf("bob sees %v", bobList["repos"])
	}
	first := rdo(t, e, "GET", "/repos?owner=ada&limit=2", adaCookie, "")
	adaList := rdecode(t, first)
	if len(adaList["repos"].([]any)) != 2 {
		t.Fatalf("ada page one = %v", adaList["repos"])
	}
	link := first.Header().Get("Link")
	if !strings.Contains(link, `rel="next"`) {
		t.Fatalf("first page misses a next link: %q", link)
	}
	secondTarget := link[strings.Index(link, "<")+1 : strings.Index(link, ">")]
	second := rdecode(t, rdo(t, e, "GET", secondTarget, adaCookie, ""))
	if len(second["repos"].([]any)) != 1 {
		t.Fatalf("ada page two = %v", second["repos"])
	}
	rassertProblem(t, rdo(t, e, "GET", "/repos?sort=bogus", adaCookie, ""), 422, "validation")
	rassertProblem(t, rdo(t, e, "GET", "/repos?visibility=bogus", adaCookie, ""), 422, "validation")
	empty := rdecode(t, rdo(t, e, "GET", "/repos?owner=ghost", adaCookie, ""))
	if len(empty["repos"].([]any)) != 0 {
		t.Fatalf("ghost owner = %v", empty["repos"])
	}
}

func TestRepoRenameValidation(t *testing.T) {
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	ada, _ := e.svc.GetUser(t.Context(), "ada")
	ruser(t, e, ada.ID, "bob")
	adaCookie := rlogin(t, e, "ada", "long-test-password-01")
	rseedRepo(t, e, ada.ID, "old", "private")
	etag := rdo(t, e, "GET", "/ada/old", adaCookie, "").Header().Get("ETag")
	withETag := map[string]string{"If-Match": etag}
	rassertProblem(t, rdoFull(t, e, "POST", "/ada/old/settings/rename", adaCookie, "application/json", withETag, `{"name":"x.git"}`), 422, "validation")
	rassertProblem(t, rdoFull(t, e, "POST", "/ada/old/settings/rename", adaCookie, "application/json", withETag, `{"name":"ok","new_owner":"ghost"}`), 422, "validation")
	rassertProblem(t, rdoFull(t, e, "POST", "/ada/old/settings/rename", adaCookie, "application/json", withETag, `{"name":"ok","new_owner":"bob"}`), 403, "forbidden")
	moved := rdoFull(t, e, "POST", "/ada/old/settings/rename", adaCookie, "application/json", withETag, `{"name":"new"}`)
	rassertProblem(t, moved, 503, "unavailable")
}

func TestBranchTagValidation(t *testing.T) {
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	ada, _ := e.svc.GetUser(t.Context(), "ada")
	adaCookie := rlogin(t, e, "ada", "long-test-password-01")
	rseedRepo(t, e, ada.ID, "code", "public")

	rassertProblem(t, rdo(t, e, "POST", "/ada/code/branches/create", adaCookie, `{"name":"a..b","from":"main"}`), 422, "validation")
	rassertProblem(t, rdo(t, e, "POST", "/ada/code/branches/create", adaCookie, `{"name":"ok","from":"main"}`), 503, "unavailable")
	rassertProblem(t, rdo(t, e, "GET", "/ada/code/branches/main", "", ""), 503, "unavailable")
	rassertProblem(t, rdo(t, e, "POST", "/ada/code/tags/create", adaCookie, `{"name":"v1","target":"main"}`), 503, "unavailable")
	rassertProblem(t, rdo(t, e, "POST", "/ada/code/tags/create", adaCookie, `{"name":"bad name","target":"main"}`), 422, "validation")
	rassertProblem(t, rdo(t, e, "POST", "/ada/code/branches/x/delete", adaCookie, ""), 428, "precondition-required")
	rassertProblem(t, rdo(t, e, "POST", "/ada/code/tags/x/delete", adaCookie, ""), 428, "precondition-required")
}

func TestCodeValidation(t *testing.T) {
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	ada, _ := e.svc.GetUser(t.Context(), "ada")
	rseedRepo(t, e, ada.ID, "pub", "public")

	rassertProblem(t, rdo(t, e, "GET", "/ada/pub/tree/main/docs?limit=1001", "", ""), 400, "invalid")
	rassertProblem(t, rdo(t, e, "GET", "/ada/pub/tree/main/docs?limit=999", "", ""), 503, "unavailable")
	rassertProblem(t, rdo(t, e, "GET", "/ada/pub/tree/main/docs?limit=1000", "", ""), 503, "unavailable")
	rassertProblem(t, rdo(t, e, "GET", "/ada/pub/tree/main/.git", "", ""), 422, "validation")
	rassertProblem(t, rdo(t, e, "GET", "/ada/pub/blob/main/", "", ""), 422, "validation")
	rassertProblem(t, rdo(t, e, "GET", "/ada/pub/blame/main/f?start_line=5&end_line=2", "", ""), 422, "validation")
	rassertProblem(t, rdo(t, e, "GET", "/ada/pub/log/main?first_parent=true", "", ""), 400, "invalid")
	rassertProblem(t, rdo(t, e, "GET", "/ada/pub/diff/main", "", ""), 422, "validation")
	rassertProblem(t, rdo(t, e, "GET", "/ada/pub/diff/main..other?context=5", "", ""), 400, "invalid")
	rassertProblem(t, rdo(t, e, "GET", "/ada/pub/diff/main..other?renames=false", "", ""), 400, "invalid")
	rassertProblem(t, rdo(t, e, "GET", "/ada/pub/commit/abc", "", ""), 422, "validation")
	rassertProblem(t, rdo(t, e, "GET", "/ada/pub/commit/0123456789abcdef0123456789abcdef01234567", "", ""), 503, "unavailable")
}

func TestRepoPagesZeroJS(t *testing.T) {
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	ada, _ := e.svc.GetUser(t.Context(), "ada")
	adaCookie := rlogin(t, e, "ada", "long-test-password-01")
	rseedRepo(t, e, ada.ID, "pub", "public")
	for _, target := range []struct {
		path string
		auth string
		code int
	}{
		{"/new/repo", "", 200},
		{"/repos", "", 200},
		{"/ada/pub", "", 200},
		{"/ada/pub/settings", adaCookie, 200},
		{"/ada/pub/settings/collaborators", adaCookie, 200},
		{"/ada/pub/branches", "", 503},
		{"/ada/pub/tags", "", 503},
	} {
		rec := rdoFull(t, e, "GET", target.path, target.auth, "text/html", nil, "")
		if rec.Code != target.code {
			t.Fatalf("GET %s html = %d, want %d: %s", target.path, rec.Code, target.code, rec.Body.String())
		}
		if err := zerojs.Assert(rec.Body.Bytes()); err != nil {
			t.Fatalf("GET %s html failed zero-JS: %v", target.path, err)
		}
		if csp := rec.Header().Get("Content-Security-Policy"); csp == "" {
			t.Fatalf("GET %s html misses CSP", target.path)
		}
	}
}
