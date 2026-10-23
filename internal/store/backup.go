package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// BackupInfo describes a verified backup file.
type BackupInfo struct {
	CreatedAt     time.Time
	Path          string
	SHA256        string
	Bytes         int64
	SchemaVersion int64
}

// Backup writes a verified snapshot of the database to dst, which must not exist. The snapshot
// is taken with VACUUM INTO, checked, fsynced, and renamed into place.
func (s *Store) Backup(ctx context.Context, dst string) (BackupInfo, error) {
	target, err := absPath(dst)
	if err != nil {
		return BackupInfo{}, err
	}
	if _, serr := os.Stat(target); serr == nil {
		return BackupInfo{}, fmt.Errorf("Backup: %s: %w: target exists", target, ErrInvalid)
	}
	partial := target + ".partial"
	if err = createEmpty(partial); err != nil {
		return BackupInfo{}, err
	}
	placed := false
	defer func() {
		if !placed {
			_ = os.Remove(partial)
		}
	}()
	if err = s.vacuumInto(ctx, partial); err != nil {
		return BackupInfo{}, err
	}
	if err = fsyncPath(partial); err != nil {
		return BackupInfo{}, err
	}
	if err = s.verifyBackup(ctx, partial); err != nil {
		return BackupInfo{}, err
	}
	if err = os.Rename(partial, target); err != nil {
		return BackupInfo{}, fmt.Errorf("Backup: rename: %w", err)
	}
	placed = true
	if err = fsyncPath(filepath.Dir(target)); err != nil {
		return BackupInfo{}, err
	}
	var newest int64
	for _, m := range s.migrations {
		if m.version > newest {
			newest = m.version
		}
	}
	return fileInfo(target, newest)
}

func createEmpty(path string) error {
	f, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("createEmpty: %w", err)
	}
	return f.Close()
}

func (s *Store) vacuumInto(ctx context.Context, partial string) error {
	db, err := sql.Open("sqlite", fileDSN(s.path, backupParams))
	if err != nil {
		return fmt.Errorf("vacuumInto: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("vacuumInto: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err = conn.ExecContext(ctx, sqlVacuumInto, partial); err != nil {
		return fmt.Errorf("vacuumInto: %w", err)
	}
	return nil
}

func (s *Store) verifyBackup(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", fileDSN(path, backupParams))
	if err != nil {
		return fmt.Errorf("verifyBackup: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("verifyBackup: %w", err)
	}
	defer func() { _ = conn.Close() }()
	var check string
	if err = conn.QueryRowContext(ctx, sqlQuickCheck).Scan(&check); err != nil {
		return fmt.Errorf("verifyBackup: %w", err)
	}
	if check != "ok" {
		return fmt.Errorf("verifyBackup: quick_check: %s", check)
	}
	var maxVersion int64
	if err = conn.QueryRowContext(ctx, sqlMaxMigration).Scan(&maxVersion); err != nil {
		return fmt.Errorf("verifyBackup: %w", err)
	}
	var newest int64
	for _, m := range s.migrations {
		if m.version > newest {
			newest = m.version
		}
	}
	if maxVersion != newest {
		return fmt.Errorf("verifyBackup: schema version %d, want %d", maxVersion, newest)
	}
	if _, err = selectFTS(ctx, conn, ftsCandidates); err != nil {
		return fmt.Errorf("verifyBackup: %w", err)
	}
	return nil
}

func fsyncPath(path string) error {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("fsyncPath: %w", err)
	}
	serr := f.Sync()
	cerr := f.Close()
	if err = errors.Join(serr, cerr); err != nil {
		return fmt.Errorf("fsyncPath: %w", err)
	}
	return nil
}

func fileInfo(path string, schemaVersion int64) (BackupInfo, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return BackupInfo{}, fmt.Errorf("fileInfo: %w", err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return BackupInfo{}, fmt.Errorf("fileInfo: %w", err)
	}
	return BackupInfo{
		Path:          path,
		Bytes:         n,
		SchemaVersion: schemaVersion,
		SHA256:        hex.EncodeToString(h.Sum(nil)),
		CreatedAt:     time.Now().UTC(),
	}, nil
}

func versionAtLeast(version string, minimum [3]int) (bool, error) {
	parts := strings.Split(version, ".")
	if len(parts) < 3 {
		return false, fmt.Errorf("versionAtLeast: %q: want three numeric parts", version)
	}
	var got [3]int
	for i := range got {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return false, fmt.Errorf("versionAtLeast: %q: %w", version, err)
		}
		got[i] = n
	}
	for i := range got {
		if got[i] != minimum[i] {
			return got[i] > minimum[i], nil
		}
	}
	return true, nil
}
