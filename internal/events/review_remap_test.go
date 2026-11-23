package events

import (
	"errors"
	"strings"
	"testing"
	"time"

	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/queue"
	"github.com/gosuda/rukie/internal/store"
)

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
	jobs := queue.Open(st, feedTime)
	claimed, err := jobs.ClaimKinds(feedCtx(t), "review", []queue.Kind{queue.KindReviewRemap}, time.Minute, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].ID != "review-remap:"+rec.InvocationID || claimed[0].RepoID != rec.RepoID {
		t.Fatalf("review jobs = %+v", claimed)
	}
	var payload pushPayload
	if err := jsonv2.Unmarshal([]byte(claimed[0].Payload), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Repo != rec.RepoID || len(payload.Landed) != 2 || payload.Landed[1] != rec.Landed[1] {
		t.Fatalf("review payload = %+v", payload)
	}
	if err := jobs.Complete(feedCtx(t), "review", claimed[0].ID, claimed[0].LeaseToken); err != nil {
		t.Fatal(err)
	}
	if err := sp.Store(rec); err != nil {
		t.Fatal(err)
	}
	if err := sp.Replay(feedCtx(t), st, EnqueuePush); err != nil {
		t.Fatal(err)
	}
	if job := jobState(t, st, claimed[0].ID); job.State != "done" {
		t.Fatalf("replay resurrected completed job: %+v", job)
	}
	rec.InvocationID = "push-review-2"
	rec.Landed = []LandedRef{{Name: "refs/changes/1/patchset/3", New: strings.Repeat("d", 40)}}
	if err := st.Write(feedCtx(t), func(tx *store.WriteTx) error { return EnqueuePush(tx, rec) }); err != nil {
		t.Fatal(err)
	}
	if job := jobState(t, st, "review-remap:"+rec.InvocationID); job.State != "queued" {
		t.Fatalf("new target has no retry job: %+v", job)
	}
}

func TestOrdinaryPushDoesNotEnqueueReviewRemap(t *testing.T) {
	st := openFeedStore(t)
	sp := openFeedSpool(t)
	meta := PushMeta{InvocationID: "push-no-review", RepoID: 11, ActorID: 7}
	if reply := HandlePostReceive(feedCtx(t), st, sp, meta, postReceiveCall(), feedTime()); reply.RejectAll {
		t.Fatal(reply.Reason)
	}
	err := st.Read(feedCtx(t), func(tx *store.ReadTx) error {
		_, err := tx.GetJob("review-remap:" + meta.InvocationID)
		return err
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ordinary push remap job error = %v, want not found", err)
	}
}
