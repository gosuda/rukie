package runner

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/api"
	"github.com/gosuda/rukie/internal/ci"
	"github.com/gosuda/rukie/internal/store"
)

type crashClock struct{ now time.Time }

func (f *crashClock) Now() time.Time          { return f.now }
func (f *crashClock) Advance(d time.Duration) { f.now = f.now.Add(d) }

func openCrashCI(t *testing.T, clk *crashClock) *ci.Controller {
	t.Helper()
	st, err := store.Open(t.Context(), store.Options{Path: t.TempDir() + "/ci.sqlite"})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	seed := sha256.Sum256([]byte("f6-crash-test"))
	seal := sha256.Sum256([]byte("f6-crash-seal"))
	c := ci.NewController(st, ci.Config{
		Now:     clk.Now,
		Mono:    func() int64 { return clk.now.UnixNano() },
		SignKey: ed25519.NewKeyFromSeed(seed[:]),
		SealKey: seal[:],
		Deliver: func(context.Context, ci.Outbox, string) (string, error) { return "live", nil },
	})
	if err := c.Recover(t.Context()); err != nil {
		t.Fatalf("recover: %v", err)
	}
	return c
}

const crashSpecTOML = `schema = "rukie.ci/v1"

[triggers]
change = true

[[job]]
name = "solo"
runs_on = ["linux-amd64"]
image = "docker.io/library/golang@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
pure = true
retry = { max = 3, on = ["lost"] }
steps = [{ name = "s", run = "echo hi" }]
`

func crashSpec(t *testing.T) ci.Spec {
	t.Helper()
	spec, err := ci.DecodeSpec([]byte(crashSpecTOML))
	if err != nil {
		t.Fatalf("decode spec: %v", err)
	}
	return spec
}

func crashRun(t *testing.T, c *ci.Controller, key string) ci.Run {
	t.Helper()
	run, err := c.CreateRun(t.Context(), ci.CreateRunRequest{
		RepoID: 7, Principal: "ada", Subject: "0123456789abcdef0123456789abcdef01234567",
		Tree: "tree-one", BaseCommit: "base-one", SpecFrom: "base",
		Trust: "trusted", TriggerKey: key, Class: "interactive", Spec: crashSpec(t),
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	return run
}

func crashRunner(t *testing.T, c *ci.Controller) (string, string) {
	t.Helper()
	runner, token, err := c.CreateRunner(t.Context(), []string{"linux-amd64"}, "trusted", 2, "podman-rootless/1", nil)
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}
	return runner.ID, token
}

func crashServer(t *testing.T, c *ci.Controller) (*httptest.Server, *Client, string, string) {
	t.Helper()
	id, token := crashRunner(t, c)
	srv := httptest.NewServer(api.Deps{CI: c}.CIRunnerV1(http.NotFoundHandler()))
	t.Cleanup(srv.Close)
	return srv, NewClient(srv.URL, id, token), id, token
}

func crashRevoke(t *testing.T, c *ci.Controller, id string) {
	t.Helper()
	if err := c.RevokeRunner(t.Context(), id); err != nil {
		t.Fatalf("revoke: %v", err)
	}
}

func mustHTTPLease(t *testing.T, client *Client) *Grant {
	t.Helper()
	grant, err := client.Lease(1)
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	if grant == nil {
		t.Fatal("lease returned no work")
	}
	return grant
}

func TestRunnerKillRequeuesWithNewFence(t *testing.T) {
	clk := &crashClock{now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	c := openCrashCI(t, clk)
	crashRun(t, c, "change:kill:tip")
	_, client, _, _ := crashServer(t, c)
	first := mustHTTPLease(t, client)
	if _, err := client.Heartbeat(first.Attempt); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	clk.Advance(200 * time.Second)
	if err := c.Reap(t.Context()); err != nil {
		t.Fatalf("reap: %v", err)
	}
	clk.Advance(31 * time.Second)
	if _, err := client.Heartbeat(first.Attempt); err == nil {
		t.Fatal("lost lease still heartbeats")
	} else if _, ok := errors.AsType[*LeaseLostError](err); !ok {
		t.Fatalf("lost heartbeat = %v, want E_LEASE_LOST", err)
	}
	second := mustHTTPLease(t, client)
	if second.Attempt.Fence == first.Attempt.Fence {
		t.Fatal("requeued lease reuses the fence")
	}
	if _, err := client.Heartbeat(second.Attempt); err != nil {
		t.Fatalf("new heartbeat: %v", err)
	}
	if _, err := client.Finish(first.Attempt, FinishInput{Outcome: "passed", Fence: first.Attempt.Fence}); err == nil {
		t.Fatal("fenced-out finish was accepted")
	} else if _, ok := errors.AsType[*LeaseLostError](err); !ok {
		t.Fatalf("fenced-out finish = %v, want E_LEASE_LOST", err)
	}
}

func TestRunnerCrashMatrix(t *testing.T) {
	clk := &crashClock{now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	c := openCrashCI(t, clk)
	crashRun(t, c, "change:matrix:tip")
	_, client, _, _ := crashServer(t, c)
	first := mustHTTPLease(t, client)
	clk.Advance(200 * time.Second)
	if err := c.Reap(t.Context()); err != nil {
		t.Fatalf("reap: %v", err)
	}
	clk.Advance(31 * time.Second)
	if _, err := client.AppendLog(first.Attempt, 0, 0, []byte("late"), nil); err == nil {
		t.Fatal("lost lease still appends logs")
	} else if _, ok := errors.AsType[*LeaseLostError](err); !ok {
		t.Fatalf("lost append = %v, want E_LEASE_LOST", err)
	}
	second := mustHTTPLease(t, client)
	if second.Attempt.Fence == first.Attempt.Fence {
		t.Fatal("requeued lease reuses the fence")
	}
	if _, err := client.Heartbeat(second.Attempt); err != nil {
		t.Fatalf("requeued heartbeat: %v", err)
	}
	id := runnerIDOf(t, c, second.Attempt.ID)
	crashRevoke(t, c, id)
	if _, err := client.Heartbeat(second.Attempt); err == nil {
		t.Fatal("revoked runner still heartbeats")
	} else if !strings.Contains(err.Error(), "E_UNAUTHENTICATED") {
		t.Fatalf("revoked heartbeat = %v, want E_UNAUTHENTICATED", err)
	}
}

func runnerIDOf(t *testing.T, c *ci.Controller, attemptID string) string {
	t.Helper()
	attempt, _, _, err := c.GetAttempt(t.Context(), attemptID)
	if err != nil {
		t.Fatalf("get attempt: %v", err)
	}
	return attempt.RunnerID
}

func TestRunnerKillAfter200Restart(t *testing.T) {
	clk := &crashClock{now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	c := openCrashCI(t, clk)
	crashRun(t, c, "change:restart:tip")
	srv, client, _, _ := crashServer(t, c)
	_ = srv
	grant := mustHTTPLease(t, client)
	if _, err := client.Heartbeat(grant.Attempt); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	finish := FinishInput{
		Outcome: "passed", Fence: grant.Attempt.Fence,
		LogSHA: Digest(nil), LogSeq: -1, LogEnd: 0,
	}
	first, err := client.Finish(grant.Attempt, finish)
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if first.Replayed {
		t.Fatal("first finish reports replay")
	}
	restarted := NewClient(srv.URL, client.Runner, client.Token)
	second, err := restarted.Finish(grant.Attempt, finish)
	if err != nil {
		t.Fatalf("refinish: %v", err)
	}
	if !second.Replayed {
		t.Fatal("same-digest refinish does not replay")
	}
	conflict := finish
	conflict.Outcome = "failed"
	conflict.Class = "script_failure"
	if _, cerr := restarted.Finish(grant.Attempt, conflict); cerr == nil {
		t.Fatal("different-digest refinish was accepted")
	} else if !strings.Contains(cerr.Error(), "E_RESULT_CONFLICT") {
		t.Fatalf("conflict finish = %v, want E_RESULT_CONFLICT", cerr)
	}
	pending, err := c.PendingOutbox(t.Context())
	if err != nil {
		t.Fatalf("outbox: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("outbox holds %d rows, want exactly one verdict", len(pending))
	}
}

func TestCICacheHitAttestsWithoutRun(t *testing.T) {
	clk := &crashClock{now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	c := openCrashCI(t, clk)
	crashRun(t, c, "change:cache:one")
	_, client, _, _ := crashServer(t, c)
	grant := mustHTTPLease(t, client)
	if _, err := client.Heartbeat(grant.Attempt); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if _, err := client.Finish(grant.Attempt, FinishInput{
		Outcome: "passed", Fence: grant.Attempt.Fence,
		LogSHA: Digest(nil), LogSeq: -1, LogEnd: 0,
	}); err != nil {
		t.Fatalf("finish: %v", err)
	}
	before, err := c.PendingOutbox(t.Context())
	if err != nil {
		t.Fatalf("outbox: %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("outbox holds %d rows, want one verdict", len(before))
	}
	crashRun(t, c, "change:cache:two")
	cached, err := client.Lease(1)
	if err != nil {
		t.Fatalf("second lease: %v", err)
	}
	if cached != nil {
		t.Fatal("cache hit grants a runner")
	}
	_, jobs, err := c.Run(t.Context(), mustRunID(t, c, "change:cache:two"))
	if err != nil {
		t.Fatalf("run get: %v", err)
	}
	hit := false
	for _, job := range jobs {
		if job.State == "cached" {
			hit = true
		}
	}
	if !hit {
		t.Fatal("cache hit did not mark the job cached")
	}
	if derr := c.MarkOutboxDelivered(t.Context(), before[0].ID, before[0].Digest); derr != nil {
		t.Fatalf("deliver: %v", derr)
	}
	after, err := c.PendingOutbox(t.Context())
	if err != nil {
		t.Fatalf("outbox: %v", err)
	}
	if len(after) != 1 || after[0].Chain != before[0].Chain {
		t.Fatalf("cached run left %+v, want one attestation on the same chain", after)
	}
	if after[0].Statement.Supersedes == nil {
		t.Fatal("cached attestation does not supersede the run verdict")
	}
}

func mustRunID(t *testing.T, c *ci.Controller, key string) string {
	t.Helper()
	runs, err := c.Runs(t.Context(), 7)
	if err != nil {
		t.Fatalf("runs: %v", err)
	}
	for _, run := range runs {
		if run.TriggerKey == key {
			return run.ID
		}
	}
	t.Fatalf("run %s not found", key)
	return ""
}
