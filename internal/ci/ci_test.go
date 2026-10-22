package ci_test

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/ci"
	"github.com/gosuda/rukie/internal/store"
)

const testImage = "docker.io/library/golang@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

var (
	testSeed = [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}
	testPriv = ed25519.NewKeyFromSeed(testSeed[:])
	testSeal = []byte("0123456789abcdef0123456789abcdef")
)

type fakeClock struct{ now time.Time }

func (f *fakeClock) Now() time.Time          { return f.now }
func (f *fakeClock) Advance(d time.Duration) { f.now = f.now.Add(d) }

type deliveredRow struct{ row ci.Outbox }

func tempStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.Context(), store.Options{Path: t.TempDir() + "/ci.sqlite"})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	return st
}

func openCIOn(t *testing.T, clk *fakeClock, st *store.Store, delivered *[]deliveredRow) *ci.Controller {
	t.Helper()
	if delivered == nil {
		delivered = &[]deliveredRow{}
	}
	c := ci.NewController(st, ci.Config{
		Now:     clk.Now,
		Mono:    func() int64 { return clk.now.UnixNano() },
		SignKey: testPriv,
		SealKey: testSeal,
		Deliver: func(_ context.Context, row ci.Outbox, _ string) (string, error) {
			*delivered = append(*delivered, deliveredRow{row: row})
			return "live", nil
		},
	})
	if err := c.Recover(t.Context()); err != nil {
		t.Fatalf("recover: %v", err)
	}
	return c
}

func openCI(t *testing.T, clk *fakeClock) (*ci.Controller, *[]deliveredRow) {
	t.Helper()
	st := tempStore(t)
	var delivered []deliveredRow
	return openCIOn(t, clk, st, &delivered), &delivered
}

func bareCI(t *testing.T, clk *fakeClock, st *store.Store) *ci.Controller {
	t.Helper()
	return ci.NewController(st, ci.Config{
		Now:     clk.Now,
		Mono:    func() int64 { return clk.now.UnixNano() },
		SignKey: testPriv,
		SealKey: testSeal,
		Deliver: func(_ context.Context, _ ci.Outbox, _ string) (string, error) { return "live", nil },
	})
}

func baseSpecTOML() string {
	return `schema = "rukie.ci/v1"

[triggers]
change = true

[land]
required = ["test"]

[[job]]
name = "lint"
runs_on = ["linux-amd64"]
image = "` + testImage + `"
pure = true
steps = [{ name = "vet", run = "go vet ./..." }]

[[job]]
name = "test"
runs_on = ["linux-amd64"]
image = "` + testImage + `"
needs = ["lint"]
pure = true
steps = [{ name = "test", run = "go test ./..." }]
`
}

func mustDecode(t *testing.T, text string) ci.Spec {
	t.Helper()
	spec, err := ci.DecodeSpec([]byte(text))
	if err != nil {
		t.Fatalf("decode spec: %v", err)
	}
	return spec
}

func mustCreateRun(t *testing.T, c *ci.Controller, spec ci.Spec, trust, key string) ci.Run {
	t.Helper()
	run, err := c.CreateRun(t.Context(), ci.CreateRunRequest{
		RepoID: 7, Principal: "ada", Subject: "0123456789abcdef0123456789abcdef01234567",
		Tree: "tree-one", BaseCommit: "base-one", SpecFrom: "base",
		Trust: trust, TriggerKey: key, Class: "interactive", Spec: spec,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	return run
}

func mustRunner(t *testing.T, c *ci.Controller, scope string) (string, string) {
	t.Helper()
	id, token, _ := mustRunnerWithKey(t, c, scope)
	return id, token
}

func mustRunnerWithKey(t *testing.T, c *ci.Controller, scope string) (string, string, *ecdh.PrivateKey) {
	t.Helper()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate runner key: %v", err)
	}
	runner, token, err := c.CreateRunner(t.Context(), []string{"linux-amd64"}, scope, 2, "podman-rootless/1", key.PublicKey().Bytes())
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}
	return runner.ID, token, key
}

func mustLease(t *testing.T, c *ci.Controller, id, token string) *ci.Lease {
	t.Helper()
	lease, err := c.Lease(t.Context(), id, token)
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	if lease == nil {
		t.Fatal("lease returned no work")
	}
	return lease
}

func mustStart(t *testing.T, c *ci.Controller, lease *ci.Lease) {
	t.Helper()
	if _, err := c.Start(t.Context(), lease.Attempt.ID, lease.Token, lease.Attempt.Fence); err != nil {
		t.Fatalf("start: %v", err)
	}
}

func mustAppend(t *testing.T, c *ci.Controller, lease *ci.Lease, seq int64, data string, events []ci.LogEvent) {
	t.Helper()
	offset := int64(0)
	if seq > 0 {
		attempt, _, _, err := c.GetAttempt(t.Context(), lease.Attempt.ID)
		if err != nil {
			t.Fatalf("get attempt: %v", err)
		}
		offset = attempt.LogBase + int64(len(attempt.Log))
	}
	chunk := ci.LogAppend{Fence: lease.Attempt.Fence, Seq: seq, Offset: offset, SHA256: ci.Digest([]byte(data)), Data: []byte(data), Events: events}
	if _, err := c.AppendLogs(t.Context(), lease.Attempt.ID, lease.Token, chunk); err != nil {
		t.Fatalf("append logs: %v", err)
	}
}

func mustFinish(t *testing.T, c *ci.Controller, lease *ci.Lease, outcome, class string, exit int, artifacts []ci.Artifact) (ci.FinishReply, string) {
	t.Helper()
	ctx := t.Context()
	attempt, _, _, err := c.GetAttempt(ctx, lease.Attempt.ID)
	if err != nil {
		t.Fatalf("get attempt: %v", err)
	}
	req := ci.FinishRequest{
		Fence: lease.Attempt.Fence, Outcome: outcome, Class: class, ExitCode: exit,
		LogSeq: attempt.LogSeq, LogEnd: attempt.LogBase + int64(len(attempt.Log)), LogDigest: ci.Digest(attempt.Log),
		Artifacts: artifacts,
	}
	digest, err := ci.FinishDigest(req)
	if err != nil {
		t.Fatalf("finish digest: %v", err)
	}
	req.ResultDigest = digest
	reply, err := c.Complete(ctx, lease.Attempt.ID, lease.Token, req)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	return reply, digest
}

func mustRelay(t *testing.T, c *ci.Controller) int {
	t.Helper()
	delivered, err := c.RelayOnce(t.Context())
	if err != nil {
		t.Fatalf("relay: %v", err)
	}
	return delivered
}

func mustPending(t *testing.T, c *ci.Controller) int {
	t.Helper()
	rows, err := c.PendingOutbox(t.Context())
	if err != nil {
		t.Fatalf("pending outbox: %v", err)
	}
	return len(rows)
}
