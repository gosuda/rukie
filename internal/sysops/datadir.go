package sysops

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// DataSubdirs are the data_dir entries K10 owns. run/ is ephemeral and
// excluded from backups; repos/ is daemon-owned and copied read-only.
var DataSubdirs = []string{
	"repos", "blobs", "cas", "ci-work", "ci-logs",
	"webhook-outbox", "mirrors", "tmp", "backups", "run", "spool",
}

// EnsureDataDir creates the data dir and every subdirectory at 0700.
func EnsureDataDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("sysops: data_dir is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("sysops: create data dir: %w", err)
	}
	for _, sub := range DataSubdirs {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return fmt.Errorf("sysops: create %s: %w", sub, err)
		}
	}
	return nil
}

// ErrDataDirLive reports an offline-only command refused because a
// server holds the data_dir lock.
var ErrDataDirLive = fmt.Errorf("sysops: data_dir is live")

// DirLock is an exclusive data_dir lock held by a running server.
// Offline commands (restore, migrate) refuse while it is held.
type DirLock struct {
	file *os.File
	path string
}

func lockPath(dir string) string { return filepath.Join(dir, "run", "lock") }

// AcquireDataDirLock takes the exclusive non-blocking lock, creating
// run/ and the lock file as needed.
func AcquireDataDirLock(dir string) (*DirLock, error) {
	if err := os.MkdirAll(filepath.Join(dir, "run"), 0o700); err != nil {
		return nil, fmt.Errorf("sysops: create run dir: %w", err)
	}
	path := lockPath(dir)
	f, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("sysops: open data_dir lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%w: %s is held by a running server", ErrDataDirLive, path)
	}
	return &DirLock{file: f, path: path}, nil
}

// Release drops the lock.
func (l *DirLock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	return l.file.Close()
}

// IsDataDirLive probes whether a server currently holds the lock.
func IsDataDirLive(dir string) bool {
	f, err := os.OpenFile(filepath.Clean(lockPath(dir)), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return true
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}
