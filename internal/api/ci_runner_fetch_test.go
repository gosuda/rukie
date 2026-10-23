package api_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/op"
)

type readCounter struct{ n *int }

func (r readCounter) Read(_ []byte) (int, error) {
	*r.n++
	return 0, io.EOF
}

func TestRunnerFetchTokenBound(t *testing.T) {
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	admin, _ := e.svc.GetUser(t.Context(), "ada")
	repoID := rseedRepo(t, e, admin.ID, "hello", "private")
	trigger := rtoken(t, e, admin, "repo:read", "repo:write", "ci:read", "ci:trigger")
	adminToken := rtoken(t, e, admin, "repo:read", "repo:write", "admin:write")
	runnerID, runnerToken := f6CreateRunner(t, e, adminToken)
	f6CreateRun(t, e, trigger, ciTestSubject)
	h := f6Handler(e)
	grant := f6Lease(t, h, runnerID, runnerToken)
	fetch, _ := grant["fetch"].(map[string]any)
	secret, _ := fetch["token"].(string)
	if !strings.HasPrefix(secret, "rk_") {
		t.Fatalf("fetch token = %q, want rk_ shape", secret)
	}
	if len(secret) != 46 {
		t.Fatalf("fetch token length = %d, want 46", len(secret))
	}
	tok, user, err := e.svc.LookupToken(t.Context(), secret, "127.0.0.1")
	if err != nil {
		t.Fatalf("fetch token does not authenticate: %v", err)
	}
	if user.Name != "ada" {
		t.Fatalf("fetch token owner = %q, want the run principal ada", user.Name)
	}
	repos, err := e.svc.TokenRepos(t.Context(), tok.Hash)
	if err != nil {
		t.Fatalf("token repos: %v", err)
	}
	if len(repos) != 1 || repos[0] != repoID {
		t.Fatalf("fetch token repos = %v, want only [%d]", repos, repoID)
	}
	if !tok.Scopes.Has(op.ScopeRepoRead) || tok.Scopes.Has(op.ScopeRepoWrite) {
		t.Fatalf("fetch token scopes grant push or miss read")
	}
	ttl := time.Duration(tok.ExpiresAt-time.Now().UnixMilli()) * time.Millisecond
	if ttl < 39*time.Minute || ttl > 41*time.Minute {
		t.Fatalf("fetch token ttl = %v, want about 40m", ttl)
	}
}

func TestRunnerArtifactPutAuthFirst(t *testing.T) {
	_, h, runnerID, runnerToken := f6Setup(t, ciTestSubject)
	grant := f6Lease(t, h, runnerID, runnerToken)
	attemptID, attemptToken, fence := f6Attempt(grant)
	emptyDigest := "sha256:" + f6DigestHex(nil)
	n := 0
	huge := httptest.NewRequest("PUT", "/-/ci/v1/attempts/"+attemptID+"/artifact.put/"+emptyDigest, readCounter{n: &n})
	huge.ContentLength = 1 << 30
	huge.Header.Set("Rukie-Fence", strconv.FormatInt(fence, 10))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, huge)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "E_UNAUTHENTICATED") {
		t.Fatalf("unauthenticated 1GiB put = %d: %s, want 401", rec.Code, rec.Body.String())
	}
	if n != 0 {
		t.Fatalf("unauthenticated put read the body %d times", n)
	}
	over := httptest.NewRequest("PUT", "/-/ci/v1/attempts/"+attemptID+"/artifact.put/"+emptyDigest, strings.NewReader("x"))
	over.ContentLength = (512 << 20) + 1
	over.Header.Set("Authorization", string(attemptToken))
	over.Header.Set("Rukie-Fence", strconv.FormatInt(fence, 10))
	overRec := httptest.NewRecorder()
	h.ServeHTTP(overRec, over)
	if overRec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(overRec.Body.String(), "E_LIMIT") {
		t.Fatalf("over-cap put = %d: %s, want 413 E_LIMIT", overRec.Code, overRec.Body.String())
	}
}

func TestRunnerHeartbeatAuthFirst(t *testing.T) {
	_, h, runnerID, runnerToken := f6Setup(t, ciTestSubject)
	grant := f6Lease(t, h, runnerID, runnerToken)
	attemptID, attemptToken, _ := f6Attempt(grant)
	badAuth := f6do(t, h, f6JSON("POST", "/-/ci/v1/attempts/"+attemptID+"/heartbeat", "Bearer bogus", `{invalid`))
	if badAuth.Code != http.StatusUnauthorized || !strings.Contains(badAuth.Body.String(), "E_UNAUTHENTICATED") {
		t.Fatalf("bad-token heartbeat = %d: %s, want 401 before body parse", badAuth.Code, badAuth.Body.String())
	}
	goodAuth := f6do(t, h, f6JSON("POST", "/-/ci/v1/attempts/"+attemptID+"/heartbeat", attemptToken, `{invalid`))
	if goodAuth.Code != http.StatusUnprocessableEntity || !strings.Contains(goodAuth.Body.String(), "E_INVALID") {
		t.Fatalf("good-token malformed heartbeat = %d: %s, want 422 E_INVALID", goodAuth.Code, goodAuth.Body.String())
	}
}
