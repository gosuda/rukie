package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/api"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/web/zerojs"
)

func assertCINoJS(t *testing.T, body string) {
	t.Helper()
	if err := zerojs.Assert([]byte(body)); err != nil {
		t.Fatalf("zero-js: %v", err)
	}
}

const ciTestImage = "docker.io/library/golang@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

const ciTestSubject = "0123456789abcdef0123456789abcdef01234567"

func ciTestSpec() string {
	return `schema = "rukie.ci/v1"

[[job]]
name = "solo"
runs_on = ["linux-amd64"]
image = "` + ciTestImage + `"
pure = true
steps = [{ name = "s", run = "echo hi" }]
`
}

func rciCreate(t *testing.T, e *repoEnv, token string) map[string]any {
	t.Helper()
	escaped, _ := jsonv2.Marshal(map[string]string{
		"subject": ciTestSubject, "tree": "tree-one", "spec": ciTestSpec(),
	})
	rec := rdo(t, e, "POST", "/ada/hello/ci/runs", token, string(escaped))
	if rec.Code != http.StatusCreated {
		t.Fatalf("ci.run.create = %d: %s", rec.Code, rec.Body.String())
	}
	var decoded map[string]any
	if err := jsonv2.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	run, ok := decoded["run"].(map[string]any)
	if !ok || run["id"] == nil {
		t.Fatalf("run lacks an id: %v", decoded)
	}
	return run
}

func TestCIOpsRegistered(t *testing.T) {
	table := op.NewTable()
	deps := api.Deps{}
	if err := api.RegisterCI(table, deps); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := table.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	want := map[string]op.Scope{
		"ci.run.list": op.ScopeCIRead, "ci.run.get": op.ScopeCIRead,
		"ci.run.create": op.ScopeCITrigger, "ci.run.update": op.ScopeCITrigger,
		"ci.run.retry": op.ScopeCITrigger, "ci.job.list": op.ScopeCIRead,
		"ci.log.get": op.ScopeCIRead, "ci.artifact.list": op.ScopeCIRead,
		"ci.artifact.get": op.ScopeCIRead, "ci.spec.verify": op.ScopeCITrigger,
		"ci.spec.get": op.ScopeCIRead, "ci.runner.create": op.ScopeAdminWrite,
		"ci.runner.list": op.ScopeAdminWrite, "ci.runner.revoke": op.ScopeAdminWrite,
	}
	seen := map[string]bool{}
	for _, spec := range table.Specs() {
		scope, ok := want[spec.Name]
		if !ok {
			continue
		}
		seen[spec.Name] = true
		if spec.Scope != scope {
			t.Errorf("op %s scope = %v, want %v", spec.Name, spec.Scope, scope)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("op %s is not registered", name)
		}
	}
}

func TestCIRunCreateAndGet(t *testing.T) {
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	admin, _ := e.svc.GetUser(t.Context(), "ada")
	rseedRepo(t, e, admin.ID, "hello", "private")
	token := rtoken(t, e, admin, "repo:read", "repo:write", "ci:read", "ci:trigger")
	run := rciCreate(t, e, token)
	runID, _ := run["id"].(string)
	got := rdo(t, e, "GET", "/ada/hello/ci/runs/"+runID, token, "")
	if got.Code != http.StatusOK {
		t.Fatalf("ci.run.get = %d: %s", got.Code, got.Body.String())
	}
	if !strings.Contains(got.Body.String(), runID) {
		t.Fatalf("run get misses the id: %s", got.Body.String())
	}
	listed := rdo(t, e, "GET", "/ada/hello/ci/runs", token, "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), runID) {
		t.Fatalf("ci.run.list = %d: %s", listed.Code, listed.Body.String())
	}
	jobs := rdo(t, e, "GET", "/ada/hello/ci/jobs?run="+runID, token, "")
	if jobs.Code != http.StatusOK || !strings.Contains(jobs.Body.String(), "solo") {
		t.Fatalf("ci.job.list = %d: %s", jobs.Code, jobs.Body.String())
	}
	rassertProblem(t, rdo(t, e, "GET", "/ada/hello/ci/runs/nope", token, ""), 404, "not-found")
}

func TestCIRunCreateValidation(t *testing.T) {
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	admin, _ := e.svc.GetUser(t.Context(), "ada")
	rseedRepo(t, e, admin.ID, "hello", "private")
	token := rtoken(t, e, admin, "repo:read", "repo:write", "ci:read", "ci:trigger")
	rassertProblem(t, rdo(t, e, "POST", "/ada/hello/ci/runs", token, `{"subject":"`+ciTestSubject+`"}`), 503, "unavailable")
	rassertProblem(t, rdo(t, e, "POST", "/ada/hello/ci/runs", token, `{"subject":"`+ciTestSubject+`","tree":"t","spec":"bogus = ["}`), 422, "validation")
	rassertProblem(t, rdo(t, e, "POST", "/ada/hello/ci/runs", token, `{"tree":"t","spec":"schema = \"rukie.ci/v1\"\n"}`), 422, "validation")
	readToken := rtoken(t, e, admin, "repo:read", "ci:read")
	rassertProblem(t, rdo(t, e, "POST", "/ada/hello/ci/runs", readToken, `{"subject":"x"}`), 403, "forbidden")
}

func TestCIRunCancelNoop(t *testing.T) {
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	admin, _ := e.svc.GetUser(t.Context(), "ada")
	rseedRepo(t, e, admin.ID, "hello", "private")
	token := rtoken(t, e, admin, "repo:read", "repo:write", "ci:read", "ci:trigger")
	run := rciCreate(t, e, token)
	runID, _ := run["id"].(string)
	rassertProblem(t, rdo(t, e, "POST", "/ada/hello/ci/runs/"+runID+"/retry", token, `{}`), 409, "conflict")
	cancelled := rdo(t, e, "POST", "/ada/hello/ci/runs/"+runID+"/cancel", token, `{"state":"cancelled"}`)
	if cancelled.Code != http.StatusCreated || !strings.Contains(cancelled.Body.String(), `"cancelled"`) {
		t.Fatalf("cancel = %d: %s", cancelled.Code, cancelled.Body.String())
	}
	again := rdo(t, e, "POST", "/ada/hello/ci/runs/"+runID+"/cancel", token, `{"state":"cancelled"}`)
	if again.Code != http.StatusCreated || !strings.Contains(again.Body.String(), `"noop":true`) {
		t.Fatalf("second cancel = %d: %s, want noop", again.Code, again.Body.String())
	}
	rassertProblem(t, rdo(t, e, "POST", "/ada/hello/ci/runs/"+runID+"/cancel", token, `{"state":"nope"}`), 422, "validation")
}

func TestCISpecVerify(t *testing.T) {
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	admin, _ := e.svc.GetUser(t.Context(), "ada")
	rseedRepo(t, e, admin.ID, "hello", "private")
	token := rtoken(t, e, admin, "repo:read", "repo:write", "ci:read", "ci:trigger")
	escaped, _ := jsonv2.Marshal(map[string]string{"spec": ciTestSpec()})
	valid := rdo(t, e, "POST", "/ada/hello/ci/spec/validate", token, string(escaped))
	if valid.Code != http.StatusCreated || !strings.Contains(valid.Body.String(), `"valid":true`) {
		t.Fatalf("spec.verify = %d: %s", valid.Code, valid.Body.String())
	}
	if !strings.Contains(valid.Body.String(), "solo") {
		t.Fatalf("spec.verify misses the job digest: %s", valid.Body.String())
	}
	bad, _ := jsonv2.Marshal(map[string]string{"spec": "schema = \"rukie.ci/v1\"\n"})
	invalid := rdo(t, e, "POST", "/ada/hello/ci/spec/validate", token, string(bad))
	if invalid.Code != http.StatusCreated || !strings.Contains(invalid.Body.String(), `"valid":false`) {
		t.Fatalf("bad spec.verify = %d: %s", invalid.Code, invalid.Body.String())
	}
	rassertProblem(t, rdo(t, e, "POST", "/ada/hello/ci/spec/validate", token, `{}`), 503, "unavailable")
}

func TestCIRunnerAdminOnly(t *testing.T) {
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	admin, _ := e.svc.GetUser(t.Context(), "ada")
	rseedRepo(t, e, admin.ID, "hello", "private")
	mallory := ruser(t, e, admin.ID, "mallory")
	malloryToken := rtoken(t, e, mallory, "repo:read", "repo:write", "ci:read", "ci:trigger")
	rassertProblem(t, rdo(t, e, "POST", "/-/ci/v1/runners", malloryToken, `{"labels":["linux-amd64"],"scope":"trusted","capacity":1}`), 403, "forbidden")
	adminToken := rtoken(t, e, admin, "repo:read", "repo:write", "admin:write")
	created := rdo(t, e, "POST", "/-/ci/v1/runners", adminToken, `{"labels":["linux-amd64"],"scope":"trusted","capacity":1}`)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"token"`) {
		t.Fatalf("runner.create = %d: %s", created.Code, created.Body.String())
	}
	var decoded struct {
		Runner struct {
			ID string `json:"id"`
		} `json:"runner"`
	}
	if err := jsonv2.Unmarshal(created.Body.Bytes(), &decoded); err != nil || decoded.Runner.ID == "" {
		t.Fatalf("runner create lacks an id: %v", created.Body.String())
	}
	listed := rdo(t, e, "GET", "/-/ci/v1/runners", adminToken, "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), decoded.Runner.ID) {
		t.Fatalf("runner.list = %d: %s", listed.Code, listed.Body.String())
	}
	revoked := rdo(t, e, "POST", "/-/ci/v1/runners/"+decoded.Runner.ID+"/revoke", adminToken, `{}`)
	if revoked.Code != http.StatusCreated || !strings.Contains(revoked.Body.String(), "revoked") {
		t.Fatalf("runner.revoke = %d: %s", revoked.Code, revoked.Body.String())
	}
	rassertProblem(t, rdo(t, e, "POST", "/-/ci/v1/runners/nope/revoke", adminToken, `{}`), 404, "not-found")
}

func TestCIPagesRender(t *testing.T) {
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	admin, _ := e.svc.GetUser(t.Context(), "ada")
	rseedRepo(t, e, admin.ID, "hello", "private")
	token := rtoken(t, e, admin, "repo:read", "repo:write", "ci:read", "ci:trigger")
	run := rciCreate(t, e, token)
	runID, _ := run["id"].(string)
	handler := e.deps.CIRaw(e.server)
	fetch := func(target, auth, accept string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("Accept", accept)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	tab := fetch("/ada/hello/ci", token, "text/html")
	if tab.Code != http.StatusOK || !strings.Contains(tab.Body.String(), runID) {
		t.Fatalf("CI tab = %d: %s", tab.Code, tab.Body.String())
	}
	assertCINoJS(t, tab.Body.String())
	detail := fetch("/ada/hello/ci/runs/"+runID, token, "text/html")
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), "solo") {
		t.Fatalf("run page = %d: %s", detail.Code, detail.Body.String())
	}
	assertCINoJS(t, detail.Body.String())
	badge := fetch("/ada/hello/ci/badge.svg", token, "image/svg+xml")
	if badge.Code != http.StatusOK || !strings.Contains(badge.Body.String(), "<svg") {
		t.Fatalf("badge = %d: %s", badge.Code, badge.Body.String())
	}
	logPlain := fetch("/ada/hello/ci/runs/"+runID+"/jobs/nope/log", token, "text/plain")
	if logPlain.Code != http.StatusNotFound {
		t.Fatalf("unknown job log = %d, want 404", logPlain.Code)
	}
}

func TestCIPagesSendNoScriptCSP(t *testing.T) {
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	admin, _ := e.svc.GetUser(t.Context(), "ada")
	rseedRepo(t, e, admin.ID, "hello", "private")
	token := rtoken(t, e, admin, "repo:read", "repo:write", "ci:read", "ci:trigger")
	run := rciCreate(t, e, token)
	runID, _ := run["id"].(string)
	handler := e.deps.CIRaw(e.server)
	fetch := func(target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("Accept", "text/html")
		req.Header.Set("Authorization", token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	for _, target := range []string{"/ada/hello/ci", "/ada/hello/ci/runs/" + runID} {
		rec := fetch(target)
		if rec.Code != http.StatusOK {
			t.Fatalf("CI page %s = %d: %s", target, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "script-src 'none'") {
			t.Fatalf("CI page %s CSP = %q, want script-src 'none'", target, got)
		}
	}
}

func TestCIArtifactUnknownDigest404(t *testing.T) {
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	admin, _ := e.svc.GetUser(t.Context(), "ada")
	rseedRepo(t, e, admin.ID, "hello", "private")
	token := rtoken(t, e, admin, "repo:read", "repo:write", "ci:read", "ci:trigger")
	handler := e.deps.CIRaw(e.server)
	fetch := func(accept string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/ada/hello/ci/blobs/sha256:"+strings.Repeat("0", 64), nil)
		req.Header.Set("Accept", accept)
		req.Header.Set("Authorization", token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := fetch("application/json"); rec.Code != http.StatusNotFound {
		t.Fatalf("artifact json = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := fetch("application/octet-stream"); rec.Code != http.StatusNotFound {
		t.Fatalf("artifact bytes = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCIRunGetJSONThroughFront(t *testing.T) {
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	admin, _ := e.svc.GetUser(t.Context(), "ada")
	rseedRepo(t, e, admin.ID, "hello", "private")
	token := rtoken(t, e, admin, "repo:read", "repo:write", "ci:read", "ci:trigger")
	run := rciCreate(t, e, token)
	runID, _ := run["id"].(string)
	handler := e.deps.CIRaw(e.server)
	req := httptest.NewRequest(http.MethodGet, "/ada/hello/ci/runs/"+runID, nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("run json = %d: %s", rec.Code, rec.Body.String())
	}
	if ctype := rec.Header().Get("Content-Type"); !strings.Contains(ctype, "application/json") {
		t.Fatalf("run content type = %q, want JSON", ctype)
	}
	var decoded map[string]any
	if err := jsonv2.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("run body is not JSON: %v", err)
	}
	got, _ := decoded["run"].(map[string]any)
	if got["id"] != runID {
		t.Fatalf("run id = %v, want %s", decoded, runID)
	}
}
