package api_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/web/zerojs"
)

var f6IDRe = regexp.MustCompile(`(rnr_|att_|job_|run_)[0-9a-f]{32}`)
var f6TokenRe = regexp.MustCompile(`"token":"[0-9a-f]{32}"`)
var f6MillisRe = regexp.MustCompile(`\b1[0-9]{12}\b`)

func f6Normalize(body string) string {
	body = f6IDRe.ReplaceAllString(body, "${1}<id>")
	body = f6TokenRe.ReplaceAllString(body, `"token":"<token>"`)
	return f6MillisRe.ReplaceAllString(body, "1")
}

func f6DigestHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type f6Req struct {
	headers map[string]string
	method  string
	target  string
	auth    string
	body    string
	raw     []byte
}

func f6Handler(e *repoEnv) http.Handler {
	return e.deps.CIRunnerV1(e.deps.CIRaw(e.server))
}

func f6JSON(method, target, auth, body string) f6Req {
	return f6Req{method: method, target: target, auth: auth, body: body}
}

func f6do(t *testing.T, h http.Handler, req f6Req) *httptest.ResponseRecorder {
	t.Helper()
	payload := req.raw
	if payload == nil {
		payload = []byte(req.body)
	}
	request := httptest.NewRequest(req.method, req.target, strings.NewReader(string(payload)))
	for key, value := range req.headers {
		request.Header.Set(key, value)
	}
	if _, ok := req.headers["Content-Type"]; !ok && len(payload) > 0 && req.raw == nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if req.auth != "" {
		request.Header.Set("Authorization", req.auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, request)
	return rec
}

func f6CreateRunner(t *testing.T, e *repoEnv, adminToken string) (string, string) {
	t.Helper()
	rec := rdo(t, e, "POST", "/-/ci/v1/runners", adminToken, `{"labels":["linux-amd64"],"scope":"untrusted","capacity":2}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("runner.create = %d: %s", rec.Code, rec.Body.String())
	}
	var decoded struct {
		Token  string `json:"token"`
		Runner struct {
			ID string `json:"id"`
		} `json:"runner"`
	}
	if err := jsonv2.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("runner body: %v", err)
	}
	if decoded.Token == "" || decoded.Runner.ID == "" {
		t.Fatalf("runner create lacks token or id: %s", rec.Body.String())
	}
	return decoded.Runner.ID, "Bearer " + decoded.Token
}

func f6CreateRun(t *testing.T, e *repoEnv, token, subject string) string {
	t.Helper()
	escaped, _ := jsonv2.Marshal(map[string]string{
		"subject": subject, "tree": "tree-one", "spec": ciTestSpec(),
	})
	rec := rdo(t, e, "POST", "/ada/hello/ci/runs", token, string(escaped))
	if rec.Code != http.StatusCreated {
		t.Fatalf("ci.run.create = %d: %s", rec.Code, rec.Body.String())
	}
	var decoded map[string]any
	if err := jsonv2.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("run decode: %v", err)
	}
	run, _ := decoded["run"].(map[string]any)
	id, _ := run["id"].(string)
	if id == "" {
		t.Fatalf("run lacks an id: %v", decoded)
	}
	return id
}

func f6Setup(t *testing.T, subject string) (*repoEnv, http.Handler, string, string) {
	t.Helper()
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	admin, _ := e.svc.GetUser(t.Context(), "ada")
	rseedRepo(t, e, admin.ID, "hello", "private")
	trigger := rtoken(t, e, admin, "repo:read", "repo:write", "ci:read", "ci:trigger")
	adminToken := rtoken(t, e, admin, "repo:read", "repo:write", "admin:write")
	runnerID, runnerToken := f6CreateRunner(t, e, adminToken)
	f6CreateRun(t, e, trigger, subject)
	return e, f6Handler(e), runnerID, runnerToken
}

func f6Lease(t *testing.T, h http.Handler, runnerID, runnerToken string) map[string]any {
	t.Helper()
	rec := f6do(t, h, f6JSON("POST", "/-/ci/v1/lease", runnerToken, `{"runner_id":"`+runnerID+`","wait_ms":1000}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("lease = %d: %s", rec.Code, rec.Body.String())
	}
	var grant map[string]any
	if err := jsonv2.Unmarshal(rec.Body.Bytes(), &grant); err != nil {
		t.Fatalf("grant decode: %v", err)
	}
	return grant
}

func f6Attempt(grant map[string]any) (string, string, int64) {
	attempt, _ := grant["attempt"].(map[string]any)
	id, _ := attempt["id"].(string)
	token, _ := attempt["token"].(string)
	fence, _ := attempt["fence"].(float64)
	return id, "Bearer " + token, int64(fence)
}

func f6Fence(fence int64) string {
	return `{"fence":` + strconv.FormatInt(fence, 10) + `}`
}

func f6Golden(t *testing.T, name, body string) {
	t.Helper()
	path := filepath.Join("testdata", "f6", name+".golden")
	got := f6Normalize(body)
	if os.Getenv("F6_UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden %s missing: %v (run with F6_UPDATE_GOLDEN=1)", path, err)
	}
	var gotJSON, wantJSON any
	if err := jsonv2.Unmarshal([]byte(got), &gotJSON); err != nil {
		t.Fatalf("golden %s got is not JSON: %v\n%s", name, err, got)
	}
	if err := jsonv2.Unmarshal(want, &wantJSON); err != nil {
		t.Fatalf("golden %s want is not JSON: %v", name, err)
	}
	if !jsonEqual(gotJSON, wantJSON) {
		t.Fatalf("golden %s drifted:\n%s", name, got)
	}
}

func jsonEqual(a, b any) bool {
	switch left := a.(type) {
	case map[string]any:
		right, ok := b.(map[string]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for key, value := range left {
			other, ok := right[key]
			if !ok || !jsonEqual(value, other) {
				return false
			}
		}
		return true
	case []any:
		right, ok := b.([]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for i := range left {
			if !jsonEqual(left[i], right[i]) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}

func TestRunnerProtocolGolden(t *testing.T) {
	_, h, runnerID, runnerToken := f6Setup(t, ciTestSubject)
	leaseRec := f6do(t, h, f6JSON("POST", "/-/ci/v1/lease", runnerToken, `{"runner_id":"`+runnerID+`","wait_ms":1000}`))
	if leaseRec.Code != http.StatusOK {
		t.Fatalf("lease = %d: %s", leaseRec.Code, leaseRec.Body.String())
	}
	f6Golden(t, "lease", leaseRec.Body.String())
	var grant map[string]any
	if err := jsonv2.Unmarshal(leaseRec.Body.Bytes(), &grant); err != nil {
		t.Fatalf("grant decode: %v", err)
	}
	attemptID, attemptToken, fence := f6Attempt(grant)
	heartbeat := f6do(t, h, f6JSON("POST", "/-/ci/v1/attempts/"+attemptID+"/heartbeat", attemptToken, f6Fence(fence)))
	if heartbeat.Code != http.StatusOK {
		t.Fatalf("heartbeat = %d: %s", heartbeat.Code, heartbeat.Body.String())
	}
	f6Golden(t, "heartbeat", heartbeat.Body.String())
	dataDigest := "sha256:" + f6DigestHex([]byte("hello"))
	logRec := f6do(t, h, f6JSON("POST", "/-/ci/v1/attempts/"+attemptID+"/log.append", attemptToken, `{"fence":`+strconv.FormatInt(fence, 10)+`,"seq":0,"offset":0,"sha256":"`+dataDigest+`","data_b64":"aGVsbG8=","events":[]}`))
	if logRec.Code != http.StatusOK {
		t.Fatalf("log.append = %d: %s", logRec.Code, logRec.Body.String())
	}
	f6Golden(t, "log.append", logRec.Body.String())
	putRec := f6do(t, h, f6Req{
		method: "PUT", target: "/-/ci/v1/attempts/" + attemptID + "/artifact.put/" + dataDigest,
		auth: attemptToken, raw: []byte("hello"),
		headers: map[string]string{"Content-Type": "application/octet-stream", "Rukie-Fence": strconv.FormatInt(fence, 10)},
	})
	if putRec.Code != http.StatusOK {
		t.Fatalf("artifact.put = %d: %s", putRec.Code, putRec.Body.String())
	}
	f6Golden(t, "artifact.put", putRec.Body.String())
	finishRec := f6do(t, h, f6JSON("POST", "/-/ci/v1/attempts/"+attemptID+"/finish", attemptToken, `{"outcome":"passed","class":"","exit_code":0,"fence":`+strconv.FormatInt(fence, 10)+`,"log":{"sha256":"`+dataDigest+`","last_seq":0,"end_offset":5},"artifacts":[]}`))
	if finishRec.Code != http.StatusOK {
		t.Fatalf("finish = %d: %s", finishRec.Code, finishRec.Body.String())
	}
	f6Golden(t, "finish", finishRec.Body.String())
}

func TestRunnerF6AcceptsEmptyArtifact(t *testing.T) {
	_, h, runnerID, runnerToken := f6Setup(t, ciTestSubject)
	grant := f6Lease(t, h, runnerID, runnerToken)
	attemptID, attemptToken, fence := f6Attempt(grant)
	emptyDigest := "sha256:" + f6DigestHex(nil)
	rec := f6do(t, h, f6Req{
		method: "PUT", target: "/-/ci/v1/attempts/" + attemptID + "/artifact.put/" + emptyDigest,
		auth: attemptToken, raw: []byte{},
		headers: map[string]string{"Content-Type": "application/octet-stream", "Rukie-Fence": strconv.FormatInt(fence, 10)},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("empty artifact.put = %d: %s, want 200", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), emptyDigest) {
		t.Fatalf("empty artifact misses digest: %s", rec.Body.String())
	}
}

func TestRunnerF6MintRegisterRotate(t *testing.T) {
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	admin, _ := e.svc.GetUser(t.Context(), "ada")
	rseedRepo(t, e, admin.ID, "hello", "private")
	trigger := rtoken(t, e, admin, "repo:read", "repo:write", "ci:read", "ci:trigger")
	adminToken := rtoken(t, e, admin, "repo:read", "repo:write", "admin:write")
	f6CreateRun(t, e, trigger, ciTestSubject)
	h := f6Handler(e)
	mint := f6do(t, h, f6JSON("POST", "/-/ci/v1/registration-tokens", adminToken, `{"scope":"untrusted","labels":["linux-amd64"],"capacity":2}`))
	if mint.Code != http.StatusOK {
		t.Fatalf("mint = %d: %s", mint.Code, mint.Body.String())
	}
	var minted struct {
		Token string `json:"token"`
	}
	if err := jsonv2.Unmarshal(mint.Body.Bytes(), &minted); err != nil || !strings.HasPrefix(minted.Token, "rkt_") {
		t.Fatalf("minted token = %q, %v", minted.Token, err)
	}
	register := f6do(t, h, f6JSON("POST", "/-/ci/v1/register", "", `{"token":"`+minted.Token+`"}`))
	if register.Code != http.StatusOK {
		t.Fatalf("register = %d: %s", register.Code, register.Body.String())
	}
	var registered struct {
		Token  string `json:"token"`
		Runner struct {
			ID string `json:"id"`
		} `json:"runner"`
	}
	if err := jsonv2.Unmarshal(register.Body.Bytes(), &registered); err != nil || registered.Token == "" {
		t.Fatalf("register body = %s, %v", register.Body.String(), err)
	}
	reuse := f6do(t, h, f6JSON("POST", "/-/ci/v1/register", "", `{"token":"`+minted.Token+`"}`))
	if reuse.Code != http.StatusUnauthorized || !strings.Contains(reuse.Body.String(), "E_UNAUTHENTICATED") {
		t.Fatalf(" reused token = %d: %s, want 401 E_UNAUTHENTICATED", reuse.Code, reuse.Body.String())
	}
	runnerToken := "Bearer " + registered.Token
	runnerID := registered.Runner.ID
	lease := f6do(t, h, f6JSON("POST", "/-/ci/v1/lease", runnerToken, `{"runner_id":"`+runnerID+`","wait_ms":1000}`))
	if lease.Code != http.StatusOK {
		t.Fatalf("lease = %d: %s", lease.Code, lease.Body.String())
	}
	rotated := f6do(t, h, f6JSON("POST", "/-/ci/v1/runners/"+runnerID+"/rotate", runnerToken, `{"fence":0}`))
	if rotated.Code != http.StatusOK {
		t.Fatalf("rotate = %d: %s", rotated.Code, rotated.Body.String())
	}
	var rotation struct {
		Token string `json:"token"`
		Fence int64  `json:"fence"`
	}
	if err := jsonv2.Unmarshal(rotated.Body.Bytes(), &rotation); err != nil || rotation.Fence != 1 {
		t.Fatalf("rotation = %s, %v", rotated.Body.String(), err)
	}
	oldLease := f6do(t, h, f6JSON("POST", "/-/ci/v1/lease", runnerToken, `{"runner_id":"`+runnerID+`","wait_ms":10}`))
	if oldLease.Code != http.StatusOK && oldLease.Code != http.StatusNoContent {
		t.Fatalf("old token lease = %d: %s, want accepted during the window", oldLease.Code, oldLease.Body.String())
	}
	stale := f6do(t, h, f6JSON("POST", "/-/ci/v1/runners/"+runnerID+"/rotate", "Bearer "+rotation.Token, `{"fence":0}`))
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), "E_LEASE_LOST") {
		t.Fatalf("stale rotate = %d: %s, want 409 E_LEASE_LOST", stale.Code, stale.Body.String())
	}
	fresh := f6do(t, h, f6JSON("POST", "/-/ci/v1/runners/"+runnerID+"/rotate", "Bearer "+rotation.Token, `{"fence":1}`))
	if fresh.Code != http.StatusOK {
		t.Fatalf("second rotate = %d: %s", fresh.Code, fresh.Body.String())
	}
}

func TestRunnerF6CancelRefusesPassed(t *testing.T) {
	spec := `schema = "rukie.ci/v1"

[triggers]
change = true

[[job]]
name = "solo"
runs_on = ["linux-amd64"]
image = "` + ciTestImage + `"
interruptible = true
steps = [{ name = "s", run = "echo hi" }]
`
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	admin, _ := e.svc.GetUser(t.Context(), "ada")
	rseedRepo(t, e, admin.ID, "hello", "private")
	trigger := rtoken(t, e, admin, "repo:read", "repo:write", "ci:read", "ci:trigger")
	adminToken := rtoken(t, e, admin, "repo:read", "repo:write", "admin:write")
	runnerID, runnerToken := f6CreateRunner(t, e, adminToken)
	escaped, _ := jsonv2.Marshal(map[string]string{"subject": ciTestSubject, "tree": "tree-one", "spec": spec})
	created := rdo(t, e, "POST", "/ada/hello/ci/runs", trigger, string(escaped))
	if created.Code != http.StatusCreated {
		t.Fatalf("run create = %d: %s", created.Code, created.Body.String())
	}
	var runOut map[string]any
	if err := jsonv2.Unmarshal(created.Body.Bytes(), &runOut); err != nil {
		t.Fatalf("run decode: %v", err)
	}
	runID, _ := runOut["run"].(map[string]any)["id"].(string)
	h := f6Handler(e)
	grant := f6Lease(t, h, runnerID, runnerToken)
	attemptID, attemptToken, fence := f6Attempt(grant)
	hb := f6do(t, h, f6JSON("POST", "/-/ci/v1/attempts/"+attemptID+"/heartbeat", attemptToken, f6Fence(fence)))
	if hb.Code != http.StatusOK {
		t.Fatalf("heartbeat = %d: %s", hb.Code, hb.Body.String())
	}
	if err := e.deps.CI.Cancel(t.Context(), runID, "test-cancel"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	cancelled := f6do(t, h, f6JSON("POST", "/-/ci/v1/attempts/"+attemptID+"/heartbeat", attemptToken, f6Fence(fence)))
	if cancelled.Code != http.StatusOK || !strings.Contains(cancelled.Body.String(), "test-cancel") {
		t.Fatalf("cancel heartbeat = %d: %s, want the reason", cancelled.Code, cancelled.Body.String())
	}
	emptyDigest := "sha256:" + f6DigestHex(nil)
	passed := f6do(t, h, f6JSON("POST", "/-/ci/v1/attempts/"+attemptID+"/finish", attemptToken, `{"outcome":"passed","class":"","exit_code":0,"fence":`+strconv.FormatInt(fence, 10)+`,"log":{"sha256":"`+emptyDigest+`","last_seq":-1,"end_offset":0},"artifacts":[]}`))
	if passed.Code != http.StatusUnprocessableEntity || !strings.Contains(passed.Body.String(), "E_INVALID") {
		t.Fatalf("passed after cancel = %d: %s, want 422 E_INVALID", passed.Code, passed.Body.String())
	}
	done := f6do(t, h, f6JSON("POST", "/-/ci/v1/attempts/"+attemptID+"/finish", attemptToken, `{"outcome":"errored","class":"cancelled","exit_code":143,"fence":`+strconv.FormatInt(fence, 10)+`,"log":{"sha256":"`+emptyDigest+`","last_seq":-1,"end_offset":0},"artifacts":[]}`))
	if done.Code != http.StatusOK {
		t.Fatalf("cancelled finish = %d: %s", done.Code, done.Body.String())
	}
}

func TestRunnerF6RejectsStaleFence(t *testing.T) {
	_, h, runnerID, runnerToken := f6Setup(t, ciTestSubject)
	grant := f6Lease(t, h, runnerID, runnerToken)
	attemptID, attemptToken, fence := f6Attempt(grant)
	stale := f6do(t, h, f6JSON("POST", "/-/ci/v1/attempts/"+attemptID+"/heartbeat", attemptToken, f6Fence(fence+1)))
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), "E_LEASE_LOST") {
		t.Fatalf("stale fence = %d: %s, want 409 E_LEASE_LOST", stale.Code, stale.Body.String())
	}
}

func TestRunnerF6RejectsLogGap(t *testing.T) {
	_, h, runnerID, runnerToken := f6Setup(t, ciTestSubject)
	grant := f6Lease(t, h, runnerID, runnerToken)
	attemptID, attemptToken, fence := f6Attempt(grant)
	dataDigest := "sha256:" + f6DigestHex([]byte("hello"))
	gap := f6do(t, h, f6JSON("POST", "/-/ci/v1/attempts/"+attemptID+"/log.append", attemptToken, `{"fence":`+strconv.FormatInt(fence, 10)+`,"seq":5,"offset":99,"sha256":"`+dataDigest+`","data_b64":"aGVsbG8=","events":[]}`))
	if gap.Code != http.StatusConflict || !strings.Contains(gap.Body.String(), "E_OFFSET_GAP") {
		t.Fatalf("log gap = %d: %s, want 409 E_OFFSET_GAP", gap.Code, gap.Body.String())
	}
}

func TestRunnerF6FinishConflict(t *testing.T) {
	_, h, runnerID, runnerToken := f6Setup(t, ciTestSubject)
	grant := f6Lease(t, h, runnerID, runnerToken)
	attemptID, attemptToken, fence := f6Attempt(grant)
	emptyDigest := "sha256:" + f6DigestHex(nil)
	finish := func(outcome, class string, exit int) *httptest.ResponseRecorder {
		return f6do(t, h, f6JSON("POST", "/-/ci/v1/attempts/"+attemptID+"/finish", attemptToken, `{"outcome":"`+outcome+`","class":"`+class+`","exit_code":`+strconv.Itoa(exit)+`,"fence":`+strconv.FormatInt(fence, 10)+`,"log":{"sha256":"`+emptyDigest+`","last_seq":-1,"end_offset":0},"artifacts":[]}`))
	}
	first := finish("passed", "", 0)
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"replayed":false`) {
		t.Fatalf("finish = %d: %s", first.Code, first.Body.String())
	}
	replay := finish("passed", "", 0)
	if replay.Code != http.StatusOK || !strings.Contains(replay.Body.String(), `"replayed":true`) {
		t.Fatalf("replay = %d: %s, want replayed:true", replay.Code, replay.Body.String())
	}
	conflict := finish("failed", "script_failure", 1)
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "E_RESULT_CONFLICT") {
		t.Fatalf("conflict = %d: %s, want 409 E_RESULT_CONFLICT", conflict.Code, conflict.Body.String())
	}
}

func TestRunnerF6RejectsUnauthenticated(t *testing.T) {
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	h := f6Handler(e)
	bad := f6do(t, h, f6JSON("POST", "/-/ci/v1/lease", "Bearer nope", `{"runner_id":"rnr_nope","wait_ms":10}`))
	if bad.Code != http.StatusUnauthorized || !strings.Contains(bad.Body.String(), "E_UNAUTHENTICATED") {
		t.Fatalf("bad lease = %d: %s, want 401 E_UNAUTHENTICATED", bad.Code, bad.Body.String())
	}
	hb := f6do(t, h, f6JSON("POST", "/-/ci/v1/attempts/att_nope/heartbeat", "Bearer nope", `{"fence":1}`))
	if hb.Code != http.StatusUnauthorized && hb.Code != http.StatusNotFound {
		t.Fatalf("bad heartbeat = %d: %s", hb.Code, hb.Body.String())
	}
}

func TestRunnerF6AdminRunnersPage(t *testing.T) {
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	admin, _ := e.svc.GetUser(t.Context(), "ada")
	rseedRepo(t, e, admin.ID, "hello", "private")
	adminToken := rtoken(t, e, admin, "repo:read", "repo:write", "admin:write")
	runnerID, _ := f6CreateRunner(t, e, adminToken)
	h := f6Handler(e)
	rec := f6do(t, h, f6Req{method: "GET", target: "/admin/runners", auth: adminToken, headers: map[string]string{"Accept": "text/html"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("runners page = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, runnerID) || !strings.Contains(body, "linux-amd64") {
		t.Fatalf("runners page misses the runner: %s", body)
	}
	if err := zerojs.Assert([]byte(body)); err != nil {
		t.Fatalf("zero-js: %v", err)
	}
	if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "script-src 'none'") {
		t.Fatalf("runners page CSP = %q, want script-src 'none'", got)
	}
	anon := f6do(t, h, f6Req{method: "GET", target: "/admin/runners", headers: map[string]string{"Accept": "text/html"}})
	if anon.Code == http.StatusOK {
		t.Fatal("anonymous users read the runners page")
	}
}
