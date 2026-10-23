package sysops

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func writeBackupFile(t *testing.T, backupDir, rel, content string) ManifestFile {
	t.Helper()
	src := filepath.Join(backupDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(src), 0o700); err != nil {
		t.Fatalf("backup parent: %v", err)
	}
	if err := os.WriteFile(src, []byte(content), 0o600); err != nil {
		t.Fatalf("backup file: %v", err)
	}
	sum := sha256.Sum256([]byte(content))
	return ManifestFile{
		Path:   rel,
		SHA256: fmt.Sprintf("%x", sum),
		Size:   int64(len(content)),
	}
}

func mustReadDst(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func TestRestoreRollbackLeavesUnreachedDestinations(t *testing.T) {
	cfg := testConfig(t)
	backupDir := t.TempDir()

	fresh := writeBackupFile(t, backupDir, "repos/fresh.git/HEAD", "fresh-new")
	parked := writeBackupFile(t, backupDir, "repos/a.git/HEAD", "a-new")
	blocked := writeBackupFile(t, backupDir, "repos/blocked/x", "blocked-new")
	unreached := writeBackupFile(t, backupDir, "repos/later.git/HEAD", "later-new")

	freshDst := filepath.Join(cfg.Ruk.RepoRoot, "fresh.git", "HEAD")
	parkedDst := filepath.Join(cfg.Ruk.RepoRoot, "a.git", "HEAD")
	laterDst := filepath.Join(cfg.Ruk.RepoRoot, "later.git", "HEAD")

	if err := os.MkdirAll(filepath.Dir(parkedDst), 0o700); err != nil {
		t.Fatalf("parked parent: %v", err)
	}
	if err := os.WriteFile(parkedDst, []byte("a-old"), 0o600); err != nil {
		t.Fatalf("parked dst: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(laterDst), 0o700); err != nil {
		t.Fatalf("later parent: %v", err)
	}
	if err := os.WriteFile(laterDst, []byte("later-original"), 0o600); err != nil {
		t.Fatalf("later dst: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Ruk.RepoRoot, "blocked"), []byte("in the way"), 0o600); err != nil {
		t.Fatalf("blocker: %v", err)
	}

	doc := ManifestDoc{Files: []ManifestFile{fresh, parked, blocked, unreached}}
	if _, err := placeFilesStaged(cfg, backupDir, doc); err == nil {
		t.Fatal("placeFilesStaged past a blocked destination succeeded, want a mid-install failure")
	}

	if _, err := os.Stat(freshDst); !os.IsNotExist(err) {
		t.Fatalf("fresh dst survives rollback: %v", err)
	}
	if got := mustReadDst(t, parkedDst); got != "a-old" {
		t.Fatalf("parked dst = %q, want original %q", got, "a-old")
	}
	if got := mustReadDst(t, laterDst); got != "later-original" {
		t.Fatalf("unreached dst = %q, want untouched %q", got, "later-original")
	}
}
