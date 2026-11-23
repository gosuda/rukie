package mirror

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/queue"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

func TestReconcileGuardStopsAutomaticMirrorRecovery(t *testing.T) {
	for _, route := range []string{"blocked-push", "uncertain-result"} {
		for _, failure := range []string{"rewritten", "original-remote-required", "plan-mismatch", "incomplete"} {
			t.Run(route+"/"+failure, func(t *testing.T) {
				w, st, q, now := openWorker(t)
				repoID := insertRepo(t, st, now, "reconcile-guard")
				mirror := insertMirror(t, st, now, repoID, true)
				cfg, configErr := mirrorConfigOf(mirror)
				if configErr != nil {
					t.Fatal(configErr)
				}
				local := map[string]string{"refs/heads/main": "new"}
				remote := map[string]string{"refs/heads/main": "old"}
				plan, planErr := ComputePlan(local, remote, cfg, "main", "main", "")
				if !errors.Is(planErr, ErrNeedsConfirmation) {
					t.Fatalf("initial plan = %+v, %v", plan, planErr)
				}
				const pub = "run-reconcile-guard"
				run := insertRun(t, st, now, mirror.ID, pub)
				run.ConfirmedDigest = plan.Digest
				if err := st.Write(testCtx(t), func(tx *store.WriteTx) error {
					if updateErr := tx.UpdateRun(now, run.ID, run); updateErr != nil {
						return updateErr
					}
					_, dirtyErr := tx.TouchMirrorDirty(now, repoID)
					return dirtyErr
				}); err != nil {
					t.Fatal(err)
				}
				const jobID = "mirror-run:" + pub
				enqueueMirrorJob(t, q, jobID, repoID, mustMarshalPayload(t, map[string]any{"run": pub, "mirror": mirror.ID}))
				claimed, claimErr := q.ClaimKinds(testCtx(t), "owner", []queue.Kind{queue.KindMirrorRun}, mirrorClaimLease, 1)
				if claimErr != nil || len(claimed) != 1 {
					t.Fatalf("claim = %+v, %v", claimed, claimErr)
				}
				e := &execution{worker: *w, owner: "owner", job: claimed[0], start: now, cfg: cfg}
				if done, err := e.load(testCtx(t), pub); err != nil || done {
					t.Fatalf("load = %v, %v", done, err)
				}
				e.trackPlanned(plan)
				const blockedPlan = "legacy-plan"
				if route == "uncertain-result" {
					e.tracked[0].status = "uncertain"
					e.tracked[0].planID = blockedPlan
				}
				before := *e.tracked[0]
				reconciles, sends, observations := 0, 0, 0
				reconcile := func(_ context.Context, planID string) (ruk.PushReconcileReply, error) {
					reconciles++
					if planID != blockedPlan {
						t.Fatalf("reconcile plan = %q", planID)
					}
					if failure == "original-remote-required" {
						return ruk.PushReconcileReply{}, &ruk.Error{Code: wire.CodeInvalid, Message: "legacy push plan requires explicit original_remote"}
					}
					if failure == "incomplete" {
						return ruk.PushReconcileReply{}, &ruk.IncompleteReconciliationError{Reason: "missing continuation page"}
					}
					if failure == "plan-mismatch" {
						planID = "another-plan"
					}
					old := "old"
					return ruk.PushReconcileReply{Plan: planID, Rewritten: failure == "rewritten", Observations: []ruk.ReconcileObservation{
						{RemoteRef: "refs/heads/main", Observed: &old, Verdict: "equal_old"},
					}}, nil
				}
				var recoveryErr error
				if route == "blocked-push" {
					updates := []ruk.PushUpdate{e.updateForMode(e.tracked[0])}
					_, recoveryErr = e.pushWithReconciliation(testCtx(t), "initial-request", updates,
						func(context.Context, string, []ruk.PushUpdate) (ruk.PushReply, error) {
							sends++
							return ruk.PushReply{}, blockedPush(blockedPlan)
						}, reconcile, func(context.Context) (map[string]string, error) {
							observations++
							return remote, nil
						})
					if sends != 1 {
						t.Fatalf("blocked push sent %d times", sends)
					}
				} else {
					recoveryErr = e.resolveUncertainWith(testCtx(t), reconcile, func(context.Context, []*trackedRef) error {
						sends++
						return nil
					})
					if sends != 0 {
						t.Fatalf("uncertain result was resent %d times", sends)
					}
				}
				if _, ok := errors.AsType[*reconcileNeedsAttentionError](recoveryErr); !ok {
					t.Fatalf("recovery error is not terminal: %v", recoveryErr)
				}
				if failure == "original-remote-required" && !errors.Is(recoveryErr, ruk.ErrInvalid) {
					t.Fatalf("recovery lost its typed argument failure: %v", recoveryErr)
				}
				if reconciles != 1 || observations != 0 || *e.tracked[0] != before {
					t.Fatalf("recovery changed state after refusal: calls = %d, reads = %d, ref = %+v", reconciles, observations, *e.tracked[0])
				}
				if done, err := e.finishCall(testCtx(t), sendUncertain(fmt.Errorf("recovery: %w", recoveryErr)), &plan, nil); err != nil || !done {
					t.Fatalf("finishCall = %v, %v", done, err)
				}
				jobDone(t, q, jobID)
				if err := st.Read(testCtx(t), func(tx *store.ReadTx) error {
					finished, readErr := tx.GetRunByPublicID(pub)
					if readErr != nil {
						return readErr
					}
					if finished.State != "failed" || finished.OutcomeClass != "needs_attention" || finished.RetryDigest != "" || finished.ConfirmedDigest != plan.Digest {
						t.Fatalf("run retained automatic approval after refusal: %+v", finished)
					}
					current, mirrorErr := tx.GetMirror(mirror.ID)
					if mirrorErr != nil {
						return mirrorErr
					}
					if current.State != "needs_attention" || current.NextRunAt != 0 || current.DirtySince != 0 {
						t.Fatalf("terminal reconciliation kept the mirror scheduled: %+v", current)
					}
					due, dueErr := tx.DueMirrors(now.Add(24*time.Hour).UnixMilli(), 10)
					if dueErr != nil {
						return dueErr
					}
					if len(due) != 0 {
						t.Fatalf("terminal reconciliation remained due: %+v", due)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestLegacyReconcileExtraRefsPreserveRequestedCAS(t *testing.T) {
	old := "original"
	expected := ruk.PresentExpect(old)
	update := ruk.PushUpdate{RemoteRef: "refs/heads/main", Source: ruk.CommitSource("wanted"), ExpectedOld: &expected, Force: true}
	reply := ruk.PushReconcileReply{Plan: "old-plan", Observations: []ruk.ReconcileObservation{
		{RemoteRef: "refs/heads/main", Verdict: "equal_old", Observed: &old},
		{RemoteRef: "refs/heads/extra-one", Verdict: "equal_new"},
		{RemoteRef: "refs/heads/extra-two", Verdict: "moved"},
	}}
	reconcile := func(context.Context, string) (ruk.PushReconcileReply, error) { return reply, nil }
	e := &execution{}
	sends := 0
	_, recoveryErr := e.pushWithReconciliation(t.Context(), "request", []ruk.PushUpdate{update},
		func(_ context.Context, _ string, updates []ruk.PushUpdate) (ruk.PushReply, error) {
			sends++
			if len(updates) != 1 || updates[0].ExpectedOld != &expected || !updates[0].Force || *updates[0].Source.Commit != "wanted" {
				t.Fatalf("recovery changed the requested CAS: %+v", updates)
			}
			if sends == 1 {
				return ruk.PushReply{}, blockedPush("old-plan")
			}
			return ruk.PushReply{Plan: "new-plan", State: "confirmed"}, nil
		}, reconcile, func(context.Context) (map[string]string, error) {
			return map[string]string{"refs/heads/main": old}, nil
		})
	if recoveryErr != nil || sends != 2 {
		t.Fatalf("legacy extra refs blocked recovery: %v, sends = %d", recoveryErr, sends)
	}
	tracked := &trackedRef{
		plan:   RefPlan{Ref: update.RemoteRef, Old: old, New: "wanted", Action: ActionForceUpdate},
		status: "uncertain", planID: "old-plan",
	}
	e.tracked = []*trackedRef{tracked}
	resends := 0
	if err := e.resolveUncertainWith(t.Context(), reconcile, func(_ context.Context, refs []*trackedRef) error {
		resends++
		if len(refs) != 1 || refs[0] != tracked || refs[0].plan.Old != old || refs[0].plan.New != "wanted" {
			t.Fatalf("uncertain reconciliation changed the requested ref: %+v", refs)
		}
		return nil
	}); err != nil || resends != 1 {
		t.Fatalf("legacy extra refs blocked uncertain recovery: %v, sends = %d", err, resends)
	}
}

func TestPartialReconcileFailureDoesNotResend(t *testing.T) {
	for _, route := range []string{"blocked-push", "uncertain-result"} {
		t.Run(route, func(t *testing.T) {
			tracked := &trackedRef{
				plan: RefPlan{Ref: "refs/heads/main", Old: "old", New: "wanted", Action: ActionForceUpdate},
				status: "uncertain", planID: "plan",
			}
			e := &execution{tracked: []*trackedRef{tracked}}
			before := *tracked
			sends, reads := 0, 0
			reconcile := func(context.Context, string) (ruk.PushReconcileReply, error) {
				wanted := "wanted"
				return ruk.PushReconcileReply{
					Plan: "plan", State: "uncertain",
					Observations: []ruk.ReconcileObservation{{RemoteRef: tracked.plan.Ref, Observed: &wanted, Verdict: "equal_new"}},
				}, fmt.Errorf("final reconciliation page: %w", ruk.ErrIo)
			}
			var recoveryErr error
			if route == "blocked-push" {
				_, recoveryErr = e.pushWithReconciliation(t.Context(), "request", []ruk.PushUpdate{{
					RemoteRef: tracked.plan.Ref, Source: ruk.CommitSource(tracked.plan.New),
				}}, func(context.Context, string, []ruk.PushUpdate) (ruk.PushReply, error) {
					sends++
					return ruk.PushReply{}, blockedPush("plan")
				}, reconcile, func(context.Context) (map[string]string, error) {
					reads++
					return nil, nil
				})
				if sends != 1 {
					t.Fatalf("partial reconciliation resent the push %d times", sends)
				}
			} else {
				recoveryErr = e.resolveUncertainWith(t.Context(), reconcile, func(context.Context, []*trackedRef) error {
					sends++
					return nil
				})
				if sends != 0 {
					t.Fatalf("partial reconciliation resent %d refs", sends)
				}
			}
			if !errors.Is(recoveryErr, ruk.ErrIo) || reads != 0 || *tracked != before {
				t.Fatalf("partial reconciliation changed the ref or error: %+v, %v, reads = %d", tracked, recoveryErr, reads)
			}
			if _, terminal := errors.AsType[*ruk.IncompleteReconciliationError](recoveryErr); terminal {
				t.Fatalf("transport failure requires operator recovery: %v", recoveryErr)
			}
		})
	}
}
