package mirror

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	jsonv1 "encoding/json"

	"github.com/gosuda/rukie/internal/queue"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

func blockedPush(plan string) error {
	return &ruk.Error{
		Code:   wire.CodePushUncertain,
		Detail: jsonv1.RawMessage(fmt.Sprintf(`{"push":{"plan":%q,"state":"uncertain","refs":{"items":[{"remote_ref":"refs/heads/main","status":"uncertain"}]}}}`, plan)),
	}
}

func unchangedReconcile(_ context.Context, plan string) (ruk.PushReconcileReply, error) {
	return ruk.PushReconcileReply{Plan: plan}, nil
}

func TestPushRecoveryRetainsCASAndConfirmedRows(t *testing.T) {
	expected := ruk.PresentExpect("original")
	updates := []ruk.PushUpdate{
		{RemoteRef: "refs/heads/landed", Source: ruk.CommitSource("wanted"), ExpectedOld: &expected},
		{RemoteRef: "refs/heads/deleted", Source: ruk.DeleteSource(), ExpectedOld: &expected},
		{RemoteRef: "refs/heads/main", Source: ruk.CommitSource("new"), ExpectedOld: &expected, Force: true},
	}
	var ids []string
	var batches [][]ruk.PushUpdate
	var events []string
	send := func(_ context.Context, id string, updates []ruk.PushUpdate) (ruk.PushReply, error) {
		ids = append(ids, id)
		batches = append(batches, slices.Clone(updates))
		events = append(events, "send")
		if len(batches) == 1 {
			return ruk.PushReply{}, blockedPush("blocked-plan")
		}
		return ruk.PushReply{Plan: "new-plan", State: "rejected", Refs: []ruk.PushRefOutcome{{RemoteRef: "refs/heads/main", Status: ruk.RejectedStatus("remote ref moved")}}}, nil
	}
	reconcile := func(_ context.Context, plan string) (ruk.PushReconcileReply, error) {
		if plan != "blocked-plan" {
			t.Fatalf("reconcile plan = %q", plan)
		}
		events = append(events, "reconcile")
		return ruk.PushReconcileReply{Plan: plan}, nil
	}
	observe := func(context.Context) (map[string]string, error) {
		events = append(events, "observe target")
		return map[string]string{"refs/heads/landed": "wanted", "refs/heads/main": "third-party"}, nil
	}
	e := &execution{}
	reply, err := e.pushWithReconciliation(t.Context(), "initial-request", updates, send, reconcile, observe)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(events, []string{"send", "reconcile", "observe target", "send"}) {
		t.Fatalf("events = %v", events)
	}
	if len(batches) != 2 || len(batches[1]) != 1 || ids[0] == ids[1] {
		t.Fatalf("batches = %+v, ids = %v", batches, ids)
	}
	pending := batches[1][0]
	if pending.RemoteRef != "refs/heads/main" || pending.ExpectedOld != &expected || !pending.Force || *pending.Source.Commit != "new" {
		t.Fatalf("pending update changed its original CAS: %+v", pending)
	}
	if len(updates) != 3 || updates[0].RemoteRef != "refs/heads/landed" {
		t.Fatalf("original updates changed: %+v", updates)
	}
	if reply.Plan != "new-plan" || reply.State != "rejected" || len(reply.Refs) != 3 {
		t.Fatalf("reply = %+v", reply)
	}
	byRef := make(map[string]ruk.PushRefStatus)
	for _, row := range reply.Refs {
		byRef[row.RemoteRef] = row.Status
	}
	if landed := byRef["refs/heads/landed"]; landed.Kind != "confirmed" || landed.New == nil || *landed.New != "wanted" {
		t.Fatalf("landed outcome = %+v", landed)
	}
	if deleted := byRef["refs/heads/deleted"]; deleted.Kind != "confirmed" || deleted.New != nil {
		t.Fatalf("deleted outcome = %+v", deleted)
	}
	if moved := byRef["refs/heads/main"]; moved.Kind != "rejected" {
		t.Fatalf("moved outcome = %+v", moved)
	}
}

func TestPushRecoveryDoesNotSendAnEmptyBatch(t *testing.T) {
	e := &execution{}
	updates := []ruk.PushUpdate{{RemoteRef: "refs/heads/main", Source: ruk.CommitSource("wanted")}}
	calls := 0
	send := func(context.Context, string, []ruk.PushUpdate) (ruk.PushReply, error) {
		calls++
		return ruk.PushReply{}, blockedPush("old-plan")
	}
	observe := func(context.Context) (map[string]string, error) {
		return map[string]string{"refs/heads/main": "wanted"}, nil
	}
	reply, err := e.pushWithReconciliation(t.Context(), "request", updates, send, unchangedReconcile, observe)
	if err != nil || calls != 1 || reply.State != "confirmed" || len(reply.Refs) != 1 || reply.Refs[0].Status.Kind != "confirmed" {
		t.Fatalf("reply = %+v, error = %v, sends = %d", reply, err, calls)
	}
}

func TestPushRecoveryKeepsFailuresAndBoundsRetries(t *testing.T) {
	for _, test := range []struct {
		initial    error
		observeErr error
		name       string
		updates    int
		wantSends  int
		wantReads  int
		newPlans   bool
	}{
		{name: "missing typed plan", initial: &ruk.Error{Code: wire.CodePushUncertain}, updates: 1, wantSends: 1},
		{name: "other refusal", initial: &ruk.Error{Code: wire.CodeForbidden}, updates: 1, wantSends: 1},
		{name: "failed observation", observeErr: errors.New("private origin refused"), updates: 1, wantSends: 1, wantReads: 1},
		{name: "same plan still blocks", updates: 2, wantSends: 2, wantReads: 1},
		{name: "new plans exceed ref count", newPlans: true, updates: 1, wantSends: 2, wantReads: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := &execution{}
			updates := make([]ruk.PushUpdate, test.updates)
			for i := range updates {
				updates[i] = ruk.PushUpdate{RemoteRef: fmt.Sprintf("refs/heads/%d", i), Source: ruk.CommitSource("wanted")}
			}
			sends, reads := 0, 0
			send := func(context.Context, string, []ruk.PushUpdate) (ruk.PushReply, error) {
				sends++
				if test.initial != nil {
					return ruk.PushReply{}, test.initial
				}
				plan := "blocked-plan"
				if test.newPlans {
					plan = fmt.Sprintf("blocked-plan-%d", sends)
				}
				return ruk.PushReply{}, blockedPush(plan)
			}
			observe := func(context.Context) (map[string]string, error) {
				reads++
				return map[string]string{}, test.observeErr
			}
			_, err := e.pushWithReconciliation(t.Context(), "request", updates, send, unchangedReconcile, observe)
			if err == nil || sends != test.wantSends || reads != test.wantReads {
				t.Fatalf("error = %v, sends = %d, reads = %d", err, sends, reads)
			}
			if test.initial != nil && !errors.Is(err, test.initial) {
				t.Fatalf("lost original refusal: %v", err)
			}
			if test.observeErr != nil && !errors.Is(err, test.observeErr) {
				t.Fatalf("lost observation failure: %v", err)
			}
		})
	}
}

func TestBlockedPlanFailureKeepsMirrorScheduled(t *testing.T) {
	for _, test := range []struct {
		name      string
		detail    string
		wantClass string
		minDelay  time.Duration
		blocked   bool
	}{
		{name: "foreign authentication", detail: `{"http":{"status":401}}`, wantClass: "uncertain", blocked: true},
		{name: "current target authentication", detail: `{"http":{"status":401}}`, wantClass: "auth"},
		{name: "foreign rate limit", detail: `{"http":{"status":429,"retry_after_seconds":120}}`, wantClass: "uncertain", minDelay: 120 * time.Second, blocked: true},
		{name: "foreign rate-limited forbidden", detail: `{"http":{"status":403,"retry_after_seconds":120}}`, wantClass: "uncertain", minDelay: 120 * time.Second, blocked: true},
		{name: "foreign moved repository", detail: `{"http":{"status":301,"location":"https://other.example/r.git"}}`, wantClass: "uncertain", blocked: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			w, st, q, now := openWorker(t)
			repoID := insertRepo(t, st, now, "blocked")
			mirror := insertMirror(t, st, now, repoID, true)
			pub := "run-blocked"
			insertRun(t, st, now, mirror.ID, pub)
			if err := st.Write(testCtx(t), func(tx *store.WriteTx) error {
				_, err := tx.TouchMirrorDirty(now, repoID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
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
			refusal := &ruk.Error{Code: wire.CodeForbidden, Message: "remote refused", Detail: jsonv1.RawMessage(test.detail)}
			sends := 0
			send := func(context.Context, string, []ruk.PushUpdate) (ruk.PushReply, error) {
				sends++
				return ruk.PushReply{}, blockedPush("foreign-plan")
			}
			reconcile := func(_ context.Context, plan string) (ruk.PushReconcileReply, error) {
				if test.blocked {
					return ruk.PushReconcileReply{}, refusal
				}
				return ruk.PushReconcileReply{Plan: plan}, nil
			}
			observe := func(context.Context) (map[string]string, error) {
				return nil, refusal
			}
			updates := []ruk.PushUpdate{{RemoteRef: "refs/heads/main", Source: ruk.CommitSource("wanted")}}
			_, recoveryErr := e.pushWithReconciliation(testCtx(t), "request", updates, send, reconcile, observe)
			if !errors.Is(recoveryErr, refusal) || sends != 1 {
				t.Fatalf("recovery error = %v, sends = %d", recoveryErr, sends)
			}
			if done, err := e.finishCall(testCtx(t), recoveryErr, nil, nil); err != nil || !done {
				t.Fatalf("finishCall = %v, %v", done, err)
			}
			jobDone(t, q, jobID)
			if err := st.Read(testCtx(t), func(tx *store.ReadTx) error {
				run, err := tx.GetRunByPublicID(pub)
				if err != nil {
					return err
				}
				result, err := tx.GetMirror(mirror.ID)
				if err != nil {
					return err
				}
				if run.State != "failed" || run.OutcomeClass != test.wantClass || run.AttemptNo != 1 {
					t.Fatalf("run = %+v", run)
				}
				if !test.blocked {
					if result.State != "auth_failed" || result.NextRunAt != 0 || result.DirtySince != 0 {
						t.Fatalf("current target refusal = %+v", result)
					}
					return nil
				}
				if result.State != "failing" || result.NextRunAt < now.Add(test.minDelay).UnixMilli() || result.DirtySince != now.UnixMilli() {
					t.Fatalf("blocked mirror stopped retrying: %+v", result)
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
		})
	}
}
