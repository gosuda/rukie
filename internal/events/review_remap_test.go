package events

import (
	"context"
	"strings"
	"testing"
	"time"

	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/queue"
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
