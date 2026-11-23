package mirror

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	jsonv1 "encoding/json"

	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
)

func blockedPush(plan string) error {
	return &ruk.Error{
		Code:   wire.CodePushUncertain,
		Detail: jsonv1.RawMessage(fmt.Sprintf(`{"push":{"plan":%q,"state":"uncertain","refs":{"items":[{"remote_ref":"refs/heads/main","status":"uncertain"}]}}}`, plan)),
	}
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
	observe := func(_ context.Context, plan string) (map[string]string, error) {
		if plan != "blocked-plan" {
			t.Fatalf("reconcile plan = %q", plan)
		}
		events = append(events, "observe target")
		return map[string]string{"refs/heads/landed": "wanted", "refs/heads/main": "third-party"}, nil
	}
	e := &execution{}
	reply, err := e.pushWithReconciliation(t.Context(), "initial-request", updates, send, observe)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(events, []string{"send", "observe target", "send"}) {
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
	observe := func(context.Context, string) (map[string]string, error) {
		return map[string]string{"refs/heads/main": "wanted"}, nil
	}
	reply, err := e.pushWithReconciliation(t.Context(), "request", updates, send, observe)
	if err != nil || calls != 1 || reply.State != "confirmed" || len(reply.Refs) != 1 || reply.Refs[0].Status.Kind != "confirmed" {
		t.Fatalf("reply = %+v, error = %v, sends = %d", reply, err, calls)
	}
}

func TestPushRecoveryKeepsFailuresAndBoundsRetries(t *testing.T) {
	for _, test := range []struct {
		name       string
		initial    error
		observeErr error
		newPlans   bool
		updates    int
		wantSends  int
		wantReads  int
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
			observe := func(context.Context, string) (map[string]string, error) {
				reads++
				return map[string]string{}, test.observeErr
			}
			_, err := e.pushWithReconciliation(t.Context(), "request", updates, send, observe)
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
