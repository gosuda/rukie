package store

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
)

func openReadOnlyAt(ctx context.Context, path string, opts Options, fsys fs.FS) (*Store, error) {
	migs, err := loadMigrations(fsys)
	if err != nil {
		return nil, fmt.Errorf("openReadOnlyAt: %w", err)
	}
	rdb, readers, err := openReaderPool(ctx, path, readerCount(opts.ReadConns))
	if err != nil {
		return nil, fmt.Errorf("openReadOnlyAt: %w", err)
	}
	conn := <-readers
	err = verifyReadOnlySchema(ctx, conn, migs)
	readers <- conn
	if err != nil {
		drainClose(readers)
		_ = rdb.Close()
		return nil, fmt.Errorf("openReadOnlyAt: %w", err)
	}
	return &Store{
		path:        path,
		log:         cmp.Or(opts.Logger, slog.Default()),
		migrations:  migs,
		rdb:         rdb,
		readers:     readers,
		readerCount: cap(readers),
		readClosing: make(chan struct{}),
		closeSem:    make(chan struct{}, 1),
	}, nil
}

func verifyReadOnlySchema(ctx context.Context, conn *sql.Conn, migs []migration) error {
	if err := checkSQLiteVersion(ctx, conn); err != nil {
		return fmt.Errorf("verifyReadOnlySchema: %w", err)
	}
	var id int64
	if err := conn.QueryRowContext(ctx, sqlAppID).Scan(&id); err != nil {
		return fmt.Errorf("verifyReadOnlySchema: %w", err)
	}
	if id != appID {
		return fmt.Errorf("verifyReadOnlySchema: application_id %d: %w", id, ErrAppIDMismatch)
	}
	applied, err := appliedMigrations(ctx, conn)
	if err != nil {
		return fmt.Errorf("verifyReadOnlySchema: %w", err)
	}
	if err = checkApplied(applied, migs); err != nil {
		return fmt.Errorf("verifyReadOnlySchema: %w", err)
	}
	for _, m := range migs {
		if _, ok := applied[m.version]; !ok {
			return fmt.Errorf("verifyReadOnlySchema: %s: %w", m.name, ErrMigrationRequired)
		}
	}
	if len(applied) != len(migs) {
		return fmt.Errorf("verifyReadOnlySchema: unexpected applied migration: %w", ErrMigrationChanged)
	}
	return nil
}
