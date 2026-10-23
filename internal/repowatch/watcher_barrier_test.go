package repowatch

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gosuda/rukie/internal/queue"
	"github.com/gosuda/rukie/internal/store"
)

func purgeJob() store.Job {
	return store.Job{
		Kind:    string(queue.KindRepoPurge),
		Payload: `{"repo_id":1,"path":"ada/hello.git","not_before":0}`,
		RepoID:  1,
	}
}

func TestHandlePurgeRefusesDuringBackup(t *testing.T) {
	acquired := 0
	w := New(Config{Barrier: func() (func(), bool) {
		acquired++
		return func() {}, false
	}})
	err := w.HandlePurge(t.Context(), purgeJob())
	if !errors.Is(err, store.ErrBackupInProgress) {
		t.Fatalf("err = %v", err)
	}
	if acquired != 1 {
		t.Fatalf("acquired = %d", acquired)
	}
}

func TestHandlePurgeReleasesBarrierOnEarlyError(t *testing.T) {
	var acquired atomic.Int64
	var released atomic.Int64
	w := New(Config{Barrier: func() (func(), bool) {
		acquired.Add(1)
		return func() { released.Add(1) }, true
	}})
	err := w.HandlePurge(t.Context(), purgeJob())
	if err == nil {
		t.Fatalf("store-less purge succeeded")
	}
	if acquired.Load() != 1 || released.Load() != 1 {
		t.Fatalf("acquired=%d released=%d", acquired.Load(), released.Load())
	}
}

func TestSuperviseRestartsWorker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := New(Config{})
		w.retryEvery = time.Millisecond
		ctx, stop := context.WithCancel(t.Context())
		calls := 0
		done := make(chan struct{})
		go func() {
			defer close(done)
			w.supervise(ctx, func(context.Context) error {
				calls++
				if calls == 1 {
					return errors.New("boom")
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
