package sysops

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/store"
)

func TestBarrierBlocksMutationsAndReleases(t *testing.T) {
	dir := t.TempDir()
	release, held := HoldSharedBarrier(dir)
	if !held {
		t.Fatal("shared hold on a free mutex refused")
	}
	if _, active := BarrierActive(dir); active {
		t.Fatal("barrier record exists with no backup running")
	}
	done := make(chan error, 1)
	go func() {
		_, err := BeginBarrierWait(dir, 200*time.Millisecond)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("exclusive take succeeded while a mutation holds shared; in-flight pushes are not drained")
		}
		if !isBarrierHeld(err) {
			t.Fatalf("take error = %v, want a barrier-held refusal", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("exclusive take neither waited nor refused")
	}
	release()
	b, err := BeginBarrier(dir)
	if err != nil {
		t.Fatalf("BeginBarrier: %v", err)
	}
	if b.ID() == "" {
		t.Fatal("barrier has no id")
	}
	if id, active := BarrierActive(dir); !active || id != b.ID() {
		t.Fatalf("Active = (%q, %v), want (%q, true)", id, active, b.ID())
	}
	if _, held := HoldSharedBarrier(dir); held {
		t.Fatal("shared hold succeeded under an exclusive barrier; a push would write past the snapshot")
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, active := BarrierActive(dir); active {
		t.Fatal("barrier record survives Close")
	}
	release2, ok := HoldSharedBarrier(dir)
	if !ok {
		t.Fatal("shared hold refused after Close")
	}
	release2()
	if err := b.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func isBarrierHeld(err error) bool {
	return errors.Is(err, ErrBarrierHeld) || (err != nil && strings.Contains(err.Error(), "in-flight mutations"))
}

func TestBackupTakesBarrierWhileLive(t *testing.T) {
	cfg := testConfig(t)
	seedRepos(t, cfg.Ruk.RepoRoot)
	st := openTestStore(t, cfg.Store.SQLite.Path)
	lock, err := AcquireDataDirLock(cfg.DataDir)
	if err != nil {
		t.Fatalf("simulate live server: %v", err)
	}
	defer func() { _ = lock.Release() }()
	manifest, err := Backup(t.Context(), st, cfg, filepath.Join(cfg.DataDir, "backups"))
	if err != nil {
		t.Fatalf("online Backup: %v", err)
	}
	doc, err := ReadManifest(filepath.Dir(manifest))
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if doc.BarrierID == "" {
		t.Fatal("online manifest carries no barrier_id; restore cannot prove t_M/t_R coexisted")
	}
	if doc.SnapshotSource != "barrier-held" {
		t.Fatalf("snapshot_source = %q, want barrier-held", doc.SnapshotSource)
	}
	if doc.TM <= 0 || doc.TR < doc.TM {
		t.Fatalf("t_M = %d, t_R = %d, want 0 < t_M <= t_R", doc.TM, doc.TR)
	}
	if _, active := BarrierActive(cfg.DataDir); active {
		t.Fatal("barrier still held after Backup returned")
	}
	if err := VerifyManifest(filepath.Dir(manifest), doc); err != nil {
		t.Fatalf("VerifyManifest: %v", err)
	}
}

func TestRestoreRefusesLegacyManifestWithoutBarrier(t *testing.T) {
	cfg := testConfig(t)
	seedRepos(t, cfg.Ruk.RepoRoot)
	st := openTestStore(t, cfg.Store.SQLite.Path)
	manifest, err := Backup(t.Context(), st, cfg, filepath.Join(cfg.DataDir, "backups"))
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if cerr := st.Close(t.Context()); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	legacyDir := filepath.Join(cfg.DataDir, "legacy")
	if merr := os.MkdirAll(legacyDir, 0o700); merr != nil {
		t.Fatalf("legacy dir: %v", merr)
	}
	doc, rerr := ReadManifest(filepath.Dir(manifest))
	if rerr != nil {
		t.Fatalf("ReadManifest: %v", rerr)
	}
	doc.BarrierID = ""
	if werr := writeManifest(legacyDir, doc); werr != nil {
		t.Fatalf("write legacy manifest: %v", werr)
	}
	for _, f := range doc.Files {
		raw, frerr := os.ReadFile(filepath.Join(filepath.Dir(manifest), f.Path))
		if frerr != nil {
			t.Fatalf("copy backup file: %v", frerr)
		}
		dst := filepath.Join(legacyDir, f.Path)
		if merr := os.MkdirAll(filepath.Dir(dst), 0o700); merr != nil {
			t.Fatalf("legacy parent: %v", merr)
		}
		if werr := os.WriteFile(dst, raw, 0o600); werr != nil {
			t.Fatalf("legacy file: %v", werr)
		}
	}
	if _, err := Restore(t.Context(), cfg, legacyDir); err == nil {
		t.Fatal("Restore of a barrier-less manifest succeeded; only --force may proceed past a legacy backup")
	}
	rep, lerr := RestoreLenient(t.Context(), cfg, legacyDir)
	if lerr != nil {
		t.Fatalf("RestoreLenient: %v", lerr)
	}
	if len(rep.Warnings) == 0 {
		t.Fatal("forced restore of a legacy manifest recorded no warning")
	}
}

func TestRestoreRollsBackMidCopyFailure(t *testing.T) {
	cfg := testConfig(t)
	if err := os.MkdirAll(filepath.Join(cfg.Ruk.RepoRoot, "ada", "hello"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Ruk.RepoRoot, "ada", "hello", "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	st := openTestStore(t, cfg.Store.SQLite.Path)
	manifest, err := Backup(t.Context(), st, cfg, filepath.Join(cfg.DataDir, "backups"))
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if err := st.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	blocker := filepath.Join(cfg.Ruk.RepoRoot, "ada")
	_ = os.RemoveAll(blocker)
	if berr := os.WriteFile(blocker, []byte("in the way"), 0o600); berr != nil {
		t.Fatalf("blocker: %v", berr)
	}
	if _, rerr := Restore(t.Context(), cfg, filepath.Dir(manifest)); rerr == nil {
		t.Fatal("Restore past a blocked destination succeeded, want a mid-copy failure")
	}
	stuck, serr := store.Open(t.Context(), store.Options{Path: cfg.Store.SQLite.Path})
	if serr != nil {
		t.Fatalf("restored store does not open after rollback: %v", serr)
	}
	_ = stuck.Close(t.Context())
}
