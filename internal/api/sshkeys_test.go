package api_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/api"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/web/zerojs"

	"golang.org/x/crypto/ssh"
)

func openKeysTestEnv(t *testing.T) *testEnv {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keys.sqlite")
	raw, openErr := sql.Open("sqlite", "file:"+path+"?_txlock=immediate&_journal_mode=WAL&_synchronous=FULL&_foreign_keys=on&_busy_timeout=5000")
	if openErr != nil {
		t.Fatalf("sql open: %v", openErr)
	}
	entries, dirErr := os.ReadDir("../store/migrations")
	if dirErr != nil {
		t.Fatalf("migrations dir: %v", dirErr)
	}
	for _, e := range entries {
		body, readErr := os.ReadFile(filepath.Join("../store/migrations", e.Name()))
		if readErr != nil {
			t.Fatalf("read %s: %v", e.Name(), readErr)
		}
		if _, execErr := raw.Exec(string(body)); execErr != nil {
			t.Fatalf("apply %s: %v", e.Name(), execErr)
		}
	}
	if closeErr := raw.Close(); closeErr != nil {
		t.Fatalf("sql close: %v", closeErr)
	}
	svc, svcErr := auth.Open(path)
	if svcErr != nil {
		t.Fatalf("auth open: %v", svcErr)
	}
	t.Cleanup(func() { _ = svc.Close() })
	deps := api.Deps{Service: svc, Limiter: access.NewLimiter(), Reveals: auth.NewRevealCache()}
	table := op.NewTable()
	if regErr := api.RegisterAuth(table, deps); regErr != nil {
		t.Fatalf("register auth: %v", regErr)
	}
	if regErr := api.RegisterUser(table, deps); regErr != nil {
		t.Fatalf("register user: %v", regErr)
	}
	if regErr := api.RegisterSSHKeys(table, deps); regErr != nil {
		t.Fatalf("register sshkeys: %v", regErr)
	}
	views := op.NewViews()
	api.RegisterAuthViews(views)
	api.RegisterUserViews(views)
	api.RegisterSSHKeysViews(views)
	var key [32]byte
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
	return &testEnv{server: server, svc: svc, deps: deps}
}

func keysUser(t *testing.T, e *testEnv, name string, admin bool) (auth.User, string) {
	t.Helper()
	pw := randomPW(t)
	owner, err := e.svc.GetUser(t.Context(), "ada")
	if err != nil {
		owner, err = e.svc.BootstrapAdmin(t.Context(), "ada", randomPW(t), "127.0.0.1")
		if err != nil {
			t.Fatalf("bootstrap ada: %v", err)
		}
	}
	user, err := e.svc.CreateUser(t.Context(), owner.ID, auth.CreateUserInput{
		Name: name, Email: name + "@example.test", Password: pw, IsAdmin: admin,
	}, "127.0.0.1")
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	cookie := loginCookie(t, e, name, pw)
	return user, cookie
}

func keysBearer(t *testing.T, e *testEnv, method, target, body, secret string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+secret)
	rec := httptest.NewRecorder()
	e.server.ServeHTTP(rec, req)
	return rec
}

func testKeyLine(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
}

func TestSSHKeyOpsHTTP(t *testing.T) {
	e := openKeysTestEnv(t)
	user, cookie := keysUser(t, e, "bob", false)

	if rec := doJSON(t, e, "GET", "/settings/keys", "", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("anonymous list = %d", rec.Code)
	}
	scopes, err := auth.ParseScopeSet([]string{"repo:read", "repo:write"})
	if err != nil {
		t.Fatalf("scopes: %v", err)
	}
	secret, err := e.svc.CreateToken(t.Context(), auth.CreateTokenInput{
		Name: "agent", Scopes: scopes, UserID: user.ID,
	})
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	if rec := keysBearer(t, e, "GET", "/settings/keys", "", secret.Secret); rec.Code != http.StatusForbidden {
		t.Fatalf("token list = %d", rec.Code)
	}

	rec := doJSON(t, e, "POST", "/settings/keys", cookie, `{"key":"not a key"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad key = %d: %s", rec.Code, rec.Body.String())
	}
	line := testKeyLine(t)
	rec = doJSON(t, e, "POST", "/settings/keys", cookie, `{"key":"`+line+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add = %d: %s", rec.Code, rec.Body.String())
	}
	added := decodeBody(t, rec)
	fp, _ := added["fingerprint"].(string)
	if !strings.HasPrefix(fp, "SHA256:") {
		t.Fatalf("fingerprint = %v", added)
	}
	id := int64(added["id"].(float64))
	rec = doJSON(t, e, "POST", "/settings/keys", cookie, `{"key":"`+line+`"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate = %d: %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, e, "GET", "/settings/keys", cookie, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
	}
	listed := decodeBody(t, rec)
	keys, _ := listed["keys"].([]any)
	if len(keys) != 1 {
		t.Fatalf("keys = %v", listed)
	}
	rec = doJSON(t, e, "POST", "/settings/keys/999999/delete", cookie, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing delete = %d", rec.Code)
	}
	_, otherCookie := keysUser(t, e, "cat", false)
	rec = doJSON(t, e, "POST", "/settings/keys/"+strconv.FormatInt(id, 10)+"/delete", otherCookie, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user delete = %d", rec.Code)
	}
	rec = doJSON(t, e, "POST", "/settings/keys/"+strconv.FormatInt(id, 10)+"/delete", cookie, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, e, "GET", "/settings/keys", cookie, "")
	listed = decodeBody(t, rec)
	if keys, _ := listed["keys"].([]any); len(keys) != 0 {
		t.Fatalf("keys after delete = %v", listed)
	}
}

func TestSSHKeyListPageRenders(t *testing.T) {
	e := openKeysTestEnv(t)
	_, cookie := keysUser(t, e, "dan", true)
	rec := doForm(t, e, "GET", "/settings/keys", cookie, "text/html", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("page = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "SSH keys") || !strings.Contains(body, "Add a key") {
		t.Fatalf("page misses key UI")
	}
	if err := zerojs.Assert([]byte(body)); err != nil {
		t.Fatalf("zero-js: %v", err)
	}
}

func TestOwnerKeysPlain(t *testing.T) {
	e := openKeysTestEnv(t)
	user, cookie := keysUser(t, e, "erin", true)
	line := testKeyLine(t)
	rec := doJSON(t, e, "POST", "/settings/keys", cookie, `{"key":"`+line+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add = %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/erin.keys", nil)
	recorder := httptest.NewRecorder()
	api.ServeOwnerKeys(recorder, req, e.deps, "erin")
	res := recorder.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("keys = %d", res.StatusCode)
	}
	if ctype := res.Header.Get("Content-Type"); ctype != "text/plain; charset=utf-8" {
		t.Fatalf("content-type = %q", ctype)
	}
	raw, _ := io.ReadAll(res.Body)
	if strings.TrimSpace(string(raw)) != line {
		t.Fatalf("keys body = %q, want %q", raw, line)
	}
	empty := httptest.NewRecorder()
	api.ServeOwnerKeys(empty, httptest.NewRequest(http.MethodGet, "/ghost.keys", nil), e.deps, "ghost")
	if empty.Result().StatusCode != http.StatusNotFound {
		t.Fatalf("unknown owner = %d", empty.Result().StatusCode)
	}
	admin, err := e.svc.GetUser(t.Context(), "ada")
	if err != nil {
		t.Fatalf("ada: %v", err)
	}
	tag, err := e.svc.ETagFor(user.ID)
	if err != nil {
		t.Fatalf("etag: %v", err)
	}
	inactive := false
	if _, err := e.svc.UpdateUser(t.Context(), admin.ID, auth.UpdateUserInput{Target: user.Name, IsActive: &inactive, IfMatch: tag}, "127.0.0.1"); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	gone := httptest.NewRecorder()
	api.ServeOwnerKeys(gone, httptest.NewRequest(http.MethodGet, "/erin.keys", nil), e.deps, "erin")
	if gone.Result().StatusCode != http.StatusNotFound {
		t.Fatalf("inactive owner = %d", gone.Result().StatusCode)
	}
}

func TestOwnerKeysPath(t *testing.T) {
	valid := map[string]string{"/ada.keys": "ada", "/ada.keys/": "ada", "/a-d_0.9.keys": "a-d_0.9"}
	for path, want := range valid {
		got, ok := api.OwnerKeysPath(path)
		if !ok || got != want {
			t.Fatalf("%q = %q, %v", path, got, ok)
		}
	}
	for _, path := range []string{"/", "/ada", "/ada.git", "/.keys", "/a/b.keys", "/ada.keys/x", ""} {
		if _, ok := api.OwnerKeysPath(path); ok {
			t.Fatalf("%q matched", path)
		}
	}
}

func TestOwnerKeysReservedFallsThrough(t *testing.T) {
	e := openKeysTestEnv(t)
	_, cookie := keysUser(t, e, "alice", false)
	line := testKeyLine(t)
	rec := doJSON(t, e, "POST", "/settings/keys", cookie, `{"key":"`+line+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add = %d", rec.Code)
	}
	if owner, ok := api.OwnerKeysPath("/alice.keys"); !ok || owner != "alice" {
		t.Fatalf("/alice.keys = %q, %v, want alice true", owner, ok)
	}
	got := httptest.NewRecorder()
	api.ServeOwnerKeys(got, httptest.NewRequest(http.MethodGet, "/alice.keys", nil), e.deps, "alice")
	if got.Result().StatusCode != http.StatusOK {
		t.Fatalf("alice keys = %d", got.Result().StatusCode)
	}
	raw, _ := io.ReadAll(got.Result().Body)
	if strings.TrimSpace(string(raw)) != line {
		t.Fatalf("alice keys body = %q, want %q", raw, line)
	}
	for _, path := range []string{"/login.keys", "/settings.keys", "/admin.keys", "/api.keys", "/Login.keys"} {
		if owner, ok := api.OwnerKeysPath(path); ok {
			t.Fatalf("%q routes owner-side as %q, want site fall-through", path, owner)
		}
	}
}

func TestKeysExportOpRouteMatchesTable(t *testing.T) {
	e := openKeysTestEnv(t)
	opv := api.KeysExportOp(e.deps)
	if opv.Name != "auth.sshkey.keys" {
		t.Fatalf("name = %q", opv.Name)
	}
	if opv.Path != "/{owner}.keys" {
		t.Fatalf("path = %q", opv.Path)
	}
	if opv.Method != http.MethodGet || opv.Kind != op.KindRead {
		t.Fatalf("method/kind = %q/%v", opv.Method, opv.Kind)
	}
	if opv.Scope != op.ScopePublicRead {
		t.Fatalf("scope = %v", opv.Scope)
	}
	matched := false
	for _, r := range api.Routes() {
		if r.Op == opv.Name {
			matched = true
			if r.Pattern != "GET "+opv.Path {
				t.Fatalf("route = %q, want GET %s", r.Pattern, opv.Path)
			}
		}
	}
	if !matched {
		t.Fatalf("no route entry derives from %q", opv.Name)
	}
	scratch := op.NewTable()
	if err := op.Register(scratch, opv); err != nil {
		t.Fatalf("op fails table validation: %v", err)
	}
}

func TestKeysExportOpHandler(t *testing.T) {
	e := openKeysTestEnv(t)
	user, cookie := keysUser(t, e, "erin", false)
	line := testKeyLine(t)
	if rec := doJSON(t, e, "POST", "/settings/keys", cookie, `{"key":"`+line+`"}`); rec.Code != http.StatusCreated {
		t.Fatalf("add = %d", rec.Code)
	}
	opv := api.KeysExportOp(e.deps)
	out, err := opv.Handler(t.Context(), op.AnonymousPrincipal(), api.KeysExportIn{Owner: "erin"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(out.Keys) != 1 || strings.TrimSpace(out.Keys[0]) != line {
		t.Fatalf("keys = %q", out.Keys)
	}
	first := opv.ETag(out)
	line2 := testKeyLine(t)
	if rec := doJSON(t, e, "POST", "/settings/keys", cookie, `{"key":"`+line2+`"}`); rec.Code != http.StatusCreated {
		t.Fatalf("add second = %d", rec.Code)
	}
	out2, err := opv.Handler(t.Context(), op.AnonymousPrincipal(), api.KeysExportIn{Owner: "erin"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(out2.Keys) != 2 {
		t.Fatalf("keys = %q", out2.Keys)
	}
	if second := opv.ETag(out2); second == first {
		t.Fatalf("etag unchanged after key add")
	}
	if _, err := opv.Handler(t.Context(), op.AnonymousPrincipal(), api.KeysExportIn{Owner: "ghost"}); !isKeysNotFound(err) {
		t.Fatalf("unknown user err = %v", err)
	}
	admin, err := e.svc.GetUser(t.Context(), "ada")
	if err != nil {
		t.Fatalf("ada: %v", err)
	}
	tag, err := e.svc.ETagFor(user.ID)
	if err != nil {
		t.Fatalf("etag: %v", err)
	}
	inactive := false
	if _, err := e.svc.UpdateUser(t.Context(), admin.ID, auth.UpdateUserInput{Target: user.Name, IsActive: &inactive, IfMatch: tag}, "127.0.0.1"); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if _, err := opv.Handler(t.Context(), op.AnonymousPrincipal(), api.KeysExportIn{Owner: "erin"}); !isKeysNotFound(err) {
		t.Fatalf("inactive user err = %v", err)
	}
}

func isKeysNotFound(err error) bool {
	oerr, ok := errors.AsType[*op.Error](err)
	return ok && oerr.Code == op.CodeNotFound
}
