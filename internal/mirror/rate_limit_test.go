package mirror

import (
	"errors"
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

func TestScheduledRetryRetainsOnlyMatchingApproval(t *testing.T) {
	w, st, q, now := openWorker(t)
	repoID := insertRepo(t, st, now, "approved-retry")
	mirror := insertMirror(t, st, now, repoID, true)
	cfg, configErr := mirrorConfigOf(mirror)
	if configErr != nil {
		t.Fatal(configErr)
	}
	local := map[string]string{"refs/heads/main": "base"}
	remote := map[string]string{"refs/heads/main": "base", "refs/tags/obsolete": "old"}
	plan, planErr := ComputePlan(local, remote, cfg, "main", "main", "")
	if !errors.Is(planErr, ErrNeedsConfirmation) || len(plan.Deletes) != 1 {
		t.Fatalf("prune plan = %+v, %v", plan, planErr)
	}
	pub := "run-approved-retry"
	run := insertRun(t, st, now, mirror.ID, pub)
	run.ConfirmedDigest = plan.Digest
	run.PlanDigest = plan.Digest
	if err := st.Write(testCtx(t), func(tx *store.WriteTx) error {
		return tx.UpdateRun(now, run.ID, run)
	}); err != nil {
		t.Fatal(err)
	}
	jobID := "mirror-run:" + pub
	enqueueMirrorJob(t, q, jobID, repoID, mustMarshalPayload(t, map[string]any{"run": pub, "mirror": mirror.ID}))
	claimed, err := q.ClaimKinds(testCtx(t), "owner", []queue.Kind{queue.KindMirrorRun}, mirrorClaimLease, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	e := &execution{worker: *w, owner: "owner", job: claimed[0], start: now, cfg: cfg}
	if done, err := e.load(testCtx(t), pub); err != nil || done {
		t.Fatalf("load = %v, %v", done, err)
	}
	refusal := &ruk.Error{
		Code:    wire.CodeCapacity,
		Message: "remote rate limit",
		Detail:  jsonv1.RawMessage(`{"http":{"status":429,"retry_after_seconds":120}}`),
	}
	if done, err := e.finishCall(testCtx(t), refusal, &plan, nil); err != nil || !done {
		t.Fatalf("finishCall = %v, %v", done, err)
	}
	jobDone(t, q, jobID)
	var retryAt int64
	if err := st.Read(testCtx(t), func(tx *store.ReadTx) error {
		result, err := tx.GetMirror(mirror.ID)
		if err != nil {
			return err
		}
		retryAt = result.NextRunAt
		if retryAt < now.Add(120*time.Second).UnixMilli() {
			t.Fatalf("retry scheduled before Retry-After: %+v", result)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	clock := time.UnixMilli(retryAt - 1)
	w.Clock = func() time.Time { return clock }
	w.Queue = queue.Open(st, w.Clock)
	if err := w.scheduleOnce(testCtx(t)); err != nil {
		t.Fatal(err)
	}
	if err := st.Read(testCtx(t), func(tx *store.ReadTx) error {
		latest, err := tx.LatestMirrorRun(mirror.ID)
		if err != nil {
			return err
		}
		if latest.PublicID != pub || latest.State != "failed" || latest.AttemptNo != 1 {
			t.Fatalf("original run is not terminal before retry: %+v", latest)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	clock = time.UnixMilli(retryAt)
	if err := w.scheduleOnce(testCtx(t)); err != nil {
		t.Fatal(err)
	}
	var retry store.MirrorRunRow
	if err := st.Read(testCtx(t), func(tx *store.ReadTx) error {
		var err error
		retry, err = tx.LatestMirrorRun(mirror.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if retry.PublicID == pub || retry.State != "queued" || retry.AttemptNo != 0 || retry.ConfirmedDigest != plan.Digest {
		t.Fatalf("retry lost the scoped approval: %+v", retry)
	}
	if _, err := ComputePlan(local, remote, cfg, "main", "main", retry.ConfirmedDigest); err != nil {
		t.Fatalf("identical approved retry rejected: %v", err)
	}
	changed := map[string]string{"refs/heads/main": "base", "refs/tags/obsolete": "changed"}
	if _, err := ComputePlan(local, changed, cfg, "main", "main", retry.ConfirmedDigest); !errors.Is(err, ErrNeedsConfirmation) {
		t.Fatalf("changed prune accepted old approval: %v", err)
	}
	other := cfg
	other.TargetURL = "https://example.com/octo/other.git"
	if _, err := ComputePlan(local, remote, other, "main", "main", retry.ConfirmedDigest); !errors.Is(err, ErrNeedsConfirmation) {
		t.Fatalf("different target accepted old approval: %v", err)
	}
	retry.State = "failed"
	retry.ConfirmedDigest = ""
	if err := st.Write(testCtx(t), func(tx *store.WriteTx) error {
		return tx.UpdateRun(clock, retry.ID, retry)
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.scheduleOnce(testCtx(t)); err != nil {
		t.Fatal(err)
	}
	if err := st.Read(testCtx(t), func(tx *store.ReadTx) error {
		latest, err := tx.LatestMirrorRun(mirror.ID)
		if err != nil {
			return err
		}
		if latest.ID == retry.ID || latest.ConfirmedDigest != "" {
			t.Fatalf("older approval resurfaced past a newer run: %+v", latest)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
