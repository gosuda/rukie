package mirror

import (
	"context"
	"errors"
	"maps"
	"testing"
	"time"

	jsonv1 "encoding/json"

	"github.com/gosuda/rukie/internal/queue"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

func TestPartialPushRetryPreservesRemainingApproval(t *testing.T) {
	w, st, q, now := openWorker(t)
	repoID := insertRepo(t, st, now, "partial-approved-retry")
	mirror := insertMirror(t, st, now, repoID, true)
	cfg, cerr := mirrorConfigOf(mirror)
	if cerr != nil {
		t.Fatal(cerr)
	}
	local := map[string]string{"refs/heads/main": "new"}
	remote := map[string]string{"refs/heads/main": "old", "refs/tags/obsolete": "tag"}
	plan, perr := ComputePlan(local, remote, cfg, "main", "main", "")
	if !errors.Is(perr, ErrNeedsConfirmation) || len(plan.Deletes) != 1 {
		t.Fatalf("initial plan = %+v, %v", plan, perr)
	}
	pub := "run-partial-approved-retry"
	run := insertRun(t, st, now, mirror.ID, pub)
	run.ConfirmedDigest = plan.Digest
	if err := st.Write(testCtx(t), func(tx *store.WriteTx) error {
		return tx.UpdateRun(now, run.ID, run)
	}); err != nil {
		t.Fatal(err)
	}
	jobID := "mirror-run:" + pub
	enqueueMirrorJob(t, q, jobID, repoID, mustMarshalPayload(t, map[string]any{"run": pub, "mirror": mirror.ID}))
	claimed, claimErr := q.ClaimKinds(testCtx(t), "owner", []queue.Kind{queue.KindMirrorRun}, mirrorClaimLease, 1)
	if claimErr != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v, %v", claimed, claimErr)
	}
	e := &execution{worker: *w, owner: "owner", job: claimed[0], start: now, cfg: cfg}
	if done, err := e.load(testCtx(t), pub); err != nil || done {
		t.Fatalf("load = %v, %v", done, err)
	}
	e.trackPlanned(plan)
	refusal := &ruk.Error{Code: wire.CodeCapacity, Message: "rate limit", Detail: jsonv1.RawMessage("{\"http\":{\"status\":429,\"retry_after_seconds\":120}}")}
	calls := 0
	send := func(_ context.Context, _ string, updates []ruk.PushUpdate) (ruk.PushReply, error) {
		calls++
		if calls == 1 {
			if len(updates) != 1 || updates[0].RemoteRef != "refs/tags/obsolete" || !updates[0].Source.IsDelete() {
				t.Fatalf("first push = %+v", updates)
			}
			return confirmBatchReply("confirmed-delete")(updates), nil
		}
		if len(updates) != 1 || updates[0].RemoteRef != "refs/heads/main" || updates[0].ExpectedOld == nil || updates[0].ExpectedOld.Present == nil || *updates[0].ExpectedOld.Present != "old" {
			t.Fatalf("remaining push lost CAS: %+v", updates)
		}
		return ruk.PushReply{}, refusal
	}
	pushErr := e.pushDeletesFirst(testCtx(t), send)
	if !errors.Is(pushErr, refusal) || calls != 2 {
		t.Fatalf("partial push = %v after %d calls", pushErr, calls)
	}
	if done, err := e.finishCall(testCtx(t), pushErr, &plan, nil); err != nil || !done {
		t.Fatalf("finishCall = %v, %v", done, err)
	}
	jobDone(t, q, jobID)
	var retryAt int64
	if err := st.Read(testCtx(t), func(tx *store.ReadTx) error {
		finished, gerr := tx.GetRunByPublicID(pub)
		if gerr != nil {
			return gerr
		}
		if finished.ConfirmedDigest != plan.Digest || finished.PlanDigest != plan.Digest {
			t.Fatalf("original approval audit changed: %+v", finished)
		}
		result, gerr := tx.GetMirror(mirror.ID)
		if gerr != nil {
			return gerr
		}
		retryAt = result.NextRunAt
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	w.Clock = func() time.Time { return time.UnixMilli(retryAt) }
	w.Queue = queue.Open(st, w.Clock)
	if err := w.scheduleOnce(testCtx(t)); err != nil {
		t.Fatal(err)
	}
	var retry store.MirrorRunRow
	if err := st.Read(testCtx(t), func(tx *store.ReadTx) error {
		var gerr error
		retry, gerr = tx.LatestMirrorRun(mirror.ID)
		return gerr
	}); err != nil {
		t.Fatal(err)
	}
	remaining := maps.Clone(remote)
	delete(remaining, "refs/tags/obsolete")
	if next, err := ComputePlan(local, remaining, cfg, "main", "main", retry.ConfirmedDigest); err != nil || len(next.Deletes) != 0 {
		t.Fatalf("approved remaining update rejected: %+v, %v", next, err)
	}
	for _, change := range []string{"target", "local", "remote", "new-delete", "reappeared-delete"} {
		t.Run(change, func(t *testing.T) {
			changedCfg := cfg
			changedLocal := maps.Clone(local)
			changedRemote := maps.Clone(remaining)
			switch change {
			case "target":
				changedCfg.TargetURL = "https://example.com/octo/other.git"
			case "local":
				changedLocal["refs/heads/main"] = "other-new"
			case "remote":
				changedRemote["refs/heads/main"] = "other-old"
			case "new-delete":
				changedRemote["refs/tags/new"] = "unapproved"
			case "reappeared-delete":
				changedRemote["refs/tags/obsolete"] = "tag"
			}
			if _, err := ComputePlan(changedLocal, changedRemote, changedCfg, "main", "main", retry.ConfirmedDigest); !errors.Is(err, ErrNeedsConfirmation) {
				t.Fatalf("changed plan accepted derived approval: %v", err)
			}
		})
	}
}

func TestRemainingApprovalTracksConfirmedUpdatesAndDFDeletes(t *testing.T) {
	for _, scenario := range []string{"update", "delete", "directory-file", "mixed"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := Mirror{TargetURL: "https://example.com/repo.git", Mode: ModeExact, RefSelection: RefSelectionAll}
			local := map[string]string{"refs/heads/main": "new", "refs/heads/a/b": "child"}
			remote := map[string]string{"refs/heads/main": "old", "refs/heads/a": "parent", "refs/tags/obsolete": "tag"}
			plan, perr := ComputePlan(local, remote, cfg, "main", "main", "")
			if !errors.Is(perr, ErrNeedsConfirmation) {
				t.Fatalf("plan = %+v, %v", plan, perr)
			}
			e := &execution{cfg: cfg, loaded: loadedRun{run: store.MirrorRunRow{ConfirmedDigest: plan.Digest}}}
			e.trackPlanned(plan)
			observed := maps.Clone(remote)
			for _, tracked := range e.tracked {
				ref := tracked.plan.Ref
				landed := scenario == "mixed" || scenario == "update" && ref == "refs/heads/main" || scenario == "delete" && ref == "refs/tags/obsolete" || scenario == "directory-file" && ref == "refs/heads/a"
				if !landed || tracked.plan.Action == ActionBlockedDF {
					continue
				}
				if tracked.plan.New == "" {
					tracked.status = "deleted"
					delete(observed, ref)
				} else {
					tracked.status = "ok"
					tracked.confirmed = &tracked.plan.New
					observed[ref] = tracked.plan.New
				}
			}
			digest := e.remainingApprovedDigest(plan)
			if digest == "" {
				t.Fatal("confirmed progress did not derive remaining approval")
			}
			if next, err := ComputePlan(local, observed, cfg, "main", "main", digest); err != nil {
				t.Fatalf("remaining plan = %+v, %v", next, err)
			}
			if planDigest(plan, cfg.TargetURL) != plan.Digest {
				t.Fatal("deriving approval mutated the original plan")
			}
			e.cfg.TargetURL = "https://example.com/other.git"
			if e.remainingApprovedDigest(plan) != "" {
				t.Fatal("approval was rebound to another destination")
			}
		})
	}
}

func TestRemainingApprovalRequiresExactConfirmedProgress(t *testing.T) {
	cfg := Mirror{TargetURL: "https://example.com/repo.git", Mode: ModeExact, RefSelection: RefSelectionAll}
	plan, perr := ComputePlan(map[string]string{"refs/heads/main": "new"}, map[string]string{"refs/heads/main": "old"}, cfg, "main", "main", "")
	if !errors.Is(perr, ErrNeedsConfirmation) {
		t.Fatal(perr)
	}
	for _, state := range []string{"uncertain", "rejected", "wrong-result", "wrong-expectation", "wrong-approval"} {
		t.Run(state, func(t *testing.T) {
			e := &execution{cfg: cfg, loaded: loadedRun{run: store.MirrorRunRow{ConfirmedDigest: plan.Digest}}}
			e.trackPlanned(plan)
			tracked := e.tracked[0]
			tracked.status = state
			switch state {
			case "wrong-result":
				tracked.status = "ok"
				wrong := "other"
				tracked.confirmed = &wrong
			case "wrong-expectation":
				tracked.status = "ok"
				tracked.confirmed = &tracked.plan.New
				tracked.plan.Old = "other"
			case "wrong-approval":
				tracked.status = "ok"
				tracked.confirmed = &tracked.plan.New
				e.loaded.run.ConfirmedDigest = "unapproved"
			}
			if e.remainingApprovedDigest(plan) != "" {
				t.Fatal("unproven progress changed the approved scope")
			}
		})
	}
}
