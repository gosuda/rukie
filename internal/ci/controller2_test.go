package ci_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/ci"
)

func retrySpecTOML() string {
	return `schema = "rukie.ci/v1"

[[job]]
name = "flaky"
runs_on = ["linux-amd64"]
image = "` + testImage + `"
pure = true
retry = { max = 1, on = ["infra"] }
steps = [{ name = "s", run = "echo hi" }]

[[job]]
name = "strict"
runs_on = ["linux-amd64"]
image = "` + testImage + `"
pure = true
retry = { max = 3, on = [] }
steps = [{ name = "s", run = "echo hi" }]
`
}

func driveToTerminal(t *testing.T, c *ci.Controller, lease *ci.Lease, outcome, class string, exit int) {
	t.Helper()
	mustStart(t, c, lease)
	mustAppend(t, c, lease, 0, "out\n", nil)
	mustFinish(t, c, lease, outcome, class, exit, nil)
}

func TestCIBoundedRetry(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	c, _ := openCI(t, clk)
	spec := mustDecode(t, retrySpecTOML())
	run := mustCreateRun(t, c, spec, "trusted", "change:retry:tip")
	_ = run
	runnerID, token := mustRunner(t, c, "trusted")
	first := mustLease(t, c, runnerID, token)
	if first.Job.Spec.Name != "flaky" {
		t.Fatalf("leased %q, want flaky first", first.Job.Spec.Name)
	}
	driveToTerminal(t, c, first, "errored", "infra", 1)
	backed, err := c.Lease(ctx, runnerID, token)
	if err != nil {
		t.Fatalf("backed-off lease: %v", err)
	}
	if backed == nil || backed.Job.Spec.Name != "strict" {
		t.Fatal("the backed-off retry did not yield to the ready job")
	}
	driveToTerminal(t, c, backed, "failed", "script_failure", 1)
	clk.Advance(31 * time.Second)
	second := mustLease(t, c, runnerID, token)
	if second.Job.Spec.Name != "flaky" {
		t.Fatalf("leased %q, want the flaky retry", second.Job.Spec.Name)
	}
	driveToTerminal(t, c, second, "errored", "infra", 1)
	drained, err := c.Lease(ctx, runnerID, token)
	if err != nil {
		t.Fatalf("drain lease: %v", err)
	}
	if drained != nil {
		t.Fatal("a third flaky attempt was granted past max 1")
	}
	jobs := runJobs(t, c, run)
	if jobs["flaky"].State != "errored" {
		t.Fatalf("flaky = %q, want errored", jobs["flaky"].State)
	}
	rows, err := c.PendingOutbox(ctx)
	if err != nil {
		t.Fatalf("pending outbox: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("pending verdicts = %d, want the strict failure only", len(rows))
	}
	if !strings.Contains(rows[0].Chain, "/strict/") || rows[0].Statement.Result != "fail" {
		t.Fatalf("pending verdict = %+v, want the strict fail", rows[0])
	}
	if jobs["strict"].State != "failed" {
		t.Fatalf("strict = %q, want failed", jobs["strict"].State)
	}
}

func TestCIOneAttestationPerJob(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	c, delivered := openCI(t, clk)
	spec := mustDecode(t, singleJobSpecTOML())
	_ = mustCreateRun(t, c, spec, "trusted", "push:main:prime")
	runnerID, token := mustRunner(t, c, "trusted")
	lease := mustLease(t, c, runnerID, token)
	driveToTerminal(t, c, lease, "passed", "", 0)
	mustRelay(t, c)
	if len(*delivered) != 1 {
		t.Fatalf("prime delivered %d rows, want 1", len(*delivered))
	}
	first := (*delivered)[0].row
	if first.State != "pending" || first.Statement.Result != "pass" || first.Statement.Supersedes != nil {
		t.Fatalf("prime row = %+v, want a pending pass with no supersedes", first)
	}
	second := mustCreateRun(t, c, spec, "trusted", "push:main:second")
	_ = second
	if _, err := c.Lease(ctx, runnerID, token); err != nil {
		t.Fatalf("cache lease: %v", err)
	}
	mustRelay(t, c)
	if len(*delivered) != 2 {
		t.Fatalf("cache hit delivered %d rows, want 2", len(*delivered))
	}
	hit := (*delivered)[1].row
	if hit.Statement.Supersedes == nil || *hit.Statement.Supersedes != first.Digest {
		t.Fatal("the cache-hit attestation does not supersede the prime verdict")
	}
}

func TestCIPublicationRule(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	c, delivered := openCI(t, clk)
	spec := mustDecode(t, singleJobSpecTOML())
	_ = mustCreateRun(t, c, spec, "trusted", "push:main:t")
	runnerID, token := mustRunner(t, c, "trusted")
	lease := mustLease(t, c, runnerID, token)
	driveToTerminal(t, c, lease, "passed", "", 0)
	mustRelay(t, c)
	if len(*delivered) != 1 {
		t.Fatalf("trusted pass delivered %d rows, want 1", len(*delivered))
	}
	untrustedSpec := spec
	untrusted := ci.CreateRunRequest{
		RepoID: 7, Principal: "mallory", Subject: "0123456789abcdef0123456789abcdef01234567",
		Tree: "tree-two", BaseCommit: "base-one", SpecFrom: "base",
		Trust: "untrusted", TriggerKey: "change:9:tip", Class: "interactive", Spec: untrustedSpec,
	}
	untrustedRun, err := c.CreateRun(ctx, untrusted)
	if err != nil {
		t.Fatalf("untrusted create: %v", err)
	}
	_ = untrustedRun
	untrustedRunner, untrustedToken, err := c.CreateRunner(ctx, []string{"linux-amd64"}, "untrusted", 2, "podman-rootless/1", nil)
	if err != nil {
		t.Fatalf("untrusted runner: %v", err)
	}
	untrustedLease, err := c.Lease(ctx, untrustedRunner.ID, untrustedToken)
	if err != nil {
		t.Fatalf("untrusted lease: %v", err)
	}
	if untrustedLease == nil {
		t.Fatal("untrusted run found no work")
	}
	driveToTerminal(t, c, untrustedLease, "passed", "", 0)
	mustRelay(t, c)
	if len(*delivered) != 1 {
		t.Fatalf("untrusted pass over a trusted head delivered %d rows, want 1", len(*delivered))
	}
	followup, err := c.CreateRun(ctx, ci.CreateRunRequest{
		RepoID: 7, Principal: "ada", Subject: "0123456789abcdef0123456789abcdef01234567",
		Tree: "tree-two", BaseCommit: "base-one", SpecFrom: "base",
		Trust: "trusted", TriggerKey: "push:main:followup", Class: "interactive", Spec: spec,
	})
	if err != nil {
		t.Fatalf("followup create: %v", err)
	}
	_ = followup
	followupLease, err := c.Lease(ctx, runnerID, token)
	if err != nil {
		t.Fatalf("followup lease: %v", err)
	}
	if followupLease == nil {
		t.Fatal("untrusted pass poisoned the trusted cache")
	}
}

func TestCIUntrustedPassWritesNoCache(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	st := tempStore(t)
	var delivered []deliveredRow
	c := openCIOn(t, clk, st, &delivered)
	spec := mustDecode(t, singleJobSpecTOML())
	untrustedRunner, untrustedToken, err := c.CreateRunner(ctx, []string{"linux-amd64"}, "untrusted", 2, "podman-rootless/1", nil)
	if err != nil {
		t.Fatalf("untrusted runner: %v", err)
	}
	run := mustCreateRun(t, c, spec, "untrusted", "change:u:tip")
	_ = run
	lease, err := c.Lease(ctx, untrustedRunner.ID, untrustedToken)
	if err != nil {
		t.Fatalf("untrusted lease: %v", err)
	}
	if lease == nil {
		t.Fatal("untrusted run found no work")
	}
	driveToTerminal(t, c, lease, "passed", "", 0)
	mustRelay(t, c)
	if len(delivered) != 1 {
		t.Fatalf("untrusted pass delivered %d rows, want 1", len(delivered))
	}
	trusted := mustCreateRun(t, c, spec, "trusted", "push:main:t2")
	_ = trusted
	runnerID, token := mustRunner(t, c, "trusted")
	lease2, err := c.Lease(ctx, runnerID, token)
	if err != nil {
		t.Fatalf("trusted lease: %v", err)
	}
	if lease2 == nil {
		t.Fatal("untrusted pass poisoned the trusted cache")
	}
}

func TestCISupersedeCancelsQueued(t *testing.T) {
	ctx := t.Context()
	c, _ := openCI(t, startClock())
	spec := mustDecode(t, baseSpecTOML())
	mkPush := func(key string) ci.Run {
		run, err := c.CreateRun(ctx, ci.CreateRunRequest{
			RepoID: 7, Principal: "ada", Subject: "0123456789abcdef0123456789abcdef01234567",
			Tree: "tree-one", BaseCommit: "base-one", SpecFrom: "base",
			Trust: "trusted", TriggerKey: key, Group: "push:main", Class: "push", Spec: spec,
		})
		if err != nil {
			t.Fatalf("create %s: %v", key, err)
		}
		return run
	}
	first := mkPush("push:main:tip")
	_ = mkPush("push:main:tip2")
	jobs := runJobs(t, c, first)
	for _, job := range jobs {
		if job.State != "cancelled" {
			t.Fatalf("superseded job %s = %q, want cancelled", job.Spec.Name, job.State)
		}
	}
}

func TestCITriggerDedup(t *testing.T) {
	c2, _ := openCI(t, startClock())
	spec := mustDecode(t, baseSpecTOML())
	first := mustCreateRun(t, c2, spec, "trusted", "same:key")
	second := mustCreateRun(t, c2, spec, "trusted", "same:key")
	if first.ID != second.ID {
		t.Fatal("duplicate trigger created a second run")
	}
	jobs := runJobs(t, c2, first)
	if len(jobs) != 2 {
		t.Fatalf("deduped run holds %d jobs, want 2", len(jobs))
	}
}

func TestCIExclusiveNotSignalled(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	c, _ := openCI(t, clk)
	text := `schema = "rukie.ci/v1"

[[job]]
name = "deploy"
runs_on = ["linux-amd64"]
image = "` + testImage + `"
effects = "exclusive"
retry = { max = 0, on = [] }
steps = [{ name = "s", run = "echo hi" }]
`
	run := mustCreateRun(t, c, mustDecode(t, text), "trusted", "change:ex:tip")
	runnerID, token := mustRunner(t, c, "trusted")
	lease := mustLease(t, c, runnerID, token)
	mustStart(t, c, lease)
	if err := c.Cancel(ctx, run.ID, "superseded"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	_, jobs, err := c.Run(ctx, run.ID)
	if err != nil {
		t.Fatalf("run get: %v", err)
	}
	if jobs[0].State != "running" {
		t.Fatalf("exclusive job = %q, want running", jobs[0].State)
	}
	attempt, _, _, err := c.GetAttempt(ctx, lease.Attempt.ID)
	if err != nil {
		t.Fatalf("get attempt: %v", err)
	}
	if attempt.CancelReason != "" {
		t.Fatal("exclusive attempt was signalled")
	}
}

func TestCIExclusiveLostNeedsAck(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	c, _ := openCI(t, clk)
	text := `schema = "rukie.ci/v1"

[[job]]
name = "deploy"
runs_on = ["linux-amd64"]
image = "` + testImage + `"
effects = "exclusive"
retry = { max = 0, on = [] }
steps = [{ name = "s", run = "echo hi" }]
`
	run := mustCreateRun(t, c, mustDecode(t, text), "trusted", "change:exa:tip")
	runnerID, token := mustRunner(t, c, "trusted")
	lease := mustLease(t, c, runnerID, token)
	mustStart(t, c, lease)
	clk.Advance(200 * time.Second)
	if err := c.Reap(ctx); err != nil {
		t.Fatalf("reap: %v", err)
	}
	jobs := runJobs(t, c, run)
	if jobs["deploy"].State != "errored" {
		t.Fatalf("lost exclusive job = %q, want errored", jobs["deploy"].State)
	}
	if _, err := c.RetryRun(ctx, run.ID, nil, false); ci.CodeOf(err) != "E_FORBIDDEN" {
		t.Fatalf("unacknowledged exclusive retry = %v, want E_FORBIDDEN", err)
	}
	retry, err := c.RetryRun(ctx, run.ID, nil, true)
	if err != nil {
		t.Fatalf("acknowledged retry: %v", err)
	}
	retryJobs := runJobs(t, c, retry)
	if retryJobs["deploy"].State != "queued" {
		t.Fatalf("retried exclusive job = %q, want queued", retryJobs["deploy"].State)
	}
}

func TestCIRetryRunCarriesPassed(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	c, _ := openCI(t, clk)
	run := mustCreateRun(t, c, mustDecode(t, baseSpecTOML()), "trusted", "change:rr:tip")
	runnerID, token := mustRunner(t, c, "trusted")
	lease := mustLease(t, c, runnerID, token)
	if lease.Job.Spec.Name != "lint" {
		t.Fatalf("leased %q, want lint", lease.Job.Spec.Name)
	}
	driveToTerminal(t, c, lease, "passed", "", 0)
	testLease := mustLease(t, c, runnerID, token)
	if testLease.Job.Spec.Name != "test" {
		t.Fatalf("leased %q, want test", testLease.Job.Spec.Name)
	}
	driveToTerminal(t, c, testLease, "failed", "script_failure", 1)
	retry, err := c.RetryRun(ctx, run.ID, nil, false)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	jobs := runJobs(t, c, retry)
	if jobs["lint"].State != "carried" {
		t.Fatalf("passed job = %q, want carried", jobs["lint"].State)
	}
	if jobs["test"].State != "queued" {
		t.Fatalf("failed job = %q, want queued", jobs["test"].State)
	}
	if _, err := c.RetryRun(ctx, retry.ID, nil, false); err == nil {
		t.Fatal("retry of a non-terminal run was accepted")
	}
}

func TestCISecretsReachOnlyTrustedScope(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	c, _ := openCI(t, clk)
	text := `schema = "rukie.ci/v1"

[[job]]
name = "deploy"
runs_on = ["linux-amd64"]
image = "` + testImage + `"
secrets = ["DEPLOY_TOKEN"]
steps = [{ name = "s", run = "echo hi" }]
`
	spec := mustDecode(t, text)
	if err := c.CreateSecret(ctx, 7, "DEPLOY_TOKEN", []byte("s3cr3t")); err != nil {
		t.Fatalf("create secret: %v", err)
	}
	run := mustCreateRun(t, c, spec, "trusted", "change:sec:tip")
	untrustedRunner, untrustedToken, err := c.CreateRunner(ctx, []string{"linux-amd64"}, "untrusted", 2, "podman-rootless/1", nil)
	if err != nil {
		t.Fatalf("untrusted runner: %v", err)
	}
	if _, lerr := c.Lease(ctx, untrustedRunner.ID, untrustedToken); lerr != nil {
		t.Fatalf("untrusted lease: %v", lerr)
	}
	_ = run
	runnerID, token := mustRunner(t, c, "trusted")
	lease, err := c.Lease(ctx, runnerID, token)
	if err != nil {
		t.Fatalf("trusted lease: %v", err)
	}
	if lease == nil {
		t.Fatal("trusted runner found no secret job")
	}
	if len(lease.Secrets) != 1 || lease.Secrets[0].Name != "DEPLOY_TOKEN" || lease.Secrets[0].Version != 1 {
		t.Fatalf("sealed secrets = %+v", lease.Secrets)
	}
	if string(lease.Secrets[0].Ciphertext) == "s3cr3t" {
		t.Fatal("secret travels in plaintext")
	}
}

func TestCILeaseFailsWithoutSecretValue(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	c, _ := openCI(t, clk)
	text := `schema = "rukie.ci/v1"

[[job]]
name = "deploy"
runs_on = ["linux-amd64"]
image = "` + testImage + `"
secrets = ["MISSING"]
steps = [{ name = "s", run = "echo hi" }]
`
	_ = mustCreateRun(t, c, mustDecode(t, text), "trusted", "change:nosec:tip")
	runnerID, token := mustRunner(t, c, "trusted")
	if _, err := c.Lease(ctx, runnerID, token); ci.CodeOf(err) != "E_NOT_FOUND" {
		t.Fatalf("lease without a secret value = %v, want E_NOT_FOUND", err)
	}
}

func TestCIRefusesUntrustedSpecOverride(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	c, _ := openCI(t, clk)
	spec := mustDecode(t, baseSpecTOML())
	_, err := c.CreateRun(ctx, ci.CreateRunRequest{
		RepoID: 7, Principal: "mallory", Subject: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Tree: "t", BaseCommit: "b",
		SpecFrom: "tree", Trust: "untrusted", TriggerKey: "change:evil:tip", Spec: spec,
	})
	if ci.CodeOf(err) != "E_FORBIDDEN" {
		t.Fatalf("untrusted tree override = %v, want E_FORBIDDEN", err)
	}
}

func TestCIRejectsBadRunnerRegistration(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	c, _ := openCI(t, clk)
	if _, _, err := c.CreateRunner(ctx, nil, "trusted", 2, "p", nil); ci.CodeOf(err) != "E_INVALID" {
		t.Fatalf("empty labels = %v, want E_INVALID", err)
	}
	if _, _, err := c.CreateRunner(ctx, []string{"linux-amd64"}, "odd", 2, "p", nil); ci.CodeOf(err) != "E_INVALID" {
		t.Fatalf("bad scope = %v, want E_INVALID", err)
	}
	if _, _, err := c.CreateRunner(ctx, []string{"linux-amd64"}, "trusted", 0, "p", nil); ci.CodeOf(err) != "E_INVALID" {
		t.Fatalf("zero capacity = %v, want E_INVALID", err)
	}
	if _, _, err := c.CreateRunner(ctx, []string{"linux-amd64"}, "trusted", 2, "p", []byte("short")); ci.CodeOf(err) != "E_INVALID" {
		t.Fatalf("short pubkey = %v, want E_INVALID", err)
	}
	if err := c.RevokeRunner(ctx, "rnr_missing"); ci.CodeOf(err) != "E_NOT_FOUND" {
		t.Fatalf("revoke unknown = %v, want E_NOT_FOUND", err)
	}
}

func TestCIRejectsBadTokens(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	c, _ := openCI(t, clk)
	_ = mustCreateRun(t, c, mustDecode(t, baseSpecTOML()), "trusted", "change:tok:tip")
	runnerID, _ := mustRunner(t, c, "trusted")
	if _, err := c.Lease(ctx, runnerID, "wrong"); ci.CodeOf(err) != "E_UNAUTHENTICATED" {
		t.Fatalf("wrong runner token = %v, want E_UNAUTHENTICATED", err)
	}
	if _, _, _, err := c.GetAttempt(ctx, "att_missing"); ci.CodeOf(err) != "E_NOT_FOUND" {
		t.Fatalf("unknown attempt = %v, want E_NOT_FOUND", err)
	}
	if _, err := c.Start(ctx, "att_missing", "token", 1); ci.CodeOf(err) != "E_NOT_FOUND" {
		t.Fatalf("unknown start = %v, want E_NOT_FOUND", err)
	}
}

func TestCICompleteBeforeStart(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	c, _ := openCI(t, clk)
	_ = mustCreateRun(t, c, mustDecode(t, baseSpecTOML()), "trusted", "change:early:tip")
	runnerID, token := mustRunner(t, c, "trusted")
	lease := mustLease(t, c, runnerID, token)
	if _, err := c.Heartbeat(ctx, lease.Attempt.ID, lease.Token, lease.Attempt.Fence); ci.CodeOf(err) != "E_NOT_STARTED" {
		t.Fatalf("heartbeat before start = %v, want E_NOT_STARTED", err)
	}
	early := ci.FinishRequest{Fence: lease.Attempt.Fence, Outcome: "passed", ResultDigest: "sha256:" + strings.Repeat("0", 64)}
	if _, err := c.Complete(ctx, lease.Attempt.ID, lease.Token, early); ci.CodeOf(err) != "E_NOT_STARTED" {
		t.Fatalf("finish before start = %v, want E_NOT_STARTED", err)
	}
	mustStart(t, c, lease)
	if err := c.Release(ctx, lease.Attempt.ID, lease.Token, lease.Attempt.Fence, "shutdown"); ci.CodeOf(err) != "E_NOT_STARTED" {
		t.Fatalf("release after start = %v, want E_NOT_STARTED", err)
	}
}

func TestCIReleaseReturnsQueued(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	c, _ := openCI(t, clk)
	run := mustCreateRun(t, c, mustDecode(t, baseSpecTOML()), "trusted", "change:rel:tip")
	runnerID, token := mustRunner(t, c, "trusted")
	lease := mustLease(t, c, runnerID, token)
	if err := c.Release(ctx, lease.Attempt.ID, lease.Token, lease.Attempt.Fence, "shutdown"); err != nil {
		t.Fatalf("release: %v", err)
	}
	jobs := runJobs(t, c, run)
	var queued bool
	for _, job := range jobs {
		if job.State == "queued" {
			queued = true
		}
	}
	if !queued {
		t.Fatal("released job did not return to queued")
	}
}

func TestCILogViolationMatrix(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	c, _ := openCI(t, clk)
	_ = mustCreateRun(t, c, mustDecode(t, baseSpecTOML()), "trusted", "change:log:tip")
	runnerID, token := mustRunner(t, c, "trusted")
	lease := mustLease(t, c, runnerID, token)
	mustStart(t, c, lease)
	gap := ci.LogAppend{Fence: lease.Attempt.Fence, Seq: 1, Offset: 0, SHA256: ci.Digest([]byte("x")), Data: []byte("x")}
	if _, err := c.AppendLogs(ctx, lease.Attempt.ID, lease.Token, gap); ci.CodeOf(err) != "E_OFFSET_GAP" {
		t.Fatalf("seq gap = %v, want E_OFFSET_GAP", err)
	}
	good := ci.LogAppend{Fence: lease.Attempt.Fence, Seq: 0, Offset: 0, SHA256: ci.Digest([]byte("x")), Data: []byte("x")}
	if _, err := c.AppendLogs(ctx, lease.Attempt.ID, lease.Token, good); err != nil {
		t.Fatalf("first append: %v", err)
	}
	conflict := ci.LogAppend{Fence: lease.Attempt.Fence, Seq: 0, Offset: 0, SHA256: ci.Digest([]byte("y")), Data: []byte("y")}
	if _, err := c.AppendLogs(ctx, lease.Attempt.ID, lease.Token, conflict); ci.CodeOf(err) != "E_SEQ_CONFLICT" {
		t.Fatalf("seq conflict = %v, want E_SEQ_CONFLICT", err)
	}
	badDigest := ci.LogAppend{Fence: lease.Attempt.Fence, Seq: 1, Offset: 1, SHA256: ci.Digest([]byte("zzz")), Data: []byte("y")}
	if _, err := c.AppendLogs(ctx, lease.Attempt.ID, lease.Token, badDigest); ci.CodeOf(err) != "E_DIGEST_MISMATCH" {
		t.Fatalf("bad digest = %v, want E_DIGEST_MISMATCH", err)
	}
	big := ci.LogAppend{Fence: lease.Attempt.Fence, Seq: 1, Offset: 1, SHA256: ci.Digest(make([]byte, 65537)), Data: make([]byte, 65537)}
	if _, err := c.AppendLogs(ctx, lease.Attempt.ID, lease.Token, big); ci.CodeOf(err) != "E_LIMIT" {
		t.Fatalf("oversize chunk = %v, want E_LIMIT", err)
	}
}

func TestCIFinishViolationMatrix(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	c, delivered := openCI(t, clk)
	_ = delivered
	_ = mustCreateRun(t, c, mustDecode(t, baseSpecTOML()), "trusted", "change:fin:tip")
	runnerID, token := mustRunner(t, c, "trusted")
	lease := mustLease(t, c, runnerID, token)
	mustStart(t, c, lease)
	missing := ci.FinishRequest{Fence: lease.Attempt.Fence, Outcome: "passed", ResultDigest: "x"}
	if _, err := c.Complete(ctx, lease.Attempt.ID, lease.Token, missing); ci.CodeOf(err) != "E_DIGEST_MISMATCH" {
		t.Fatalf("missing digest = %v, want E_DIGEST_MISMATCH", err)
	}
	attempt, _, _, err := c.GetAttempt(ctx, lease.Attempt.ID)
	if err != nil {
		t.Fatalf("get attempt: %v", err)
	}
	incomplete := ci.FinishRequest{
		Fence: lease.Attempt.Fence, Outcome: "passed", LogSeq: attempt.LogSeq + 1,
		LogEnd: attempt.LogBase + int64(len(attempt.Log)), LogDigest: ci.Digest(attempt.Log),
	}
	incomplete.ResultDigest, _ = ci.FinishDigest(incomplete)
	if _, err := c.Complete(ctx, lease.Attempt.ID, lease.Token, incomplete); ci.CodeOf(err) != "E_LOG_INCOMPLETE" {
		t.Fatalf("incomplete log = %v, want E_LOG_INCOMPLETE", err)
	}
	ghost := ci.Artifact{Name: "out/coverage.out", Digest: "sha256:" + strings.Repeat("f", 64), Size: 8}
	ghostReq := ci.FinishRequest{
		Fence: lease.Attempt.Fence, Outcome: "passed",
		LogSeq: attempt.LogSeq, LogEnd: attempt.LogBase + int64(len(attempt.Log)), LogDigest: ci.Digest(attempt.Log),
		Artifacts: []ci.Artifact{ghost},
	}
	ghostReq.ResultDigest, _ = ci.FinishDigest(ghostReq)
	if _, err := c.Complete(ctx, lease.Attempt.ID, lease.Token, ghostReq); ci.CodeOf(err) != "E_BLOB_MISSING" {
		t.Fatalf("missing blob = %v, want E_BLOB_MISSING", err)
	}
}

func TestCIStepEventsOrdered(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	c, _ := openCI(t, clk)
	text := `schema = "rukie.ci/v1"

[[job]]
name = "steps"
runs_on = ["linux-amd64"]
image = "` + testImage + `"
steps = [{ name = "build", run = "go build ./..." }, { name = "test", run = "go test ./..." }]
`
	_ = mustCreateRun(t, c, mustDecode(t, text), "trusted", "change:steps:tip")
	runnerID, token := mustRunner(t, c, "trusted")
	lease := mustLease(t, c, runnerID, token)
	mustStart(t, c, lease)
	bad := ci.LogAppend{
		Fence: lease.Attempt.Fence, Seq: 0, Offset: 0,
		SHA256: ci.Digest([]byte("x")), Data: []byte("x"),
		Events: []ci.LogEvent{{Kind: "step_finished", Index: 1, Offset: 0, ExitCode: 0}},
	}
	if _, err := c.AppendLogs(ctx, lease.Attempt.ID, lease.Token, bad); ci.CodeOf(err) != "E_INVALID" {
		t.Fatalf("finish before start event = %v, want E_INVALID", err)
	}
	mustAppend(t, c, lease, 0, "building\n", []ci.LogEvent{{Kind: "step_started", Index: 0, Offset: 0}})
	mustAppend(t, c, lease, 1, "built\n", []ci.LogEvent{{Kind: "step_finished", Index: 0, Offset: 15}, {Kind: "step_started", Index: 1, Offset: 15}})
	attempt, _, _, err := c.GetAttempt(ctx, lease.Attempt.ID)
	if err != nil {
		t.Fatalf("get attempt: %v", err)
	}
	if len(attempt.Steps) != 2 || attempt.Steps[0].State != "passed" || attempt.Steps[1].State != "running" {
		t.Fatalf("steps = %+v, want passed then running", attempt.Steps)
	}
}

func TestCISingleFlightFollowersWait(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	st := tempStore(t)
	var delivered []deliveredRow
	c := openCIOn(t, clk, st, &delivered)
	spec := mustDecode(t, singleJobSpecTOML())
	first := mustCreateRun(t, c, spec, "trusted", "push:main:first")
	second := mustCreateRun(t, c, spec, "trusted", "push:main:second")
	_ = first
	_ = second
	runnerA, tokenA := mustRunner(t, c, "trusted")
	runnerB, tokenB := mustRunner(t, c, "trusted")
	leaseA := mustLease(t, c, runnerA, tokenA)
	if leaseA.Job.RunID == second.ID {
		t.Fatal("first lease skipped the earlier run")
	}
	leaseB, err := c.Lease(ctx, runnerB, tokenB)
	if err != nil {
		t.Fatalf("second lease: %v", err)
	}
	if leaseB != nil {
		t.Fatal("follower executed while the leader holds the digest")
	}
	jobs := runJobs(t, c, second)
	var waiting bool
	for _, job := range jobs {
		if job.WaitReason == "single_flight" {
			waiting = true
		}
	}
	if !waiting {
		t.Fatal("follower lacks the single_flight wait reason")
	}
}

func TestCIFairQueuePrefersLowestVtime(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	st := tempStore(t)
	var delivered []deliveredRow
	c := openCIOn(t, clk, st, &delivered)
	spec := mustDecode(t, singleJobSpecTOML())
	mkRun := func(principal, key string) ci.Run {
		run, err := c.CreateRun(ctx, ci.CreateRunRequest{
			RepoID: 7, Principal: principal, Subject: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Tree: "tree-fair",
			BaseCommit: "b", SpecFrom: "base", Trust: "trusted",
			TriggerKey: key, Class: "interactive", Spec: spec,
		})
		if err != nil {
			t.Fatalf("create run: %v", err)
		}
		return run
	}
	runA := mkRun("ada", "fair:a")
	runB := mkRun("mallory", "fair:b")
	runnerID, token := mustRunner(t, c, "trusted")
	first, err := c.Lease(ctx, runnerID, token)
	if err != nil || first == nil {
		t.Fatalf("first lease: %v", err)
	}
	if first.Job.RunID != runA.ID {
		t.Fatalf("first lease went to %q, want the earlier run", first.Job.RunID)
	}
	_ = runB
}

func TestCIClockJumpRebasesLeases(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	var mono int64
	st := tempStore(t)
	var delivered []deliveredRow
	c := ci.NewController(st, ci.Config{
		Now: clk.Now, Mono: func() int64 { return mono },
		SignKey: testPriv, SealKey: testSeal,
		Deliver: func(context.Context, ci.Outbox, string) (string, error) { return "live", nil },
	})
	if err := c.Recover(ctx); err != nil {
		t.Fatalf("recover: %v", err)
	}
	_ = delivered
	_ = mustCreateRun(t, c, mustDecode(t, baseSpecTOML()), "trusted", "change:clock:tip")
	runnerID, token := mustRunner(t, c, "trusted")
	lease := mustLease(t, c, runnerID, token)
	mustStart(t, c, lease)
	clk.Advance(10 * time.Second)
	mono += int64(10 * time.Second)
	if err := c.Reap(ctx); err != nil {
		t.Fatalf("reap: %v", err)
	}
	clk.Advance(10 * time.Second)
	mono += int64(10 * time.Second)
	if rerr := c.Reap(ctx); rerr != nil {
		t.Fatalf("reap without jump: %v", rerr)
	}
	attempt, _, _, err := c.GetAttempt(ctx, lease.Attempt.ID)
	if err != nil {
		t.Fatalf("get attempt: %v", err)
	}
	if attempt.RebasedAt != 0 {
		t.Fatal("steady clock rebased the lease")
	}
	clk.Advance(10 * time.Second)
	if rerr := c.Reap(ctx); rerr != nil {
		t.Fatalf("reap across the jump: %v", rerr)
	}
	attempt, _, _, err = c.GetAttempt(ctx, lease.Attempt.ID)
	if err != nil {
		t.Fatalf("get attempt: %v", err)
	}
	if attempt.RebasedAt == 0 {
		t.Fatal("a 10s clock jump did not rebase the lease")
	}
	if _, err := c.Heartbeat(ctx, lease.Attempt.ID, lease.Token, lease.Attempt.Fence); err != nil {
		t.Fatalf("heartbeat after jump: %v", err)
	}
}

func TestCIOutboxSignedAtFixed(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	var payloads [][]byte
	st := tempStore(t)
	var delivered []deliveredRow
	first := true
	c := ci.NewController(st, ci.Config{
		Now: clk.Now, Mono: func() int64 { return clk.now.UnixNano() },
		SignKey: testPriv, SealKey: testSeal,
		Deliver: func(_ context.Context, row ci.Outbox, _ string) (string, error) {
			payloads = append(payloads, row.Payload)
			if first {
				first = false
				return "", &ci.Problem{Code: "E_CAPACITY"}
			}
			return "live", nil
		},
	})
	if err := c.Recover(ctx); err != nil {
		t.Fatalf("recover: %v", err)
	}
	_ = delivered
	_ = mustCreateRun(t, c, mustDecode(t, singleJobSpecTOML()), "trusted", "push:main:fixed")
	runnerID, token := mustRunner(t, c, "trusted")
	lease := mustLease(t, c, runnerID, token)
	driveToTerminal(t, c, lease, "passed", "", 0)
	rows, err := c.PendingOutbox(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("pending = %d, %v; want 1", len(rows), err)
	}
	if !strings.HasSuffix(rows[0].Statement.SignedAt, "Z") {
		t.Fatalf("signed_at = %q, want whole-second UTC", rows[0].Statement.SignedAt)
	}
	if _, rerr := c.RelayOnce(ctx); rerr != nil {
		t.Fatalf("first relay: %v", rerr)
	}
	clk.Advance(90 * time.Second)
	if _, rerr := c.RelayOnce(ctx); rerr != nil {
		t.Fatalf("second relay: %v", rerr)
	}
	if len(payloads) != 2 {
		t.Fatalf("relay sent %d payloads, want 2", len(payloads))
	}
	if string(payloads[0]) != string(payloads[1]) {
		t.Fatal("a retry relayed different payload bytes")
	}
	rows, err = c.PendingOutbox(ctx)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(rows) != 0 {
		t.Fatal("delivered row is still pending")
	}
}

func TestCIRelayDeadLettersForbidden(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	st := tempStore(t)
	var delivered []deliveredRow
	c := ci.NewController(st, ci.Config{
		Now: clk.Now, Mono: func() int64 { return clk.now.UnixNano() },
		SignKey: testPriv, SealKey: testSeal,
		Deliver: func(context.Context, ci.Outbox, string) (string, error) { return "", &ci.Problem{Code: "E_FORBIDDEN"} },
	})
	if err := c.Recover(ctx); err != nil {
		t.Fatalf("recover: %v", err)
	}
	_ = delivered
	_ = mustCreateRun(t, c, mustDecode(t, singleJobSpecTOML()), "trusted", "push:main:dead")
	runnerID, token := mustRunner(t, c, "trusted")
	lease := mustLease(t, c, runnerID, token)
	driveToTerminal(t, c, lease, "passed", "", 0)
	if _, err := c.RelayOnce(ctx); err != nil {
		t.Fatalf("relay: %v", err)
	}
	rows, err := c.PendingOutbox(ctx)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(rows) != 0 {
		t.Fatal("forbidden row is still pending")
	}
}

func TestCIRequiredProofs(t *testing.T) {
	spec := mustDecode(t, baseSpecTOML())
	proofs, err := ci.RequiredProofs(spec)
	if err != nil {
		t.Fatalf("required proofs: %v", err)
	}
	if len(proofs) != 1 || !strings.HasPrefix(proofs[0], "ci/test/") {
		t.Fatalf("proofs = %v, want one ci/test kind", proofs)
	}
	if len(proofs[0]) > 3+63+1+64 {
		t.Fatalf("proof kind %q exceeds the attest kind bound", proofs[0])
	}
}

func TestCIStartWindowExpiry(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	c, _ := openCI(t, clk)
	_ = mustCreateRun(t, c, mustDecode(t, baseSpecTOML()), "trusted", "change:window:tip")
	runnerID, token := mustRunner(t, c, "trusted")
	lease := mustLease(t, c, runnerID, token)
	clk.Advance(61 * time.Second)
	if err := c.Reap(ctx); err != nil {
		t.Fatalf("reap: %v", err)
	}
	if _, err := c.Start(ctx, lease.Attempt.ID, lease.Token, lease.Attempt.Fence); ci.CodeOf(err) != "E_LEASE_LOST" {
		t.Fatalf("start past the window = %v, want E_LEASE_LOST", err)
	}
}

func TestCIHeartbeatRevivesSuspect(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	c, _ := openCI(t, clk)
	_ = mustCreateRun(t, c, mustDecode(t, baseSpecTOML()), "trusted", "change:sus:tip")
	runnerID, token := mustRunner(t, c, "trusted")
	lease := mustLease(t, c, runnerID, token)
	mustStart(t, c, lease)
	clk.Advance(31 * time.Second)
	if err := c.Reap(ctx); err != nil {
		t.Fatalf("reap: %v", err)
	}
	attempt, _, _, err := c.GetAttempt(ctx, lease.Attempt.ID)
	if err != nil {
		t.Fatalf("get attempt: %v", err)
	}
	if attempt.State != "suspect" {
		t.Fatalf("attempt = %q, want suspect", attempt.State)
	}
	reply, err := c.Heartbeat(ctx, lease.Attempt.ID, lease.Token, lease.Attempt.Fence)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if reply.State != "started" {
		t.Fatalf("revived state = %q, want started", reply.State)
	}
}

func TestCITimeoutMarksTimedOut(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	st := tempStore(t)
	var delivered []deliveredRow
	c := openCIOn(t, clk, st, &delivered)
	text := `schema = "rukie.ci/v1"

[[job]]
name = "slow"
runs_on = ["linux-amd64"]
image = "` + testImage + `"
timeout = "1m"
steps = [{ name = "s", run = "sleep 600" }]
`
	_ = mustCreateRun(t, c, mustDecode(t, text), "trusted", "change:slow:tip")
	runnerID, token := mustRunner(t, c, "trusted")
	lease := mustLease(t, c, runnerID, token)
	mustStart(t, c, lease)
	clk.Advance(91 * time.Second)
	if err := c.Reap(ctx); err != nil {
		t.Fatalf("reap: %v", err)
	}
	attempt, _, _, err := c.GetAttempt(ctx, lease.Attempt.ID)
	if err != nil {
		t.Fatalf("get attempt: %v", err)
	}
	if attempt.State != "timed_out" {
		t.Fatalf("attempt = %q, want timed_out", attempt.State)
	}
	if pending := mustPending(t, c); pending != 0 {
		t.Fatalf("a timed-out job left %d verdict rows", pending)
	}
}

func TestCICancelTerminalNoop(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	c, _ := openCI(t, clk)
	run := mustCreateRun(t, c, mustDecode(t, baseSpecTOML()), "trusted", "change:noop:tip")
	runnerID, token := mustRunner(t, c, "trusted")
	lease := mustLease(t, c, runnerID, token)
	driveToTerminal(t, c, lease, "passed", "", 0)
	testLease := mustLease(t, c, runnerID, token)
	driveToTerminal(t, c, testLease, "passed", "", 0)
	_, jobs, err := c.Run(ctx, run.ID)
	if err != nil {
		t.Fatalf("run get: %v", err)
	}
	_ = jobs
	if err := c.Cancel(ctx, run.ID, "late"); err != nil {
		t.Fatalf("cancel of a terminal run: %v", err)
	}
}

func artifactSpecTOML() string {
	return `schema = "rukie.ci/v1"

[land]
required = ["solo"]

[[job]]
name = "solo"
runs_on = ["linux-amd64"]
image = "` + testImage + `"
pure = true
artifacts = ["bundle.zip"]
steps = [{ name = "s", run = "echo hi" }]
`
}

func TestCIRepoBlobIsolatesRepos(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	c, _ := openCI(t, clk)
	run := mustCreateRun(t, c, mustDecode(t, artifactSpecTOML()), "trusted", "push:main:artifacts")
	_ = run
	runnerID, token := mustRunner(t, c, "trusted")
	lease := mustLease(t, c, runnerID, token)
	mustStart(t, c, lease)
	data := []byte("bundle-bytes")
	digest := ci.Digest(data)
	if err := c.PutBlob(ctx, lease.Attempt.ID, lease.Token, lease.Attempt.Fence, digest, data); err != nil {
		t.Fatalf("put blob: %v", err)
	}
	attempt, _, _, err := c.GetAttempt(ctx, lease.Attempt.ID)
	if err != nil {
		t.Fatalf("get attempt: %v", err)
	}
	req := ci.FinishRequest{
		Fence: lease.Attempt.Fence, Outcome: "passed", Class: "", ExitCode: 0,
		LogSeq: attempt.LogSeq, LogEnd: attempt.LogBase + int64(len(attempt.Log)), LogDigest: ci.Digest(attempt.Log),
		Artifacts: []ci.Artifact{{Name: "bundle.zip", Digest: digest, Size: int64(len(data))}},
	}
	req.ResultDigest, err = ci.FinishDigest(req)
	if err != nil {
		t.Fatalf("finish digest: %v", err)
	}
	if _, err := c.Complete(ctx, lease.Attempt.ID, lease.Token, req); err != nil {
		t.Fatalf("complete: %v", err)
	}
	blob, ok := c.RepoBlob(ctx, 7, digest)
	if !ok {
		t.Fatal("own-repo blob lookup missed")
	}
	if !bytes.Equal(blob, data) {
		t.Fatal("own-repo blob bytes differ")
	}
	if _, ok := c.RepoBlob(ctx, 8, digest); ok {
		t.Fatal("cross-repo blob lookup succeeded")
	}
	if _, ok := c.RepoBlob(ctx, 7, "sha256:"+strings.Repeat("0", 64)); ok {
		t.Fatal("unknown digest lookup succeeded")
	}
}

func TestCIRevokedRunnerIsolated(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	c, _ := openCI(t, clk)
	run := mustCreateRun(t, c, mustDecode(t, singleJobSpecTOML()), "trusted", "push:main:revoke")
	_ = run
	runnerID, token := mustRunner(t, c, "trusted")
	lease := mustLease(t, c, runnerID, token)
	mustStart(t, c, lease)
	if err := c.RevokeRunner(ctx, runnerID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := c.Heartbeat(ctx, lease.Attempt.ID, lease.Token, lease.Attempt.Fence); ci.CodeOf(err) != "E_UNAUTHENTICATED" {
		t.Fatalf("revoked heartbeat = %v, want E_UNAUTHENTICATED", err)
	}
	chunk := ci.LogAppend{Fence: lease.Attempt.Fence, Seq: 0, Offset: 0, SHA256: ci.Digest([]byte("hi")), Data: []byte("hi")}
	if _, err := c.AppendLogs(ctx, lease.Attempt.ID, lease.Token, chunk); ci.CodeOf(err) != "E_UNAUTHENTICATED" {
		t.Fatalf("revoked append = %v, want E_UNAUTHENTICATED", err)
	}
	blobData := []byte("late-bytes")
	if err := c.PutBlob(ctx, lease.Attempt.ID, lease.Token, lease.Attempt.Fence, ci.Digest(blobData), blobData); ci.CodeOf(err) != "E_UNAUTHENTICATED" {
		t.Fatalf("revoked blob = %v, want E_UNAUTHENTICATED", err)
	}
	attempt, _, _, err := c.GetAttempt(ctx, lease.Attempt.ID)
	if err != nil {
		t.Fatalf("get attempt: %v", err)
	}
	req := ci.FinishRequest{
		Fence: lease.Attempt.Fence, Outcome: "passed", Class: "", ExitCode: 0,
		LogSeq: attempt.LogSeq, LogEnd: attempt.LogBase + int64(len(attempt.Log)), LogDigest: ci.Digest(attempt.Log),
	}
	req.ResultDigest, err = ci.FinishDigest(req)
	if err != nil {
		t.Fatalf("finish digest: %v", err)
	}
	if _, cerr := c.Complete(ctx, lease.Attempt.ID, lease.Token, req); ci.CodeOf(cerr) != "E_UNAUTHENTICATED" {
		t.Fatalf("revoked complete = %v, want E_UNAUTHENTICATED", cerr)
	}
	if _, lerr := c.Lease(ctx, runnerID, token); ci.CodeOf(lerr) != "E_UNAUTHENTICATED" {
		t.Fatalf("revoked lease = %v, want E_UNAUTHENTICATED", lerr)
	}
	freshID, freshToken := mustRunner(t, c, "trusted")
	clk.Advance(31 * time.Second)
	followup, err := c.Lease(ctx, freshID, freshToken)
	if err != nil {
		t.Fatalf("followup lease: %v", err)
	}
	if followup == nil {
		t.Fatal("revocation stranded the job")
	}
}

func TestCIWritesRefuseAfterLeaseLoss(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	c, _ := openCI(t, clk)
	run := mustCreateRun(t, c, mustDecode(t, singleJobSpecTOML()), "trusted", "push:main:lost")
	_ = run
	runnerID, token := mustRunner(t, c, "trusted")
	lease := mustLease(t, c, runnerID, token)
	mustStart(t, c, lease)
	mustAppend(t, c, lease, 0, "hello", nil)
	clk.Advance(151 * time.Second)
	chunk := ci.LogAppend{Fence: lease.Attempt.Fence, Seq: 1, Offset: 5, SHA256: ci.Digest([]byte("world")), Data: []byte("world")}
	if _, err := c.AppendLogs(ctx, lease.Attempt.ID, lease.Token, chunk); ci.CodeOf(err) != "E_LEASE_LOST" {
		t.Fatalf("late append = %v, want E_LEASE_LOST", err)
	}
	blobData := []byte("late-bytes")
	if err := c.PutBlob(ctx, lease.Attempt.ID, lease.Token, lease.Attempt.Fence, ci.Digest(blobData), blobData); ci.CodeOf(err) != "E_LEASE_LOST" {
		t.Fatalf("late blob = %v, want E_LEASE_LOST", err)
	}
}
