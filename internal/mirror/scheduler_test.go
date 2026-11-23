package mirror

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/queue"
	"github.com/gosuda/rukie/internal/store"
)

func TestConcurrentSchedulersUseLatestApprovalAndQueueOnce(t *testing.T) {
	const schedulerCount = 4
	ctx := testCtx(t)
	now := time.Unix(1700000000, 0).UTC()
	arrived := make(chan struct{}, schedulerCount)
	release := make(chan struct{})
	releaseAll := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseAll)
	var gate atomic.Bool
	var waiting atomic.Int64
	st, err := store.Open(ctx, store.Options{
		Path: t.TempDir() + "/scheduler.sqlite",
		Barrier: func() (func(), bool) {
			if gate.Load() && waiting.Add(1) <= schedulerCount {
				arrived <- struct{}{}
				select {
				case <-release:
				case <-ctx.Done():
					return func() {}, false
				}
			}
			return func() {}, true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	repoID := insertRepo(t, st, now, "concurrent-schedule")
	mirror := insertMirror(t, st, now, repoID, true)
	if err := st.Write(ctx, func(tx *store.WriteTx) error {
		if _, err := tx.InsertRun(now, store.MirrorRunRow{
			PublicID:        "old-approved-attempt",
			MirrorID:        mirror.ID,
			Kind:            "manual",
			State:           "failed",
			Trigger:         "manual",
			ConfirmedDigest: "old-approval",
		}); err != nil {
			return err
		}
		return tx.SetMirrorResult(now, mirror.ID, store.MirrorResult{
			State:     "failing",
			NextRunAt: now.UnixMilli(),
		})
	}); err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return now }
	q := queue.Open(st, clock)
	w := Worker{Store: st, Queue: q, Clock: clock}
	errs := make(chan error, schedulerCount)
	gate.Store(true)
	for range schedulerCount {
		go func() { errs <- w.scheduleOnce(ctx) }()
	}
	for range schedulerCount {
		select {
		case <-arrived:
		case <-ctx.Done():
			t.Fatal("schedulers did not reach the write barrier")
		}
	}
	if err := st.Write(ctx, func(tx *store.WriteTx) error {
		_, err := tx.InsertRun(now, store.MirrorRunRow{
			PublicID: "new-unapproved-attempt",
			MirrorID: mirror.ID,
			Kind:     "manual",
			State:    "failed",
			Trigger:  "manual",
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	releaseAll()
	for range schedulerCount {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("schedulers did not finish")
		}
	}
	var runs []store.MirrorRunRow
	if err := st.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		runs, _, err = tx.ListRuns(mirror.ID, "queued", "", 10)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ConfirmedDigest != "" {
		t.Fatalf("queued runs duplicated or resurrected old approval: %+v", runs)
	}
	jobs, err := q.ClaimKinds(ctx, "scheduler-test", []queue.Kind{queue.KindMirrorRun}, mirrorClaimLease, 10)
	if err != nil || len(jobs) != 1 || jobs[0].ID != "mirror-run:"+runs[0].PublicID {
		t.Fatalf("queued jobs = %+v, %v", jobs, err)
	}
	if kind, pub, _ := splitPayload(jobs[0].Payload); kind != payloadKindRun || pub != runs[0].PublicID {
		t.Fatalf("queued payload = %q", jobs[0].Payload)
	}
}
