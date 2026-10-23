package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
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

func TestBackupSnapshotUnderQueuedWrites(t *testing.T) {
	src := openTest(t, Options{})
	const pre = 20
	wantBackup := make([]string, pre)
	for i := range pre {
		wantBackup[i] = tagFor("pre", i)
		if err := src.Write(t.Context(), func(tx *WriteTx) error {
			return insertProbe(tx, wantBackup[i], "seed")
		}); err != nil {
			t.Fatalf("seed %s: %v", wantBackup[i], err)
		}
	}
	started := make(chan struct{})
	gate := make(chan struct{})
	gateDone := make(chan error, 1)
	go func() {
		gateDone <- src.Write(t.Context(), func(*WriteTx) error {
			close(started)
			<-gate
			return nil
		})
	}()
	<-started
	const queued = 10
	wantQueued := make([]string, queued)
	for i := range queued {
		wantQueued[i] = tagFor("qb", i)
	}
	results := make([]error, queued)
	var wg sync.WaitGroup
	for i := range queued {
		wg.Go(func() {
			results[i] = src.Write(t.Context(), func(tx *WriteTx) error {
				return insertProbe(tx, wantQueued[i], "queued")
			})
		})
	}
	filled := false
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if src.Stats().QueueLen == queued {
			filled = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	dst := filepath.Join(t.TempDir(), "queued.sqlite")
	info, berr := src.Backup(t.Context(), dst)
	close(gate)
	if err := <-gateDone; err != nil {
		t.Fatalf("gate write: %v", err)
	}
	wg.Wait()
	if !filled {
		t.Fatalf("queue never held %d writes behind the gate", queued)
	}
	if berr != nil {
		t.Fatalf("Backup under queued writes: %v", berr)
	}
	if info.SchemaVersion != 2 || info.Bytes <= 0 || len(info.SHA256) != 64 {
		t.Fatalf("BackupInfo = %+v", info)
	}
	for i, err := range results {
		if err != nil {
			t.Errorf("queued write %d: %v", i, err)
		}
	}
	collectTags := func(s *Store) []string {
		t.Helper()
		var out []string
		err := s.Read(t.Context(), func(tx *ReadTx) error {
			rows, err := tx.tx.QueryContext(tx.ctx, `SELECT tag FROM probe_rows ORDER BY tag`)
			if err != nil {
				return err
			}
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				var tag string
				if err := rows.Scan(&tag); err != nil {
					return err
				}
				out = append(out, tag)
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatalf("tags: %v", err)
		}
		return out
	}
	copied := openWith(t, info.Path, fixtureFS(t), Options{})
	slices.Sort(wantBackup)
	if got := collectTags(copied); !slices.Equal(got, wantBackup) {
		t.Errorf("backup tags = %v, want committed prefix %v", got, wantBackup)
	}
	wantSource := slices.Sorted(slices.Values(slices.Concat(wantBackup, wantQueued)))
	if got := collectTags(src); !slices.Equal(got, wantSource) {
		t.Errorf("source tags = %v, want %v", got, wantSource)
	}
}
