package store

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestEncodeULIDVectors(t *testing.T) {
	var zero [16]byte
	if got := encodeULID(zero); got != strings.Repeat("0", 26) {
		t.Fatalf("zero ULID = %q", got)
	}
	var one [16]byte
	one[15] = 1
	if got := encodeULID(one); got != strings.Repeat("0", 25)+"1" {
		t.Fatalf("ULID of 1 = %q", got)
	}
	var ts [16]byte
	putBigEndian48(ts[:6], 1469922850259)
	if got := encodeULID(ts); !strings.HasPrefix(got, "01ARZ3NDEK") {
		t.Fatalf("timestamp prefix = %q, want 01ARZ3NDEK for timestamp 1469922850259", got)
	}
}

func TestNewIDShapeAndMonotonic(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	prev := NewID(now)
	if len(prev) != 26 {
		t.Fatalf("ID length %d, want 26", len(prev))
	}
	for range 20000 {
		next := NewID(now)
		if next <= prev {
			t.Fatalf("IDs not strictly increasing: %s then %s", prev, next)
		}
		prev = next
	}
	back := NewID(now.Add(-time.Hour))
	if back <= prev {
		t.Fatalf("clock regression produced a smaller ID: %s then %s", prev, back)
	}
	for _, c := range string(prev) {
		if !strings.ContainsRune(crockford, c) {
			t.Fatalf("ID %s contains %q outside Crockford", prev, c)
		}
	}
}

func TestNewIDTimestampOrders(t *testing.T) {
	earlier := NewID(time.UnixMilli(1_700_000_000_000))
	later := NewID(time.UnixMilli(1_700_000_001_000))
	if earlier >= later {
		t.Fatalf("later timestamp sorts before earlier: %s >= %s", earlier, later)
	}
}

func newPolicyWriter(maxBatch int, linger time.Duration) *writer {
	return newWriter(nil, nil, Options{MaxBatch: maxBatch, MaxLinger: linger}, slog.Default())
}

func TestCollectIsolatedRequestDoesNotLinger(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newPolicyWriter(8, 2*time.Millisecond)
		start := time.Now()
		batch := w.collect([]*writeReq{{ctx: context.Background()}})
		if len(batch) != 1 || time.Since(start) != 0 {
			t.Fatalf("isolated batch=%d elapsed=%v; want 1 and 0", len(batch), time.Since(start))
		}
	})
}

func TestCollectBurstLingersTwoMilliseconds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newPolicyWriter(8, 2*time.Millisecond)
		w.stats.lastBatch.Store(2)
		go func() {
			time.Sleep(time.Millisecond)
			w.reqs <- &writeReq{ctx: context.Background()}
			time.Sleep(2 * time.Millisecond)
			w.reqs <- &writeReq{ctx: context.Background()}
		}()
		start := time.Now()
		batch := w.collect([]*writeReq{{ctx: context.Background()}})
		if len(batch) != 2 || time.Since(start) != 2*time.Millisecond {
			t.Fatalf("burst batch=%d elapsed=%v; want 2 and 2ms", len(batch), time.Since(start))
		}
		time.Sleep(2 * time.Millisecond)
		if len(w.reqs) != 1 {
			t.Fatalf("request after the linger window left queued: %d", len(w.reqs))
		}
	})
}

func TestCollectStopsAtMaxBatchWithoutLinger(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newPolicyWriter(2, 2*time.Millisecond)
		w.stats.lastBatch.Store(2)
		w.reqs <- &writeReq{ctx: context.Background()}
		start := time.Now()
		batch := w.collect([]*writeReq{{ctx: context.Background()}})
		if len(batch) != 2 || time.Since(start) != 0 {
			t.Fatalf("full batch=%d elapsed=%v; want 2 and 0", len(batch), time.Since(start))
		}
	})
}

func TestCollectWhileClosingDoesNotLinger(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newPolicyWriter(8, 2*time.Millisecond)
		w.stats.lastBatch.Store(2)
		w.reqs <- &writeReq{ctx: context.Background()}
		close(w.closing)
		start := time.Now()
		batch := w.collect([]*writeReq{{ctx: context.Background()}})
		if len(batch) != 2 || time.Since(start) != 0 {
			t.Fatalf("closing batch=%d elapsed=%v; want 2 and 0", len(batch), time.Since(start))
		}
	})
}
