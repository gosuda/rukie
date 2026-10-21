package store

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	sqlite "modernc.org/sqlite"
)

const appID = 1381321545

// Options configures Open. Zero values select the defaults noted.
type Options struct {
	// Logger receives slow-operation warnings. Nil selects slog.Default.
	Logger *slog.Logger
	// Path is the database file. Its parent directory is created 0700 and the file 0600.
	Path string
	// ReadConns is the reader pool size. Zero selects max(2, GOMAXPROCS); values clamp to [2, 64].
	ReadConns int
	// MaxBatch caps requests per COMMIT. Zero selects 64.
	MaxBatch int
	// MaxLinger bounds the wait for a fuller batch. Zero selects 2ms.
	MaxLinger time.Duration
	// QueueDepth bounds queued write requests. Zero selects 256.
	QueueDepth int
	// AllowPOSIXLocks permits Open where OFD locks are unavailable.
	AllowPOSIXLocks bool
	Barrier         func() (release func(), ok bool)
}

// Stats is advisory and intended for metrics.
type Stats struct {
	QueueLen       int
	ReadersInUse   int
	Batches        uint64
	Requests       uint64
	LastBatchSize  int
	RolledBack     uint64
	CommitFailures uint64
}

// Store is safe for concurrent use. Writes serialize through one writer connection, so commits are
// linearizable. Reads run at snapshot isolation under WAL. At most one Store exists per path per process.
type Store struct {
	closeErr     error
	log          *slog.Logger
	writer       *writer
	wdb          *sql.DB
	rdb          *sql.DB
	readers      chan *sql.Conn
	readClosing  chan struct{}
	closeSem     chan struct{}
	path         string
	ftsTokenizer string
	barrier      func() (release func(), ok bool)
	migrations   []migration
	readerCount  int
	signalOnce   sync.Once
	abortOnce    sync.Once
	closed       bool
}

// Open verifies the database, applies embedded migrations, and starts the writer and reader pool.
// It returns ErrAlreadyOpen for a path already open in this process.
func Open(ctx context.Context, opts Options) (*Store, error) {
	return open(ctx, opts, embeddedMigrations)
}

func open(ctx context.Context, opts Options, fsys fs.FS) (*Store, error) {
	path, err := absPath(opts.Path)
	if err != nil {
		return nil, err
	}
	if err = reservePath(path); err != nil {
		return nil, err
	}
	st, err := openAt(ctx, path, opts, fsys)
	if err != nil {
		releasePath(path)
		return nil, err
	}
	return st, nil
}

func openAt(ctx context.Context, path string, opts Options, fsys fs.FS) (*Store, error) {
	if err := setupProcess(opts.AllowPOSIXLocks); err != nil {
		return nil, err
	}
	if err := ensureFile(path); err != nil {
		return nil, err
	}
	migs, err := loadMigrations(fsys)
	if err != nil {
		return nil, err
	}
	wdb, wc, err := openWriter(ctx, path)
	if err != nil {
		return nil, err
	}
	tokenizer, err := selectFTS(ctx, wc, ftsCandidates)
	if err == nil {
		err = checkAppID(ctx, wc)
	}
	if err == nil {
		err = applyMigrations(ctx, wc, migs, time.Now)
	}
	if err != nil {
		_ = wc.Close()
		_ = wdb.Close()
		return nil, err
	}
	rdb, readers, err := openReaderPool(ctx, path, readerCount(opts.ReadConns))
	if err != nil {
		_ = wc.Close()
		_ = wdb.Close()
		return nil, err
	}
	st := &Store{
		path:         path,
		ftsTokenizer: tokenizer,
		barrier:      opts.Barrier,
		log:          cmp.Or(opts.Logger, slog.Default()),
		migrations:   migs,
		wdb:          wdb,
		rdb:          rdb,
		readers:      readers,
		readerCount:  cap(readers),
		readClosing:  make(chan struct{}),
		closeSem:     make(chan struct{}, 1),
	}
	st.writer = newWriter(wc, wdb, opts, st.log)
	go st.writer.loop()
	return st, nil
}

// Read runs fn in one read transaction on a pooled reader. Every statement in fn shares one
// snapshot. It returns ErrClosed once Close has begun.
func (s *Store) Read(ctx context.Context, fn func(tx *ReadTx) error) error {
	if fn == nil {
		return ErrInvalid
	}
	conn, err := s.acquireReader(ctx)
	if err != nil {
		return err
	}
	start := time.Now()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		s.releaseReader(conn, isBroken(err))
		return fmt.Errorf("Read: begin: %w", err)
	}
	rtx := &ReadTx{txCore: txCore{ctx: ctx, tx: tx}}
	err = runRead(fn, rtx)
	rtx.done = true
	_ = tx.Rollback()
	s.releaseReader(conn, isBroken(err))
	if elapsed := time.Since(start); elapsed > slowRead {
		s.log.Warn("store read is slow", "elapsed", elapsed)
	}
	return err
}

// Write enqueues fn for the single writer and waits for its batch's COMMIT. When Write returns
// ctx.Err(), fn never ran. fn runs exactly once and is never retried.
func (s *Store) Write(ctx context.Context, fn func(tx *WriteTx) error) error {
	if fn == nil {
		return ErrInvalid
	}
	if s.barrier != nil {
		release, ok := s.barrier()
		if !ok {
			return ErrBackupInProgress
		}
		defer release()
	}
	r := &writeReq{ctx: ctx, fn: fn, done: make(chan error, 1)}
	if err := s.writer.submit(ctx, r); err != nil {
		return err
	}
	return <-r.done
}

// Stats returns advisory counters.
func (s *Store) Stats() Stats {
	return Stats{
		QueueLen:       len(s.writer.reqs),
		ReadersInUse:   s.readerCount - len(s.readers),
		Batches:        s.writer.stats.batches.Load(),
		Requests:       s.writer.stats.requests.Load(),
		LastBatchSize:  int(s.writer.stats.lastBatch.Load()),
		RolledBack:     s.writer.stats.rolledBack.Load(),
		CommitFailures: s.writer.stats.commitFailures.Load(),
	}
}

// Close stops new work, drains queued writes, checkpoints, and closes the database. Close returns
// within its context: if ctx expires first, it returns an error and the store stays open with its
// path reserved. Call Close again to finish the shutdown and release the path.
func (s *Store) Close(ctx context.Context) error {
	s.signalOnce.Do(func() {
		close(s.writer.closing)
		close(s.readClosing)
	})
	stop := context.AfterFunc(ctx, func() { s.abortOnce.Do(func() { close(s.writer.abort) }) })
	defer stop()
	select {
	case <-s.writer.done:
	case <-ctx.Done():
		return fmt.Errorf("Close: writer still busy: %w", ctx.Err())
	}
	select {
	case s.closeSem <- struct{}{}:
	case <-ctx.Done():
		return fmt.Errorf("Close: teardown in progress: %w", ctx.Err())
	}
	defer func() { <-s.closeSem }()
	if !s.closed {
		s.closed = true
		s.closeErr = s.teardown(ctx)
	}
	return s.closeErr
}

func (s *Store) teardown(ctx context.Context) error {
	drainErr := s.drainIdleReaders()
	rdbErr := s.rdb.Close()
	connErr := s.writer.closeConn(ctx)
	releasePath(s.path)
	return errors.Join(drainErr, rdbErr, connErr)
}

func (s *Store) drainIdleReaders() error {
	var errs []error
	for {
		select {
		case conn := <-s.readers:
			if conn != nil {
				if err := conn.Close(); err != nil {
					errs = append(errs, err)
				}
			}
		default:
			return errors.Join(errs...)
		}
	}
}

var (
	openMu    sync.Mutex
	openPaths = map[string]struct{}{}
)

func reservePath(path string) error {
	openMu.Lock()
	defer openMu.Unlock()
	if _, taken := openPaths[path]; taken {
		return ErrAlreadyOpen
	}
	openPaths[path] = struct{}{}
	return nil
}

func releasePath(path string) {
	openMu.Lock()
	defer openMu.Unlock()
	delete(openPaths, path)
}

func absPath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("absPath: %w: empty path", ErrInvalid)
	}
	abs, err := filepath.Abs(filepath.Clean(p))
	if err != nil {
		return "", fmt.Errorf("absPath: %w", err)
	}
	return abs, nil
}

func setupProcess(allowPOSIX bool) error {
	sqlite.StrictPragmas(true)
	_, err := sqlite.OFDLocking(true)
	if errors.Is(err, sqlite.ErrOFDLockingTooLate) {
		return fmt.Errorf("setupProcess: %w", err)
	}
	if err != nil && !errors.Is(err, sqlite.ErrOFDLockingUnavailable) {
		return fmt.Errorf("setupProcess: %w", err)
	}
	if sqlite.OFDLockingEnabled() || allowPOSIX {
		return nil
	}
	return fmt.Errorf("setupProcess: %w", ErrOFDUnavailable)
}

func ensureFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("ensureFile: %w", err)
	}
	f, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("ensureFile: %w", err)
	}
	return f.Close()
}

func readerCount(requested int) int {
	n := cmp.Or(requested, runtime.GOMAXPROCS(0))
	return min(max(n, 2), 64)
}

func isBroken(err error) bool {
	return errors.Is(err, sql.ErrConnDone) || isBadConn(err)
}

func runRead(fn func(*ReadTx) error, rtx *ReadTx) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%w: %v", ErrPanic, p)
		}
	}()
	return fn(rtx)
}
