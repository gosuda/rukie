package ci_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/ci"
)

func startClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
}

func runJobs(t *testing.T, c *ci.Controller, run ci.Run) map[string]ci.Job {
	t.Helper()
	_, jobs, err := c.Run(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("run get: %v", err)
	}
	out := map[string]ci.Job{}
	for _, job := range jobs {
		out[job.Spec.Name] = job
	}
	return out
}

func TestCIRequiresRecoverBeforeLease(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	st := tempStore(t)
	c := ci.NewController(st, ci.Config{Now: clk.Now, Mono: func() int64 { return clk.now.UnixNano() }})
	if _, err := c.Lease(ctx, "rnr_x", "token"); ci.CodeOf(err) != "E_RECOVERING" {
		t.Fatalf("lease before recover = %v, want E_RECOVERING", err)
	}
	if _, err := c.Start(ctx, "att_x", "token", 1); ci.CodeOf(err) != "E_RECOVERING" {
		t.Fatalf("start before recover = %v, want E_RECOVERING", err)
	}
	if _, err := c.Heartbeat(ctx, "att_x", "token", 1); ci.CodeOf(err) != "E_RECOVERING" {
		t.Fatalf("heartbeat before recover = %v, want E_RECOVERING", err)
	}
}

func TestCIDurableRestart(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	st := tempStore(t)
	var delivered []deliveredRow
	c := openCIOn(t, clk, st, &delivered)
	run := mustCreateRun(t, c, mustDecode(t, baseSpecTOML()), "trusted", "change:1:tip")
	runnerID, token := mustRunner(t, c, "trusted")
	lease := mustLease(t, c, runnerID, token)
	mustStart(t, c, lease)
	if _, err := c.Heartbeat(ctx, lease.Attempt.ID, lease.Token, lease.Attempt.Fence); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	before, _, _, err := c.GetAttempt(ctx, lease.Attempt.ID)
	if err != nil {
		t.Fatalf("get attempt: %v", err)
	}
	clk.Advance(45 * time.Second)
	c2 := bareCI(t, clk, st)
	if _, herr := c2.Heartbeat(ctx, lease.Attempt.ID, lease.Token, lease.Attempt.Fence); ci.CodeOf(herr) != "E_RECOVERING" {
		t.Fatalf("heartbeat on unrecovered controller = %v, want E_RECOVERING", herr)
	}
	if rerr := c2.Recover(ctx); rerr != nil {
		t.Fatalf("recover: %v", rerr)
	}
	_ = delivered
	after, _, _, err := c2.GetAttempt(ctx, lease.Attempt.ID)
	if err != nil {
		t.Fatalf("get attempt after restart: %v", err)
	}
	if after.PreRestartHeartbeatAt != before.LastHeartbeatAt {
		t.Fatalf("restart did not preserve the heartbeat: %d vs %d", after.PreRestartHeartbeatAt, before.LastHeartbeatAt)
	}
	if after.LastHeartbeatAt != clk.now.UnixMilli() {
		t.Fatalf("restart did not rebase the heartbeat to boot: %d", after.LastHeartbeatAt)
	}
	if after.LostAt != clk.now.UnixMilli()+150000 {
		t.Fatalf("restart did not rebase lost_at: %d", after.LostAt)
	}
	if _, err := c2.Heartbeat(ctx, lease.Attempt.ID, lease.Token, lease.Attempt.Fence); err != nil {
		t.Fatalf("heartbeat after restart: %v", err)
	}
	_ = run
}

func TestCIBootRecoveryA(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	st := tempStore(t)
	var delivered []deliveredRow
	c := openCIOn(t, clk, st, &delivered)
	_ = mustCreateRun(t, c, mustDecode(t, baseSpecTOML()), "trusted", "change:a:tip")
	runnerID, token := mustRunner(t, c, "trusted")
	lease := mustLease(t, c, runnerID, token)
	clk.Advance(10 * time.Second)
	c2 := openCIOn(t, clk, st, nil)
	reply, err := c2.Start(ctx, lease.Attempt.ID, lease.Token, lease.Attempt.Fence)
	if err != nil {
		t.Fatalf("start after recovery A: %v", err)
	}
	if reply.HeartbeatPersistedAt != clk.now.UnixMilli() {
		t.Fatal("start did not persist the heartbeat")
	}
	st2 := tempStore(t)
	c3 := openCIOn(t, clk, st2, nil)
	_ = mustCreateRun(t, c3, mustDecode(t, baseSpecTOML()), "trusted", "change:a2:tip")
	runner3, token3 := mustRunner(t, c3, "trusted")
	lease3 := mustLease(t, c3, runner3, token3)
	clk.Advance(61 * time.Second)
	if err := c3.Reap(ctx); err != nil {
		t.Fatalf("reap: %v", err)
	}
	if _, err := c3.Start(ctx, lease3.Attempt.ID, lease3.Token, lease3.Attempt.Fence); ci.CodeOf(err) != "E_LEASE_LOST" {
		t.Fatalf("start past the window = %v, want E_LEASE_LOST", err)
	}
	_ = runnerID
	_ = token
}

func TestCIBootRecoveryB(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	c, _ := openCI(t, clk)
	_ = mustCreateRun(t, c, mustDecode(t, baseSpecTOML()), "trusted", "change:b:tip")
	runnerID, token := mustRunner(t, c, "trusted")
	lease := mustLease(t, c, runnerID, token)
	mustStart(t, c, lease)
	clk.Advance(20 * time.Second)
	if err := c.Reap(ctx); err != nil {
		t.Fatalf("reap: %v", err)
	}
	attempt, _, _, err := c.GetAttempt(ctx, lease.Attempt.ID)
	if err != nil {
		t.Fatalf("get attempt: %v", err)
	}
	if attempt.State != "started" {
		t.Fatalf("revived attempt = %q, want started", attempt.State)
	}
	if _, herr := c.Heartbeat(ctx, lease.Attempt.ID, lease.Token, lease.Attempt.Fence); herr != nil {
		t.Fatalf("heartbeat after revive: %v", herr)
	}
	clk.Advance(160 * time.Second)
	if rerr := c.Reap(ctx); rerr != nil {
		t.Fatalf("reap: %v", rerr)
	}
	attempt, _, _, err = c.GetAttempt(ctx, lease.Attempt.ID)
	if err != nil {
		t.Fatalf("get attempt: %v", err)
	}
	if attempt.State != "lost" {
		t.Fatalf("attempt = %q, want lost", attempt.State)
	}
}

func TestCIFinishReplayAndConflict(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	c, _ := openCI(t, clk)
	_ = mustCreateRun(t, c, mustDecode(t, baseSpecTOML()), "trusted", "change:c:tip")
	runnerID, token := mustRunner(t, c, "trusted")
	lease := mustLease(t, c, runnerID, token)
	mustStart(t, c, lease)
	mustAppend(t, c, lease, 0, "ok\n", nil)
	reply, digest := mustFinish(t, c, lease, "passed", "", 0, nil)
	if reply.Replayed {
		t.Fatal("first finish reported replayed")
	}
	attempt, _, _, err := c.GetAttempt(ctx, lease.Attempt.ID)
	if err != nil {
		t.Fatalf("get attempt: %v", err)
	}
	req := ci.FinishRequest{
		Fence: lease.Attempt.Fence, Outcome: "passed", Class: "", ExitCode: 0,
		LogSeq: attempt.LogSeq, LogEnd: int64(len(attempt.Log)), LogDigest: ci.Digest(attempt.Log),
		ResultDigest: digest,
	}
	replayed, err := c.Complete(ctx, lease.Attempt.ID, lease.Token, req)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replayed.Replayed || replayed.Job != reply.Job {
		t.Fatalf("replay = %+v, want replayed with the stored reply", replayed)
	}
	conflicting := req
	conflicting.ResultDigest = "sha256:" + strings.Repeat("0", 64)
	if _, err := c.Complete(ctx, lease.Attempt.ID, lease.Token, conflicting); ci.CodeOf(err) != "E_RESULT_CONFLICT" {
		t.Fatalf("conflicting finish = %v, want E_RESULT_CONFLICT", err)
	}
}

func TestRunnerKillRequeuesWithNewFence(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	c, delivered := openCI(t, clk)
	_ = delivered
	_ = mustCreateRun(t, c, mustDecode(t, baseSpecTOML()), "trusted", "change:kill:tip")
	runnerID, token := mustRunner(t, c, "trusted")
	lease := mustLease(t, c, runnerID, token)
	mustStart(t, c, lease)
	oldFence := lease.Attempt.Fence
	clk.Advance(200 * time.Second)
	if err := c.Reap(ctx); err != nil {
		t.Fatalf("reap: %v", err)
	}
	clk.Advance(31 * time.Second)
	requeued, err := c.Lease(ctx, runnerID, token)
	if err != nil {
		t.Fatalf("re-lease: %v", err)
	}
	if requeued == nil {
		t.Fatal("lost job did not requeue")
	}
	if requeued.Attempt.Fence == oldFence {
		t.Fatal("requeued attempt reuses the fence")
	}
	if _, err := c.Heartbeat(ctx, lease.Attempt.ID, lease.Token, oldFence); ci.CodeOf(err) != "E_LEASE_LOST" {
		t.Fatalf("old heartbeat = %v, want E_LEASE_LOST", err)
	}
	stale := ci.LogAppend{Fence: oldFence, Seq: 0, Offset: 0, SHA256: ci.Digest([]byte("late")), Data: []byte("late")}
	if _, err := c.AppendLogs(ctx, lease.Attempt.ID, lease.Token, stale); ci.CodeOf(err) != "E_LEASE_LOST" {
		t.Fatalf("old log append = %v, want E_LEASE_LOST", err)
	}
	staleFinish := ci.FinishRequest{Fence: oldFence, Outcome: "passed", ResultDigest: "sha256:" + strings.Repeat("0", 64)}
	if _, err := c.Complete(ctx, lease.Attempt.ID, lease.Token, staleFinish); ci.CodeOf(err) != "E_LEASE_LOST" {
		t.Fatalf("old finish = %v, want E_LEASE_LOST", err)
	}
}

func TestCICacheKeyHit(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	c, _ := openCI(t, clk)
	spec := mustDecode(t, baseSpecTOML())
	first := mustCreateRun(t, c, spec, "trusted", "push:main:tip1")
	jobs := runJobs(t, c, first)
	lintDigest := jobs["lint"].ActionDigest
	if lintDigest == "" {
		t.Fatal("lint has no action digest")
	}
	runnerID, token := mustRunner(t, c, "trusted")
	lease := mustLease(t, c, runnerID, token)
	if lease.Job.Spec.Name != "lint" {
		t.Fatalf("leased %q, want lint", lease.Job.Spec.Name)
	}
	mustStart(t, c, lease)
	mustAppend(t, c, lease, 0, "vet ok\n", nil)
	mustFinish(t, c, lease, "passed", "", 0, nil)
	second := mustCreateRun(t, c, spec, "trusted", "push:main:tip2")
	jobs2 := runJobs(t, c, second)
	if jobs2["lint"].ActionDigest != lintDigest {
		t.Fatal("same tree produces a different action digest")
	}
	other := ci.CreateRunRequest{
		RepoID: 7, Principal: "ada", Subject: "fedcba9876543210fedcba9876543210fedcba98",
		Tree: "tree-two", BaseCommit: "base-two", SpecFrom: "base",
		Trust: "trusted", TriggerKey: "push:main:tip4", Class: "interactive", Spec: spec,
	}
	otherRun, err := c.CreateRun(ctx, other)
	if err != nil {
		t.Fatalf("create run on another tree: %v", err)
	}
	otherJobs := runJobs(t, c, otherRun)
	if otherJobs["lint"].ActionDigest == lintDigest {
		t.Fatal("a different tree reuses the action digest")
	}
}

func TestCICacheHitAttestsWithoutRun(t *testing.T) {
	clk := startClock()
	ctx := t.Context()
	c, delivered := openCI(t, clk)
	spec := mustDecode(t, singleJobSpecTOML())
	prime := mustCreateRun(t, c, spec, "trusted", "push:main:prime")
	_ = prime
	runnerID, token := mustRunner(t, c, "trusted")
	lease := mustLease(t, c, runnerID, token)
	mustStart(t, c, lease)
	mustAppend(t, c, lease, 0, "ok\n", nil)
	mustFinish(t, c, lease, "passed", "", 0, nil)
	mustRelay(t, c)
	if len(*delivered) != 1 {
		t.Fatalf("prime run delivered %d rows, want 1", len(*delivered))
	}
	second := mustCreateRun(t, c, spec, "trusted", "push:main:second")
	lease2, err := c.Lease(ctx, runnerID, token)
	if err != nil {
		t.Fatalf("second lease: %v", err)
	}
	if lease2 != nil {
		t.Fatal("cache hit still granted a lease")
	}
	jobs := runJobs(t, c, second)
	var cached bool
	for _, job := range jobs {
		if job.State == "cached" {
			cached = true
		}
		if job.CurrentAttempt != "" {
			t.Fatal("cache hit created an attempt")
		}
	}
	if !cached {
		t.Fatal("no job was marked cached")
	}
	mustRelay(t, c)
	if len(*delivered) != 2 {
		t.Fatalf("cache hit delivered %d rows, want 2", len(*delivered))
	}
	if (*delivered)[1].row.Statement.Result != "pass" {
		t.Fatal("cache hit did not attest a pass")
	}
}

func singleJobSpecTOML() string {
	return `schema = "rukie.ci/v1"

[[job]]
name = "solo"
runs_on = ["linux-amd64"]
image = "` + testImage + `"
pure = true
steps = [{ name = "s", run = "echo hi" }]
`
}
