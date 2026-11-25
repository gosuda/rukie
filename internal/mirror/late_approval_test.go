package mirror

import (
	"context"
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/queue"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

func TestLatePushConfirmationPreservesRemainingApproval(t *testing.T) {
	w, st, q, now := openWorker(t)
	repoID := insertRepo(t, st, now, "late-approved-retry")
	mirror := insertMirror(t, st, now, repoID, true)
	cfg, cerr := mirrorConfigOf(mirror)
	if cerr != nil {
		t.Fatal(cerr)
	}
	local := map[string]string{"refs/heads/main": "new", "refs/heads/pending": "next"}
	remote := map[string]string{"refs/heads/main": "old", "refs/heads/pending": "previous"}
	plan, perr := ComputePlan(local, remote, cfg, "main", "main", "")
	if !errors.Is(perr, ErrNeedsConfirmation) {
		t.Fatalf("initial plan = %+v, %v", plan, perr)
	}
	pub := "run-late-approved-retry"
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
	send := func(_ context.Context, _ string, updates []ruk.PushUpdate) (ruk.PushReply, error) {
		refs := make([]ruk.PushRefOutcome, 0, len(updates))
		for _, update := range updates {
			refs = append(refs, ruk.PushRefOutcome{RemoteRef: update.RemoteRef, Status: ruk.UncertainStatus()})
		}
		return ruk.PushReply{Plan: "lost-report", State: "uncertain", Refs: refs}, nil
	}
	if err := e.pushDeletesFirst(testCtx(t), send); err != nil {
		t.Fatal(err)
	}
	if done, err := e.finishCall(testCtx(t), sendUncertain(errors.New("reconciliation unavailable")), &plan, nil); err != nil || !done {
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
		row, gerr := tx.GetMirror(mirror.ID)
		if gerr != nil {
			return gerr
		}
		retryAt = row.NextRunAt
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
	nextJobs, nextErr := w.Queue.ClaimKinds(testCtx(t), "owner", []queue.Kind{queue.KindMirrorRun}, mirrorClaimLease, 1)
	if nextErr != nil || len(nextJobs) != 1 {
		t.Fatalf("retry claim = %v, %v", nextJobs, nextErr)
	}
	next := &execution{worker: *w, owner: "owner", job: nextJobs[0], start: w.Clock(), cfg: cfg, remoteDefault: "main"}
	if done, err := next.load(testCtx(t), retry.PublicID); err != nil || done {
		t.Fatalf("retry load = %v, %v", done, err)
	}
	observed := map[string]string{"refs/heads/main": "new", "refs/heads/pending": "previous"}
	prior, priorErr := next.reconcilePriorUncertain(testCtx(t), observed)
	if priorErr != nil {
		t.Fatal(priorErr)
	}
	remaining, rerr := next.computeApprovedPlan(local, remoteSnapshot{refs: observed, defaultBranch: "main"}, prior)
	if rerr != nil {
		t.Fatalf("late confirmed progress lost remaining approval: %+v, %v", remaining, rerr)
	}
	next.trackPlanned(remaining)
	calls := 0
	refusal := &ruk.Error{Code: wire.CodeCapacity, Message: "target busy"}
	if err := next.pushDeletesFirst(testCtx(t), func(_ context.Context, _ string, updates []ruk.PushUpdate) (ruk.PushReply, error) {
		calls++
		if len(updates) != 1 || updates[0].RemoteRef != "refs/heads/pending" || updates[0].ExpectedOld == nil || updates[0].ExpectedOld.Present == nil || *updates[0].ExpectedOld.Present != "previous" {
			t.Fatalf("retry repeated completed work or changed CAS: %+v", updates)
		}
		return ruk.PushReply{}, refusal
	}); !errors.Is(err, refusal) || calls != 1 {
		t.Fatalf("retry push = %v after %d calls", err, calls)
	}
	if done, err := next.finishCall(testCtx(t), refusal, &remaining, nil); err != nil || !done {
		t.Fatalf("second finishCall = %v, %v", done, err)
	}
	if err := st.Read(testCtx(t), func(tx *store.ReadTx) error {
		finished, gerr := tx.GetRunByPublicID(retry.PublicID)
		if gerr != nil {
			return gerr
		}
		if finished.ConfirmedDigest != retry.ConfirmedDigest {
			t.Fatalf("retry approval audit changed: %+v", finished)
		}
		if _, planErr := ComputePlan(local, observed, cfg, "main", "main", finished.RetryDigest); planErr != nil {
			t.Fatalf("second failure lost recovered approval: %v", planErr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestObservedApprovalRetainsExactOriginalScope(t *testing.T) {
	for _, scenario := range []string{
		"update", "create", "delete", "directory-file", "partial-progress", "lost-response",
		"changed-target", "changed-local", "changed-remote", "new-delete", "wrong-old",
		"wrong-new", "missing-history", "rejected", "no-approval", "wrong-approval",
	} {
		t.Run(scenario, func(t *testing.T) {
			w, st, _, now := openWorker(t)
			repoID := insertRepo(t, st, now, "observed-approval")
			mirror := insertMirror(t, st, now, repoID, true)
			cfg, cerr := mirrorConfigOf(mirror)
			if cerr != nil {
				t.Fatal(cerr)
			}
			local := map[string]string{"refs/heads/main": "new", "refs/heads/pending": "next", "refs/heads/a/b": "child"}
			remote := map[string]string{"refs/heads/main": "old", "refs/heads/pending": "previous", "refs/heads/a": "parent", "refs/tags/obsolete": "tag"}
			progressRef := "refs/heads/main"
			switch scenario {
			case "create":
				progressRef = "refs/heads/created"
				local[progressRef] = "created"
			case "delete":
				progressRef = "refs/tags/obsolete"
			case "directory-file":
				progressRef = "refs/heads/a"
			}
			plan, perr := ComputePlan(local, remote, cfg, "main", "main", "")
			if !errors.Is(perr, ErrNeedsConfirmation) {
				t.Fatalf("initial plan = %+v, %v", plan, perr)
			}
			approved := plan.Digest
			observed := maps.Clone(remote)
			row := store.RefStateRow{MirrorID: mirror.ID, Ref: progressRef, LocalOid: local[progressRef], RemoteOid: remote[progressRef], Status: "uncertain"}
			if row.LocalOid == "" {
				delete(observed, progressRef)
			} else {
				observed[progressRef] = row.LocalOid
			}
			accept := false
			switch scenario {
			case "update", "create", "delete", "directory-file":
				accept = true
			case "partial-progress":
				previous := &execution{cfg: cfg, loaded: loadedRun{run: store.MirrorRunRow{ConfirmedDigest: approved}}}
				previous.trackPlanned(plan)
				for _, tracked := range previous.tracked {
					if tracked.plan.Ref == "refs/tags/obsolete" {
						tracked.status = "deleted"
					}
				}
				approved = previous.remainingApprovedDigest(plan)
				if approved == "" {
					t.Fatal("initial confirmed deletion lost approval")
				}
				delete(observed, "refs/tags/obsolete")
				accept = true
			case "lost-response":
				row.Status = ""
				accept = true
			case "changed-target":
				cfg.TargetURL = "https://example.com/other.git"
			case "changed-local":
				local["refs/heads/pending"] = "unapproved-new"
			case "changed-remote":
				observed["refs/heads/pending"] = "unapproved-old"
			case "new-delete":
				observed["refs/tags/unapproved"] = "unapproved"
			case "wrong-old":
				row.RemoteOid = "not-approved"
			case "wrong-new":
				row.LocalOid = "not-observed"
			case "rejected":
				row.Status = "rejected"
			case "no-approval":
				approved = ""
			case "wrong-approval":
				approved = "not-approved"
			}
			if scenario != "missing-history" {
				if err := st.Write(testCtx(t), func(tx *store.WriteTx) error {
					return tx.ReplaceRefStates(mirror.ID, []store.RefStateRow{row})
				}); err != nil {
					t.Fatal(err)
				}
			}
			e := &execution{worker: *w, cfg: cfg, loaded: loadedRun{
				run: store.MirrorRunRow{ConfirmedDigest: approved}, mirror: mirror,
				repo: store.RepoRow{DefaultBranch: "main"},
			}}
			unchanged := maps.Clone(observed)
			prior, priorErr := e.reconcilePriorUncertain(testCtx(t), observed)
			if priorErr != nil {
				t.Fatal(priorErr)
			}
			result, resultErr := e.computeApprovedPlan(local, remoteSnapshot{refs: observed, defaultBranch: "main"}, prior)
			if !maps.Equal(observed, unchanged) {
				t.Fatal("reconciliation changed the actual remote snapshot")
			}
			if e.loaded.run.ConfirmedDigest != approved {
				t.Fatal("reconciliation changed the original approval audit")
			}
			if !accept {
				if !errors.Is(resultErr, ErrNeedsConfirmation) || e.recoveredApproval != "" {
					t.Fatalf("unapproved scope accepted: %+v, %v", result, resultErr)
				}
				return
			}
			if resultErr != nil {
				t.Fatal(resultErr)
			}
			if e.approvedDigest() != result.Digest {
				t.Fatal("recovered approval does not match remaining work")
			}
			e.cfg.TargetURL = "https://example.com/changed-again.git"
			if _, err := e.computeApprovedPlan(local, remoteSnapshot{refs: observed, defaultBranch: "main"}, prior); !errors.Is(err, ErrNeedsConfirmation) || e.recoveredApproval != "" {
				t.Fatalf("reused execution retained stale approval: %v", err)
			}
		})
	}
}
