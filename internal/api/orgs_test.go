package api_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"net/http"
	"net/http/httptest"
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
)

type orgEnv struct {
	server *op.Server
	svc    *auth.Service
	st     *store.Store
	deps   api.Deps
}

func buildOrgsEnv(t *testing.T) *orgEnv {
	t.Helper()
	path := t.TempDir() + "/f4.sqlite"
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
	deps := api.Deps{Service: svc, Limiter: access.NewLimiter(), Reveals: auth.NewRevealCache(), Store: st}
	table := op.NewTable()
	for _, step := range []func() error{
		func() error { return api.RegisterAll(table, &api.DocStore{}) },
		func() error { return api.RegisterAuth(table, deps) },
		func() error { return api.RegisterUser(table, deps) },
		func() error { return api.RegisterAdmin(table, deps) },
		func() error { return api.RegisterOrgs(table, deps) },
	} {
		if serr := step(); serr != nil {
			t.Fatalf("register: %v", err)
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
	api.RegisterOrgsViews(views)
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
	return &orgEnv{server: server, svc: svc, st: st, deps: deps}
}

func odo(t *testing.T, e *orgEnv, method, target, authHeader, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if authHeader != "" {
		if strings.HasPrefix(authHeader, "Bearer ") {
			req.Header.Set("Authorization", authHeader)
		} else {
			req.Header.Set("Cookie", authHeader)
		}
	}
	out := httptest.NewRecorder()
	e.server.ServeHTTP(out, req)
	return out
}

func odecode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := jsonv2.Unmarshal(rec.Body.Bytes(), &decoded, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("decode %d: %v\n%s", rec.Code, err, rec.Body.String())
	}
	return decoded
}

func ologin(t *testing.T, e *orgEnv, username, password string) string {
	t.Helper()
	rec := odo(t, e, "POST", "/login", "", `{"username":"`+username+`","password":"`+password+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("login = %d: %s", rec.Code, rec.Body.String())
	}
	parts := strings.Split(rec.Header().Get("Set-Cookie"), ";")
	return strings.TrimSpace(parts[0])
}

func obootstrap(t *testing.T, e *orgEnv, name, pw string) {
	t.Helper()
	if _, err := e.svc.BootstrapAdmin(t.Context(), name, pw, "127.0.0.1"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
}

func ouser(t *testing.T, e *orgEnv, adminID int64, name string) auth.User {
	t.Helper()
	user, err := e.svc.CreateUser(t.Context(), adminID, auth.CreateUserInput{
		Name: name, Email: name + "@example.test", Password: "long-test-password-01",
	}, "127.0.0.1")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

func otoken(t *testing.T, e *orgEnv, user auth.User, repos []int64, scopes ...string) string {
	t.Helper()
	parsed, err := auth.ParseScopeSet(scopes)
	if err != nil {
		t.Fatalf("scopes: %v", err)
	}
	secret, err := e.svc.CreateToken(t.Context(), auth.CreateTokenInput{
		Name: "f4", Scopes: parsed, UserID: user.ID, Repos: repos,
	})
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	return "Bearer " + secret.Secret
}

func oseedRepo(t *testing.T, e *orgEnv, ownerID, orgID int64, name string) int64 {
	t.Helper()
	var id int64
	err := e.st.Write(t.Context(), func(tx *store.WriteTx) error {
		var ierr error
		id, ierr = tx.InsertRepo(time.Now(), store.RepoInsert{
			OwnerUserID:   sql.NullInt64{Int64: ownerID, Valid: orgID == 0},
			OrgID:         orgID,
			Name:          name,
			Visibility:    "private",
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

func ocreateOrg(t *testing.T, e *orgEnv, cookie, name, visibility string) map[string]any {
	t.Helper()
	rec := odo(t, e, "POST", "/new/org", cookie,
		`{"name":"`+name+`","visibility":"`+visibility+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create org = %d: %s", rec.Code, rec.Body.String())
	}
	return odecode(t, rec)
}

func TestTokenLimitedToOneRepoAndOpSet(t *testing.T) {
	e := buildOrgsEnv(t)
	obootstrap(t, e, "ada", "long-test-password-01")
	adaCookie := ologin(t, e, "ada", "long-test-password-01")
	ada, err := e.svc.GetUser(t.Context(), "ada")
	if err != nil {
		t.Fatalf("get ada: %v", err)
	}
	r1 := oseedRepo(t, e, ada.ID, 0, "one")
	r2 := oseedRepo(t, e, ada.ID, 0, "two")
	narrow := otoken(t, e, ada, []int64{r1}, "repo:read")
	if rec := odo(t, e, "GET", "/ada/one", narrow, ""); rec.Code != http.StatusOK {
		t.Fatalf("narrow token reads r1 = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := odo(t, e, "GET", "/ada/two", narrow, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("narrow token reads r2 = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	wide := otoken(t, e, ada, nil, "repo:read")
	if rec := odo(t, e, "GET", "/ada/two", wide, ""); rec.Code != http.StatusOK {
		t.Fatalf("open token reads r2 = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := odo(t, e, "POST", "/ada/one/settings/general", narrow,
		`{"description":"x"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("read token mutates = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if rec := odo(t, e, "POST", "/settings/tokens", adaCookie,
		`{"name":"scoped","scopes":"repo:read","repos":[999999]}`); rec.Code != http.StatusNotFound {
		t.Fatalf("token over missing repo = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	rec := odo(t, e, "POST", "/settings/tokens", adaCookie,
		`{"name":"scoped","scopes":"repo:read","repos":[`+itoa(r1)+`]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("token create with repos = %d", rec.Code)
	}
	_ = r2
}

func TestOrgOwnersTeamAdmin(t *testing.T) {
	e := buildOrgsEnv(t)
	obootstrap(t, e, "ada", "long-test-password-01")
	adaCookie := ologin(t, e, "ada", "long-test-password-01")
	ada, _ := e.svc.GetUser(t.Context(), "ada")
	ocreateOrg(t, e, adaCookie, "acme", "private")
	bob := ouser(t, e, ada.ID, "bob")
	var orgID int64
	if err := e.st.Read(t.Context(), func(tx *store.ReadTx) error {
		row, ferr := tx.FindOrgByName("acme")
		if ferr != nil {
			return ferr
		}
		orgID = row.ID
		return nil
	}); err != nil {
		t.Fatalf("find org: %v", err)
	}
	repoID := oseedRepo(t, e, 0, orgID, "widget")
	devBody, _ := jsonv2.Marshal(map[string]any{"name": "devs", "permission": "write"}, jsonv1.DefaultOptionsV1())
	if rec := odo(t, e, "POST", "/acme/-/teams/create", adaCookie, string(devBody)); rec.Code != http.StatusCreated {
		t.Fatalf("create team = %d: %s", rec.Code, rec.Body.String())
	}
	grantBody, _ := jsonv2.Marshal(map[string]any{"repo": "widget"}, jsonv1.DefaultOptionsV1())
	if rec := odo(t, e, "POST", "/acme/-/teams/devs/repos", adaCookie, string(grantBody)); rec.Code != http.StatusOK {
		t.Fatalf("grant repo = %d: %s", rec.Code, rec.Body.String())
	}
	addBody, _ := jsonv2.Marshal(map[string]any{"user": "bob"}, jsonv1.DefaultOptionsV1())
	if rec := odo(t, e, "POST", "/acme/-/teams/devs/members", adaCookie, string(addBody)); rec.Code != http.StatusOK {
		t.Fatalf("add member = %d: %s", rec.Code, rec.Body.String())
	}
	bobCookie := ologin(t, e, "bob", "long-test-password-01")
	if rec := odo(t, e, "GET", "/acme/-/teams", bobCookie, ""); rec.Code != http.StatusOK {
		t.Fatalf("member lists teams = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := odo(t, e, "GET", "/acme", bobCookie, ""); rec.Code != http.StatusOK {
		t.Fatalf("member views org = %d: %s", rec.Code, rec.Body.String())
	}
	_ = repoID
	_ = bob
}

func TestTeamMemberAddImpliesOrgMember(t *testing.T) {
	e := buildOrgsEnv(t)
	obootstrap(t, e, "ada", "long-test-password-01")
	adaCookie := ologin(t, e, "ada", "long-test-password-01")
	ada, _ := e.svc.GetUser(t.Context(), "ada")
	ocreateOrg(t, e, adaCookie, "acme", "public")
	ouser(t, e, ada.ID, "bob")
	addBody, _ := jsonv2.Marshal(map[string]any{"user": "bob"}, jsonv1.DefaultOptionsV1())
	if rec := odo(t, e, "POST", "/acme/-/teams/owners/members", adaCookie, string(addBody)); rec.Code != http.StatusOK {
		t.Fatalf("add to owners = %d: %s", rec.Code, rec.Body.String())
	}
	rec := odo(t, e, "GET", "/acme/-/members", adaCookie, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list members = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "bob") {
		t.Fatalf("bob missing from member list: %s", body)
	}
	if !strings.Contains(body, `"role":"owner"`) {
		t.Fatalf("bob missing owner role: %s", body)
	}
	bobCookie := ologin(t, e, "bob", "long-test-password-01")
	orl := odo(t, e, "GET", "/settings/orgs", bobCookie, "")
	if orl.Code != http.StatusOK {
		t.Fatalf("bob lists orgs = %d: %s", orl.Code, orl.Body.String())
	}
	if !strings.Contains(orl.Body.String(), "acme") {
		t.Fatalf("acme missing from bob orgs: %s", orl.Body.String())
	}
	var outbox int64
	if err := e.st.Read(t.Context(), func(tx *store.ReadTx) error {
		rows, aerr := tx.Outbox().After("no-such-consumer", 100)
		if aerr != nil {
			return aerr
		}
		for _, row := range rows {
			if row.Kind == "team.member.added" {
				outbox++
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("outbox: %v", err)
	}
	if outbox == 0 {
		t.Fatal("no team.member.added outbox row")
	}
}

func TestLastOwnerGuard(t *testing.T) {
	e := buildOrgsEnv(t)
	obootstrap(t, e, "ada", "long-test-password-01")
	adaCookie := ologin(t, e, "ada", "long-test-password-01")
	ada, _ := e.svc.GetUser(t.Context(), "ada")
	ocreateOrg(t, e, adaCookie, "acme", "public")
	ouser(t, e, ada.ID, "bob")
	if rec := odo(t, e, "POST", "/acme/-/members/ada/remove", adaCookie, ""); rec.Code != http.StatusConflict {
		t.Fatalf("remove sole owner = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if rec := odo(t, e, "POST", "/acme/-/members/leave", adaCookie, ""); rec.Code != http.StatusConflict {
		t.Fatalf("leave as sole owner = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	addBody, _ := jsonv2.Marshal(map[string]any{"user": "bob"}, jsonv1.DefaultOptionsV1())
	if rec := odo(t, e, "POST", "/acme/-/teams/owners/members", adaCookie, string(addBody)); rec.Code != http.StatusOK {
		t.Fatalf("add bob to owners = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := odo(t, e, "POST", "/acme/-/members/ada/remove", adaCookie, ""); rec.Code != http.StatusOK {
		t.Fatalf("remove shared owner = %d: %s", rec.Code, rec.Body.String())
	}
	bobCookie := ologin(t, e, "bob", "long-test-password-01")
	if rec := odo(t, e, "POST", "/acme/-/teams/owners/members/bob/remove", bobCookie, ""); rec.Code != http.StatusConflict {
		t.Fatalf("bob removes last owner self = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

func TestAuthzWorkedResolutions(t *testing.T) {
	e := buildOrgsEnv(t)
	obootstrap(t, e, "ada", "long-test-password-01")
	adaCookie := ologin(t, e, "ada", "long-test-password-01")
	ada, _ := e.svc.GetUser(t.Context(), "ada")
	ouser(t, e, ada.ID, "bob")
	bobCookie := ologin(t, e, "bob", "long-test-password-01")
	bobToken := otoken(t, e, ada, nil, "repo:read", "repo:write", "repo:create", "intents:read", "intents:write")
	if rec := odo(t, e, "POST", "/new/org", bobToken, `{"name":"x"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token creates org = %d, want 401: %s", rec.Code, rec.Body.String())
	}
	_ = bobCookie
	_ = adaCookie
	if rec := odo(t, e, "GET", "/no-such-owner-xyz", "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown owner = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	ocreateOrg(t, e, adaCookie, "priv", "private")
	if rec := odo(t, e, "GET", "/orgs/priv", "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("anon views private org = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	ocreateOrg(t, e, adaCookie, "open", "public")
	if rec := odo(t, e, "GET", "/orgs/open", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("anon views public org = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := odo(t, e, "POST", "/new/org", "", `{"name":"anon"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon creates org = %d, want 401: %s", rec.Code, rec.Body.String())
	}
	dup := odo(t, e, "POST", "/new/org", adaCookie, `{"name":"open"}`)
	if dup.Code != http.StatusConflict {
		t.Fatalf("duplicate org = %d, want 409: %s", dup.Code, dup.Body.String())
	}
	empty := odo(t, e, "POST", "/new/org", adaCookie, `{"name":""}`)
	if empty.Code != http.StatusBadRequest {
		t.Fatalf("empty org name = %d, want 400: %s", empty.Code, empty.Body.String())
	}
	reserved := odo(t, e, "POST", "/new/org", adaCookie, `{"name":"new"}`)
	if reserved.Code != http.StatusBadRequest && reserved.Code != http.StatusUnprocessableEntity && reserved.Code != http.StatusConflict {
		t.Fatalf("reserved org name = %d, want 4xx: %s", reserved.Code, reserved.Body.String())
	}
	if rec := odo(t, e, "POST", "/priv/-/teams/owners/members", bobToken, `{"user":"bob"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token adds team member = %d, want 401: %s", rec.Code, rec.Body.String())
	}
}
