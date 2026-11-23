package mirror

import (
	"testing"
	"time"

	jsonv1 "encoding/json"

	"github.com/gosuda/rukie/internal/queue"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

func TestRateLimitFinishesAttemptAndSchedulesNextRun(t *testing.T) {
	w, st, q, now := openWorker(t)
	repoID := insertRepo(t, st, now, "rate-limited")
	mirror := insertMirror(t, st, now, repoID, true)
	pub := "run-rate-limited"
	insertRun(t, st, now, mirror.ID, pub)
	jobID := "mirror-run:" + pub
	enqueueMirrorJob(t, q, jobID, repoID, mustMarshalPayload(t, map[string]any{"run": pub, "mirror": mirror.ID}))
	claimed, err := q.ClaimKinds(testCtx(t), "owner", []queue.Kind{queue.KindMirrorRun}, mirrorClaimLease, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	e := &execution{worker: *w, owner: "owner", job: claimed[0], start: now}
	if done, err := e.load(testCtx(t), pub); err != nil || done {
		t.Fatalf("load = %v, %v", done, err)
	}
	refusal := &ruk.Error{
		Code:    wire.CodeCapacity,
		Message: "remote rate limit",
		Detail:  jsonv1.RawMessage(`{"http":{"status":429,"retry_after_seconds":120,"body_preview":"slow down"}}`),
	}
	if done, err := e.finishCall(testCtx(t), refusal, nil, nil); err != nil || !done {
		t.Fatalf("finishCall = %v, %v", done, err)
	}
	jobDone(t, q, jobID)
	var run store.MirrorRunRow
	var result store.MirrorRow
	if err := st.Read(testCtx(t), func(tx *store.ReadTx) error {
		var err error
		run, err = tx.GetRunByPublicID(pub)
		if err != nil {
			return err
		}
		result, err = tx.GetMirror(mirror.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if run.State != "failed" || run.OutcomeClass != "rate_limited" || run.AttemptNo != 1 || run.FinishedAt != now.UnixMilli() {
		t.Fatalf("run = %+v", run)
	}
	if result.State != "failing" || result.LastHTTPStatus != 429 || result.LastErrorClass != "rate_limited" || result.NextRunAt < now.Add(120*time.Second).UnixMilli() {
		t.Fatalf("mirror = %+v", result)
	}
	after := queue.Open(st, func() time.Time { return time.UnixMilli(result.NextRunAt) })
	if jobs, err := after.ClaimKinds(testCtx(t), "after", []queue.Kind{queue.KindMirrorRun}, mirrorClaimLease, 10); err != nil || len(jobs) != 0 {
		t.Fatalf("terminal run was requeued: %+v, %v", jobs, err)
	}
	if err := st.Read(testCtx(t), func(tx *store.ReadTx) error {
		before, err := tx.DueMirrors(result.NextRunAt-1, 10)
		if err != nil {
			return err
		}
		if len(before) != 0 {
			t.Fatalf("mirror scheduled before retry delay: %+v", before)
		}
		due, err := tx.DueMirrors(result.NextRunAt, 10)
		if err != nil {
			return err
		}
		if len(due) != 1 || due[0].ID != mirror.ID {
			t.Fatalf("due mirrors = %+v", due)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
