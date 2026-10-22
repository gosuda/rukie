package api_test

import (
	"context"
	"crypto/rand"
	"net/http"
	"strings"
	"testing"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/api"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/store"
)

func f7Env(t *testing.T) *repoEnv {
	t.Helper()
	path := t.TempDir() + "/f7.sqlite"
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
		func() error { return api.RegisterReview(table, deps) },
		func() error { return api.RegisterIntents(table, deps) },
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
	api.RegisterReviewViews(views)
	api.RegisterIntentsViews(views)
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

func f7Setup(t *testing.T) (*repoEnv, string) {
	t.Helper()
	e := f7Env(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	return e, rlogin(t, e, "ada", "long-test-password-01")
}

func f7SeedRepo(t *testing.T, e *repoEnv, ownerID int64) int64 {
	t.Helper()
	return rseedRepo(t, e, ownerID, "hello", "private")
}

func f7CreateIntent(t *testing.T, e *repoEnv, cookie, body string) int64 {
	t.Helper()
	rec := rdo(t, e, "POST", "/ada/hello/tasks/new", cookie, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("intent create = %d: %s", rec.Code, rec.Body.String())
	}
	m := rdecode(t, rec)
	intent, ok := m["intent"].(map[string]any)
	if !ok {
		t.Fatalf("intent create misses intent: %s", rec.Body.String())
	}
	num, ok := intent["number"].(float64)
	if !ok {
		t.Fatalf("intent create misses number: %s", rec.Body.String())
	}
	return int64(num)
}

func f7CreateAsk(t *testing.T, e *repoEnv, cookie string, num int64, body string) string {
	t.Helper()
	rec := rdo(t, e, "POST", "/ada/hello/tasks/"+itoa(num)+"/asks", cookie, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("ask create = %d: %s", rec.Code, rec.Body.String())
	}
	m := rdecode(t, rec)
	ask, ok := m["ask"].(map[string]any)
	if !ok {
		t.Fatalf("ask create misses ask: %s", rec.Body.String())
	}
	id, ok := ask["public_id"].(string)
	if !ok || id == "" {
		t.Fatalf("ask create misses public_id: %s", rec.Body.String())
	}
	return id
}

func f7TriageUser(t *testing.T, e *repoEnv, adminID, repoID int64, name, role string) (auth.User, string) {
	t.Helper()
	user := ruser(t, e, adminID, name)
	rgrant(t, e, repoID, user.ID, role)
	return user, rlogin(t, e, name, "long-test-password-01")
}

func TestTwoAgentsCannotClaimOneIntent(t *testing.T) {
	e, ada := f7Setup(t)
	admin, _ := e.svc.GetUser(t.Context(), "ada")
	repoID := f7SeedRepo(t, e, admin.ID)
	_, bob := f7TriageUser(t, e, admin.ID, repoID, "bob", "triage")
	num := f7CreateIntent(t, e, ada, `{"title":"shared plan","lease_mode":"enforce","claim_paths":["src"]}`)
	f7CreateAsk(t, e, bob, num, `{"question":"which plan wins?","blocks_claim":true}`)
	rec := rdo(t, e, "POST", "/ada/hello/tasks/"+itoa(num)+"/claim", ada, `{"ttl_s":3600}`)
	rassertProblem(t, rec, 409, "lease-conflict")
	if !strings.Contains(rec.Body.String(), "lease-hold") {
		t.Fatalf("claim denial misses lease-hold: %s", rec.Body.String())
	}
	other := rdo(t, e, "POST", "/ada/hello/tasks/"+itoa(num)+"/claim", bob, `{"ttl_s":3600}`)
	rassertProblem(t, other, 409, "lease-conflict")
}

func TestLeaseEnforceReturnsClaimed(t *testing.T) {
	e, ada := f7Setup(t)
	admin, _ := e.svc.GetUser(t.Context(), "ada")
	repoID := f7SeedRepo(t, e, admin.ID)
	_, bob := f7TriageUser(t, e, admin.ID, repoID, "bob", "triage")
	num := f7CreateIntent(t, e, ada, `{"title":"enforced plan","lease_mode":"enforce","claim_paths":["src"]}`)
	got := rdo(t, e, "GET", "/ada/hello/tasks/"+itoa(num), ada, "")
	if got.Code != http.StatusOK {
		t.Fatalf("intent get = %d: %s", got.Code, got.Body.String())
	}
	mode := rdecode(t, got)["intent"].(map[string]any)["lease_mode"]
	if mode != "enforce" {
		t.Fatalf("lease mode = %v, want enforce", mode)
	}
	f7CreateAsk(t, e, bob, num, `{"question":"hold the lease","blocks_claim":true}`)
	rec := rdo(t, e, "POST", "/ada/hello/tasks/"+itoa(num)+"/claim", ada, `{"ttl_s":3600}`)
	rassertProblem(t, rec, 409, "lease-conflict")
	rassertProblem(t, rdo(t, e, "POST", "/ada/hello/tasks/"+itoa(num)+"/edit", ada, `{"lease_mode":"nope"}`), 422, "validation")
}

func TestLeaseAdviseWarnsOnly(t *testing.T) {
	e, ada := f7Setup(t)
	admin, _ := e.svc.GetUser(t.Context(), "ada")
	f7SeedRepo(t, e, admin.ID)
	num := f7CreateIntent(t, e, ada, `{"title":"advisory plan","claim_paths":["src"]}`)
	got := rdo(t, e, "GET", "/ada/hello/tasks/"+itoa(num), ada, "")
	if got.Code != http.StatusOK {
		t.Fatalf("intent get = %d: %s", got.Code, got.Body.String())
	}
	if mode := rdecode(t, got)["intent"].(map[string]any)["lease_mode"]; mode != "advise" {
		t.Fatalf("lease mode = %v, want advise", mode)
	}
	rec := rdo(t, e, "POST", "/ada/hello/tasks/"+itoa(num)+"/claim", ada, `{"ttl_s":3600}`)
	if rec.Code == 409 {
		t.Fatalf("advise claim hard-failed: %s", rec.Body.String())
	}
	rassertProblem(t, rec, 503, "unavailable")
}

func TestAskAnswerUnblocksLease(t *testing.T) {
	e, ada := f7Setup(t)
	admin, _ := e.svc.GetUser(t.Context(), "ada")
	repoID := f7SeedRepo(t, e, admin.ID)
	bob, bobCookie := f7TriageUser(t, e, admin.ID, repoID, "bob", "triage")
	num := f7CreateIntent(t, e, ada, `{"title":"blocked plan","claim_paths":["src"]}`)
	aid := f7CreateAsk(t, e, bobCookie, num, `{"question":"pick one","blocks_claim":true}`)
	rassertProblem(t, rdo(t, e, "POST", "/ada/hello/tasks/"+itoa(num)+"/claim", ada, `{"ttl_s":3600}`), 409, "lease-conflict")
	bobToken := rtoken(t, e, bob, "repo:read", "intents:read", "intents:write")
	rassertProblem(t, rdo(t, e, "POST", "/ada/hello/asks/"+aid+"/answer", bobToken, `{"answer":"left"}`), 403, "forbidden")
	rec := rdo(t, e, "POST", "/ada/hello/asks/"+aid+"/answer", bobCookie, `{"answer":"left"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("ask answer = %d: %s", rec.Code, rec.Body.String())
	}
	if state := rdecode(t, rec)["ask"].(map[string]any)["state"]; state != "answered" {
		t.Fatalf("ask state = %v, want answered", state)
	}
	rassertProblem(t, rdo(t, e, "POST", "/ada/hello/tasks/"+itoa(num)+"/claim", ada, `{"ttl_s":3600}`), 503, "unavailable")
}

func TestAskExpiryDoesNotAnswer(t *testing.T) {
	e, ada := f7Setup(t)
	admin, _ := e.svc.GetUser(t.Context(), "ada")
	repoID := f7SeedRepo(t, e, admin.ID)
	_, bob := f7TriageUser(t, e, admin.ID, repoID, "bob", "triage")
	num := f7CreateIntent(t, e, ada, `{"title":"timed plan","claim_paths":["src"]}`)
	aid := f7CreateAsk(t, e, bob, num, `{"question":"still needed?","blocks_claim":true}`)
	rec := rdo(t, e, "POST", "/ada/hello/asks/"+aid+"/cancel", bob, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("ask cancel = %d: %s", rec.Code, rec.Body.String())
	}
	if state := rdecode(t, rec)["ask"].(map[string]any)["state"]; state != "expired" {
		t.Fatalf("ask state = %v, want expired", state)
	}
	rassertProblem(t, rdo(t, e, "POST", "/ada/hello/asks/"+aid+"/answer", bob, `{"answer":"late"}`), 409, "conflict")
	rassertProblem(t, rdo(t, e, "POST", "/ada/hello/tasks/"+itoa(num)+"/claim", ada, `{"ttl_s":3600}`), 503, "unavailable")
}

func f7IntentVersion(t *testing.T, e *repoEnv, cookie string, num int64) int64 {
	t.Helper()
	rec := rdo(t, e, "GET", "/ada/hello/tasks/"+itoa(num), cookie, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("intent get = %d: %s", rec.Code, rec.Body.String())
	}
	version, _ := rdecode(t, rec)["intent"].(map[string]any)["version"].(float64)
	return int64(version)
}

func TestIntentCloseNeedsAcceptance(t *testing.T) {
	e, ada := f7Setup(t)
	admin, _ := e.svc.GetUser(t.Context(), "ada")
	repoID := f7SeedRepo(t, e, admin.ID)
	_, mallory := f7TriageUser(t, e, admin.ID, repoID, "mallory", "write")
	_, cara := f7TriageUser(t, e, admin.ID, repoID, "cara", "maintain")
	bare := f7CreateIntent(t, e, ada, `{"title":"bare work"}`)
	rassertProblem(t, rdo(t, e, "POST", "/ada/hello/tasks/"+itoa(bare)+"/state", ada, `{"state":"closed","reason":"completed","version":`+itoa(f7IntentVersion(t, e, ada, bare))+`}`), 409, "conflict")
	linkBare := rdo(t, e, "POST", "/ada/hello/tasks/"+itoa(bare)+"/changes", ada, `{"change_id":"change-k0","relation":"implements"}`)
	if linkBare.Code != http.StatusCreated {
		t.Fatalf("change link = %d: %s", linkBare.Code, linkBare.Body.String())
	}
	rec := rdo(t, e, "POST", "/ada/hello/tasks/"+itoa(bare)+"/state", ada, `{"state":"closed","reason":"completed","version":`+itoa(f7IntentVersion(t, e, ada, bare))+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("close satisfied = %d: %s", rec.Code, rec.Body.String())
	}
	if state := rdecode(t, rec)["intent"].(map[string]any)["state"]; state != "closed" {
		t.Fatalf("intent state = %v, want closed", state)
	}
	gated := f7CreateIntent(t, e, ada, `{"title":"gated work","contract":{"requirements":[{"kind":"review/approve","result":"approved","min_signers":1}]}}`)
	link := rdo(t, e, "POST", "/ada/hello/tasks/"+itoa(gated)+"/changes", ada, `{"change_id":"change-k1","relation":"implements"}`)
	if link.Code != http.StatusCreated {
		t.Fatalf("change link = %d: %s", link.Code, link.Body.String())
	}
	rassertProblem(t, rdo(t, e, "POST", "/ada/hello/tasks/"+itoa(gated)+"/state", ada, `{"state":"closed","reason":"completed","version":`+itoa(f7IntentVersion(t, e, ada, gated))+`}`), 409, "conflict")
	dup := f7CreateIntent(t, e, ada, `{"title":"dup work"}`)
	rassertProblem(t, rdo(t, e, "POST", "/ada/hello/tasks/"+itoa(dup)+"/state", mallory, `{"state":"closed","reason":"duplicate","version":`+itoa(f7IntentVersion(t, e, mallory, dup))+`}`), 403, "forbidden")
	rec = rdo(t, e, "POST", "/ada/hello/tasks/"+itoa(dup)+"/state", cara, `{"state":"closed","reason":"duplicate","version":`+itoa(f7IntentVersion(t, e, cara, dup))+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("maintainer duplicate close = %d: %s", rec.Code, rec.Body.String())
	}
	if state := rdecode(t, rec)["intent"].(map[string]any)["state"]; state != "closed" {
		t.Fatalf("intent state = %v, want closed", state)
	}
}
