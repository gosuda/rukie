package events

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/queue"
	"github.com/gosuda/rukie/internal/review"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

func claimReviewJobs(t *testing.T, st *store.Store) []store.Job {
	t.Helper()
	jobs, err := queue.Open(st, feedTime).ClaimKinds(feedCtx(t), "review", []queue.Kind{queue.KindReviewRemap}, time.Minute, 10)
	if err != nil {
		t.Fatal(err)
	}
	return jobs
}

func TestReviewRemapJobSurvivesSpoolReplay(t *testing.T) {
	st := openFeedStore(t)
	sp := openFeedSpool(t)
	rec := PushRecord{
		Landed: []LandedRef{
			{Name: "refs/changes/1/patchset/2", New: strings.Repeat("b", 40)},
			{Name: "refs/changes/2/patchset/3", New: strings.Repeat("c", 40)},
		},
		InvocationID: "push-review-1", RepoID: 11, ActorID: 7,
		AttemptedAt: feedTime().UnixMilli(),
	}
	if err := sp.Store(rec); err != nil {
		t.Fatal(err)
	}
	if err := sp.Replay(feedCtx(t), st, EnqueuePush); err != nil {
		t.Fatal(err)
	}
	claimed := claimReviewJobs(t, st)
	if len(claimed) != 2 {
		t.Fatalf("review jobs = %+v, want one per target", claimed)
	}
	jobs := queue.Open(st, feedTime)
	for _, job := range claimed {
		if !strings.HasPrefix(job.ID, "review-remap-ref:") || job.RepoID != rec.RepoID {
			t.Fatalf("review job = %+v", job)
		}
		var payload pushPayload
		if err := jsonv2.Unmarshal([]byte(job.Payload), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Repo != rec.RepoID || len(payload.Landed) != 1 {
			t.Fatalf("review payload = %+v", payload)
		}
		if err := jobs.Complete(feedCtx(t), "review", job.ID, job.LeaseToken); err != nil {
			t.Fatal(err)
		}
	}
	if err := sp.Store(rec); err != nil {
		t.Fatal(err)
	}
	if err := sp.Replay(feedCtx(t), st, EnqueuePush); err != nil {
		t.Fatal(err)
	}
	for _, previous := range claimed {
		if job := jobState(t, st, previous.ID); job.State != "done" {
			t.Fatalf("replay resurrected completed job: %+v", job)
		}
	}
	rec.InvocationID = "push-review-2"
	rec.Landed = []LandedRef{{Name: "refs/changes/1/patchset/3", New: strings.Repeat("d", 40)}}
	if err := st.Write(feedCtx(t), func(tx *store.WriteTx) error { return EnqueuePush(tx, rec) }); err != nil {
		t.Fatal(err)
	}
	if current := claimReviewJobs(t, st); len(current) != 1 {
		t.Fatalf("new target retry jobs = %+v", current)
	}
}

func TestOrdinaryPushDoesNotEnqueueReviewRemap(t *testing.T) {
	st := openFeedStore(t)
	sp := openFeedSpool(t)
	meta := PushMeta{InvocationID: "push-no-review", RepoID: 11, ActorID: 7}
	if reply := HandlePostReceive(feedCtx(t), st, sp, meta, postReceiveCall(), feedTime()); reply.RejectAll {
		t.Fatal(reply.Reason)
	}
	if jobs := claimReviewJobs(t, st); len(jobs) != 0 {
		t.Fatalf("ordinary push queued remaps: %+v", jobs)
	}
}

func TestFollowerRecoversMissingReviewCallback(t *testing.T) {
	st := openFeedStore(t)
	sp := openFeedSpool(t)
	const name = "refs/changes/1/patchset/2"
	head := strings.Repeat("b", 40)
	follower := OpenFollowerWith(st, 11, nil, nil, feedTime)
	if err := follower.Ingest(feedCtx(t), []ruk.RefEvent{tailEvent(5, name, "", head)}); err != nil {
		t.Fatal(err)
	}
	jobs := claimReviewJobs(t, st)
	if len(jobs) != 1 || jobs[0].RepoID != 11 {
		t.Fatalf("missing callback recovery = %+v", jobs)
	}
	if err := queue.Open(st, feedTime).Complete(feedCtx(t), "review", jobs[0].ID, jobs[0].LeaseToken); err != nil {
		t.Fatal(err)
	}
	call := wire.HookCall{Phase: wire.HookPostReceive, Commands: []wire.HookCommand{{Name: name, New: head}}}
	meta := PushMeta{InvocationID: "push-late-callback", RepoID: 11, ActorID: 7}
	if reply := HandlePostReceive(feedCtx(t), st, sp, meta, call, feedTime()); reply.RejectAll {
		t.Fatal(reply.Reason)
	}
	if replayed := claimReviewJobs(t, st); len(replayed) != 0 {
		t.Fatalf("late callback duplicated a confirmed target: %+v", replayed)
	}
	other := OpenFollowerWith(st, 12, nil, nil, feedTime)
	if err := other.Ingest(feedCtx(t), []ruk.RefEvent{tailEvent(5, name, "", head)}); err != nil {
		t.Fatal(err)
	}
	if scoped := claimReviewJobs(t, st); len(scoped) != 1 || scoped[0].RepoID != 12 {
		t.Fatalf("repository-scoped recovery = %+v", scoped)
	}
}

func TestReviewRemapRecoversFromUnchangedSnapshot(t *testing.T) {
	for _, source := range []string{"resnapshot", "reconcile"} {
		t.Run(source, func(t *testing.T) {
			st := openFeedStore(t)
			const repoID = 11
			tips := map[string]string{"refs/changes/1/patchset/2": strings.Repeat("b", 40)}
			seedTips(t, st, repoID, tips)
			setCursor(t, st, repoID, 20)
			list := func(context.Context, string, string) (ruk.RefsListReply, error) {
				return listReply(20, tips), nil
			}
			switch source {
			case "resnapshot":
				follower := OpenFollowerWith(st, repoID, func(context.Context, uint64) (ruk.EventsTailReply, error) {
					return expiredReply(20, 20), nil
				}, list, feedTime)
				if err := follower.PollOnce(feedCtx(t)); err != nil {
					t.Fatal(err)
				}
			case "reconcile":
				if _, err := OpenReconciler(st, repoID, list, feedTime).ReconcileRepo(feedCtx(t)); err != nil {
					t.Fatal(err)
				}
			}
			if jobs := claimReviewJobs(t, st); len(jobs) != 1 {
				t.Fatalf("confirmed snapshot recovery = %+v", jobs)
			}
			if n := countOutboxKind(t, st, kindRefUpdated); n != 0 {
				t.Fatalf("unchanged tips emitted %d ref updates", n)
			}
		})
	}
}

func TestStaleSnapshotDoesNotScheduleReviewRemap(t *testing.T) {
	st := openFeedStore(t)
	setCursor(t, st, 11, 20)
	follower := OpenFollowerWith(st, 11, func(context.Context, uint64) (ruk.EventsTailReply, error) {
		return expiredReply(19, 19), nil
	}, func(context.Context, string, string) (ruk.RefsListReply, error) {
		return listReply(19, map[string]string{"refs/changes/1/patchset/2": strings.Repeat("b", 40)}), nil
	}, feedTime)
	if err := follower.PollOnce(feedCtx(t)); err != nil {
		t.Fatal(err)
	}
	if jobs := claimReviewJobs(t, st); len(jobs) != 0 {
		t.Fatalf("stale snapshot scheduled jobs: %+v", jobs)
	}
}

func TestReconcileRevivesDeadReviewRemapWithoutDuplicatingLiveWork(t *testing.T) {
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
	var initial store.ChangeRow
	var target review.PublishedChange
	var ref string
	if err := st.Write(ctx, func(tx *store.WriteTx) error {
		repoID, err := tx.InsertRepo(now, store.RepoInsert{
			Name: "review-recovery", Visibility: "private", DefaultBranch: "main",
			OwnerUserID: sql.NullInt64{Int64: author.ID, Valid: true},
		})
		if err != nil {
			return err
		}
		initial, _, err = review.IngressChange(tx, now, review.Ingress{
			RepoID: repoID, AuthorID: author.ID, Branch: "main", HeadOid: strings.Repeat("a", 40),
		})
		if err != nil {
			return err
		}
		if _, err := tx.InsertComment(now, store.CommentRow{
			ChangeID: initial.ID, AuthorUserID: author.ID, HeadOid: initial.HeadOid,
			Body: "preserve this anchor", Path: sql.NullString{String: "main.go", Valid: true},
			Line: sql.NullInt64{Int64: 1, Valid: true},
		}); err != nil {
			return err
		}
		head := strings.Repeat("b", 40)
		_, ref, err = review.IngressChange(tx, now, review.Ingress{
			RepoID: repoID, AuthorID: author.ID, Branch: "main", HeadOid: head, MayEdit: true,
		})
		if err != nil {
			return err
		}
		var ok bool
		target, ok = review.PublishedTarget(repoID, ref, head)
		if !ok {
			return fmt.Errorf("TestReconcileRevivesDeadReviewRemapWithoutDuplicatingLiveWork: invalid target %q", ref)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	tips := map[string]string{ref: target.HeadOid}
	seedTips(t, st, initial.RepoID, tips)
	setCursor(t, st, initial.RepoID, 20)
	clock := func() time.Time { return now }
	reconciler := OpenReconciler(st, initial.RepoID, func(context.Context, string, string) (ruk.RefsListReply, error) {
		return listReply(20, tips), nil
	}, clock)
	if _, err := reconciler.ReconcileRepo(ctx); err != nil {
		t.Fatal(err)
	}
	q := queue.Open(st, clock)
	unavailable := errors.New("diff unavailable")
	var failed store.Job
	var staleToken string
	for attempt := range 10 {
		jobs, err := q.ClaimKinds(ctx, "review-recovery", []queue.Kind{queue.KindReviewRemap}, time.Minute, 10)
		if err != nil || len(jobs) != 1 {
			t.Fatalf("attempt %d claims = %+v, %v", attempt, jobs, err)
		}
		job := jobs[0]
		staleToken = job.LeaseToken
		if err := review.RemapPublished(ctx, st, target, func(context.Context, string, string) (map[string][]review.Span, error) {
			return nil, unavailable
		}); !errors.Is(err, unavailable) {
			t.Fatalf("attempt %d remap = %v", attempt, err)
		}
		if _, err := reconciler.ReconcileRepo(ctx); err != nil {
			t.Fatal(err)
		}
		if current := jobState(t, st, job.ID); current != job {
			t.Fatalf("reconciliation changed a live lease: %+v, want %+v", current, job)
		}
		if err := q.Fail(ctx, "review-recovery", job.ID, job.LeaseToken); err != nil {
			t.Fatal(err)
		}
		failed = jobState(t, st, job.ID)
		if attempt < 9 {
			if failed.State != "queued" || failed.Attempts != int64(attempt+1) {
				t.Fatalf("failed attempt = %+v", failed)
			}
			if _, err := reconciler.ReconcileRepo(ctx); err != nil {
				t.Fatal(err)
			}
			if current := jobState(t, st, failed.ID); current != failed {
				t.Fatalf("reconciliation changed queued backoff: %+v, want %+v", current, failed)
			}
			now = time.UnixMilli(failed.RunAfter)
		}
	}
	if failed.State != "dead" || failed.Attempts != 10 {
		t.Fatalf("exhausted job = %+v", failed)
	}
	if err := st.Read(ctx, func(tx *store.ReadTx) error {
		comments, err := tx.ListCommentsAtHeadIncludingUnmatched(initial.ID, initial.HeadOid)
		if err != nil {
			return err
		}
		if len(comments) != 1 || comments[0].Unmatched || comments[0].Line.Int64 != 1 {
			t.Fatalf("failed remaps lost the original anchor: %+v", comments)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.ReconcileRepo(ctx); err != nil {
		t.Fatal(err)
	}
	revived := jobState(t, st, failed.ID)
	if revived.State != "queued" || revived.Attempts != 0 || revived.RunAfter != now.UnixMilli() {
		t.Fatalf("dead target was not recovered: %+v", revived)
	}
	jobs, err := q.ClaimKinds(ctx, "review-recovery", []queue.Kind{queue.KindReviewRemap}, time.Minute, 10)
	if err != nil || len(jobs) != 1 || jobs[0].ID != failed.ID {
		t.Fatalf("recovered claims = %+v, %v", jobs, err)
	}
	if err := q.Complete(ctx, "review-recovery", failed.ID, staleToken); !errors.Is(err, store.ErrStale) {
		t.Fatalf("old lease completed the revived job: %v", err)
	}
	if err := review.RemapPublished(ctx, st, target, func(_ context.Context, oldHead, newHead string) (map[string][]review.Span, error) {
		if oldHead != initial.HeadOid || newHead != target.HeadOid {
			return nil, fmt.Errorf("TestReconcileRevivesDeadReviewRemapWithoutDuplicatingLiveWork: diff = %s..%s", oldHead, newHead)
		}
		return map[string][]review.Span{"main.go": {{OldStart: 1, NewStart: 2, Lines: 1}}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.Complete(ctx, "review-recovery", jobs[0].ID, jobs[0].LeaseToken); err != nil {
		t.Fatal(err)
	}
	if err := st.Read(ctx, func(tx *store.ReadTx) error {
		comments, err := tx.ListCommentsAtHeadIncludingUnmatched(initial.ID, target.HeadOid)
		if err != nil {
			return err
		}
		if len(comments) != 1 || comments[0].Unmatched || comments[0].Line.Int64 != 2 || comments[0].RemappedFrom != initial.HeadOid {
			t.Fatalf("recovered comment = %+v", comments)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	completed := jobState(t, st, failed.ID)
	if _, err := reconciler.ReconcileRepo(ctx); err != nil {
		t.Fatal(err)
	}
	if current := jobState(t, st, completed.ID); current.State != "done" || current != completed {
		t.Fatalf("reconciliation resurrected a completed job: %+v", current)
	}
	if jobs, err := q.ClaimKinds(ctx, "review-recovery", []queue.Kind{queue.KindReviewRemap}, time.Minute, 10); err != nil || len(jobs) != 0 {
		t.Fatalf("duplicate recovered jobs = %+v, %v", jobs, err)
	}
}
