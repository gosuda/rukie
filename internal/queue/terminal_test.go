package queue

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/store"
)

func TestTerminalFailurePreservesLeaseFencingAndRequiresExplicitReset(t *testing.T) {
	now := time.Unix(1000, 0)
	q, st := openQueue(t, &now)
	ctx := testCtx(t)
	spec := EnqueueSpec{ID: "terminal-review", Kind: KindReviewRemap, RepoID: 1, Payload: `{}`}
	if _, err := q.Enqueue(ctx, spec); err != nil {
		t.Fatal(err)
	}
	first, firstErr := q.Claim(ctx, "worker", time.Minute, 1)
	if firstErr != nil || len(first) != 1 {
		t.Fatalf("first lease = %+v, %v", first, firstErr)
	}
	now = now.Add(2 * time.Minute)
	second, secondErr := q.Claim(ctx, "worker", time.Minute, 1)
	if secondErr != nil || len(second) != 1 || second[0].LeaseToken == first[0].LeaseToken {
		t.Fatalf("replacement lease = %+v, %v", second, secondErr)
	}
	cause := fmt.Errorf("handler: %w: deterministic failure", ErrTerminal)
	if err := q.fail(ctx, "worker", spec.ID, first[0].LeaseToken, cause); !errors.Is(err, store.ErrStale) {
		t.Fatalf("stale terminal failure = %v", err)
	}
	if current, err := q.Job(ctx, spec.ID); err != nil || current != second[0] {
		t.Fatalf("stale failure changed the current lease: %+v, %v", current, err)
	}
	if err := st.Write(ctx, func(tx *store.WriteTx) error {
		return tx.FailJobPermanently("worker", spec.ID, second[0].LeaseToken, "", now)
	}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("empty terminal reason = %v", err)
	}
	if err := q.fail(ctx, "worker", spec.ID, second[0].LeaseToken, cause); err != nil {
		t.Fatal(err)
	}
	failed, failedErr := q.Job(ctx, spec.ID)
	if failedErr != nil || failed.State != "dead" || failed.Attempts != 1 || failed.TerminalError != cause.Error() || failed.LeaseToken != "" || failed.LeaseOwner != "" || failed.LeaseExpiresAt != 0 || failed.RunAfter != 0 {
		t.Fatalf("terminal job = %+v, %v", failed, failedErr)
	}
	if err := q.Complete(ctx, "worker", spec.ID, second[0].LeaseToken); !errors.Is(err, store.ErrStale) {
		t.Fatalf("stale completion after terminal failure = %v", err)
	}
	now = now.Add(30 * 24 * time.Hour)
	reset := store.JobSpec{ID: spec.ID, Kind: string(spec.Kind), RepoID: spec.RepoID, Payload: spec.Payload, At: now}
	if err := st.Write(ctx, func(tx *store.WriteTx) error {
		inserted, enqueueErr := tx.EnqueueRecoverableJob(reset)
		if enqueueErr != nil {
			return enqueueErr
		}
		if inserted {
			t.Error("automatic enqueue revived a terminal failure")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if current, err := q.Job(ctx, spec.ID); err != nil || current != failed {
		t.Fatalf("automatic revival changed terminal state: %+v, %v", current, err)
	}
	if jobs, err := q.Claim(ctx, "worker", time.Minute, 10); err != nil || len(jobs) != 0 {
		t.Fatalf("terminal job was claimed: %+v, %v", jobs, err)
	}
	if err := st.Write(ctx, func(tx *store.WriteTx) error { return tx.ResetJob(reset, now) }); err != nil {
		t.Fatal(err)
	}
	if current, err := q.Job(ctx, spec.ID); err != nil || current.State != "queued" || current.TerminalError != "" || current.Attempts != 0 {
		t.Fatalf("explicit reset did not clear terminal failure: %+v, %v", current, err)
	}
	if jobs, err := q.Claim(ctx, "worker", time.Minute, 1); err != nil || len(jobs) != 1 || jobs[0].LeaseToken == second[0].LeaseToken {
		t.Fatalf("explicit reset could not be claimed: %+v, %v", jobs, err)
	}
}
