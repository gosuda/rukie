package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var embeddedMigrations embed.FS

const rebuildDirective = "-- rukie:foreign_keys=off"

var (
	migrationName = regexp.MustCompile(`^([0-9]{4})_([a-z][a-z0-9_]*)\.sql$`)
	txControl     = regexp.MustCompile(`(?i)(^|;)\s*(BEGIN|COMMIT|ROLLBACK|SAVEPOINT|RELEASE)\b|\bPRAGMA\s+foreign_keys\b`)
	lineComment   = regexp.MustCompile(`--[^\n]*`)
)

type migration struct {
	name     string
	sql      string
	checksum string
	version  int64
	rebuild  bool
}

func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, fmt.Errorf("loadMigrations: %w", err)
	}
	out := make([]migration, 0, len(entries))
	for i, e := range entries {
		m, perr := parseMigration(fsys, e.Name())
		if perr != nil {
			return nil, perr
		}
		if m.version != int64(i+1) {
			return nil, fmt.Errorf("loadMigrations: %s: versions must be contiguous from 1", e.Name())
		}
		out = append(out, m)
	}
	return out, nil
}

func parseMigration(fsys fs.FS, name string) (migration, error) {
	parts := migrationName.FindStringSubmatch(name)
	if parts == nil {
		return migration{}, fmt.Errorf("parseMigration: %s: name must match NNNN_name.sql", name)
	}
	version, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return migration{}, fmt.Errorf("parseMigration: %s: %w", name, err)
	}
	raw, err := fs.ReadFile(fsys, path.Join("migrations", name))
	if err != nil {
		return migration{}, fmt.Errorf("parseMigration: %s: %w", name, err)
	}
	body := string(raw)
	if strings.TrimSpace(body) == "" {
		return migration{}, fmt.Errorf("parseMigration: %s: empty body", name)
	}
	if txControl.MatchString(lineComment.ReplaceAllString(body, "")) {
		return migration{}, fmt.Errorf("parseMigration: %s: transaction control is owned by the runner", name)
	}
	sum := sha256.Sum256(raw)
	first, _, _ := strings.Cut(body, "\n")
	return migration{
		name:     name,
		version:  version,
		sql:      body,
		checksum: hex.EncodeToString(sum[:]),
		rebuild:  strings.TrimSpace(first) == rebuildDirective,
	}, nil
}

func applyMigrations(ctx context.Context, conn *sql.Conn, ms []migration, now func() time.Time) error {
	if _, err := conn.ExecContext(ctx, sqlCreateMigrations); err != nil {
		return fmt.Errorf("applyMigrations: %w", err)
	}
	applied, err := appliedMigrations(ctx, conn)
	if err != nil {
		return err
	}
	if err = checkApplied(applied, ms); err != nil {
		return err
	}
	for _, m := range ms {
		if _, done := applied[m.version]; done {
			continue
		}
		if err = applyOne(ctx, conn, m, now); err != nil {
			return err
		}
	}
	return nil
}

func appliedMigrations(ctx context.Context, conn *sql.Conn) (map[int64]string, error) {
	rows, err := conn.QueryContext(ctx, sqlAppliedMigrations)
	if err != nil {
		return nil, fmt.Errorf("appliedMigrations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]string{}
	for rows.Next() {
		var version int64
		var checksum string
		if err = rows.Scan(&version, &checksum); err != nil {
			return nil, fmt.Errorf("appliedMigrations: scan: %w", err)
		}
		out[version] = checksum
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("appliedMigrations: %w", err)
	}
	return out, nil
}

func checkApplied(applied map[int64]string, ms []migration) error {
	newest := int64(len(ms))
	for version := range applied {
		if version > newest {
			return fmt.Errorf("checkApplied: version %d: %w", version, ErrSchemaTooNew)
		}
	}
	for _, m := range ms {
		if sum, ok := applied[m.version]; ok && sum != m.checksum {
			return fmt.Errorf("checkApplied: %s: %w", m.name, ErrMigrationChanged)
		}
	}
	return nil
}

func applyOne(ctx context.Context, conn *sql.Conn, m migration, now func() time.Time) (err error) {
	if m.rebuild {
		if _, err = conn.ExecContext(ctx, sqlFKOff); err != nil {
			return fmt.Errorf("applyOne: %s: disable foreign keys: %w", m.name, err)
		}
		defer func() {
			_, restoreErr := conn.ExecContext(ctx, sqlFKOn)
			err = errors.Join(err, restoreErr)
		}()
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("applyOne: %s: begin: %w", m.name, err)
	}
	if err = runMigration(ctx, tx, m, now()); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("%w: %s: %w", ErrMigrationFailed, m.name, err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("applyOne: %s: commit: %w", m.name, err)
	}
	return nil
}

func runMigration(ctx context.Context, tx *sql.Tx, m migration, appliedAt time.Time) error {
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, sqlMigrationRecord, m.version, m.checksum, appliedAt.UnixMilli()); err != nil {
		return err
	}
	return foreignKeyCheck(ctx, tx)
}

func foreignKeyCheck(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, sqlFKCheck)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		return errors.New("foreign_key_check: violation")
	}
	return rows.Err()
}
