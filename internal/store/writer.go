package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync/atomic"
	"time"
)

const (
	defaultMaxBatch   = 64
	defaultMaxLinger  = 2 * time.Millisecond
	defaultQueueDepth = 256
	slowBatch         = 50 * time.Millisecond
	slowRead          = time.Second
	minBackoff        = 50 * time.Millisecond
	maxBackoff        = time.Second
)

var errBackoff = errors.New("store: writer connection in backoff")

type writeReq struct {
	ctx  context.Context
	fn   func(*WriteTx) error
	done chan error
}

type writerStats struct {
	batches        atomic.Uint64
	requests       atomic.Uint64
	lastBatch      atomic.Int64
	rolledBack     atomic.Uint64
	commitFailures atomic.Uint64
}

type writer struct {
	retryAt   time.Time
	reqs      chan *writeReq
	closing   chan struct{}
	abort     chan struct{}
	done      chan struct{}
	wdb       *sql.DB
	conn      *sql.Conn
	log       *slog.Logger
	buf       []*writeReq
	inflight  atomic.Int64
	stats     writerStats
	backoff   time.Duration
	maxBatch  int
	maxLinger time.Duration
}

func newWriter(conn *sql.Conn, wdb *sql.DB, opts Options, log *slog.Logger) *writer {
	maxBatch := opts.MaxBatch
	if maxBatch <= 0 {
		maxBatch = defaultMaxBatch
	}
	maxLinger := opts.MaxLinger
	if maxLinger <= 0 {
		maxLinger = defaultMaxLinger
	}
	queueDepth := opts.QueueDepth
	if queueDepth <= 0 {
		queueDepth = defaultQueueDepth
	}
	return &writer{
		reqs:      make(chan *writeReq, queueDepth),
		closing:   make(chan struct{}),
		abort:     make(chan struct{}),
		done:      make(chan struct{}),
		wdb:       wdb,
		conn:      conn,
		log:       log,
		buf:       make([]*writeReq, 0, maxBatch),
		maxBatch:  maxBatch,
		maxLinger: maxLinger,
	}
}

func (w *writer) submit(ctx context.Context, r *writeReq) error {
	w.inflight.Add(1)
	defer w.inflight.Add(-1)
	if w.closingNow() {
		return ErrClosed
	}
	select {
	case w.reqs <- r:
		return nil
	case <-w.closing:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *writer) loop() {
	defer close(w.done)
	for {
		select {
		case r := <-w.reqs:
			w.runBatch(r)
		case <-w.closing:
			w.drainAndExit()
			return
		}
	}
}

func (w *writer) runBatch(first *writeReq) {
	batch := w.collect(append(w.buf[:0], first))
	w.execBatch(batch)
	clear(batch)
	w.buf = batch[:0]
}

func (w *writer) drainAndExit() {
	for {
		quiet := w.inflight.Load() == 0
		select {
		case r := <-w.reqs:
			w.runBatch(r)
			continue
		default:
		}
		if quiet {
			return
		}
		runtime.Gosched()
	}
}

func (w *writer) collect(batch []*writeReq) []*writeReq {
	batch = w.drainQueued(batch)
	if len(batch) >= w.maxBatch || w.stats.lastBatch.Load() <= 1 || w.closingNow() {
		return batch
	}
	timer := time.NewTimer(w.maxLinger)
	defer timer.Stop()
	for len(batch) < w.maxBatch {
		select {
		case r := <-w.reqs:
			batch = append(batch, r)
		case <-timer.C:
			return batch
		case <-w.closing:
			return w.drainQueued(batch)
		}
	}
	return batch
}

func (w *writer) drainQueued(batch []*writeReq) []*writeReq {
	for len(batch) < w.maxBatch {
		select {
		case r := <-w.reqs:
			batch = append(batch, r)
		default:
			return batch
		}
	}
	return batch
}

func (w *writer) execBatch(batch []*writeReq) {
	start := time.Now()
	live := w.skipDead(batch)
	if len(live) == 0 {
		return
	}
	w.commitBatch(live)
	w.stats.batches.Add(1)
	w.stats.requests.Add(uint64(len(live)))
	w.stats.lastBatch.Store(int64(len(live)))
	if elapsed := time.Since(start); elapsed > slowBatch {
		w.log.Warn("store write batch is slow", "elapsed", elapsed, "size", len(live))
	}
}

func (w *writer) skipDead(batch []*writeReq) []*writeReq {
	live := batch[:0]
	for _, r := range batch {
		switch {
		case r.ctx.Err() != nil:
			r.done <- r.ctx.Err()
		case w.aborting():
			r.done <- ErrClosed
		default:
			live = append(live, r)
		}
	}
	return live
}

func (w *writer) commitBatch(live []*writeReq) {
	bctx := context.WithoutCancel(context.Background())
	if err := w.ensureConn(bctx); err != nil {
		replyAll(live, fmt.Errorf("%w: %w", ErrUnavailable, err))
		return
	}
	tx, err := w.conn.BeginTx(bctx, nil)
	if err != nil {
		w.discardConn()
		replyAll(live, fmt.Errorf("%w: %w", ErrUnavailable, err))
		return
	}
	wtx := &WriteTx{txCore: txCore{ctx: bctx, tx: tx}}
	errs := make([]error, len(live))
	for i, r := range live {
		errs[i] = w.runRequest(wtx, r)
		if errors.Is(errs[i], errSavepointMachinery) {
			wtx.done = true
			_ = tx.Rollback()
			w.discardConn()
			replyAll(live, ErrRolledBack)
			return
		}
	}
	wtx.done = true
	if cerr := tx.Commit(); cerr != nil {
		w.stats.commitFailures.Add(1)
		w.discardConn()
		for i := range errs {
			if errs[i] == nil {
				errs[i] = fmt.Errorf("%w: %w", ErrOutcomeUnknown, cerr)
			}
		}
	}
	for i, r := range live {
		r.done <- errs[i]
	}
}

func (w *writer) runRequest(wtx *WriteTx, r *writeReq) (err error) {
	tx := wtx.tx
	if _, serr := tx.ExecContext(wtx.ctx, sqlSavepoint); serr != nil {
		return errSavepointMachinery
	}
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%w: %v", ErrPanic, p)
		}
		if err != nil {
			w.stats.rolledBack.Add(1)
			if _, rerr := tx.ExecContext(wtx.ctx, sqlRollbackTo); rerr != nil {
				err = errSavepointMachinery
			}
		}
		if _, rerr := tx.ExecContext(wtx.ctx, sqlRelease); rerr != nil && err == nil {
			err = errSavepointMachinery
		}
	}()
	return mapErr(r.fn(wtx))
}

func (w *writer) ensureConn(ctx context.Context) error {
	if w.conn != nil {
		return nil
	}
	if time.Now().Before(w.retryAt) {
		return errBackoff
	}
	conn, err := w.wdb.Conn(ctx)
	if err == nil {
		if verr := verifyConn(ctx, conn, roleWriter); verr != nil {
			_ = conn.Close()
			err = verr
		} else {
			w.conn = conn
			w.backoff = 0
			return nil
		}
	}
	w.backoff = min(max(2*w.backoff, minBackoff), maxBackoff)
	w.retryAt = time.Now().Add(w.backoff)
	return err
}

func (w *writer) discardConn() {
	if w.conn == nil {
		return
	}
	discardSQLConn(w.conn)
	w.conn = nil
}

func (w *writer) closeConn(ctx context.Context) error {
	var checkpointErr, connErr error
	if w.conn != nil {
		if _, err := w.conn.ExecContext(context.WithoutCancel(ctx), sqlWALCheckpoint); err != nil {
			checkpointErr = fmt.Errorf("closeConn: checkpoint: %w", err)
		}
		connErr = w.conn.Close()
		w.conn = nil
	}
	return errors.Join(checkpointErr, connErr, w.wdb.Close())
}

func (w *writer) aborting() bool {
	select {
	case <-w.abort:
		return true
	default:
		return false
	}
}

func (w *writer) closingNow() bool {
	select {
	case <-w.closing:
		return true
	default:
		return false
	}
}

func replyAll(reqs []*writeReq, err error) {
	for _, r := range reqs {
		r.done <- err
	}
}
