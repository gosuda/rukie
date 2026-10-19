package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func seedWords(t *testing.T, s *Store, words ...string) {
	t.Helper()
	for i, w := range words {
		tag := tagFor("w", i)
		if err := s.Write(t.Context(), func(tx *WriteTx) error { return insertProbe(tx, tag, w) }); err != nil {
			t.Fatalf("seed %q: %v", w, err)
		}
	}
}

func matchTags(t *testing.T, s *Store, term string) []string {
	t.Helper()
	var out []string
	err := s.Read(t.Context(), func(tx *ReadTx) error {
		rows, err := tx.tx.QueryContext(tx.ctx, `SELECT p.tag FROM probe_rows_fts f JOIN probe_rows p ON p.id = f.rowid WHERE probe_rows_fts MATCH ? ORDER BY p.tag`, term)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var tag string
			if err = rows.Scan(&tag); err != nil {
				return err
			}
			out = append(out, tag)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("match %q: %v", term, err)
	}
	return out
}

func TestFTSSurvivesVacuumInto(t *testing.T) {
	src := openTest(t, Options{})
	seedWords(t, src, "alpha beta gamma", "delta epsilon", "gamma ray")
	info, err := src.Backup(t.Context(), filepath.Join(t.TempDir(), "backup.sqlite"))
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if info.SchemaVersion != 2 || info.Bytes <= 0 || len(info.SHA256) != 64 {
		t.Fatalf("BackupInfo = %+v", info)
	}
	copied := openWith(t, info.Path, fixtureFS(t), Options{})
	for _, term := range []string{"amma", "lpha", "psilon"} {
		want := matchTags(t, src, term)
		got := matchTags(t, copied, term)
		if len(want) == 0 || !slices.Equal(want, got) {
			t.Errorf("term %q: backup %v, source %v", term, got, want)
		}
	}
}

func TestBackupRefusesExisting(t *testing.T) {
	src := openTest(t, Options{})
	dst := filepath.Join(t.TempDir(), "taken.sqlite")
	if err := os.WriteFile(dst, []byte("occupied"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	if _, err := src.Backup(t.Context(), dst); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Backup over existing file returned %v, want ErrInvalid", err)
	}
	if data, _ := os.ReadFile(filepath.Clean(dst)); string(data) != "occupied" {
		t.Fatal("Backup modified an existing target")
	}
}

func TestBackupCleansPartial(t *testing.T) {
	src := openTest(t, Options{})
	dst := filepath.Join(t.TempDir(), "cancelled.sqlite")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := src.Backup(ctx, dst); err == nil {
		t.Fatal("Backup with a cancelled context succeeded")
	}
	for _, p := range []string{dst, dst + ".partial"} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s remains after failed backup: %v", p, err)
		}
	}
}

func TestBackupFromQueryOnlyReaderFails(t *testing.T) {
	src := openTest(t, Options{})
	seedWords(t, src, "alpha")
	dst := filepath.Join(t.TempDir(), "reader.sqlite")
	db, err := sqlOpenRaw(src.path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err = conn.ExecContext(t.Context(), `PRAGMA query_only = ON`); err != nil {
		t.Fatalf("query_only: %v", err)
	}
	if _, err = conn.ExecContext(t.Context(), sqlVacuumInto, dst); err == nil {
		t.Fatal("VACUUM INTO on a query_only connection succeeded; the backup connection design is unnecessary")
	}
}
