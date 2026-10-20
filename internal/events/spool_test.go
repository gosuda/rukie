package events

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/store"
)

func feedTime() time.Time {
	return time.UnixMilli(1700000000000)
}

func feedCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func openFeedStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(feedCtx(t), store.Options{Path: filepath.Join(t.TempDir(), "feed.sqlite")})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	return st
}

func openFeedSpool(t *testing.T) *Spool {
	t.Helper()
	sp, err := OpenSpool(t.TempDir())
	if err != nil {
		t.Fatalf("spool open: %v", err)
	}
	return sp
}

func countOutboxKind(t *testing.T, st *store.Store, kind string) int {
	t.Helper()
	var rows []store.OutboxRow
	err := st.Read(feedCtx(t), func(tx *store.ReadTx) error {
		got, rerr := tx.Outbox().After("probe", 1000)
		if rerr != nil {
			return rerr
		}
		rows = got
		return nil
	})
	if err != nil {
		t.Fatalf("outbox read: %v", err)
	}
	n := 0
	for _, row := range rows {
		if row.Kind == kind {
			n++
		}
	}
	return n
}

func TestSpoolStoreLoadRoundTrip(t *testing.T) {
	sp := openFeedSpool(t)
	rec := PushRecord{
		Landed:       []LandedRef{{Name: "refs/heads/main", Old: "a", New: "b"}},
		InvocationID: "push-01J",
		RepoID:       4,
		ActorID:      7,
		AttemptedAt:  feedTime().UnixMilli(),
	}
	if err := sp.Store(rec); err != nil {
		t.Fatalf("store: %v", err)
	}
	back, err := sp.Load("push-01J")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if back.InvocationID != rec.InvocationID || back.RepoID != 4 || back.ActorID != 7 || len(back.Landed) != 1 || back.Landed[0].New != "b" {
		t.Fatalf("round trip = %+v", back)
	}
	pending, err := sp.Pending()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 || pending[0] != "push-01J" {
		t.Fatalf("pending = %v", pending)
	}
	if err = sp.Remove("push-01J"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	pending, err = sp.Pending()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %v", pending)
	}
}

func TestSpoolRejectsTraversalID(t *testing.T) {
	sp := openFeedSpool(t)
	rec := PushRecord{InvocationID: "../evil", RepoID: 1, AttemptedAt: feedTime().UnixMilli()}
	if err := sp.Store(rec); err == nil {
		t.Fatal("traversal id stored")
	}
	bad := PushRecord{InvocationID: "", RepoID: 1, AttemptedAt: feedTime().UnixMilli()}
	if err := sp.Store(bad); err == nil {
		t.Fatal("empty id stored")
	}
	long := PushRecord{InvocationID: strings.Repeat("a", 97), RepoID: 1, AttemptedAt: feedTime().UnixMilli()}
	if err := sp.Store(long); err == nil {
		t.Fatal("overlong id stored")
	}
	if _, err := sp.Load("../evil"); err == nil {
		t.Fatal("traversal id loaded")
	}
	if err := sp.Remove("../evil"); err == nil {
		t.Fatal("traversal id removed")
	}
}

func TestSpoolReplayRemovesOnSuccess(t *testing.T) {
	sp := openFeedSpool(t)
	st := openFeedStore(t)
	rec := PushRecord{InvocationID: "push-1", RepoID: 2, ActorID: 3, AttemptedAt: feedTime().UnixMilli(),
		Landed: []LandedRef{{Name: "refs/heads/main", Old: "a", New: "b"}}}
	if err := sp.Store(rec); err != nil {
		t.Fatalf("store: %v", err)
	}
	var attempts int
	err := sp.Replay(feedCtx(t), st, func(tx *store.WriteTx, got PushRecord) error {
		attempts++
		_, perr := tx.PutPendingPush(store.PendingPush{
			Landed:       `[]`,
			InvocationID: got.InvocationID,
			RepoID:       got.RepoID,
			ActorID:      got.ActorID,
			AttemptedAt:  got.AttemptedAt,
		})
		return perr
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	pending, err := sp.Pending()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %v", pending)
	}
}

func TestSpoolReplayKeepsOnFailure(t *testing.T) {
	sp := openFeedSpool(t)
	st := openFeedStore(t)
	rec := PushRecord{InvocationID: "push-2", RepoID: 2, AttemptedAt: feedTime().UnixMilli()}
	if err := sp.Store(rec); err != nil {
		t.Fatalf("store: %v", err)
	}
	failing := func(_ *store.WriteTx, _ PushRecord) error {
		return context.DeadlineExceeded
	}
	if err := sp.Replay(feedCtx(t), st, failing); err == nil {
		t.Fatal("failing replay succeeded")
	}
	pending, err := sp.Pending()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %v, want the failed record kept", pending)
	}
	var attempts int
	err = sp.Replay(feedCtx(t), st, func(_ *store.WriteTx, _ PushRecord) error {
		attempts++
		return nil
	})
	if err != nil {
		t.Fatalf("retry replay: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	pending, err = sp.Pending()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %v", pending)
	}
}

func TestSpoolDrainLoopProcesses(t *testing.T) {
	sp := openFeedSpool(t)
	st := openFeedStore(t)
	rec := PushRecord{InvocationID: "push-3", RepoID: 2, AttemptedAt: feedTime().UnixMilli()}
	if err := sp.Store(rec); err != nil {
		t.Fatalf("store: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- DrainLoop(ctx, sp, st, func(_ *store.WriteTx, _ PushRecord) error {
			return nil
		})
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		pending, perr := sp.Pending()
		if perr != nil {
			t.Fatalf("pending: %v", perr)
		}
		if len(pending) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("drain loop never processed the record")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("drain err = %v, want canceled", err)
	}
}

func TestSpoolSurvivesDirectoryRecreation(t *testing.T) {
	dir := t.TempDir()
	sp, err := OpenSpool(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	rec := PushRecord{InvocationID: "push-4", RepoID: 1, AttemptedAt: feedTime().UnixMilli()}
	if err = sp.Store(rec); err != nil {
		t.Fatalf("store: %v", err)
	}
	again, err := OpenSpool(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	back, err := again.Load("push-4")
	if err != nil {
		t.Fatalf("load after reopen: %v", err)
	}
	if back.InvocationID != "push-4" {
		t.Fatalf("record = %+v", back)
	}
	if err = os.RemoveAll(dir); err != nil {
		t.Fatalf("remove dir: %v", err)
	}
}

func TestSpoolReplayQuarantinesPoisonRecord(t *testing.T) {
	sp := openFeedSpool(t)
	st := openFeedStore(t)
	// Seed a pending push bound to another repo so EnqueuePush reports a
	// permanent invocation mismatch for the poison record.
	seed := PushRecord{InvocationID: "poison-1", RepoID: 99, ActorID: 7, AttemptedAt: feedTime().UnixMilli()}
	if err := sp.Store(seed); err != nil {
		t.Fatalf("store seed: %v", err)
	}
	if err := sp.Store(PushRecord{InvocationID: "good-1", RepoID: 2, ActorID: 3, AttemptedAt: feedTime().UnixMilli()}); err != nil {
		t.Fatalf("store good: %v", err)
	}
	err := st.Write(feedCtx(t), func(tx *store.WriteTx) error {
		_, perr := tx.PutPendingPush(store.PendingPush{
			Landed:       `[]`,
			InvocationID: "poison-1",
			RepoID:       100,
			ActorID:      7,
			AttemptedAt:  feedTime().UnixMilli(),
		})
		return perr
	})
	if err != nil {
		t.Fatalf("seed pending push: %v", err)
	}
	var applied []string
	rerr := sp.Replay(feedCtx(t), st, func(tx *store.WriteTx, got PushRecord) error {
		applied = append(applied, got.InvocationID)
		return EnqueuePush(tx, got)
	})
	if rerr == nil {
		t.Fatal("replay with a poison record succeeded, want the mismatch reported")
	}
	if len(applied) != 2 {
		t.Fatalf("applied = %v, want both records attempted: one poison record must not block later ones", applied)
	}
	pending, err := sp.Pending()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %v, want empty: poison quarantined, good applied", pending)
	}
	again := sp.Replay(feedCtx(t), st, func(_ *store.WriteTx, got PushRecord) error {
		t.Fatalf("replayed quarantined record %s", got.InvocationID)
		return nil
	})
	if again != nil {
		t.Fatalf("second replay: %v, want nil: dead records never reprocess", again)
	}
}
