package events

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/queue"
	"github.com/gosuda/rukie/internal/review"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/store"
)

func TestPruningPreservesCurrentTerminalReviewRemap(t *testing.T) {
	st := openFeedStore(t)
	ctx := feedCtx(t)
	now := feedTime()
	author, err := st.InsertAuthUser(ctx, store.AuthUserCreate{
		Name: "ada", Lower: "ada", Email: "ada@example.test",
		Hash: "test-hash", Algo: "argon2id-v19", Now: now.UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var change store.ChangeRow
	var ref string
	if err := st.Write(ctx, func(tx *store.WriteTx) error {
		repoID, insertErr := tx.InsertRepo(now, store.RepoInsert{
			Name: "terminal-remap-prune", Visibility: "private", DefaultBranch: "main",
			OwnerUserID: sql.NullInt64{Int64: author.ID, Valid: true},
		})
		if insertErr != nil {
			return insertErr
		}
		change, ref, insertErr = review.IngressChange(tx, now, review.Ingress{
			RepoID: repoID, AuthorID: author.ID, Branch: "main", HeadOid: strings.Repeat("a", 40),
		})
		return insertErr
	}); err != nil {
		t.Fatal(err)
	}
	tips := map[string]string{ref: change.HeadOid}
	seedTips(t, st, change.RepoID, tips)
	setCursor(t, st, change.RepoID, 20)
	clock := func() time.Time { return now }
	reconciler := OpenReconciler(st, change.RepoID, func(context.Context, string, string) (ruk.RefsListReply, error) {
		return listReply(20, tips), nil
	}, clock)
	if _, err := reconciler.ReconcileRepo(ctx); err != nil {
		t.Fatal(err)
	}
	q := queue.Open(st, clock)
	claim := func() store.Job {
		t.Helper()
		jobs, claimErr := q.ClaimKinds(ctx, "prune-review", []queue.Kind{queue.KindReviewRemap}, time.Minute, 10)
		if claimErr != nil || len(jobs) != 1 {
			t.Fatalf("review claims = %+v, %v", jobs, claimErr)
		}
		return jobs[0]
	}
	fail := func(job store.Job) store.Job {
		t.Helper()
		if err := st.Write(ctx, func(tx *store.WriteTx) error {
			return tx.FailJobPermanently("prune-review", job.ID, job.LeaseToken, "alignment exceeds its cell bound", now)
		}); err != nil {
			t.Fatal(err)
		}
		return jobState(t, st, job.ID)
	}
	failed := fail(claim())
	for range 2 {
		now = now.Add(48 * time.Hour)
		if count, countErr := st.CountTerminalJobs(ctx, now.UnixMilli()); countErr != nil || count != 0 {
			t.Fatalf("current terminal remap was counted as history: %d, %v", count, countErr)
		}
		if count, pruneErr := st.PruneTerminalJobs(ctx, now.UnixMilli()); pruneErr != nil || count != 0 {
			t.Fatalf("current terminal remap was pruned: %d, %v", count, pruneErr)
		}
		if _, err := reconciler.ReconcileRepo(ctx); err != nil {
			t.Fatal(err)
		}
		if current := jobState(t, st, failed.ID); current != failed {
			t.Fatalf("pruning and reconciliation changed terminal state: %+v, want %+v", current, failed)
		}
		if jobs, claimErr := q.ClaimKinds(ctx, "prune-review", []queue.Kind{queue.KindReviewRemap}, time.Minute, 10); claimErr != nil || len(jobs) != 0 {
			t.Fatalf("pruning revived terminal work: %+v, %v", jobs, claimErr)
		}
	}
	if err := st.Write(ctx, func(tx *store.WriteTx) error {
		return tx.ResetJob(store.JobSpec{
			At: now, ID: failed.ID, Kind: failed.Kind, RepoID: failed.RepoID, Payload: failed.Payload,
		}, now)
	}); err != nil {
		t.Fatal(err)
	}
	reset := claim()
	if reset.ID != failed.ID || reset.TerminalError != "" || reset.Attempts != 0 || reset.LeaseToken == failed.LeaseToken {
		t.Fatalf("explicit reset did not reopen retained work: %+v", reset)
	}
	failed = fail(reset)
	now = now.Add(time.Hour)
	if err := st.Write(ctx, func(tx *store.WriteTx) error {
		var updateErr error
		change, ref, updateErr = review.IngressChange(tx, now, review.Ingress{
			RepoID: change.RepoID, AuthorID: author.ID, Branch: "main", HeadOid: strings.Repeat("b", 40), MayEdit: true,
		})
		return updateErr
	}); err != nil {
		t.Fatal(err)
	}
	tips[ref] = change.HeadOid
	if _, err := reconciler.ReconcileRepo(ctx); err != nil {
		t.Fatal(err)
	}
	if count, countErr := st.CountTerminalJobs(ctx, now.UnixMilli()); countErr != nil || count != 1 {
		t.Fatalf("superseded terminal remap count = %d, %v", count, countErr)
	}
	if count, pruneErr := st.PruneTerminalJobs(ctx, now.UnixMilli()); pruneErr != nil || count != 1 {
		t.Fatalf("superseded terminal remap prune = %d, %v", count, pruneErr)
	}
	if _, readErr := q.Job(ctx, failed.ID); !errors.Is(readErr, store.ErrNotFound) {
		t.Fatalf("obsolete terminal row survived: %v", readErr)
	}
	current := claim()
	if current.ID == failed.ID {
		t.Fatal("new patchset reused an obsolete job")
	}
	if err := q.Complete(ctx, "prune-review", current.ID, current.LeaseToken); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if count, countErr := st.CountTerminalJobs(ctx, now.UnixMilli()); countErr != nil || count != 1 {
		t.Fatalf("completed remap count = %d, %v", count, countErr)
	}
	if count, pruneErr := st.PruneTerminalJobs(ctx, now.UnixMilli()); pruneErr != nil || count != 1 {
		t.Fatalf("completed remap prune = %d, %v", count, pruneErr)
	}
	if err := st.Write(ctx, func(tx *store.WriteTx) error {
		inserted, enqueueErr := tx.EnqueueRecoverableJob(store.JobSpec{
			At: now, ID: "ordinary-dead", Kind: string(queue.KindReviewRemap), RepoID: change.RepoID, Payload: `{}`,
		})
		if enqueueErr != nil {
			return enqueueErr
		}
		if !inserted {
			return fmt.Errorf("TestPruningPreservesCurrentTerminalReviewRemap: ordinary job already existed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for range 10 {
		job := claim()
		if err := q.Fail(ctx, "prune-review", job.ID, job.LeaseToken); err != nil {
			t.Fatal(err)
		}
		now = now.Add(24 * time.Hour)
	}
	if count, countErr := st.CountTerminalJobs(ctx, now.UnixMilli()); countErr != nil || count != 1 {
		t.Fatalf("ordinary exhausted job count = %d, %v", count, countErr)
	}
	if count, pruneErr := st.PruneTerminalJobs(ctx, now.UnixMilli()); pruneErr != nil || count != 1 {
		t.Fatalf("ordinary exhausted job prune = %d, %v", count, pruneErr)
	}
}
