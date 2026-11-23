package store

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func readOnlyFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestReadOnlyOpenKeepsCommittedWAL(t *testing.T) {
	ctx := t.Context()
	path := testPath(t)
	fsys := fixtureFS(t)
	initial := openWith(t, path, fsys, Options{})
	if err := initial.Close(ctx); err != nil {
		t.Fatal(err)
	}
	raw, err := sqlOpenRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	conn, err := raw.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err = conn.ExecContext(ctx, `PRAGMA wal_autocheckpoint = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(ctx, `INSERT INTO probe_rows (tag, body) VALUES ('pending', 'committed in WAL')`); err != nil {
		t.Fatal(err)
	}
	beforeDB := readOnlyFileBytes(t, path)
	beforeWAL := readOnlyFileBytes(t, path+"-wal")
	if len(beforeWAL) == 0 {
		t.Fatal("fixture has no committed WAL")
	}
	barrierCalled := false
	s := openWith(t, path, fsys, Options{ReadOnly: true, Barrier: func() (func(), bool) {
		barrierCalled = true
		return func() {}, true
	}})
	if got := readInt(t, s, `SELECT count(*) FROM probe_rows WHERE tag = 'pending'`); got != 1 {
		t.Fatalf("read-only open ignored committed WAL: %d rows", got)
	}
	callbackCalled := false
	if err = s.Write(ctx, func(*WriteTx) error {
		callbackCalled = true
		return nil
	}); !errors.Is(err, ErrReadOnly) || callbackCalled || barrierCalled {
		t.Fatalf("read-only Write = %v, callback=%t, barrier=%t", err, callbackCalled, barrierCalled)
	}
	err = s.Read(ctx, func(tx *ReadTx) error {
		if _, queryErr := tx.tx.ExecContext(tx.ctx, `PRAGMA query_only = OFF`); queryErr != nil {
			return queryErr
		}
		_, writeErr := tx.tx.ExecContext(tx.ctx, `INSERT INTO probe_rows (tag, body) VALUES ('forbidden', 'read callback')`)
		return writeErr
	})
	if sqlErr, ok := errors.AsType[*sqlite.Error](err); !ok || sqlErr.Code()&0xff != sqlite3.SQLITE_READONLY {
		t.Fatalf("read-only SQLite connection allowed a write: %v", err)
	}
	if got := s.Stats(); got != (Stats{}) {
		t.Fatalf("read-only store has writer activity: %+v", got)
	}
	for range 2 {
		if err = s.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Read(ctx, func(*ReadTx) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Fatalf("Read after Close = %v, want ErrClosed", err)
	}
	if !bytes.Equal(beforeDB, readOnlyFileBytes(t, path)) || !bytes.Equal(beforeWAL, readOnlyFileBytes(t, path+"-wal")) {
		t.Fatal("read-only open, queries, or close changed database or WAL bytes")
	}
	reopened := openWith(t, path, fsys, Options{ReadOnly: true})
	if got := readInt(t, reopened, `SELECT count(*) FROM probe_rows`); got != 1 {
		t.Fatalf("read-only reopen has %d rows, want 1", got)
	}
}

func TestReadOnlyOpenDoesNotCreateDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "meta.sqlite")
	for range 2 {
		s, err := Open(t.Context(), Options{Path: path, ReadOnly: true})
		if s != nil {
			_ = s.Close(context.Background())
			t.Fatal("read-only open created a missing database")
		}
		if err == nil || errors.Is(err, ErrAlreadyOpen) {
			t.Fatalf("read-only missing database error = %v", err)
		}
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only open created the missing directory: %v", err)
	}
}

func TestReadOnlyOpenRequiresCompleteSchema(t *testing.T) {
	const third = `CREATE TABLE later (id INTEGER PRIMARY KEY) STRICT;`
	three := fsWith(t, map[string]string{
		"migrations/0002_probe.sql": fixtureMigration,
		"migrations/0003_later.sql": third,
	})
	for _, tc := range []struct {
		want   error
		seed   fs.FS
		read   fs.FS
		name   string
		mutate string
	}{
		{want: ErrMigrationRequired, seed: fsWith(t, nil), read: fixtureFS(t), name: "outdated"},
		{want: ErrMigrationRequired, seed: three, read: three, name: "missing earlier migration", mutate: `DELETE FROM schema_migrations WHERE version = 2`},
		{want: ErrMigrationChanged, seed: fixtureFS(t), read: fsWith(t, map[string]string{"migrations/0002_probe.sql": fixtureMigration + "\n"}), name: "checksum drift"},
		{want: ErrSchemaTooNew, seed: three, read: fixtureFS(t), name: "newer schema"},
		{want: ErrMigrationChanged, seed: three, read: fsWith(t, map[string]string{"migrations/0003_later.sql": third}), name: "unexpected migration in reserved gap"},
		{want: ErrAppIDMismatch, seed: fixtureFS(t), read: fixtureFS(t), name: "unowned database", mutate: `PRAGMA application_id = 0`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := testPath(t)
			initial := openWith(t, path, tc.seed, Options{})
			if tc.mutate != "" {
				if err := initial.Write(t.Context(), func(tx *WriteTx) error {
					_, queryErr := tx.tx.ExecContext(tx.ctx, tc.mutate)
					return queryErr
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := initial.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			before := readOnlyFileBytes(t, path)
			for range 2 {
				s, err := open(t.Context(), Options{Path: path, ReadOnly: true}, tc.read)
				if s != nil {
					_ = s.Close(context.Background())
					t.Fatal("read-only open accepted incompatible schema")
				}
				if !errors.Is(err, tc.want) {
					t.Fatalf("read-only schema error = %v, want %v", err, tc.want)
				}
				if !bytes.Equal(before, readOnlyFileBytes(t, path)) {
					t.Fatal("read-only schema rejection changed database bytes")
				}
			}
		})
	}
}
