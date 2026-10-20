package sysops

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/gosuda/rukie/internal/store"
)

// ErrBarrierHeld reports a second backup while a barrier is active.
var ErrBarrierHeld = errors.New("sysops: a backup barrier is already held")

const exclusiveWaitCap = 60 * time.Second

const barrierPollInterval = 100 * time.Millisecond

func ulidNow() string {
	return string(store.NewID(time.Now()))
}

// BarrierDoc is the on-disk step-0 write barrier record (ops-design
// §5.2). One file proves one linearizable point: while its backup holds
// the mutex exclusively, the server answers new mutations with 503
// backup/in-progress and Retry-After: 10, so t_M and t_R coincide.
type BarrierDoc struct {
	BarrierID string `toml:"barrier_id"`
	StartedAt int64  `toml:"started_at"`
}

func barrierPath(dataDir string) string {
	return filepath.Join(dataDir, "run", "backup.barrier")
}

func barrierLockPath(dataDir string) string {
	return filepath.Join(dataDir, "run", "backup.lock")
}

func ensureRunDir(dataDir string) error {
	if err := os.MkdirAll(filepath.Join(dataDir, "run"), 0o700); err != nil {
		return fmt.Errorf("sysops: create run dir: %w", err)
	}
	return nil
}

// Barrier is a held step-0 write barrier: an exclusive flock on the
// backup mutex plus the provenance record. No mutation holding a shared
// lock can be in flight once BeginBarrier returns, and no new mutation
// can start until Close.
type Barrier struct {
	file  *os.File
	id    string
	dir   string
	since time.Time
}

// BeginBarrier takes the backup mutex exclusively, waiting past
// in-flight mutations up to a 60 s cap, then records the barrier. The
// caller holds it through the metadata snapshot and the repo copy and
// releases before the manifest hash pass.
func BeginBarrier(dataDir string) (*Barrier, error) {
	return BeginBarrierWait(dataDir, exclusiveWaitCap)
}

// BeginBarrierWait takes the mutex with an explicit wait bound for
// tests; production uses BeginBarrier and its 60 s cap.
func BeginBarrierWait(dataDir string, wait time.Duration) (*Barrier, error) {
	if err := ensureRunDir(dataDir); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Clean(barrierLockPath(dataDir)), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("sysops: open barrier mutex: %w", err)
	}
	deadline := time.Now().Add(wait)
	for {
		if ferr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); ferr == nil {
			break
		} else if !errors.Is(ferr, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, fmt.Errorf("sysops: lock barrier mutex: %w", ferr)
		}
		if !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("%w: in-flight mutations did not drain within %s", ErrBarrierHeld, wait)
		}
		time.Sleep(barrierPollInterval)
	}
	id := ulidNow()
	var body strings.Builder
	if err := toml.NewEncoder(&body).Encode(BarrierDoc{BarrierID: id, StartedAt: time.Now().UnixMilli()}); err != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		return nil, fmt.Errorf("sysops: encode barrier: %w", err)
	}
	if werr := os.WriteFile(filepath.Clean(barrierPath(dataDir)), []byte(body.String()), 0o600); werr != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		return nil, fmt.Errorf("sysops: write barrier: %w", werr)
	}
	return &Barrier{file: f, id: id, dir: dataDir, since: time.Now()}, nil
}

// ID returns the barrier id recorded in the manifest.
func (b *Barrier) ID() string {
	if b == nil {
		return ""
	}
	return b.id
}

// HeldFor reports how long the barrier has been held.
func (b *Barrier) HeldFor() time.Duration {
	if b == nil {
		return 0
	}
	return time.Since(b.since)
}

// Close releases the mutex and removes the record. It is idempotent.
func (b *Barrier) Close() error {
	if b == nil || b.file == nil {
		return nil
	}
	_ = os.Remove(filepath.Clean(barrierPath(b.dir)))
	_ = syscall.Flock(int(b.file.Fd()), syscall.LOCK_UN)
	err := b.file.Close()
	b.file = nil
	return err
}

// BarrierActive reports whether a backup barrier record exists, with
// its id. The record is provenance; exclusion itself is the flock.
func BarrierActive(dataDir string) (string, bool) {
	var doc BarrierDoc
	if _, err := toml.DecodeFile(filepath.Clean(barrierPath(dataDir)), &doc); err != nil {
		return "", false
	}
	if doc.BarrierID == "" {
		return "", false
	}
	return doc.BarrierID, true
}

// HoldSharedBarrier takes a non-blocking shared hold on the backup
// mutex for one mutation's full write. ok=false means a backup holds it
// exclusively, or the mutex itself is unreachable: either way the caller
// must refuse the mutation with 503 backup/in-progress and Retry-After:
// 10 instead of risking a torn backup. Failures fail closed.
func HoldSharedBarrier(dataDir string) (release func(), ok bool) {
	noop := func() {}
	if err := ensureRunDir(dataDir); err != nil {
		return noop, false
	}
	f, err := os.OpenFile(filepath.Clean(barrierLockPath(dataDir)), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return noop, false
	}
	if ferr := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); ferr != nil {
		_ = f.Close()
		return noop, false
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, true
}
