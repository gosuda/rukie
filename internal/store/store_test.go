package store

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sqlite "modernc.org/sqlite"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func writeRaceOp(w, i int) (tag string, mode int) {
	seq := w*200 + i
	return tagFor("x", seq), seq % 20
}

func TestWriteRace_ManyWriters(t *testing.T) {
	baseline := runtime.NumGoroutine()
	path := testPath(t)
	s := openWith(t, path, fixtureFS(t), Options{})
	const writers, perWriter = 32, 200
	var okCount atomic.Int64
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			for i := range perWriter {
				tag, mode := writeRaceOp(w, i)
				ctx := t.Context()
				switch {
				case mode < 16:
					okTag := "ok-" + tag
					err := s.Write(ctx, func(tx *WriteTx) error { return insertProbe(tx, okTag, "race") })
					if err != nil {
						t.Errorf("success write %s: %v", okTag, err)
						continue
					}
					okCount.Add(1)
				case mode < 18:
					errTag := "err-" + tag
					err := s.Write(ctx, func(tx *WriteTx) error {
						if ierr := insertProbe(tx, errTag, "race"); ierr != nil {
							return ierr
						}
						return errors.New("planned closure error")
					})
					if err == nil || errors.Is(err, ErrPanic) {
						t.Errorf("closure error mapped to %v", err)
					}
				case mode == 18:
					panTag := "pan-" + tag
					err := s.Write(ctx, func(tx *WriteTx) error {
						if ierr := insertProbe(tx, panTag, "race"); ierr != nil {
							return ierr
						}
						panic("planned panic")
					})
					if !errors.Is(err, ErrPanic) {
						t.Errorf("panic mapped to %v, want ErrPanic", err)
					}
				default:
					cctx, cancel := context.WithCancel(ctx)
					cancel()
					ran := atomic.Bool{}
					err := s.Write(cctx, func(*WriteTx) error { ran.Store(true); return nil })
					if !errors.Is(err, context.Canceled) || ran.Load() {
						t.Errorf("cancelled write: err=%v ran=%v", err, ran.Load())
					}
				}
			}
		})
	}
	wg.Wait()
	if got := readInt(t, s, `SELECT count(*) FROM probe_rows`); got != okCount.Load() {
		t.Errorf("rows = %d, successful writes = %d", got, okCount.Load())
	}
	if got := readInt(t, s, `SELECT count(*) FROM probe_rows WHERE tag NOT LIKE 'ok-%'`); got != 0 {
		t.Errorf("failed-request rows persisted: %d", got)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitFor(t, "goroutines to return to baseline", func() bool {
		return runtime.NumGoroutine() <= baseline
	})
}

func TestWriteRace_CloseDuringTraffic(t *testing.T) {
	path := testPath(t)
	s, err := open(t.Context(), Options{Path: path}, fixtureFS(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var mu sync.Mutex
	accepted := map[string]bool{}
	rejected := map[string]bool{}
	var wg sync.WaitGroup
	for w := range 16 {
		wg.Go(func() {
			for i := range 2000 {
				tag := tagFor(fmt.Sprintf("c%d", w), i)
				werr := s.Write(t.Context(), func(tx *WriteTx) error { return insertProbe(tx, tag, "close") })
				mu.Lock()
				switch {
				case werr == nil:
					accepted[tag] = true
				case errors.Is(werr, ErrClosed):
					rejected[tag] = true
					mu.Unlock()
					return
				default:
					mu.Unlock()
					t.Errorf("write %s: unexpected %v", tag, werr)
					return
				}
				mu.Unlock()
			}
		})
	}
	waitFor(t, "some committed writes", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(accepted) >= 200
	})
	if err = s.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	wg.Wait()
	reopened := openWith(t, path, fixtureFS(t), Options{})
	for tag := range accepted {
		if readInt(t, reopened, `SELECT count(*) FROM probe_rows WHERE tag = ?`, tag) != 1 {
			t.Errorf("acknowledged write %s is missing after Close", tag)
		}
	}
	for tag := range rejected {
		if readInt(t, reopened, `SELECT count(*) FROM probe_rows WHERE tag = ?`, tag) != 0 {
			t.Errorf("rejected write %s persisted", tag)
		}
	}
}

func TestCanceledBeforeDequeue(t *testing.T) {
	s := openTest(t, Options{})
	started := make(chan struct{})
	gate := make(chan struct{})
	gateDone := make(chan error, 1)
	go func() {
		gateDone <- s.Write(t.Context(), func(*WriteTx) error {
			close(started)
			<-gate
			return nil
		})
	}()
	<-started
	cctx, cancel := context.WithCancel(t.Context())
	ran := atomic.Bool{}
	cancelled := make(chan error, 1)
	go func() {
		cancelled <- s.Write(cctx, func(*WriteTx) error { ran.Store(true); return nil })
	}()
	waitFor(t, "cancelled request to queue", func() bool { return s.Stats().QueueLen == 1 })
	cancel()
	close(gate)
	if err := <-gateDone; err != nil {
		t.Fatalf("gate write: %v", err)
	}
	if err := <-cancelled; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled write returned %v, want context.Canceled", err)
	}
	if ran.Load() {
		t.Fatal("cancelled request ran its function")
	}
}

func TestBatchSavepointIsolation(t *testing.T) {
	s := openTest(t, Options{MaxBatch: 8, MaxLinger: 50 * time.Millisecond, QueueDepth: 64})
	started := make(chan struct{})
	gate := make(chan struct{})
	gateDone := make(chan error, 1)
	go func() {
		gateDone <- s.Write(t.Context(), func(*WriteTx) error {
			close(started)
			<-gate
			return nil
		})
	}()
	<-started
	const n = 8
	results := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			tag := tagFor("b", i)
			results[i] = s.Write(t.Context(), func(tx *WriteTx) error {
				if ierr := insertProbe(tx, tag, "batch"); ierr != nil {
					return ierr
				}
				if i == 3 {
					return errors.New("planned rejection")
				}
				return nil
			})
		})
	}
	waitFor(t, "eight requests to queue behind the gate", func() bool { return s.Stats().QueueLen == n })
	close(gate)
	if err := <-gateDone; err != nil {
		t.Fatalf("gate write: %v", err)
	}
	wg.Wait()
	for i, err := range results {
		if i == 3 {
			if err == nil {
				t.Fatal("request 3 succeeded; want planned rejection")
			}
			continue
		}
		if err != nil {
			t.Errorf("request %d: %v", i, err)
		}
	}
	if got := readInt(t, s, `SELECT count(*) FROM probe_rows WHERE tag = 'b-3'`); got != 0 {
		t.Errorf("rejected request left %d rows", got)
	}
	if got := readInt(t, s, `SELECT count(*) FROM probe_rows WHERE tag LIKE 'b-%' AND tag <> 'b-3'`); got != n-1 {
		t.Errorf("committed rows = %d, want %d", got, n-1)
	}
	st := s.Stats()
	if st.Batches != 2 || st.LastBatchSize != n || st.RolledBack != 1 {
		t.Errorf("stats batches=%d last=%d rolledBack=%d; want 2, %d, 1", st.Batches, st.LastBatchSize, st.RolledBack, n)
	}
}

func TestReadSnapshotIsolation(t *testing.T) {
	s := openTest(t, Options{})
	if err := s.Write(t.Context(), func(tx *WriteTx) error { return insertProbe(tx, "before", "x") }); err != nil {
		t.Fatalf("seed: %v", err)
	}
	readStarted := make(chan struct{})
	writeDone := make(chan struct{})
	go func() {
		<-readStarted
		_ = s.Write(t.Context(), func(tx *WriteTx) error { return insertProbe(tx, "after", "x") })
		close(writeDone)
	}()
	err := s.Read(t.Context(), func(tx *ReadTx) error {
		var first, second int64
		if err := tx.tx.QueryRowContext(tx.ctx, `SELECT count(*) FROM probe_rows`).Scan(&first); err != nil {
			return err
		}
		close(readStarted)
		<-writeDone
		if err := tx.tx.QueryRowContext(tx.ctx, `SELECT count(*) FROM probe_rows`).Scan(&second); err != nil {
			return err
		}
		if first != second {
			return fmt.Errorf("snapshot moved: %d then %d", first, second)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
}

func TestSubmitHonorsCtxDuringClose(t *testing.T) {
	s := openTest(t, Options{QueueDepth: 1})
	started := make(chan struct{})
	gate := make(chan struct{})
	gateDone := make(chan error, 1)
	go func() {
		gateDone <- s.Write(t.Context(), func(*WriteTx) error {
			close(started)
			<-gate
			return nil
		})
	}()
	<-started
	queuedDone := make(chan error, 1)
	go func() {
		queuedDone <- s.Write(t.Context(), func(*WriteTx) error { return nil })
	}()
	waitFor(t, "queue to fill", func() bool { return s.Stats().QueueLen == 1 })
	shortCtx, cancelShort := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancelShort()
	if err := s.Write(shortCtx, func(*WriteTx) error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("full-queue submit returned %v, want DeadlineExceeded", err)
	}
	blockedDone := make(chan error, 1)
	go func() {
		blockedDone <- s.Write(t.Context(), func(*WriteTx) error { return nil })
	}()
	waitFor(t, "second blocked submitter", func() bool { return len(s.writer.reqs) == 1 })
	closeDone := make(chan error, 1)
	go func() { closeDone <- s.Close(t.Context()) }()
	waitFor(t, "close to begin", func() bool { return s.writer.closingNow() })
	select {
	case err := <-blockedDone:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("blocked submit returned %v, want ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked submit hung during Close")
	}
	close(gate)
	if err := <-gateDone; err != nil {
		t.Fatalf("gate write: %v", err)
	}
	if err := <-queuedDone; err != nil {
		t.Fatalf("queued write: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestErrorCodeConstants(t *testing.T) {
	s := openTest(t, Options{})
	seed := func(tx *WriteTx) error { return insertProbe(tx, "seed", "x") }
	if err := s.Write(t.Context(), seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	cases := []struct {
		want error
		fn   func(*WriteTx) error
		name string
		code int
	}{
		{
			name: "unique",
			want: ErrConflict,
			code: sqliteConstraintUnique,
			fn:   func(tx *WriteTx) error { return insertProbe(tx, "seed", "dup") },
		},
		{
			name: "primary key",
			want: ErrConflict,
			code: sqliteConstraintPK,
			fn: func(tx *WriteTx) error {
				_, err := tx.tx.ExecContext(tx.ctx, `INSERT INTO probe_rows (id, tag, body) VALUES (1, 'pk', 'x')`)
				return err
			},
		},
		{
			name: "foreign key",
			want: ErrReference,
			code: sqliteConstraintFK,
			fn: func(tx *WriteTx) error {
				_, err := tx.tx.ExecContext(tx.ctx, `INSERT INTO probe_children (row_id) VALUES (9999)`)
				return err
			},
		},
		{
			name: "check",
			want: ErrInvalid,
			code: sqliteConstraintCheck,
			fn:   func(tx *WriteTx) error { return insertProbe(tx, "empty", "") },
		},
		{
			name: "not null",
			want: ErrInvalid,
			code: sqliteConstraintNotNull,
			fn: func(tx *WriteTx) error {
				_, err := tx.tx.ExecContext(tx.ctx, `INSERT INTO probe_rows (tag, body) VALUES ('nul', NULL)`)
				return err
			},
		},
	}
	for _, tc := range cases {
		err := s.Write(t.Context(), tc.fn)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, err, tc.want)
			continue
		}
		se, ok := errors.AsType[*sqlite.Error](err)
		if !ok || se.Code() != tc.code {
			t.Errorf("%s: driver code %v, want %d", tc.name, se, tc.code)
		}
	}
}
