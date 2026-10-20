package sysops

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func seedRepos(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "ada", "hello"), 0o700); err != nil {
		t.Fatalf("mkdir repos: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "ada", "hello", "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
		t.Fatalf("write repo file: %v", err)
	}
}

func TestBackupNeverCopiesKey(t *testing.T) {
	cfg := testConfig(t)
	seedRepos(t, cfg.Ruk.RepoRoot)
	st := openTestStore(t, cfg.Store.SQLite.Path)
	key, err := LoadSecretKey(cfg.SecretKeyFile)
	if err != nil {
		t.Fatalf("LoadSecretKey: %v", err)
	}
	manifest, err := Backup(context.Background(), st, cfg, filepath.Join(cfg.DataDir, "backups"))
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	dir := filepath.Dir(manifest)
	var contents []byte
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, werr error) error {
		if werr != nil || d.IsDir() {
			return werr
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		contents = append(contents, raw...)
		if filepath.Ext(path) == ".key" {
			t.Errorf("backup holds a key file: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan backup: %v", err)
	}
	if bytes.Contains(contents, key) {
		t.Fatal("backup bytes contain the secret key; the key must never enter a backup")
	}
	doc, err := ReadManifest(dir)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if doc.KeyID != KeyID(key) {
		t.Fatalf("manifest key id = %q, want %q", doc.KeyID, KeyID(key))
	}
	if doc.SchemaVersion < 1 {
		t.Fatalf("manifest schema version = %d, want >= 1", doc.SchemaVersion)
	}
	if len(doc.Files) < 2 {
		t.Fatalf("manifest lists %d files, want meta.sqlite plus repo content", len(doc.Files))
	}
	if err := VerifyManifest(dir, doc); err != nil {
		t.Fatalf("VerifyManifest: %v", err)
	}
}

func TestBackupRefusesWithoutKey(t *testing.T) {
	cfg := testConfig(t)
	if err := os.Remove(cfg.SecretKeyFile); err != nil {
		t.Fatalf("remove key: %v", err)
	}
	st := openTestStore(t, cfg.Store.SQLite.Path)
	if _, err := Backup(context.Background(), st, cfg, filepath.Join(cfg.DataDir, "backups")); err == nil {
		t.Fatal("Backup without a secret key succeeded, want refusal: the manifest cannot record its key id")
	}
}

func TestRestoreRefusesKeyMismatch(t *testing.T) {
	cfg := testConfig(t)
	seedRepos(t, cfg.Ruk.RepoRoot)
	st := openTestStore(t, cfg.Store.SQLite.Path)
	manifest, err := Backup(context.Background(), st, cfg, filepath.Join(cfg.DataDir, "backups"))
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if err := st.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := RotateSecretKey(cfg.SecretKeyFile); err != nil {
		t.Fatalf("RotateSecretKey: %v", err)
	}
	if _, err := Restore(context.Background(), cfg, filepath.Dir(manifest)); err == nil {
		t.Fatal("Restore with a rotated key succeeded, want key-id refusal")
	} else if !IsKeyMismatch(err) {
		t.Fatalf("Restore error = %v, want key-id mismatch", err)
	}
	if _, err := RestoreLenient(context.Background(), cfg, filepath.Dir(manifest)); !IsKeyMismatch(err) {
		t.Fatalf("RestoreLenient with a rotated key = %v, want key-id mismatch: --force never bypasses a foreign backup", err)
	}
}

func TestRestoreRoundTrip(t *testing.T) {
	cfg := testConfig(t)
	seedRepos(t, cfg.Ruk.RepoRoot)
	st := openTestStore(t, cfg.Store.SQLite.Path)
	manifest, err := Backup(context.Background(), st, cfg, filepath.Join(cfg.DataDir, "backups"))
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	backupDir := filepath.Dir(manifest)
	if cerr := st.Close(context.Background()); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	// Simulate loss: wipe metadata and repos, keep the key.
	if rerr := os.Remove(cfg.Store.SQLite.Path); rerr != nil {
		t.Fatalf("remove db: %v", rerr)
	}
	if rerr := os.RemoveAll(cfg.Ruk.RepoRoot); rerr != nil {
		t.Fatalf("remove repos: %v", rerr)
	}
	rep, err := Restore(context.Background(), cfg, backupDir)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if rep.Files < 2 {
		t.Fatalf("restored %d files, want meta.sqlite plus repo content", rep.Files)
	}
	if _, err := os.Stat(filepath.Join(cfg.Ruk.RepoRoot, "ada", "hello", "HEAD")); err != nil {
		t.Fatalf("restored repo file missing: %v", err)
	}
	if err := CheckDowngrade(cfg); err != nil {
		t.Fatalf("restored database fails the downgrade check: %v", err)
	}
	restored := openTestStore(t, cfg.Store.SQLite.Path)
	_ = restored
}

func TestRestoreLenientReportsWarnings(t *testing.T) {
	cfg := testConfig(t)
	seedRepos(t, cfg.Ruk.RepoRoot)
	st := openTestStore(t, cfg.Store.SQLite.Path)
	manifest, err := Backup(context.Background(), st, cfg, filepath.Join(cfg.DataDir, "backups"))
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	backupDir := filepath.Dir(manifest)
	if cerr := st.Close(context.Background()); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	repoFile := filepath.Join(backupDir, "repos", "ada", "hello", "HEAD")
	if werr := os.WriteFile(repoFile, []byte("tampered"), 0o600); werr != nil {
		t.Fatalf("tamper backup: %v", werr)
	}
	if _, rerr := Restore(context.Background(), cfg, backupDir); rerr == nil {
		t.Fatal("strict Restore of a tampered backup succeeded, want hash refusal")
	}
	if rerr := os.Remove(cfg.Store.SQLite.Path); rerr != nil {
		t.Fatalf("remove db: %v", rerr)
	}
	if rerr := os.RemoveAll(cfg.Ruk.RepoRoot); rerr != nil {
		t.Fatalf("remove repos: %v", rerr)
	}
	rep, err := RestoreLenient(context.Background(), cfg, backupDir)
	if err != nil {
		t.Fatalf("RestoreLenient: %v", err)
	}
	if len(rep.Warnings) == 0 {
		t.Fatal("RestoreLenient reported no warnings for a tampered backup")
	}
}

func TestRestoreRefusesLiveServer(t *testing.T) {
	cfg := testConfig(t)
	lock, err := AcquireDataDirLock(cfg.DataDir)
	if err != nil {
		t.Fatalf("AcquireDataDirLock: %v", err)
	}
	defer func() { _ = lock.Release() }()
	if _, err := Restore(context.Background(), cfg, t.TempDir()); !errors.Is(err, ErrDataDirLive) {
		t.Fatalf("Restore against a live data dir = %v, want offline refusal", err)
	}
}
