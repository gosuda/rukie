package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"
)

const fixtureMigration = `CREATE TABLE probe_rows (
  id   INTEGER PRIMARY KEY,
  tag  TEXT    NOT NULL UNIQUE,
  body TEXT    NOT NULL CHECK (length(body) > 0)
) STRICT;

CREATE TABLE probe_children (
  id     INTEGER PRIMARY KEY,
  row_id INTEGER NOT NULL REFERENCES probe_rows (id)
) STRICT;

CREATE VIRTUAL TABLE probe_rows_fts USING fts5(
  body, content='probe_rows', content_rowid='id', tokenize='trigram case_sensitive 0'
);

CREATE TRIGGER probe_rows_ai AFTER INSERT ON probe_rows BEGIN
  INSERT INTO probe_rows_fts (rowid, body) VALUES (new.id, new.body);
END;

CREATE TRIGGER probe_rows_ad AFTER DELETE ON probe_rows BEGIN
  INSERT INTO probe_rows_fts (probe_rows_fts, rowid, body) VALUES ('delete', old.id, old.body);
END;

CREATE TRIGGER probe_rows_au AFTER UPDATE OF body ON probe_rows BEGIN
  INSERT INTO probe_rows_fts (probe_rows_fts, rowid, body) VALUES ('delete', old.id, old.body);
  INSERT INTO probe_rows_fts (rowid, body) VALUES (new.id, new.body);
END;
`

func baseMigration(t *testing.T) []byte {
	t.Helper()
	raw, err := fs.ReadFile(embeddedMigrations, "migrations/0001_init.sql")
	if err != nil {
		t.Fatalf("read embedded 0001: %v", err)
	}
	return raw
}

func fsWith(t *testing.T, extra map[string]string) fs.FS {
	t.Helper()
	m := fstest.MapFS{
		"migrations/0001_init.sql": &fstest.MapFile{Data: baseMigration(t)},
	}
	for name, body := range extra {
		m[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return m
}

func fixtureFS(t *testing.T) fs.FS {
	t.Helper()
	return fsWith(t, map[string]string{"migrations/0002_probe.sql": fixtureMigration})
}

func testPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "meta.sqlite")
}

func openWith(t *testing.T, path string, fsys fs.FS, opts Options) *Store {
	t.Helper()
	opts.Path = path
	s, err := open(context.Background(), opts, fsys)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

func openTest(t *testing.T, opts Options) *Store {
	t.Helper()
	return openWith(t, testPath(t), fixtureFS(t), opts)
}

func insertProbe(tx *WriteTx, tag, body string) error {
	_, err := tx.tx.ExecContext(tx.ctx, `INSERT INTO probe_rows (tag, body) VALUES (?, ?)`, tag, body)
	return err
}

func readInt(t *testing.T, s *Store, query string, args ...any) int64 {
	t.Helper()
	var n int64
	err := s.Read(context.Background(), func(tx *ReadTx) error {
		return tx.tx.QueryRowContext(tx.ctx, query, args...).Scan(&n)
	})
	if err != nil {
		t.Fatalf("readInt %q: %v", query, err)
	}
	return n
}

func rawDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	if err := setupProcess(false); err != nil {
		t.Fatalf("setupProcess: %v", err)
	}
	db, err := sql.Open("sqlite", fileDSN(path, "mode=ro"))
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func sqlOpenRaw(path string) (*sql.DB, error) {
	if err := setupProcess(false); err != nil {
		return nil, err
	}
	return sql.Open("sqlite", fileDSN(path, ""))
}

func rawInt(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("rawInt %q: %v", query, err)
	}
	return n
}

func tagFor(prefix string, n int) string {
	return fmt.Sprintf("%s-%d", prefix, n)
}
