//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/ci"
	"github.com/gosuda/rukie/internal/store"
)

const ciAttestImage = "docker.io/library/golang@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func ciAttestSpec() string {
	return `schema = "rukie.ci/v1"

[land]
required = ["solo"]

[[job]]
name = "solo"
runs_on = ["linux-amd64"]
image = "` + ciAttestImage + `"
pure = true
steps = [{ name = "s", run = "echo hi" }]
`
}

func ciSeedRepoDirect(t *testing.T, s *stack, ada userRec, owner, name string, files map[string]string) {
	t.Helper()
	repoID := s.addRepo(t, owner, name, "private")
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	rukPath := owner + "/" + name + ".git"
	if err := s.st.Write(ctx, func(tx *store.WriteTx) error {
		if ierr := tx.InsertPolicyRule(repoID, time.Now(), store.PolicyRuleInsert{
			Pattern:     "refs/heads/main",
			Mode:        "LandOnly",
			BypassAdmin: true,
			PushAllow:   `[{"users":[` + strconv.FormatInt(ada.user.ID, 10) + `]}]`,
			Required:    `[{"kind":"ci/*","result":"pass","min_signers":1}]`,
		}); ierr != nil {
			return ierr
		}
		return tx.UpdateRepo(repoID, time.Now(), store.RepoUpdate{RukPath: &rukPath})
	}); err != nil {
		t.Fatalf("seed rule: %v", err)
	}
	work, _ := s.localRepo(t, files)
	pushCtx, pushCancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer pushCancel()
	if out, err := gitRun(pushCtx, t, work, "push", s.repoURL(owner, name, "ada", ada.token), "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("seed main: %v\n%s", err, out)
	}
	bypass := false
	pushAllow := "[]"
	if err := s.st.Write(ctx, func(tx *store.WriteTx) error {
		return tx.UpdatePolicyRule(repoID, "refs/heads/main", store.PolicyRuleUpdate{
			BypassAdmin: &bypass, PushAllow: &pushAllow,
		})
	}); err != nil {
		t.Fatalf("tighten rule: %v", err)
	}
}

func ciAttestTokens(t *testing.T, h *apiHarness) (session, trigger, admin, land string) {
	t.Helper()
	session = apiLogin(t, h, "ada", "long-test-password-01")
	trigger = apiToken(t, h, session, "repo:read repo:write ci:read ci:trigger")
	admin = apiToken(t, h, session, "repo:read repo:write ci:read ci:trigger admin:write")
	land = apiToken(t, h, session, "repo:read repo:write intents:read")
	return session, trigger, admin, land
}

func ciAttestRunner(t *testing.T, h *apiHarness, adminToken string) (string, string) {
	t.Helper()
	status, decoded := apiCall(t, h, "POST", "/-/ci/v1/runners", adminToken, `{"labels":["linux-amd64"],"scope":"untrusted","capacity":1}`)
	if status != http.StatusCreated {
		t.Fatalf("runner.create = %d: %v", status, decoded)
	}
	runner, _ := decoded["runner"].(map[string]any)
	id, _ := runner["id"].(string)
	secret, _ := decoded["token"].(string)
	if id == "" || secret == "" {
		t.Fatalf("runner create lacks id/token: %v", decoded)
	}
	return id, "Bearer " + secret
}

func ciAttestRun(t *testing.T, h *apiHarness, triggerToken, subject string) string {
	t.Helper()
	raw, err := jsonv2.Marshal(map[string]string{"subject": subject, "tree": subject, "spec": ciAttestSpec()})
	if err != nil {
		t.Fatalf("marshal run: %v", err)
	}
	status, decoded := apiCall(t, h, "POST", "/ada/hello/ci/runs", triggerToken, string(raw))
	if status != http.StatusCreated {
		t.Fatalf("ci.run.create = %d: %v", status, decoded)
	}
	run, _ := decoded["run"].(map[string]any)
	id, _ := run["id"].(string)
	if id == "" {
		t.Fatalf("run lacks an id: %v", decoded)
	}
	return id
}

type ciAttempt struct {
	id    string
	token string
	fence int64
}

func ciAttestLease(t *testing.T, h *apiHarness, runnerID, runnerBearer string) ciAttempt {
	t.Helper()
	raw, err := jsonv2.Marshal(map[string]any{
		"runner_id": runnerID, "labels": []string{"linux-amd64"}, "scope": "untrusted",
		"platform": "linux-amd64", "free_slots": 1, "wait_ms": 5000,
	})
	if err != nil {
		t.Fatalf("marshal lease: %v", err)
	}
	status, decoded := apiCall(t, h, "POST", "/_/ci/runner/v1/lease", runnerBearer, string(raw))
	if status != http.StatusOK {
		t.Fatalf("runner lease = %d: %v", status, decoded)
	}
	attempt, _ := decoded["attempt"].(map[string]any)
	id, _ := attempt["id"].(string)
	secret, _ := attempt["token"].(string)
	fence, _ := attempt["fence"].(float64)
	if id == "" || secret == "" {
		t.Fatalf("lease lacks attempt id/token: %v", decoded)
	}
	return ciAttempt{id: id, token: "Bearer " + secret, fence: int64(fence)}
}

func ciAttestStart(t *testing.T, h *apiHarness, a ciAttempt) {
	t.Helper()
	raw, err := jsonv2.Marshal(map[string]int64{"fence": a.fence})
	if err != nil {
		t.Fatalf("marshal start: %v", err)
	}
	status, decoded := apiCall(t, h, "POST", "/_/ci/runner/v1/attempts/"+a.id+"/start", a.token, string(raw))
	if status != http.StatusOK {
		t.Fatalf("runner start = %d: %v", status, decoded)
	}
}

func ciAttestComplete(t *testing.T, h *apiHarness, a ciAttempt, outcome, class string, exit int) string {
	t.Helper()
	empty := ci.Digest(nil)
	raw, err := jsonv2.Marshal(map[string]any{
		"fence": a.fence, "outcome": outcome, "class": class, "exit_code": exit,
		"log": map[string]any{"last_seq": -1, "end_offset": 0, "sha256": empty},
	})
	if err != nil {
		t.Fatalf("marshal complete: %v", err)
	}
	status, decoded := apiCall(t, h, "POST", "/_/ci/runner/v1/attempts/"+a.id+"/complete", a.token, string(raw))
	if status != http.StatusOK {
		t.Fatalf("runner complete = %d: %v", status, decoded)
	}
	job, _ := decoded["job"].(string)
	return job
}

func ciAttestRelay(t *testing.T, h *apiHarness) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	delivered := 0
	for delivered < 1 && time.Now().Before(deadline) {
		n, err := h.ci.RelayOnce(t.Context())
		if err != nil {
			t.Fatalf("relay: %v", err)
		}
		delivered += n
		if delivered < 1 {
			time.Sleep(500 * time.Millisecond)
		}
	}
	if delivered != 1 {
		rows, rerr := h.ci.PendingOutbox(t.Context())
		if rerr != nil {
			t.Fatalf("relay delivered %d rows, want 1 (pending: %v)", delivered, rerr)
		}
		for _, row := range rows {
			t.Logf("pending outbox id=%s chain=%s state=%s attempts=%d err=%s", row.ID, row.Chain, row.State, row.Attempts, row.LastError)
		}
		t.Fatalf("relay delivered %d rows, want 1", delivered)
	}
}

func ciAttestAgentFlow(t *testing.T) {
	s := newStack(t, nil)
	h := mountAPI(t, s)
	ada := s.addAdmin(t, "ada", "repo:read", "repo:write")
	ciSeedRepoDirect(t, s, ada, "ada", "hello", map[string]string{"README.md": "hello\n"})
	head := f2PushFor(t, s, ada, "hello", map[string]string{"README.md": "hello\nchange\n"})
	_, trigger, adminTok, landTok := ciAttestTokens(t, h)
	runnerID, runnerBearer := ciAttestRunner(t, h, adminTok)
	runID := ciAttestRun(t, h, trigger, head)
	attempt := ciAttestLease(t, h, runnerID, runnerBearer)
	ciAttestStart(t, h, attempt)
	if got := ciAttestComplete(t, h, attempt, "failed", "script_failure", 1); got != "failed" {
		t.Fatalf("job state = %q, want failed", got)
	}
	status, decoded := apiCall(t, h, "GET", "/ada/hello/ci/runs/"+runID, trigger, "")
	if status != http.StatusOK {
		t.Fatalf("ci.run.get = %d: %v", status, decoded)
	}
	run, _ := decoded["run"].(map[string]any)
	if run["state"] != "failed" {
		t.Fatalf("run state = %v, want failed", decoded)
	}
	ciAttestRelay(t, h)
	key := "main~" + strconv.FormatInt(ada.user.ID, 10)
	planStatus, plan := apiCall(t, h, "POST", "/ada/hello/land/plans", landTok, `{"target_ref":"refs/heads/main","change":"`+key+`"}`)
	if planStatus != http.StatusCreated {
		t.Fatalf("land.plan.create = %d: %v", planStatus, plan)
	}
	planID, _ := plan["id"].(string)
	if planID == "" {
		if nested, ok := plan["plan"].(map[string]any); ok {
			planID, _ = nested["id"].(string)
		}
	}
	if planID == "" {
		t.Fatalf("plan id missing: %v", plan)
	}
	runStatus, landRun := apiCall(t, h, "POST", "/ada/hello/land/plans/"+planID+"/runs", landTok, `{}`)
	if runStatus != http.StatusConflict {
		t.Fatalf("land.run.create = %d, want 409: %v", runStatus, landRun)
	}
	detail, _ := landRun["detail"].(string)
	if !strings.Contains(detail, "requirements-unmet") || !strings.Contains(detail, "needs_ci") {
		t.Fatalf("conflict lacks the CI requirement gap: %v", landRun)
	}
	proofStatus, proofs := apiCall(t, h, "GET", "/ada/hello/changes/"+key+"/proofs", landTok, "")
	if proofStatus != http.StatusOK {
		t.Fatalf("proof.list = %d: %v", proofStatus, proofs)
	}
	raw, _ := proofs["proofs"].([]any)
	foundFail := false
	for _, item := range raw {
		row, _ := item.(map[string]any)
		kind, _ := row["kind"].(string)
		result, _ := row["result"].(string)
		if strings.HasPrefix(kind, "ci/") && result == "fail" {
			foundFail = true
		}
	}
	if !foundFail {
		t.Fatalf("no live ci fail attestation: %v", proofs)
	}
}

func ciAttestFault(t *testing.T) {
	s := newStack(t, nil)
	h := mountAPI(t, s)
	_ = s.addAdmin(t, "ada", "repo:read", "repo:write")
	repoID := s.addRepo(t, "ada", "hello", "private")
	_ = repoID
	oid := pushMain(t, s, "ada", "hello", map[string]string{"README.md": "hello\n"})
	_, trigger, adminTok, _ := ciAttestTokens(t, h)
	runnerID, runnerBearer := ciAttestRunner(t, h, adminTok)
	runID := ciAttestRun(t, h, trigger, oid)
	restartDaemon(t, s)
	h = mountAPI(t, s)
	attempt := ciAttestLease(t, h, runnerID, runnerBearer)
	ciAttestStart(t, h, attempt)
	if got := ciAttestComplete(t, h, attempt, "passed", "", 0); got != "passed" {
		t.Fatalf("job state = %q, want passed", got)
	}
	n, err := h.ci.RelayOnce(t.Context())
	if err != nil {
		t.Fatalf("relay after kill: %v", err)
	}
	if n != 1 {
		t.Fatalf("relay delivered %d rows, want 1", n)
	}
	status, decoded := apiCall(t, h, "GET", "/ada/hello/ci/runs/"+runID, trigger, "")
	if status != http.StatusOK {
		t.Fatalf("ci.run.get = %d: %v", status, decoded)
	}
	run, _ := decoded["run"].(map[string]any)
	if run["state"] != "succeeded" {
		t.Fatalf("run state = %v, want succeeded", decoded)
	}
}
