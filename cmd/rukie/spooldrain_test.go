package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/events"
	"github.com/gosuda/rukie/internal/store"
	"github.com/gosuda/rukie/internal/supervisor"
)

func openDrainStore(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), store.Options{Path: path})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	return st
}

func spoolRecord(id string) events.PushRecord {
	return events.PushRecord{
		InvocationID: id,
		Landed:       []events.LandedRef{{Name: "refs/heads/main", Old: "000000", New: "abc123"}},
		RepoID:       7,
		ActorID:      3,
		AttemptedAt:  time.Now().UnixMilli(),
	}
}

func spoolPending(t *testing.T, sp *events.Spool) []string {
	t.Helper()
	ids, err := sp.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	return ids
}

func pendingRow(t *testing.T, st *store.Store, id string) (store.PendingPush, error) {
	t.Helper()
	var row store.PendingPush
	err := st.Write(context.Background(), func(tx *store.WriteTx) error {
		got, gerr := tx.GetPendingPush(id)
		if gerr != nil {
			return gerr
		}
		row = got
		return nil
	})
	return row, err
}

// TestSpoolDrainCommitOrLeft pins the drain-end spool invariant: after
// the ordered drain, an in-flight spool record is either committed
// (applied and removed) or left on disk for the next start. Replay
// applies each record inside one store transaction and removes the
// file only after it commits, so a record is never half-applied.
func TestSpoolDrainCommitOrLeft(t *testing.T) {
	dir := t.TempDir()
	st := openDrainStore(t, filepath.Join(dir, "meta.sqlite"))
	sp, err := events.OpenSpool(dir)
	if err != nil {
		t.Fatalf("OpenSpool: %v", err)
	}

	// Committed path: the drain's spool step replays with the real
	// production attempt and the record disappears.
	if serr := sp.Store(spoolRecord("commit-one")); serr != nil {
		t.Fatalf("Store: %v", serr)
	}
	var order []string
	steps, err := supervisor.OrderedDrain(map[string]func(ctx context.Context) error{
		"spool": func(ctx context.Context) error {
			order = append(order, "spool")
			return sp.Replay(ctx, st, events.EnqueuePush)
		},
		"store": func(context.Context) error { order = append(order, "store"); return nil },
		"child": func(context.Context) error { order = append(order, "child"); return nil },
	})
	if err != nil {
		t.Fatalf("OrderedDrain: %v", err)
	}
	if derr := supervisor.Drain(context.Background(), steps); derr != nil {
		t.Fatalf("Drain: %v", derr)
	}
	if got := order; len(got) != 3 || got[0] != "spool" || got[1] != "store" || got[2] != "child" {
		t.Fatalf("drain order = %v, want [spool store child]", got)
	}
	if ids := spoolPending(t, sp); len(ids) != 0 {
		t.Fatalf("committed record still on disk: %v", ids)
	}
	row, err := pendingRow(t, st, "commit-one")
	if err != nil {
		t.Fatalf("read pending push: %v", err)
	}
	if row.InvocationID != "commit-one" {
		t.Fatalf("pending row = %+v, want the replayed invocation", row)
	}

	// Left path: a failing attempt keeps the record on disk and the
	// drain reports the failure without dropping it.
	if serr := sp.Store(spoolRecord("leave-one")); serr != nil {
		t.Fatalf("Store: %v", serr)
	}
	boom := errors.New("attempt refused")
	steps, err = supervisor.OrderedDrain(map[string]func(ctx context.Context) error{
		"spool": func(ctx context.Context) error {
			return sp.Replay(ctx, st, func(*store.WriteTx, events.PushRecord) error {
				return boom
			})
		},
		"child": func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatalf("OrderedDrain: %v", err)
	}
	if err := supervisor.Drain(context.Background(), steps); !errors.Is(err, boom) {
		t.Fatalf("Drain error = %v, want it to carry the attempt failure", err)
	}
	if ids := spoolPending(t, sp); len(ids) != 1 || ids[0] != "leave-one" {
		t.Fatalf("pending after failed replay = %v, want the record left on disk", ids)
	}
	if _, err := pendingRow(t, st, "leave-one"); err == nil {
		t.Fatal("failed replay left a pending row: nothing may commit on the failure path")
	}
}
