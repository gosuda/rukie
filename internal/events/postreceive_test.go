package events

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

func postReceiveCall() wire.HookCall {
	return wire.HookCall{
		Phase: wire.HookPostReceive,
		Commands: []wire.HookCommand{
			{Name: "refs/heads/main", Old: strings.Repeat("a", 40), New: strings.Repeat("b", 40), Authorized: true},
			{Name: "refs/tags/v1", Old: strings.Repeat("0", 40), New: strings.Repeat("c", 40), Authorized: true},
		},
		PushOptions: []string{"ci.skip"},
		NewCommits:  []string{strings.Repeat("b", 40)},
		PackBytes:   120,
		DeadlineMs:  5000,
	}
}

func jobState(t *testing.T, st *store.Store, id string) store.Job {
	t.Helper()
	var job store.Job
	err := st.Read(feedCtx(t), func(tx *store.ReadTx) error {
		got, rerr := tx.GetJob(id)
		if rerr != nil {
			return rerr
		}
		job = got
		return nil
	})
	if err != nil {
		t.Fatalf("job %s: %v", id, err)
	}
	return job
}

func TestDeriveInvocationIDStableAndScoped(t *testing.T) {
	call := postReceiveCall()
	first := DeriveInvocationID(11, 7, call)
	second := DeriveInvocationID(11, 7, call)
	if first != second {
		t.Fatalf("derivation unstable: %q vs %q", first, second)
	}
	if !strings.HasPrefix(first, "evt-") {
		t.Fatalf("id %q misses the evt prefix", first)
	}
	if strings.HasPrefix(first, "push-") {
		t.Fatalf("id %q collides with the push prefix", first)
	}
	if other := DeriveInvocationID(11, 8, call); other == first {
		t.Fatalf("different actor derived the same id %q", first)
	}
	if other := DeriveInvocationID(12, 7, call); other == first {
		t.Fatalf("different repo derived the same id %q", first)
	}
	changed := postReceiveCall()
	changed.Commands[0].New = strings.Repeat("d", 40)
	if other := DeriveInvocationID(11, 7, changed); other == first {
		t.Fatalf("different refs derived the same id %q", first)
	}
}

func TestHandlePostReceiveEnqueuesAll(t *testing.T) {
	st := openFeedStore(t)
	sp := openFeedSpool(t)
	meta := PushMeta{InvocationID: "push-01FEEDTEST", RepoID: 11, ActorID: 7}
	reply := HandlePostReceive(feedCtx(t), st, sp, meta, postReceiveCall(), feedTime())
	if reply.RejectAll {
		t.Fatalf("post-receive rejected: %v", reply.Reason)
	}
	if len(reply.Verdicts) != 2 {
		t.Fatalf("verdicts = %+v", reply.Verdicts)
	}
	for _, verdict := range reply.Verdicts {
		if verdict.Verdict.Kind != wire.HookAccept {
			t.Fatalf("verdict = %+v", verdict)
		}
	}
	pending, err := sp.Pending()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("spool kept %+v after success", pending)
	}
	for _, id := range []string{"hook-delivery:push-01FEEDTEST", "mirror-run:push-01FEEDTEST", "ci-plan:push-01FEEDTEST"} {
		job := jobState(t, st, id)
		if job.State != "queued" || job.RepoID != 11 {
			t.Fatalf("job %s = %+v", id, job)
		}
	}
	if n := countOutboxKind(t, st, kindPushReceived); n != 1 {
		t.Fatalf("push.received rows = %d, want 1", n)
	}
	if n := countOutboxKind(t, st, kindRefUpdated); n != 2 {
		t.Fatalf("ref.updated rows = %d, want 2", n)
	}
	var tips map[string]string
	err = st.Read(feedCtx(t), func(tx *store.ReadTx) error {
		got, rerr := tx.RefTips(11)
		if rerr != nil {
			return rerr
		}
		tips = got
		return nil
	})
	if err != nil {
		t.Fatalf("tips: %v", err)
	}
	if tips["refs/heads/main"] != strings.Repeat("b", 40) || tips["refs/tags/v1"] != strings.Repeat("c", 40) {
		t.Fatalf("tips = %+v", tips)
	}
}

func TestHandlePostReceiveDedupe(t *testing.T) {
	st := openFeedStore(t)
	sp := openFeedSpool(t)
	meta := PushMeta{RepoID: 11, ActorID: 7}
	ctx := feedCtx(t)
	first := HandlePostReceive(ctx, st, sp, meta, postReceiveCall(), feedTime())
	if first.RejectAll {
		t.Fatalf("first rejected: %v", first.Reason)
	}
	jobsBefore := countOutboxKind(t, st, kindRefUpdated)
	second := HandlePostReceive(ctx, st, sp, meta, postReceiveCall(), feedTime())
	if second.RejectAll {
		t.Fatalf("replay rejected: %v", second.Reason)
	}
	if n := countOutboxKind(t, st, kindRefUpdated); n != jobsBefore {
		t.Fatalf("replay enqueued %d rows, want %d", n, jobsBefore)
	}
	pending, err := sp.Pending()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("spool kept %+v after deduped replay", pending)
	}
}

func TestHandlePostReceiveDerivesID(t *testing.T) {
	st := openFeedStore(t)
	sp := openFeedSpool(t)
	reply := HandlePostReceive(feedCtx(t), st, sp, PushMeta{RepoID: 5, ActorID: 6}, postReceiveCall(), feedTime())
	if reply.RejectAll {
		t.Fatalf("rejected: %v", reply.Reason)
	}
	derived := DeriveInvocationID(5, 6, postReceiveCall())
	found := false
	err := st.Write(feedCtx(t), func(tx *store.WriteTx) error {
		got, gerr := tx.GetPendingPush(derived)
		if gerr != nil {
			return gerr
		}
		found = got.RepoID == 5 && got.ActorID == 6
		return nil
	})
	if err != nil {
		t.Fatalf("pending lookup: %v", err)
	}
	if !found {
		t.Fatal("derived invocation row missing")
	}
}

func TestHandlePostReceiveResolvesPreRecorded(t *testing.T) {
	st := openFeedStore(t)
	sp := openFeedSpool(t)
	pre := PushRecord{InvocationID: "push-pre-1", RepoID: 11, ActorID: 7, AttemptedAt: feedTime().UnixMilli()}
	err := st.Write(feedCtx(t), func(tx *store.WriteTx) error {
		_, rerr := RecordPush(tx, pre)
		return rerr
	})
	if err != nil {
		t.Fatalf("pre-record: %v", err)
	}
	meta := PushMeta{InvocationID: "push-pre-1", RepoID: 11, ActorID: 7}
	reply := HandlePostReceive(feedCtx(t), st, sp, meta, postReceiveCall(), feedTime())
	if reply.RejectAll {
		t.Fatalf("resolved post-receive rejected: %v", reply.Reason)
	}
	var landed string
	var enqueued int64
	err = st.Write(feedCtx(t), func(tx *store.WriteTx) error {
		got, gerr := tx.GetPendingPush("push-pre-1")
		if gerr != nil {
			return gerr
		}
		landed = got.Landed
		enqueued = got.Enqueued
		return nil
	})
	if err != nil {
		t.Fatalf("pending lookup: %v", err)
	}
	if enqueued != 1 {
		t.Fatal("post-receive left the push pending")
	}
	if !strings.Contains(landed, "refs/heads/main") {
		t.Fatalf("landed = %s, want the post-receive refs", landed)
	}
	if n := countOutboxKind(t, st, kindRefUpdated); n != 2 {
		t.Fatalf("ref.updated rows = %d, want 2", n)
	}
}

func TestHandlePostReceiveRejectsMismatchedInvocation(t *testing.T) {
	st := openFeedStore(t)
	sp := openFeedSpool(t)
	pre := PushRecord{InvocationID: "push-clash", RepoID: 11, ActorID: 7, AttemptedAt: feedTime().UnixMilli()}
	err := st.Write(feedCtx(t), func(tx *store.WriteTx) error {
		_, rerr := RecordPush(tx, pre)
		return rerr
	})
	if err != nil {
		t.Fatalf("pre-record: %v", err)
	}
	meta := PushMeta{InvocationID: "push-clash", RepoID: 12, ActorID: 7}
	reply := HandlePostReceive(feedCtx(t), st, sp, meta, postReceiveCall(), feedTime())
	if !reply.RejectAll {
		t.Fatal("cross-repo invocation reuse accepted")
	}
	pending, err := sp.Pending()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("spool kept %+v, want the rejected record for drain inspection", pending)
	}
	if n := countOutboxKind(t, st, kindRefUpdated); n != 0 {
		t.Fatalf("mismatched invocation enqueued %d rows", n)
	}
}

func TestHookSinkRecordAndHandle(t *testing.T) {
	st := openFeedStore(t)
	sp := openFeedSpool(t)
	sink := OpenHookSink(st, sp, feedTime)
	rec := PushRecord{InvocationID: "push-sink-1", RepoID: 11, ActorID: 7, AttemptedAt: feedTime().UnixMilli()}
	inserted, err := sink.Record(feedCtx(t), rec)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if !inserted {
		t.Fatal("first record reported duplicate")
	}
	inserted, err = sink.Record(feedCtx(t), rec)
	if err != nil {
		t.Fatalf("re-record: %v", err)
	}
	if inserted {
		t.Fatal("re-record reported inserted")
	}
	reply := sink.Handle(feedCtx(t), PushMeta{InvocationID: "push-sink-1", RepoID: 11, ActorID: 7}, postReceiveCall())
	if reply.RejectAll {
		t.Fatalf("sink handle rejected: %v", reply.Reason)
	}
}

func TestHandlePostReceiveWrongPhase(t *testing.T) {
	st := openFeedStore(t)
	sp := openFeedSpool(t)
	call := postReceiveCall()
	call.Phase = wire.HookPreReceive
	reply := HandlePostReceive(feedCtx(t), st, sp, PushMeta{InvocationID: "push-x", RepoID: 1}, call, feedTime())
	if !reply.RejectAll {
		t.Fatal("pre-receive accepted by the post-receive handler")
	}
	pending, err := sp.Pending()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("spool kept %+v for wrong phase", pending)
	}
	if n := countOutboxKind(t, st, kindPushReceived); n != 0 {
		t.Fatalf("wrong phase enqueued %d rows", n)
	}
}

func TestHandlePostReceiveEmptyCommands(t *testing.T) {
	st := openFeedStore(t)
	sp := openFeedSpool(t)
	call := postReceiveCall()
	call.Commands = nil
	reply := HandlePostReceive(feedCtx(t), st, sp, PushMeta{InvocationID: "push-x", RepoID: 1}, call, feedTime())
	if !reply.RejectAll {
		t.Fatal("empty commands accepted")
	}
}

func TestHandlePostReceiveZeroTipDeletes(t *testing.T) {
	st := openFeedStore(t)
	sp := openFeedSpool(t)
	meta := PushMeta{InvocationID: "push-del", RepoID: 21, ActorID: 7}
	seed := wire.HookCall{
		Phase:    wire.HookPostReceive,
		Commands: []wire.HookCommand{{Name: "refs/heads/gone", Old: strings.Repeat("a", 40), New: strings.Repeat("b", 40)}},
	}
	if reply := HandlePostReceive(feedCtx(t), st, sp, meta, seed, feedTime()); reply.RejectAll {
		t.Fatalf("seed rejected: %v", reply.Reason)
	}
	wipe := wire.HookCall{
		Phase:    wire.HookPostReceive,
		Commands: []wire.HookCommand{{Name: "refs/heads/gone", Old: strings.Repeat("b", 40), New: strings.Repeat("0", 40)}},
	}
	meta.InvocationID = "push-del-2"
	if reply := HandlePostReceive(feedCtx(t), st, sp, meta, wipe, feedTime()); reply.RejectAll {
		t.Fatalf("delete rejected: %v", reply.Reason)
	}
	var tips map[string]string
	err := st.Read(feedCtx(t), func(tx *store.ReadTx) error {
		got, rerr := tx.RefTips(21)
		if rerr != nil {
			return rerr
		}
		tips = got
		return nil
	})
	if err != nil {
		t.Fatalf("tips: %v", err)
	}
	if _, ok := tips["refs/heads/gone"]; ok {
		t.Fatalf("deleted tip kept: %+v", tips)
	}
	if n := countOutboxKind(t, st, kindRefUpdated); n != 2 {
		t.Fatalf("ref.updated rows = %d, want 2", n)
	}
}

func TestPostReceiveAdapterMatchesHookSite(t *testing.T) {
	st := openFeedStore(t)
	sp := openFeedSpool(t)
	var callerUserID int64 = 7
	var hookRepoID int64 = 11
	requestID := "push-01FEEDWIRE" + strings.Repeat("0", 10)
	call := postReceiveCall()
	var handle ruk.HookHandler = func(ctx context.Context, got wire.HookCall) wire.HookReply {
		meta := PushMeta{InvocationID: requestID, RepoID: hookRepoID, ActorID: callerUserID}
		return HandlePostReceive(ctx, st, sp, meta, got, time.Now())
	}
	reply := handle(feedCtx(t), call)
	if reply.RejectAll {
		t.Fatalf("wired handler rejected: %v", reply.Reason)
	}
	var invocation string
	err := st.Write(feedCtx(t), func(tx *store.WriteTx) error {
		got, gerr := tx.GetPendingPush(requestID)
		if gerr != nil {
			return gerr
		}
		invocation = got.InvocationID
		return nil
	})
	if err != nil {
		t.Fatalf("pending lookup: %v", err)
	}
	if invocation != requestID {
		t.Fatalf("invocation = %q, want %q", invocation, requestID)
	}
}

func TestHandlePostReceiveOverlongInvocation(t *testing.T) {
	st := openFeedStore(t)
	sp := openFeedSpool(t)
	meta := PushMeta{InvocationID: strings.Repeat("p", 97), RepoID: 1}
	reply := HandlePostReceive(feedCtx(t), st, sp, meta, postReceiveCall(), feedTime())
	if !reply.RejectAll {
		t.Fatal("overlong invocation accepted")
	}
}

func TestHandlePostReceiveSpoolFailure(t *testing.T) {
	st := openFeedStore(t)
	sp := openFeedSpool(t)
	if err := sp.Remove("missing"); err == nil {
		t.Fatal("removing a missing record succeeded")
	}
	broken := &Spool{dir: sp.dir + "-gone"}
	reply := HandlePostReceive(feedCtx(t), st, broken, PushMeta{InvocationID: "push-x", RepoID: 1}, postReceiveCall(), feedTime())
	if !reply.RejectAll {
		t.Fatal("spool failure accepted")
	}
	if n := countOutboxKind(t, st, kindPushReceived); n != 0 {
		t.Fatalf("failed spool enqueued %d rows", n)
	}
}
