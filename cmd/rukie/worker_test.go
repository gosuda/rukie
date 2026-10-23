package main

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gosuda/rukie/internal/store"
)

func barrierRetry(err error) bool {
	return errors.Is(err, store.ErrBackupInProgress)
}

func TestSuperviseWorkerRetriesBarrierRefusal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, stop := context.WithCancel(t.Context())
		calls := 0
		done := make(chan struct{})
		go func() {
			defer close(done)
			superviseWorker(ctx, time.Minute, barrierRetry, func(context.Context) error {
				calls++
				if calls == 1 {
					return store.ErrBackupInProgress
				}
				stop()
				return nil
			})
		}()
		<-done
		if calls != 2 {
			t.Fatalf("calls = %d", calls)
		}
	})
}

func TestSuperviseWorkerStopsOnPermanentError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		superviseWorker(t.Context(), time.Minute, barrierRetry, func(context.Context) error {
			calls++
			return errors.New("boom")
		})
		if calls != 1 {
			t.Fatalf("calls = %d", calls)
		}
	})
}

func TestSuperviseWorkerStopsWithContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, stop := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			superviseWorker(ctx, time.Minute, barrierRetry, func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			})
		}()
		stop()
		<-done
	})
}
