package sysops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"github.com/gosuda/rukie/internal/config"
	"github.com/gosuda/rukie/internal/store"
)

const ExitDowngradeRefused = 3

// DowngradeError reports a database newer than the running binary.
// There is no override: restore an older backup instead.
type DowngradeError struct {
	Have int64
	Want int64
}

func (e *DowngradeError) Error() string {
	return fmt.Sprintf("sysops: upgrade.downgrade-refused have=%d want=%d: database schema is newer than this binary; restore an older backup",
		e.Have, e.Want)
}

// IsDowngrade reports whether err is a downgrade refusal, from either
// the store engine or the offline check.
func IsDowngrade(err error) bool {
	_, matched := errors.AsType[*DowngradeError](err)
	return errors.Is(err, store.ErrSchemaTooNew) || matched
}

var (
	codeVersionOnce sync.Once
	codeVersion     int64
	codeVersionErr  error
)

// CodeSchemaVersion returns the highest migration embedded in this
// binary, derived by migrating a fresh temporary store through the
// public store API. The value is fixed per binary and cached.
func CodeSchemaVersion() (int64, error) {
	codeVersionOnce.Do(func() {
		dir, err := os.MkdirTemp("", "rukie-schemaver")
		if err != nil {
			codeVersionErr = fmt.Errorf("sysops: schema version probe: %w", err)
			return
		}
		defer func() { _ = os.RemoveAll(dir) }()
		path := filepath.Join(dir, "probe.sqlite")
		ctx := context.Background()
		st, err := store.Open(ctx, store.Options{Path: path})
		if err != nil {
			codeVersionErr = fmt.Errorf("sysops: schema version probe: %w", err)
			return
		}
		_ = st.Close(ctx)
		applied, err := AppliedSchemaVersion(path)
		if err != nil {
			codeVersionErr = err
			return
		}
		codeVersion = applied
	})
	return codeVersion, codeVersionErr
}

func readonlyDSN(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("sysops: resolve db path: %w", err)
	}
	u := url.URL{Scheme: "file", Path: abs}
	return u.String() + "?mode=ro&_query_only=on&_busy_timeout=5000", nil
}

// AppliedSchemaVersion reads the highest applied migration from dbPath
// without migrating. A missing file or a file without the migrations
// table reports 0.
func AppliedSchemaVersion(dbPath string) (int64, error) {
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	dsn, err := readonlyDSN(dbPath)
	if err != nil {
		return 0, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return 0, fmt.Errorf("sysops: open db for schema read: %w", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	var table string
	err = db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&table)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("sysops: read schema state: %w", err)
	}
	var highest int64
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(max(version), 0) FROM schema_migrations`).Scan(&highest); err != nil {
		return 0, fmt.Errorf("sysops: read schema state: %w", err)
	}
	return highest, nil
}

// PendingSchemaVersions lists applied+1 through the binary's version:
// the migrations an offline migrate would apply.
func PendingSchemaVersions(dbPath string) ([]int64, error) {
	applied, err := AppliedSchemaVersion(dbPath)
	if err != nil {
		return nil, err
	}
	code, err := CodeSchemaVersion()
	if err != nil {
		return nil, err
	}
	if applied > code {
		return nil, &DowngradeError{Have: applied, Want: code}
	}
	var pending []int64
	for v := applied + 1; v <= code; v++ {
		pending = append(pending, v)
	}
	return pending, nil
}

// CheckDowngrade refuses when the database or its meta.version marker
// was migrated past this binary. A missing database is never a
// downgrade; a missing marker defers to the table.
func CheckDowngrade(cfg config.Config) error {
	code, err := CodeSchemaVersion()
	if err != nil {
		return err
	}
	if marked, ok := ReadMetaVersion(cfg.DataDir); ok && marked > code {
		return &DowngradeError{Have: marked, Want: code}
	}
	applied, err := AppliedSchemaVersion(cfg.Store.SQLite.Path)
	if err != nil {
		return err
	}
	if applied > code {
		return &DowngradeError{Have: applied, Want: code}
	}
	return nil
}
