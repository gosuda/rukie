package store

import (
	"database/sql"
	"errors"
	"testing"
)

func probeConn(t *testing.T) (*sql.DB, *sql.Conn) {
	t.Helper()
	if err := setupProcess(false); err != nil {
		t.Fatalf("setupProcess: %v", err)
	}
	path := testPath(t)
	if err := ensureFile(path); err != nil {
		t.Fatalf("ensureFile: %v", err)
	}
	db, conn, err := openWriter(t.Context(), path)
	if err != nil {
		t.Fatalf("openWriter: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		_ = db.Close()
	})
	return db, conn
}

func TestFTSProbe(t *testing.T) {
	_, conn := probeConn(t)
	name, err := selectFTS(t.Context(), conn, ftsCandidates)
	if err != nil {
		t.Fatalf("selectFTS: %v", err)
	}
	if name != "trigram case_sensitive 0" {
		t.Fatalf("tokenizer = %q, want the primary trigram spelling", name)
	}
	var temps int64
	if err = conn.QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_temp_master`).Scan(&temps); err != nil {
		t.Fatalf("temp schema: %v", err)
	}
	if temps != 0 {
		t.Fatalf("probe left %d temp objects", temps)
	}
}

func TestFTSProbeErrorPath(t *testing.T) {
	_, conn := probeConn(t)
	bogus := []ftsCandidate{{
		name:   "no-such-tokenizer",
		create: `CREATE VIRTUAL TABLE temp.fts_probe USING fts5(body, content='fts_probe_src', content_rowid='id', tokenize='nosuchtok')`,
	}}
	if _, err := selectFTS(t.Context(), conn, bogus); !errors.Is(err, ErrFTS5Unavailable) {
		t.Fatalf("bogus tokenizer returned %v, want ErrFTS5Unavailable", err)
	}
	fallback := []ftsCandidate{bogus[0], ftsCandidates[1]}
	name, err := selectFTS(t.Context(), conn, fallback)
	if err != nil {
		t.Fatalf("fallback chain: %v", err)
	}
	if name != "trigram" {
		t.Fatalf("fallback tokenizer = %q, want trigram", name)
	}
}
