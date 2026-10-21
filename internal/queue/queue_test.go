package queue

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/store"
)

func openQueue(t *testing.T, now *time.Time) (*Queue, *store.Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	st, err := store.Open(ctx, store.Options{Path: t.TempDir() + "/queue.sqlite"})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	clock := time.Now
	if now != nil {
		clock = func() time.Time { return *now }
	}
	return Open(st, clock), st
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestKindSetHoldsSeven(t *testing.T) {
	if len(AllKinds()) != 7 {
		t.Fatalf("kinds = %d, want 7", len(AllKinds()))
	}
	for _, kind := range AllKinds() {
		if !ValidKind(kind) {
			t.Fatalf("kind %q not valid", string(kind))
		}
	}
	if ValidKind("webhook_delivery") {
		t.Fatal("unlisted kind validated")
	}
}

func TestEnqueueRejectsUnknownKind(t *testing.T) {
	q, _ := openQueue(t, nil)
	inserted, err := q.Enqueue(testCtx(t), EnqueueSpec{ID: "x", Kind: "task_run", RepoID: 1, Payload: `{}`})
	if err == nil {
		t.Fatal("unknown kind enqueued")
	}
	if inserted {
		t.Fatal("unknown kind reported inserted")
	}
}

func TestEnqueueDedupeDeterministicID(t *testing.T) {
	q, _ := openQueue(t, nil)
	ctx := testCtx(t)
	spec := EnqueueSpec{ID: "mirror-run:evt-1", Kind: KindMirrorRun, RepoID: 3, Payload: `{"repo":3}`}
	first, err := q.Enqueue(ctx, spec)
	if err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	if !first {
		t.Fatal("first enqueue reported duplicate")
	}
	second, err := q.Enqueue(ctx, spec)
	if err != nil {
		t.Fatalf("second enqueue: %v", err)
	}
	if second {
		t.Fatal("second enqueue reported inserted")
	}
	job, err := q.Job(ctx, spec.ID)
	if err != nil {
		t.Fatalf("job: %v", err)
	}
	if job.State != "queued" || job.Attempts != 0 || job.RepoID != 3 || string(job.Kind) != string(KindMirrorRun) {
		t.Fatalf("job = %+v", job)
	}
}

func TestEnqueueValidation(t *testing.T) {
	q, _ := openQueue(t, nil)
	ctx := testCtx(t)
	cases := []EnqueueSpec{
		{ID: "", Kind: KindLand, RepoID: 1, Payload: `{}`},
		{ID: "a", Kind: KindLand, RepoID: 0, Payload: `{}`},
		{ID: "a", Kind: KindLand, RepoID: 1, Payload: `not json`},
		{ID: "a", Kind: KindLand, RepoID: 1, Payload: ``},
	}
	for i, spec := range cases {
		if _, err := q.Enqueue(ctx, spec); err == nil {
			t.Fatalf("case %d enqueued", i)
		}
	}
}

func TestClaimLeaseAndExpiry(t *testing.T) {
	now := time.UnixMilli(1700000000000)
	q, _ := openQueue(t, &now)
	ctx := testCtx(t)
	for _, id := range []string{"a", "b"} {
		if _, err := q.Enqueue(ctx, EnqueueSpec{ID: id, Kind: KindCIPlan, RepoID: 1, Payload: `{}`}); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}
	claimed, err := q.Claim(ctx, "w1", time.Minute, 10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 2 {
		t.Fatalf("claimed %d, want 2", len(claimed))
	}
	for _, job := range claimed {
		if job.LeaseOwner != "w1" || job.State != "leased" {
			t.Fatalf("job = %+v", job)
		}
	}
	again, err := q.Claim(ctx, "w2", time.Minute, 10)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("claimed %d leased jobs, want 0", len(again))
	}
	now = now.Add(61 * time.Second)
	expired, err := q.Claim(ctx, "w2", time.Minute, 10)
	if err != nil {
		t.Fatalf("expired claim: %v", err)
	}
	if len(expired) != 2 {
		t.Fatalf("reclaimed %d, want 2", len(expired))
	}
}

func TestClaimLimitRespected(t *testing.T) {
	q, _ := openQueue(t, nil)
	ctx := testCtx(t)
	for _, id := range []string{"a", "b", "c"} {
		if _, err := q.Enqueue(ctx, EnqueueSpec{ID: id, Kind: KindLand, RepoID: 1, Payload: `{}`}); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	first, err := q.Claim(ctx, "w1", time.Minute, 2)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("claimed %d, want 2", len(first))
	}
	second, err := q.Claim(ctx, "w1", time.Minute, 2)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("claimed %d, want 1", len(second))
	}
}

func TestClaimValidation(t *testing.T) {
	q, _ := openQueue(t, nil)
	ctx := testCtx(t)
	if _, err := q.Claim(ctx, "", time.Minute, 1); err == nil {
		t.Fatal("empty owner claimed")
	}
	if _, err := q.Claim(ctx, "w", 0, 1); err == nil {
		t.Fatal("zero lease claimed")
	}
	if _, err := q.Claim(ctx, "w", time.Minute, 0); err == nil {
		t.Fatal("zero limit claimed")
	}
}

func TestRenewOwnership(t *testing.T) {
	now := time.UnixMilli(1700000000000)
	q, _ := openQueue(t, &now)
	ctx := testCtx(t)
	if _, err := q.Enqueue(ctx, EnqueueSpec{ID: "a", Kind: KindLand, RepoID: 1, Payload: `{}`}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	claimed, err := q.Claim(ctx, "w1", time.Minute, 1)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 1 || claimed[0].LeaseToken == "" {
		t.Fatalf("claimed = %+v, want one job carrying a fencing token", claimed)
	}
	token := claimed[0].LeaseToken
	if rerr := q.Renew(ctx, "w2", []string{"a"}, token, time.Minute); !errors.Is(rerr, store.ErrStale) {
		t.Fatalf("foreign renew err = %v, want stale", rerr)
	}
	if rerr := q.Renew(ctx, "w1", []string{"a"}, "bogus-token", time.Minute); !errors.Is(rerr, store.ErrStale) {
		t.Fatalf("wrong-token renew err = %v, want stale", rerr)
	}
	now = now.Add(30 * time.Second)
	if rerr := q.Renew(ctx, "w1", []string{"a"}, token, time.Minute); rerr != nil {
		t.Fatalf("owner renew: %v", rerr)
	}
	job, err := q.Job(ctx, "a")
	if err != nil {
		t.Fatalf("job: %v", err)
	}
	if job.LeaseExpiresAt != now.Add(time.Minute).UnixMilli() {
		t.Fatalf("lease expires at %d", job.LeaseExpiresAt)
	}
}

func TestCompleteAndFail(t *testing.T) {
	now := time.UnixMilli(1700000000000)
	q, _ := openQueue(t, &now)
	ctx := testCtx(t)
	for _, id := range []string{"a", "b"} {
		if _, err := q.Enqueue(ctx, EnqueueSpec{ID: id, Kind: KindLand, RepoID: 1, Payload: `{}`}); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	claimed, err := q.Claim(ctx, "w1", time.Minute, 2)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 2 {
		t.Fatalf("claimed %d, want 2", len(claimed))
	}
	tokens := map[string]string{}
	for _, job := range claimed {
		tokens[job.ID] = job.LeaseToken
	}
	if tokens["a"] == "" || tokens["b"] == "" {
		t.Fatalf("claims carry no fencing tokens: %+v", claimed)
	}
	if cerr := q.Complete(ctx, "w2", "a", tokens["a"]); !errors.Is(cerr, store.ErrStale) {
		t.Fatalf("foreign complete err = %v, want stale", cerr)
	}
	if cerr := q.Complete(ctx, "w1", "a", "bogus-token"); !errors.Is(cerr, store.ErrStale) {
		t.Fatalf("wrong-token complete err = %v, want stale", cerr)
	}
	if cerr := q.Complete(ctx, "w1", "a", tokens["a"]); cerr != nil {
		t.Fatalf("complete: %v", cerr)
	}
	if ferr := q.Fail(ctx, "w1", "b", tokens["b"]); ferr != nil {
		t.Fatalf("fail: %v", ferr)
	}
	done, err := q.Job(ctx, "a")
	if err != nil {
		t.Fatalf("job a: %v", err)
	}
	if done.State != "done" || done.LeaseOwner != "" || done.LeaseToken != "" {
		t.Fatalf("done job = %+v", done)
	}
	queued, err := q.Job(ctx, "b")
	if err != nil {
		t.Fatalf("job b: %v", err)
	}
	if queued.State != "queued" || queued.Attempts != 1 {
		t.Fatalf("failed job = %+v", queued)
	}
	if queued.RunAfter <= now.UnixMilli() {
		t.Fatalf("failed job run_after = %d, want a future backoff", queued.RunAfter)
	}
	if early, rerr := q.Claim(ctx, "w2", time.Minute, 10); rerr != nil {
		t.Fatalf("early reclaim: %v", rerr)
	} else if len(early) != 0 {
		t.Fatalf("early reclaimed = %+v, want nothing before the backoff passes", early)
	}
	now = now.Add(2 * time.Hour)
	reclaimed, err := q.Claim(ctx, "w2", time.Minute, 10)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0].ID != "b" {
		t.Fatalf("reclaimed = %+v, want b after the backoff", reclaimed)
	}
}

func TestCompleteIdleJobFails(t *testing.T) {
	q, _ := openQueue(t, nil)
	ctx := testCtx(t)
	if _, err := q.Enqueue(ctx, EnqueueSpec{ID: "a", Kind: KindLand, RepoID: 1, Payload: `{}`}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := q.Complete(ctx, "w1", "a", "some-token"); !errors.Is(err, store.ErrStale) {
		t.Fatalf("complete idle err = %v, want stale", err)
	}
	if err := q.Fail(ctx, "w1", "a", "some-token"); !errors.Is(err, store.ErrStale) {
		t.Fatalf("fail idle err = %v, want stale", err)
	}
	if err := q.Renew(ctx, "w1", []string{"a"}, "some-token", time.Minute); !errors.Is(err, store.ErrStale) {
		t.Fatalf("renew idle err = %v, want stale", err)
	}
	if err := q.Complete(ctx, "w1", "a", ""); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("empty-token complete err = %v, want invalid", err)
	}
}

func TestWorkerCompletesAndFails(t *testing.T) {
	q, _ := openQueue(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runCtx := testCtx(t)
	for _, id := range []string{"good", "bad"} {
		if _, err := q.Enqueue(runCtx, EnqueueSpec{ID: id, Kind: KindRepoPurge, RepoID: 1, Payload: `{}`}); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	var handled atomic.Int64
	done := make(chan error, 1)
	go func() {
		done <- q.Run(ctx, WorkerConfig{Poll: 5 * time.Millisecond, Lease: time.Minute, Batch: 10, Owner: "w1"}, func(_ context.Context, job store.Job) error {
			handled.Add(1)
			if job.ID == "bad" {
				return errors.New("purge failed")
			}
			return nil
		})
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		good, gerr := q.Job(runCtx, "good")
		bad, berr := q.Job(runCtx, "bad")
		if gerr == nil && berr == nil && good.State == "done" && bad.State == "queued" && bad.Attempts >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("good = %+v, bad = %+v", good, bad)
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if werr := <-done; !errors.Is(werr, context.Canceled) {
		t.Fatalf("worker err = %v, want canceled", werr)
	}
	if handled.Load() < 2 {
		t.Fatalf("handled %d jobs, want at least 2", handled.Load())
	}
}

func TestWorkerConfigValidation(t *testing.T) {
	q, _ := openQueue(t, nil)
	handle := func(context.Context, store.Job) error { return nil }
	base := WorkerConfig{Poll: time.Millisecond, Lease: time.Minute, Batch: 1, Owner: "w"}
	cases := []WorkerConfig{
		{Poll: 0, Lease: time.Minute, Batch: 1, Owner: "w"},
		{Poll: time.Millisecond, Lease: 0, Batch: 1, Owner: "w"},
		{Poll: time.Millisecond, Lease: time.Minute, Batch: 0, Owner: "w"},
		{Poll: time.Millisecond, Lease: time.Minute, Batch: 1, Owner: ""},
	}
	for i, cfg := range cases {
		if err := q.Run(testCtx(t), cfg, handle); err == nil {
			t.Fatalf("case %d ran", i)
		}
	}
	if err := q.Run(testCtx(t), base, nil); err == nil {
		t.Fatal("nil handler ran")
	}
}

func TestFailBacksOffThenDeadLetters(t *testing.T) {
	now := time.UnixMilli(1700000000000)
	q, _ := openQueue(t, &now)
	ctx := testCtx(t)
	if _, err := q.Enqueue(ctx, EnqueueSpec{ID: "poison", Kind: KindMirrorRun, RepoID: 1, Payload: `{}`}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	var prevRunAfter int64
	for attempt := int64(1); attempt <= 10; attempt++ {
		claimed, err := q.Claim(ctx, "w1", time.Minute, 1)
		if err != nil {
			t.Fatalf("claim %d: %v", attempt, err)
		}
		if len(claimed) != 1 {
			t.Fatalf("claim %d returned %d jobs, want 1", attempt, len(claimed))
		}
		if ferr := q.Fail(ctx, "w1", "poison", claimed[0].LeaseToken); ferr != nil {
			t.Fatalf("fail %d: %v", attempt, ferr)
		}
		job, err := q.Job(ctx, "poison")
		if err != nil {
			t.Fatalf("job after fail %d: %v", attempt, err)
		}
		if attempt < 10 {
			if job.State != "queued" || job.Attempts != attempt {
				t.Fatalf("after fail %d: job = %+v, want queued with %d attempts", attempt, job, attempt)
			}
			if job.RunAfter <= now.UnixMilli() {
				t.Fatalf("after fail %d: run_after = %d, want a future backoff", attempt, job.RunAfter)
			}
			if job.RunAfter < prevRunAfter {
				t.Fatalf("backoff shrank: run_after %d after %d", job.RunAfter, prevRunAfter)
			}
			prevRunAfter = job.RunAfter
			now = time.UnixMilli(job.RunAfter + 1)
		} else if job.State != "dead" || job.Attempts != 10 {
			t.Fatalf("after fail 10: job = %+v, want dead with 10 attempts", job)
		}
	}
	now = now.Add(30 * 24 * time.Hour)
	if reclaimed, err := q.Claim(ctx, "w1", time.Minute, 10); err != nil {
		t.Fatalf("reclaim dead: %v", err)
	} else if len(reclaimed) != 0 {
		t.Fatalf("reclaimed a dead job: %+v", reclaimed)
	}
}

func TestRunDoesNotHotLoopOnPoisonJob(t *testing.T) {
	now := time.UnixMilli(1700000000000)
	q, _ := openQueue(t, &now)
	ctx := testCtx(t)
	if _, err := q.Enqueue(ctx, EnqueueSpec{ID: "poison", Kind: KindMirrorRun, RepoID: 1, Payload: `{}`}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var handled atomic.Int64
	done := make(chan error, 1)
	go func() {
		done <- q.Run(runCtx, WorkerConfig{Poll: 10 * time.Millisecond, Lease: time.Minute, Batch: 1, Owner: "w1"},
			func(_ context.Context, _ store.Job) error {
				handled.Add(1)
				return errors.New("mirror 401")
			})
	}()
	time.Sleep(150 * time.Millisecond)
	cancel()
	if werr := <-done; !errors.Is(werr, context.Canceled) {
		t.Fatalf("worker err = %v, want canceled", werr)
	}
	if got := handled.Load(); got != 1 {
		t.Fatalf("handled %d jobs in 150ms, want exactly 1: the backoff must stop the hot loop", got)
	}
	job, err := q.Job(ctx, "poison")
	if err != nil {
		t.Fatalf("job: %v", err)
	}
	if job.State != "queued" || job.Attempts != 1 {
		t.Fatalf("poison job = %+v, want queued with 1 attempt", job)
	}
}

func TestExpiredLeaseCannotCompleteAfterReclaim(t *testing.T) {
	now := time.UnixMilli(1700000000000)
	q1, st := openQueue(t, &now)
	q2 := Open(st, func() time.Time { return now })
	ctx := testCtx(t)
	if _, err := q1.Enqueue(ctx, EnqueueSpec{ID: "a", Kind: KindLand, RepoID: 1, Payload: `{}`}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	first, err := q1.Claim(ctx, "w1", time.Minute, 1)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim = %+v, err = %v", first, err)
	}
	now = now.Add(61 * time.Second)
	second, err := q2.Claim(ctx, "w2", time.Minute, 1)
	if err != nil || len(second) != 1 {
		t.Fatalf("reclaim = %+v, err = %v", second, err)
	}
	if second[0].LeaseToken == first[0].LeaseToken {
		t.Fatal("reclaim reused the lease token; fences cannot distinguish epochs")
	}
	if cerr := q1.Complete(ctx, "w1", "a", first[0].LeaseToken); !errors.Is(cerr, store.ErrStale) {
		t.Fatalf("stale complete err = %v, want stale: the first worker lost the lease", cerr)
	}
	if cerr := q2.Complete(ctx, "w2", "a", second[0].LeaseToken); cerr != nil {
		t.Fatalf("replacement complete: %v", cerr)
	}
	done, err := q1.Job(ctx, "a")
	if err != nil {
		t.Fatalf("job: %v", err)
	}
	if done.State != "done" || done.Attempts != 0 {
		t.Fatalf("job = %+v, want done exactly once with 0 attempts", done)
	}
}
