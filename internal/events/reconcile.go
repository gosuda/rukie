package events

import (
	"context"
	"fmt"
	"time"

	"encoding/json/jsontext"

	"github.com/gosuda/rukie/internal/store"
)

const (
	ReconcileInterval = 5 * time.Minute
	sourceReconcile   = "reconcile"
)

type Reconciler struct {
	store  *store.Store
	clock  func() time.Time
	list   Lister
	repoID int64
}

func OpenReconciler(st *store.Store, repoID int64, list Lister, clock func() time.Time) *Reconciler {
	if clock == nil {
		clock = time.Now
	}
	return &Reconciler{store: st, repoID: repoID, list: list, clock: clock}
}

func (r *Reconciler) ReconcileRepo(ctx context.Context) (int, error) {
	items, asOf, lerr := ListAll(ctx, r.list, "")
	if lerr != nil {
		return 0, lerr
	}
	listed := make(map[string]string, len(items))
	for _, ref := range items {
		target := ref.Target.Hex()
		if ref.Name == "" || target == "" {
			return 0, fmt.Errorf("ReconcileRepo: %w: ref entry misses name or target", store.ErrInvalid)
		}
		listed[ref.Name] = target
	}
	now := r.clock()
	if now.UnixMilli() <= 0 {
		return 0, fmt.Errorf("ReconcileRepo: clock must be after the Unix epoch")
	}
	synthesized := 0
	werr := r.store.Write(ctx, func(tx *store.WriteTx) error {
		tips, terr := tx.WriteTips(r.repoID)
		if terr != nil {
			return terr
		}
		cursor, cerr := tx.Outbox().Cursor(eventConsumer(r.repoID))
		if cerr != nil {
			return cerr
		}
		if cursor < 0 {
			return fmt.Errorf("ReconcileRepo: stored cursor %d is negative", cursor)
		}
		if uint64(cursor) < asOf {
			return nil
		}
		for name, target := range listed {
			if tips[name] == target {
				continue
			}
			if serr := r.emit(tx, name, tips[name], target, now); serr != nil {
				return serr
			}
			if terr := tx.PutRefTip(r.repoID, name, target, now); terr != nil {
				return terr
			}
			synthesized++
		}
		for name, target := range tips {
			if _, kept := listed[name]; kept {
				continue
			}
			if serr := r.emit(tx, name, target, "", now); serr != nil {
				return serr
			}
			if derr := tx.DeleteRefTip(r.repoID, name); derr != nil {
				return derr
			}
			synthesized++
		}
		return nil
	})
	if werr != nil {
		return 0, werr
	}
	return synthesized, nil
}

func (r *Reconciler) emit(tx *store.WriteTx, name, old, current string, now time.Time) error {
	body, err := marshalEventMap(map[string]any{
		"repo":   r.repoID,
		"name":   name,
		"old":    old,
		"new":    current,
		"source": sourceReconcile,
	})
	if err != nil {
		return err
	}
	_, err = tx.Outbox().Emit(store.Event{
		OccurredAt: now,
		Kind:       kindRefUpdated,
		Body:       jsontext.Value(body),
	})
	return err
}

func ReconcileOffset(repoID int64) time.Duration {
	phase := ((repoID*37)%300 + 300) % 300
	return time.Duration(phase) * time.Second
}

func (r *Reconciler) Run(ctx context.Context) error {
	if _, err := r.ReconcileRepo(ctx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	timer := time.NewTimer(ReconcileOffset(r.repoID))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		if _, err := r.ReconcileRepo(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		timer.Reset(ReconcileInterval)
	}
}
