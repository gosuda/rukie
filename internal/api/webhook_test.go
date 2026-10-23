package api_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	"github.com/gosuda/rukie/internal/webhook"
)

type f8Env struct {
	server *op.Server
	svc    *auth.Service
	st     *store.Store
	now    time.Time
	deps   api.Deps
}

func buildF8Env(t *testing.T) *f8Env {
	return buildF8EnvWithPolicy(t, webhook.Policy{AllowInsecureHTTP: true, AllowPrivateTargets: true})
}

// f8EnvSeq spaces fake clocks across environments so the process-global inbox
// rate buckets refill between tests; business logic stays consistent because
// every handler reads the same env clock.
var f8EnvSeq atomic.Int64

func buildF8EnvWithPolicy(t *testing.T, policy webhook.Policy) *f8Env {
	t.Helper()
	e := &f8Env{now: time.Now().Add(time.Duration(f8EnvSeq.Add(1)) * 24 * time.Hour)}
	st, err := store.Open(t.Context(), store.Options{Path: t.TempDir() + "/f8.sqlite"})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	e.st = st
	svc := auth.New(st)
	e.svc = svc
	t.Cleanup(func() {
		_ = st.Close(context.Background())
	})
	var sealKey [32]byte
	_, _ = rand.Read(sealKey[:])
	var csrfKey [32]byte
	_, _ = rand.Read(csrfKey[:])
	e.deps = api.Deps{
		Service: svc, Limiter: access.NewLimiter(), Reveals: auth.NewRevealCache(),
		Store: st, Ruk: nil, RepoRoot: t.TempDir(),
		SealKey: sealKey[:], CSRFKey: csrfKey[:],
		Webhooks: policy,
		Clock:    func() time.Time { return e.now },
	}
	table := op.NewTable()
	for _, step := range []func() error{
		func() error { return api.RegisterAll(table, &api.DocStore{}) },
		func() error { return api.RegisterAuth(table, e.deps) },
		func() error { return api.RegisterUser(table, e.deps) },
		func() error { return api.RegisterAdmin(table, e.deps) },
		func() error { return api.RegisterWebhook(table, e.deps) },
		func() error { return api.RegisterInbox(table, e.deps) },
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
	api.RegisterWebhookViews(views)
	api.RegisterInboxViews(views)
	mux := http.NewServeMux()
	server, merr := op.Mount(mux, table, op.Config{
		Authenticator: auth.NewAuthenticator(svc),
		Idempotency:   op.NewMemoryIdempotency(),
		Audit:         op.FuncAuditor(func(context.Context, op.AuditRow) error { return nil }),
		Limiter:       e.deps.Limiter,
		CSRFKey:       csrfKey[:],
		Views:         views,
		Title:         "test",
		Version:       "0",
		IsSiteSegment: api.SiteSegment,
	})
	if merr != nil {
		t.Fatalf("mount: %v", merr)
	}
	e.server = server
	return e
}

func (e *f8Env) advance(d time.Duration) {
	e.now = e.now.Add(d)
}

func f8do(t *testing.T, e *f8Env, method, target, authHeader, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
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

func f8decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := jsonv2.Unmarshal(rec.Body.Bytes(), &decoded, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("decode %d: %v\n%s", rec.Code, err, rec.Body.String())
	}
	return decoded
}

func f8problem(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d: %s", rec.Code, status, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), code) {
		t.Fatalf("body misses problem %q: %s", code, rec.Body.String())
	}
}

func f8bootstrap(t *testing.T, e *f8Env, name string) {
	t.Helper()
	if _, err := e.svc.BootstrapAdmin(t.Context(), name, "long-test-password-01", "127.0.0.1"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
}

func f8login(t *testing.T, e *f8Env, username string) string {
	t.Helper()
	rec := f8do(t, e, "POST", "/login", "", `{"username":"`+username+`","password":"long-test-password-01"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("login = %d: %s", rec.Code, rec.Body.String())
	}
	parts := strings.Split(rec.Header().Get("Set-Cookie"), ";")
	return strings.TrimSpace(parts[0])
}

func f8user(t *testing.T, e *f8Env, name string) auth.User {
	t.Helper()
	ada, err := e.svc.GetUser(t.Context(), "ada")
	if err != nil {
		t.Fatal(err)
	}
	user, err := e.svc.CreateUser(t.Context(), ada.ID, auth.CreateUserInput{
		Name: name, Email: name + "@example.test", Password: "long-test-password-01",
	}, "127.0.0.1")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

func f8token(t *testing.T, e *f8Env, user auth.User, scopes ...string) string {
	t.Helper()
	parsed, err := auth.ParseScopeSet(scopes)
	if err != nil {
		t.Fatalf("scopes: %v", err)
	}
	secret, err := e.svc.CreateToken(t.Context(), auth.CreateTokenInput{
		Name: "f8", Scopes: parsed, UserID: user.ID,
	})
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	return "Bearer " + secret.Secret
}

func f8seedRepo(t *testing.T, e *f8Env, ownerID int64, name string) int64 {
	t.Helper()
	var id int64
	err := e.st.Write(t.Context(), func(tx *store.WriteTx) error {
		var ierr error
		id, ierr = tx.InsertRepo(e.now, store.RepoInsert{
			OwnerUserID:   sql.NullInt64{Int64: ownerID, Valid: true},
			Name:          name,
			Visibility:    "private",
			DefaultBranch: "main",
			RukPath:       "seed/" + name + ".git",
		})
		return ierr
	})
	if err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	return id
}

func f8createEndpoint(t *testing.T, e *f8Env, token, target, body string) (int, map[string]any) {
	t.Helper()
	rec := f8do(t, e, "POST", target, token, body)
	return rec.Code, f8decode(t, rec)
}

type webhookHit struct {
	id        string
	timestamp string
	signature string
	body      []byte
}

func f8endpointID(t *testing.T, decoded map[string]any) string {
	t.Helper()
	endpoint, _ := decoded["endpoint"].(map[string]any)
	id, _ := endpoint["id"].(string)
	if id == "" {
		t.Fatalf("endpoint id missing: %v", decoded)
	}
	return id
}

func f8endpointSecret(t *testing.T, decoded map[string]any) string {
	t.Helper()
	endpoint, _ := decoded["endpoint"].(map[string]any)
	secret, _ := endpoint["secret"].(string)
	if secret == "" {
		t.Fatalf("endpoint secret missing: %v", decoded)
	}
	return secret
}

func f8makeOrg(t *testing.T, e *f8Env, org string, owner auth.User) int64 {
	t.Helper()
	var id int64
	err := e.st.Write(t.Context(), func(tx *store.WriteTx) error {
		created, cerr := tx.InsertOrg(e.now, store.OrgInsert{Name: org, Visibility: "private"})
		if cerr != nil {
			return cerr
		}
		if aerr := tx.AddOwnersMember(created.ID, owner.ID); aerr != nil {
			return aerr
		}
		id = created.ID
		return nil
	})
	if err != nil {
		t.Fatalf("seed org: %v", err)
	}
	return id
}

func TestWebhookEndpointCreateValidation(t *testing.T) {
	e := buildF8Env(t)
	f8bootstrap(t, e, "ada")
	ada, _ := e.svc.GetUser(t.Context(), "ada")
	token := f8token(t, e, ada, "repo:create", "repo:read", "repo:write", "webhooks:read", "webhooks:write")
	_ = f8seedRepo(t, e, ada.ID, "hooks")
	base := `{"scope":"repo","url":"https://hooks.example.test/x","event_types":["task.created"]}`
	for _, tc := range []struct {
		name string
		body string
	}{
		{"userInfo", `{"scope":"repo","url":"https://user@hooks.example.test/x","event_types":["task.created"]}`},
		{"emptyTypes", `{"scope":"repo","url":"https://hooks.example.test/x","event_types":[]}`},
		{"unknownType", `{"scope":"repo","url":"https://hooks.example.test/x","event_types":["nope"]}`},
		{"globsWithoutRef", `{"scope":"repo","url":"https://hooks.example.test/x","event_types":["task.created"],"ref_globs":["refs/heads/*"]}`},
		{"badScope", `{"scope":"galaxy","url":"https://hooks.example.test/x","event_types":["task.created"]}`},
		{"ownerMismatch", `{"scope":"owner","owner_name":"mallory","url":"https://hooks.example.test/x","event_types":["task.created"]}`},
		{"longNote", `{"scope":"repo","url":"https://hooks.example.test/x","event_types":["task.created"],"note":"` + strings.Repeat("n", 257) + `"}`},
	} {
		rec := f8do(t, e, "POST", "/ada/hooks/settings/hooks", token, tc.body)
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status = %d, want 400 or 422: %s", tc.name, rec.Code, rec.Body.String())
		}
	}
	tooMany := `{"scope":"repo","url":"https://hooks.example.test/x","event_types":["task.created"],"ref_globs":[` +
		strings.Repeat(`"refs/heads/*",`, 32) + `"refs/heads/*"]}`
	if rec := f8do(t, e, "POST", "/ada/hooks/settings/hooks", token, tooMany); rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("tooManyGlobs: status = %d, want 400 or 422: %s", rec.Code, rec.Body.String())
	}
	if code, decoded := f8createEndpoint(t, e, token, "/ada/hooks/settings/hooks", base); code != http.StatusCreated {
		t.Fatalf("valid create = %d: %v", code, decoded)
	}
	rec := f8do(t, e, "POST", "/ada/hooks/settings/hooks", token, `{"scope":"repo","url":"http://user@hooks.example.test/x","event_types":["task.created"]}`)
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("http userinfo: status = %d, want 400 or 422: %s", rec.Code, rec.Body.String())
	}
	// An instance without the insecure allowance refuses plain http.
	strict := buildF8EnvWithPolicy(t, webhook.Policy{})
	f8bootstrap(t, strict, "ada")
	sada, _ := strict.svc.GetUser(t.Context(), "ada")
	stoken := f8token(t, strict, sada, "repo:create", "repo:read", "repo:write", "webhooks:read", "webhooks:write")
	_ = f8seedRepo(t, strict, sada.ID, "hooks")
	srec := f8do(t, strict, "POST", "/ada/hooks/settings/hooks", stoken, `{"scope":"repo","url":"http://hooks.example.test/x","event_types":["task.created"]}`)
	if srec.Code != http.StatusBadRequest && srec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("http without allow: status = %d, want 400 or 422: %s", srec.Code, srec.Body.String())
	}
}

func TestWebhookEndpointSecretShownOnce(t *testing.T) {
	e := buildF8Env(t)
	f8bootstrap(t, e, "ada")
	ada, _ := e.svc.GetUser(t.Context(), "ada")
	token := f8token(t, e, ada, "repo:create", "repo:read", "repo:write", "webhooks:read", "webhooks:write")
	_ = f8seedRepo(t, e, ada.ID, "hooks")
	code, created := f8createEndpoint(t, e, token, "/ada/hooks/settings/hooks",
		`{"scope":"repo","url":"https://hooks.example.test/once","event_types":["task.created"]}`)
	if code != http.StatusCreated {
		t.Fatalf("create = %d: %v", code, created)
	}
	id := f8endpointID(t, created)
	secret := f8endpointSecret(t, created)
	if !strings.HasPrefix(secret, "whsec_") {
		t.Fatalf("secret %q misses the whsec_ prefix", secret)
	}
	// Fresh endpoints carry no previous secret.
	var fresh store.WebhookEndpointRow
	if err := e.st.Read(t.Context(), func(tx *store.ReadTx) error {
		var gerr error
		fresh, gerr = tx.GetWebhookEndpoint(id)
		return gerr
	}); err != nil {
		t.Fatalf("fresh endpoint: %v", err)
	}
	if len(fresh.PrevSecretEnc) != 0 || fresh.PrevSecretExpiresAt != 0 {
		t.Fatalf("fresh endpoint carries a previous secret: %+v", fresh)
	}
	rec := f8do(t, e, "GET", "/ada/hooks/settings/hooks/"+id, token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"secret"`) {
		t.Fatalf("second read leaks the secret: %s", rec.Body.String())
	}
	if got := f8decode(t, rec)["endpoint"].(map[string]any); got["id"] != id {
		t.Fatalf("get id = %v, want %s", got["id"], id)
	}
	listed := f8do(t, e, "GET", "/ada/hooks/settings/hooks", token, "")
	if listed.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", listed.Code, listed.Body.String())
	}
	if strings.Contains(listed.Body.String(), `"secret"`) {
		t.Fatalf("list leaks the secret: %s", listed.Body.String())
	}
}

func TestWebhookEndpointRotateSecret(t *testing.T) {
	e := buildF8Env(t)
	f8bootstrap(t, e, "ada")
	ada, _ := e.svc.GetUser(t.Context(), "ada")
	token := f8token(t, e, ada, "repo:create", "repo:read", "repo:write", "webhooks:read", "webhooks:write")
	_ = f8seedRepo(t, e, ada.ID, "hooks")
	_, created := f8createEndpoint(t, e, token, "/ada/hooks/settings/hooks",
		`{"scope":"repo","url":"https://hooks.example.test/rotate","event_types":["task.created"]}`)
	id, first := f8endpointID(t, created), f8endpointSecret(t, created)
	rec := f8do(t, e, "POST", "/ada/hooks/settings/hooks/"+id+"/rotate-secret", token, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("rotate = %d: %s", rec.Code, rec.Body.String())
	}
	rotated := f8decode(t, rec)
	second := f8endpointSecret(t, rotated)
	if second == first {
		t.Fatal("rotation returned the same secret")
	}
	again := f8do(t, e, "GET", "/ada/hooks/settings/hooks/"+id, token, "")
	if strings.Contains(again.Body.String(), `"secret"`) {
		t.Fatalf("read after rotate leaks the secret: %s", again.Body.String())
	}
}

func TestWebhookEndpointTestOp(t *testing.T) {
	e := buildF8Env(t)
	f8bootstrap(t, e, "ada")
	ada, _ := e.svc.GetUser(t.Context(), "ada")
	token := f8token(t, e, ada, "repo:create", "repo:read", "repo:write", "webhooks:read", "webhooks:write")
	_ = f8seedRepo(t, e, ada.ID, "hooks")
	var hits int
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("webhook-id") == "" || r.Header.Get("webhook-timestamp") == "" || r.Header.Get("webhook-signature") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer fake.Close()
	_, created := f8createEndpoint(t, e, token, "/ada/hooks/settings/hooks",
		`{"scope":"repo","url":"`+fake.URL+`","event_types":["task.created"]}`)
	id := f8endpointID(t, created)
	rec := f8do(t, e, "POST", "/ada/hooks/settings/hooks/"+id+"/test", token, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("test = %d: %s", rec.Code, rec.Body.String())
	}
	decoded := f8decode(t, rec)
	if decoded["ok"] != true {
		t.Fatalf("test ok = %v: %v", decoded["ok"], decoded)
	}
	if hits != 1 {
		t.Fatalf("fake hits = %d, want 1", hits)
	}
	var count int
	if err := e.st.Read(t.Context(), func(tx *store.ReadTx) error {
		rows, _, lerr := tx.ListWebhookMessages(id, "", 10)
		if lerr != nil {
			return lerr
		}
		count = len(rows)
		return nil
	}); err != nil {
		t.Fatalf("messages: %v", err)
	}
	if count != 0 {
		t.Fatalf("test op stored %d messages, want none", count)
	}
	// An unreachable target reports failure without a status code.
	_, created = f8createEndpoint(t, e, token, "/ada/hooks/settings/hooks",
		`{"scope":"repo","url":"http://127.0.0.1:1/none","event_types":["task.created"]}`)
	dead := f8endpointID(t, created)
	rec = f8do(t, e, "POST", "/ada/hooks/settings/hooks/"+dead+"/test", token, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("test unreachable = %d: %s", rec.Code, rec.Body.String())
	}
	unreachable := f8decode(t, rec)
	if unreachable["ok"] == true || unreachable["error"] == "" {
		t.Fatalf("unreachable test must fail with an error: %v", unreachable)
	}
}
func TestWebhookEndpointTestRejectsInsecureHTTP(t *testing.T) {
	strict := buildF8EnvWithPolicy(t, webhook.Policy{})
	f8bootstrap(t, strict, "ada")
	sada, _ := strict.svc.GetUser(t.Context(), "ada")
	stoken := f8token(t, strict, sada, "repo:create", "repo:read", "repo:write", "webhooks:read", "webhooks:write")
	repoID := f8seedRepo(t, strict, sada.ID, "hooks")
	var id string
	if err := strict.st.Write(t.Context(), func(tx *store.WriteTx) error {
		row, ierr := tx.InsertWebhookEndpoint(strict.now, store.WebhookEndpointInsert{
			Scope: "repo", RepoID: repoID, URL: "http://hooks.example.test/ping",
			SecretEnc: []byte("seed"), EventTypes: []string{"task.created"},
			Active: true, Concurrency: 4, TimeoutSeconds: 20, CreatedBy: sada.ID,
		})
		if ierr != nil {
			return ierr
		}
		id = row.ID
		return nil
	}); err != nil {
		t.Fatalf("seed endpoint: %v", err)
	}
	f8problem(t, f8do(t, strict, "POST", "/ada/hooks/settings/hooks/"+id+"/test", stoken, ""), http.StatusBadRequest, "invalid")
}

func TestWebhookEndpointSettingsShowsVerifyRecipe(t *testing.T) {
	e := buildF8Env(t)
	f8bootstrap(t, e, "ada")
	ada, _ := e.svc.GetUser(t.Context(), "ada")
	token := f8token(t, e, ada, "repo:create", "repo:read", "repo:write", "webhooks:read", "webhooks:write")
	_ = f8seedRepo(t, e, ada.ID, "hooks")
	code, created := f8createEndpoint(t, e, token, "/ada/hooks/settings/hooks",
		`{"scope":"repo","url":"https://hooks.example.test/verify","event_types":["task.created"]}`)
	if code != http.StatusCreated {
		t.Fatalf("create = %d: %v", code, created)
	}
	id := f8endpointID(t, created)
	req := httptest.NewRequest(http.MethodGet, "/ada/hooks/settings/hooks/"+id, nil)
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Authorization", token)
	rec := httptest.NewRecorder()
	e.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("settings page = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, marker := range []string{"v1,", "HMAC-SHA256", "300", "webhook-signature", "webhook-id"} {
		if !strings.Contains(body, marker) {
			t.Fatalf("settings page misses %q", marker)
		}
	}
	if err := zerojs.Assert(rec.Body.Bytes()); err != nil {
		t.Fatalf("zero-js: %v", err)
	}
}

func TestWebhookMessageRedeliver(t *testing.T) {
	e := buildF8Env(t)
	f8bootstrap(t, e, "ada")
	ada, _ := e.svc.GetUser(t.Context(), "ada")
	token := f8token(t, e, ada, "repo:create", "repo:read", "repo:write", "webhooks:read", "webhooks:write")
	_ = f8seedRepo(t, e, ada.ID, "hooks")
	_, created := f8createEndpoint(t, e, token, "/ada/hooks/settings/hooks",
		`{"scope":"repo","url":"https://hooks.example.test/redeliver","event_types":["task.created"]}`)
	id := f8endpointID(t, created)
	// Seed one pending message straight through the store contract.
	seededID := "msg_" + string(store.NewID(e.now))
	if err := e.st.Write(t.Context(), func(tx *store.WriteTx) error {
		return tx.InsertWebhookMessages([]store.WebhookMessageInsert{{
			ID: seededID, EndpointID: id, EventType: "task.created",
			EventTime: e.now.UnixMilli(), NextAttemptAt: e.now.UnixMilli(),
			Payload: []byte(`{"type":"task.created"}`),
		}})
	}); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	// Pending messages refuse redelivery.
	f8problem(t, f8do(t, e, "POST", "/ada/hooks/settings/hooks/"+id+"/messages/"+seededID+"/redeliver", token, ""), http.StatusBadRequest, "invalid")
	// Dead messages return to pending under the same id. Production kills
	// claimed rows, so claim (as the worker would) before marking dead.
	if err := e.st.Write(t.Context(), func(tx *store.WriteTx) error {
		claimed, cerr := tx.ClaimWebhookMessages(e.now.UnixMilli(), 64, 0, "f8test-owner")
		if cerr != nil {
			return cerr
		}
		found := false
		for _, m := range claimed {
			if m.ID == seededID {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("seeded message was not claimed")
		}
		return tx.MarkWebhookDead(seededID, "gone", e.now.UnixMilli(), 410, "f8test-owner")
	}); err != nil {
		t.Fatalf("mark dead: %v", err)
	}
	rec := f8do(t, e, "POST", "/ada/hooks/settings/hooks/"+id+"/messages/"+seededID+"/redeliver", token, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("redeliver = %d: %s", rec.Code, rec.Body.String())
	}
	got := f8decode(t, rec)["message"].(map[string]any)
	if got["id"] != seededID || got["state"] != "pending" {
		t.Fatalf("redelivered = %v, want pending %s", got, seededID)
	}
}

func TestWebhookAttemptRoundTrip(t *testing.T) {
	e := buildF8Env(t)
	f8bootstrap(t, e, "ada")
	ada, _ := e.svc.GetUser(t.Context(), "ada")
	token := f8token(t, e, ada, "repo:create", "repo:read", "repo:write", "webhooks:read", "webhooks:write")
	_ = f8seedRepo(t, e, ada.ID, "hooks")
	_, created := f8createEndpoint(t, e, token, "/ada/hooks/settings/hooks",
		`{"scope":"repo","url":"https://hooks.example.test/attempts","event_types":["task.created"]}`)
	id := f8endpointID(t, created)
	seededID := "msg_" + string(store.NewID(e.now))
	if err := e.st.Write(t.Context(), func(tx *store.WriteTx) error {
		if ierr := tx.InsertWebhookMessages([]store.WebhookMessageInsert{{
			ID: seededID, EndpointID: id, EventType: "task.created",
			EventTime: e.now.UnixMilli(), NextAttemptAt: e.now.UnixMilli(),
			Payload: []byte(`{"type":"task.created"}`),
		}}); ierr != nil {
			return ierr
		}
		return tx.RecordWebhookAttempt(store.WebhookAttemptInsert{
			MessageID: seededID, AttemptNo: 1,
			StartedAt: e.now.UnixMilli(), FinishedAt: e.now.UnixMilli() + 12,
			AttemptTimestamp: e.now.Unix(),
			HTTPStatus:       500, Error: "boom",
			ReqHeaders: []byte(`{"webhook-id":"` + seededID + `","webhook-signature":"v1,<omitted>"}`),
			RespBody:   []byte("nope"),
			DurationMs: 12,
		})
	}); err != nil {
		t.Fatalf("seed attempt: %v", err)
	}
	rec := f8do(t, e, "GET", "/ada/hooks/settings/hooks/"+id+"/messages/"+seededID+"/attempts", token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("attempts = %d: %s", rec.Code, rec.Body.String())
	}
	decoded := f8decode(t, rec)
	attempts, _ := decoded["attempts"].([]any)
	if len(attempts) != 1 {
		t.Fatalf("attempts = %v, want one", decoded)
	}
	first, _ := attempts[0].(map[string]any)
	if first["attempt_no"] != float64(1) || first["http_status"] != float64(500) {
		t.Fatalf("attempt = %v", first)
	}
	if first["attempt_timestamp"] != float64(e.now.Unix()) {
		t.Fatalf("attempt_timestamp = %v, want %d", first["attempt_timestamp"], e.now.Unix())
	}
	headers, _ := first["request_headers"].(map[string]any)
	if headers["webhook-signature"] != "v1,<omitted>" {
		t.Fatalf("request headers = %v, want redacted signature", first["request_headers"])
	}
}

func TestWebhookDeliveryFlow(t *testing.T) {
	e := buildF8Env(t)
	f8bootstrap(t, e, "ada")
	ada, _ := e.svc.GetUser(t.Context(), "ada")
	token := f8token(t, e, ada, "repo:create", "repo:read", "repo:write", "webhooks:read", "webhooks:write")
	repoID := f8seedRepo(t, e, ada.ID, "hooks")
	var mu sync.Mutex
	var hits []webhookHit
	failOpen := true
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		hits = append(hits, webhookHit{
			id:        r.Header.Get("webhook-id"),
			timestamp: r.Header.Get("webhook-timestamp"),
			signature: r.Header.Get("webhook-signature"),
			body:      body,
		})
		first := failOpen
		failOpen = false
		mu.Unlock()
		if first {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer fake.Close()
	_, created := f8createEndpoint(t, e, token, "/ada/hooks/settings/hooks",
		`{"scope":"repo","url":"`+fake.URL+`","event_types":["task.created"]}`)
	id := f8endpointID(t, created)
	secret := f8endpointSecret(t, created)
	rawSecret, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil {
		t.Fatalf("secret decode: %v", err)
	}
	f8seedIntent(t, e, repoID, ada.ID, "Deliver me")
	worker := webhook.NewWorker(webhook.WorkerConfig{
		Store: e.st, SealKey: e.deps.SealKey,
		Workers: 1, GlobalMaxInflight: 4,
		PollInterval: time.Millisecond, LeaseDuration: time.Minute,
		Now: func() time.Time { return e.now },
	}, &webhook.Policy{AllowInsecureHTTP: true, AllowPrivateTargets: true})
	if derr := worker.DriveOnce(t.Context()); derr != nil {
		t.Fatalf("drive 1: %v", derr)
	}
	e.advance(6 * time.Second)
	if derr := worker.DriveOnce(t.Context()); derr != nil {
		t.Fatalf("drive 2: %v", derr)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(hits) != 2 {
		t.Fatalf("fake hits = %d, want 2", len(hits))
	}
	if hits[0].id == "" || hits[0].id != hits[1].id {
		t.Fatalf("webhook ids differ: %q vs %q", hits[0].id, hits[1].id)
	}
	for i, hit := range hits {
		ts, terr := strconv.ParseInt(hit.timestamp, 10, 64)
		if terr != nil {
			t.Fatalf("hit %d timestamp: %v", i, terr)
		}
		if !webhook.Verify([][]byte{rawSecret}, hit.id, ts, hit.body, hit.signature, e.now) {
			t.Fatalf("hit %d signature does not verify", i)
		}
	}
	var state string
	if rerr := e.st.Read(t.Context(), func(tx *store.ReadTx) error {
		rows, _, lerr := tx.ListWebhookMessages(id, "", 10)
		if lerr != nil {
			return lerr
		}
		for _, row := range rows {
			if row.ID == hits[0].id {
				state = row.State
			}
		}
		return nil
	}); rerr != nil {
		t.Fatalf("message state: %v", rerr)
	}
	if state != "delivered" {
		t.Fatalf("message state = %q, want delivered", state)
	}
	// The reporter gets an author thread for the task.
	ids := f8threadIDs(t, e, f8login(t, e, "ada"), "?state=unread")
	if len(ids) != 1 {
		t.Fatalf("unread threads = %d, want 1", len(ids))
	}
}

func TestWebhookRepoEndpointCap(t *testing.T) {
	e := buildF8Env(t)
	f8bootstrap(t, e, "ada")
	ada, _ := e.svc.GetUser(t.Context(), "ada")
	token := f8token(t, e, ada, "repo:create", "repo:read", "repo:write", "webhooks:read", "webhooks:write")
	repoID := f8seedRepo(t, e, ada.ID, "hooks")
	// Seed 31 endpoints straight through the store contract; the API proves
	// the 32nd lands and the 33rd trips the limit.
	if err := e.st.Write(t.Context(), func(tx *store.WriteTx) error {
		for i := range 31 {
			if _, ierr := tx.InsertWebhookEndpoint(e.now, store.WebhookEndpointInsert{
				Scope: "repo", RepoID: repoID, URL: "https://hooks.example.test/seed-" + strconv.Itoa(i),
				SecretEnc: []byte("seed"), EventTypes: []string{"task.created"},
				Active: true, Concurrency: 4, TimeoutSeconds: 20, CreatedBy: ada.ID,
			}); ierr != nil {
				return ierr
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed endpoints: %v", err)
	}
	if code, decoded := f8createEndpoint(t, e, token, "/ada/hooks/settings/hooks",
		`{"scope":"repo","url":"https://hooks.example.test/cap-32","event_types":["task.created"]}`); code != http.StatusCreated {
		t.Fatalf("create 32 = %d: %v", code, decoded)
	}
	rec := f8do(t, e, "POST", "/ada/hooks/settings/hooks", token,
		`{"scope":"repo","url":"https://hooks.example.test/cap-over","event_types":["task.created"]}`)
	f8problem(t, rec, http.StatusUnprocessableEntity, "limit-exceeded")
}

func TestWebhookOwnerScopeEndpoint(t *testing.T) {
	e := buildF8Env(t)
	f8bootstrap(t, e, "ada")
	ada, _ := e.svc.GetUser(t.Context(), "ada")
	bob := f8user(t, e, "bob")
	atoken := f8token(t, e, ada, "repo:create", "repo:read", "repo:write", "webhooks:read", "webhooks:write")
	btoken := f8token(t, e, bob, "repo:read", "repo:write", "webhooks:read", "webhooks:write")
	_ = f8makeOrg(t, e, "acme", ada)
	// Non-members cannot manage owner endpoints.
	rec := f8do(t, e, "POST", "/acme/whatever/settings/hooks", btoken,
		`{"scope":"owner","url":"https://hooks.example.test/acme","event_types":["repo.created"]}`)
	f8problem(t, rec, http.StatusForbidden, "forbidden")
	// The path repo is ignored for owner scope; the owner comes from the path.
	code, created := f8createEndpoint(t, e, atoken, "/acme/whatever/settings/hooks",
		`{"scope":"owner","owner_name":"acme","url":"https://hooks.example.test/acme","event_types":["repo.created"]}`)
	if code != http.StatusCreated {
		t.Fatalf("owner create = %d: %v", code, created)
	}
	id := f8endpointID(t, created)
	if got := created["endpoint"].(map[string]any); got["owner"] != "acme" || got["scope"] != "owner" {
		t.Fatalf("owner endpoint = %v", got)
	}
	// Absent owner_name is equally valid for owner scope.
	if code, _ := f8createEndpoint(t, e, atoken, "/acme/whatever/settings/hooks",
		`{"scope":"owner","url":"https://hooks.example.test/acme2","event_types":["repo.created"]}`); code != http.StatusCreated {
		t.Fatalf("owner create without name = %d", code)
	}
	// The dash placeholder routes the same op for owner scope: the owner page
	// posts here, and the path repo is ignored.
	if code, dash := f8createEndpoint(t, e, atoken, "/acme/-/settings/hooks",
		`{"scope":"owner","url":"https://hooks.example.test/acme3","event_types":["repo.created"]}`); code != http.StatusCreated {
		t.Fatalf("owner create via dash = %d: %v", code, dash)
	}
	// The endpoint loads by id under a disagreeing path repo for its owner admin.
	if rec := f8do(t, e, "GET", "/acme/other/settings/hooks/"+id, atoken, ""); rec.Code != http.StatusOK {
		t.Fatalf("owner get = %d: %s", rec.Code, rec.Body.String())
	}
	// And stays invisible to outsiders under any path.
	f8problem(t, f8do(t, e, "GET", "/acme/other/settings/hooks/"+id, btoken, ""), http.StatusForbidden, "forbidden")
}
