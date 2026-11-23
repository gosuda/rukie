package githttp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	jsonv1 "encoding/json"

	"github.com/gosuda/rukie/internal/events"
	"github.com/gosuda/rukie/internal/queue"
	"github.com/gosuda/rukie/internal/review"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

func TestAlignmentLimitStopsUnchangedReviewRemap(t *testing.T) {
	repoID, st := openJudgeStore(t, false)
	ctx := t.Context()
	now := time.Unix(1000, 0)
	clock := func() time.Time { return now }
	var original store.ChangeRow
	var target review.PublishedChange
	var ref string
	if err := st.Write(ctx, func(tx *store.WriteTx) error {
		var insertErr error
		original, _, insertErr = review.IngressChange(tx, now, review.Ingress{
			RepoID: repoID, AuthorID: judgeCaller().UserID, Branch: "main", HeadOid: strings.Repeat("a", 40),
		})
		if insertErr != nil {
			return insertErr
		}
		if _, commentErr := tx.InsertComment(now, store.CommentRow{
			ChangeID: original.ID, AuthorUserID: original.AuthorUserID, HeadOid: original.HeadOid,
			Body: "retain the original anchor", Path: sql.NullString{String: "main.go", Valid: true},
			Line: sql.NullInt64{Int64: 1, Valid: true},
		}); commentErr != nil {
			return commentErr
		}
		head := strings.Repeat("b", 40)
		_, ref, insertErr = review.IngressChange(tx, now, review.Ingress{
			RepoID: repoID, AuthorID: original.AuthorUserID, Branch: "main", HeadOid: head, MayEdit: true,
		})
		if insertErr != nil {
			return insertErr
		}
		var valid bool
		target, valid = review.PublishedTarget(repoID, ref, head)
		if !valid {
			return fmt.Errorf("TestAlignmentLimitStopsUnchangedReviewRemap: invalid ref %q", ref)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reconciler := events.OpenReconciler(st, repoID, func(context.Context, string, string) (ruk.RefsListReply, error) {
		return ruk.RefsListReply{Refs: ruk.RefPage{Items: []ruk.RefEntry{{Name: ref, Target: ruk.DirectTarget(target.HeadOid)}}}}, nil
	}, clock)
	if _, err := reconciler.ReconcileRepo(ctx); err != nil {
		t.Fatal(err)
	}
	q := queue.Open(st, clock)
	started := make(chan store.Job, 1)
	var diffCalls atomic.Int64
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		defer close(done)
		done <- q.RunKinds(runCtx, queue.WorkerConfig{
			Owner: "terminal-remap", Batch: 1, Lease: time.Minute, Poll: time.Millisecond,
		}, []queue.Kind{queue.KindReviewRemap}, func(jobCtx context.Context, job store.Job) error {
			select {
			case started <- job:
			default:
			}
			return review.RemapPublished(jobCtx, st, target, func(diffCtx context.Context, oldHead, newHead string) (map[string][]review.Span, error) {
				if oldHead != original.HeadOid || newHead != target.HeadOid {
					return nil, fmt.Errorf("TestAlignmentLimitStopsUnchangedReviewRemap: diff = %s..%s", oldHead, newHead)
				}
				return collectChangeSpans(diffCtx, func(context.Context, jsonv1.RawMessage) (diffReplyJSON, error) {
					diffCalls.Add(1)
					return diffReplyJSON{}, &ruk.Error{Code: wire.CodeLimit, Message: "alignment exceeds its cell bound"}
				})
			})
		})
	}()
	defer func() {
		cancel()
		<-done
	}()
	var job store.Job
	select {
	case job = <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("review worker did not claim the job")
	}
	deadline := time.Now().Add(10 * time.Second)
	var failed store.Job
	for {
		current, readErr := q.Job(ctx, job.ID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if current.Attempts != 0 {
			failed = current
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker did not record its failure: %+v", current)
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if runErr := <-done; !errors.Is(runErr, context.Canceled) {
		t.Fatalf("worker exit = %v", runErr)
	}
	if failed.State != "dead" || failed.Attempts != 1 || failed.LeaseOwner != "" || failed.LeaseToken != "" {
		t.Fatalf("alignment failure did not stop the job: %+v", failed)
	}
	for range 5 {
		now = now.Add(24 * time.Hour)
		if _, err := reconciler.ReconcileRepo(ctx); err != nil {
			t.Fatal(err)
		}
		if current, err := q.Job(ctx, job.ID); err != nil || current != failed {
			t.Fatalf("unchanged target revived a permanent failure: %+v, %v", current, err)
		}
		if jobs, err := q.ClaimKinds(ctx, "terminal-remap", []queue.Kind{queue.KindReviewRemap}, time.Minute, 10); err != nil || len(jobs) != 0 {
			t.Fatalf("permanent failure was claimed again: %+v, %v", jobs, err)
		}
	}
	if calls := diffCalls.Load(); calls != 1 {
		t.Fatalf("alignment calls = %d, want 1", calls)
	}
	if err := st.Read(ctx, func(tx *store.ReadTx) error {
		comments, readErr := tx.ListCommentsAtHeadIncludingUnmatched(original.ID, original.HeadOid)
		if readErr != nil {
			return readErr
		}
		if len(comments) != 1 || comments[0].Unmatched || comments[0].Line.Int64 != 1 {
			t.Fatalf("permanent diff failure changed the original anchor: %+v", comments)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Write(ctx, func(tx *store.WriteTx) error {
		head := strings.Repeat("c", 40)
		_, nextRef, insertErr := review.IngressChange(tx, now, review.Ingress{
			RepoID: repoID, AuthorID: original.AuthorUserID, Branch: "main", HeadOid: head, MayEdit: true,
		})
		if insertErr != nil {
			return insertErr
		}
		var valid bool
		target, valid = review.PublishedTarget(repoID, nextRef, head)
		if !valid {
			return fmt.Errorf("TestAlignmentLimitStopsUnchangedReviewRemap: invalid ref %q", nextRef)
		}
		ref = nextRef
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.ReconcileRepo(ctx); err != nil {
		t.Fatal(err)
	}
	jobs, claimErr := q.ClaimKinds(ctx, "terminal-remap", []queue.Kind{queue.KindReviewRemap}, time.Minute, 10)
	if claimErr != nil || len(jobs) != 1 || jobs[0].ID == job.ID {
		t.Fatalf("changed target could not retry: %+v, %v", jobs, claimErr)
	}
	if err := review.RemapPublished(ctx, st, target, func(_ context.Context, oldHead, newHead string) (map[string][]review.Span, error) {
		if oldHead != original.HeadOid || newHead != target.HeadOid {
			return nil, fmt.Errorf("TestAlignmentLimitStopsUnchangedReviewRemap: recovery diff = %s..%s", oldHead, newHead)
		}
		return map[string][]review.Span{"main.go": {{OldStart: 1, NewStart: 2, Lines: 1}}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.Complete(ctx, "terminal-remap", jobs[0].ID, jobs[0].LeaseToken); err != nil {
		t.Fatal(err)
	}
	if err := st.Read(ctx, func(tx *store.ReadTx) error {
		comments, readErr := tx.ListCommentsAtHeadIncludingUnmatched(original.ID, target.HeadOid)
		if readErr != nil {
			return readErr
		}
		if len(comments) != 1 || comments[0].Unmatched || comments[0].Line.Int64 != 2 || comments[0].RemappedFrom != original.HeadOid {
			t.Fatalf("new target failed to recover the preserved anchor: %+v", comments)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
