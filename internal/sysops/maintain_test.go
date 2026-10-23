package sysops

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupSkipsKeyInsideRepoRoot(t *testing.T) {
	cfg := testConfig(t)
	nested := filepath.Join(cfg.Ruk.RepoRoot, "secret.key")
	raw, rerr := os.ReadFile(cfg.SecretKeyFile)
	if rerr != nil {
		t.Fatalf("read key: %v", rerr)
	}
	cfg.SecretKeyFile = nested
	if werr := os.WriteFile(nested, raw, 0o600); werr != nil {
		t.Fatalf("write nested key: %v", werr)
	}
	seedRepos(t, cfg.Ruk.RepoRoot)
	st := openTestStore(t, cfg.Store.SQLite.Path)
	manifest, err := Backup(context.Background(), st, cfg, filepath.Join(cfg.DataDir, "backups"))
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	doc, err := ReadManifest(filepath.Dir(manifest))
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	for _, f := range doc.Files {
		if f.Path == "repos/secret.key" {
			t.Fatal("backup copied the secret key out of the repo root")
		}
	}
}

func TestMigrateDryRunListsNothingOnCurrent(t *testing.T) {
	cfg := testConfig(t)
	openTestStore(t, cfg.Store.SQLite.Path)
	res, err := PendingMigrations(cfg)
	if err != nil {
		t.Fatalf("PendingMigrations: %v", err)
	}
	if len(res.Pending) != 0 {
		t.Fatalf("pending = %v, want none", res.Pending)
	}
	applied, err := Migrate(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if applied.Applied {
		t.Fatal("Migrate reported work on a current database")
	}
}

func TestMigrateAppliesAndReports(t *testing.T) {
	cfg := testConfig(t)
	res, err := PendingMigrations(cfg)
	if err != nil {
		t.Fatalf("PendingMigrations: %v", err)
	}
	if len(res.Pending) == 0 {
		t.Fatal("fresh database reports no pending migrations")
	}
	if res.AppliedMax != 0 {
		t.Fatalf("applied = %d on a missing database, want 0", res.AppliedMax)
	}
	applied, err := Migrate(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if !applied.Applied || applied.AppliedMax != applied.CodeMax {
		t.Fatalf("migrate result = %+v, want full application", applied)
	}
	again, err := PendingMigrations(cfg)
	if err != nil {
		t.Fatalf("PendingMigrations after apply: %v", err)
	}
	if len(again.Pending) != 0 {
		t.Fatalf("pending after apply = %v, want none", again.Pending)
	}
}

func TestStatusWorksDuringDowngrade(t *testing.T) {
	cfg := testConfig(t)
	path := cfg.Store.SQLite.Path
	fabricateDowngradeDB(t, path)
	rep, err := Status(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Status on a newer database: %v; read-only status must survive downgrade", err)
	}
	if !rep.Downgrade {
		t.Fatal("Status missed the downgrade flag on a newer database")
	}
	if rep.SchemaApplied <= rep.SchemaCode {
		t.Fatalf("applied %d <= code %d, want applied newer", rep.SchemaApplied, rep.SchemaCode)
	}
	if rep.StoreReadable {
		t.Fatal("StoreReadable is true during a downgrade the binary cannot open")
	}
}

func TestStatusFindsLastBackup(t *testing.T) {
	cfg := testConfig(t)
	seedRepos(t, cfg.Ruk.RepoRoot)
	st := openTestStore(t, cfg.Store.SQLite.Path)
	before := time.Now().UnixMilli()
	if _, err := Backup(context.Background(), st, cfg, filepath.Join(cfg.DataDir, "backups")); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	rep, err := Status(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.BackupCount != 1 {
		t.Fatalf("backup count = %d, want 1", rep.BackupCount)
	}
	if rep.LastBackupAt < before {
		t.Fatalf("last backup %d predates the test backup %d", rep.LastBackupAt, before)
	}
	if rep.RepoEntries == 0 {
		t.Fatal("repo inventory is empty with a seeded repo root")
	}
}

func TestReconcileFindsDrift(t *testing.T) {
	cfg := testConfig(t)
	seedRepos(t, cfg.Ruk.RepoRoot)
	st := openTestStore(t, cfg.Store.SQLite.Path)
	manifest, err := Backup(context.Background(), st, cfg, filepath.Join(cfg.DataDir, "backups"))
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	backupDir := filepath.Dir(manifest)
	clean, err := Reconcile(context.Background(), cfg, backupDir)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(clean.Orphaned) != 0 || len(clean.Created) != 0 || len(clean.Mismatched) != 0 {
		t.Fatalf("clean reconcile reports drift: %+v", clean)
	}
	if rerr := os.Remove(filepath.Join(cfg.Ruk.RepoRoot, "ada", "hello", "HEAD")); rerr != nil {
		t.Fatalf("remove tracked: %v", rerr)
	}
	if werr := os.WriteFile(filepath.Join(cfg.Ruk.RepoRoot, "post-boundary"), []byte("new"), 0o600); werr != nil {
		t.Fatalf("write untracked: %v", werr)
	}
	if merr := os.MkdirAll(filepath.Join(cfg.Ruk.RepoRoot, "ada", "hello"), 0o700); merr != nil {
		t.Fatalf("mkdir: %v", merr)
	}
	if werr := os.WriteFile(filepath.Join(cfg.Ruk.RepoRoot, "ada", "hello", "refs"), []byte("changed"), 0o600); werr != nil {
		t.Fatalf("write changed: %v", werr)
	}
	drifted, err := Reconcile(context.Background(), cfg, backupDir)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(drifted.Orphaned) != 1 {
		t.Fatalf("orphaned = %v, want the deleted HEAD", drifted.Orphaned)
	}
	if len(drifted.Created) != 2 {
		t.Fatalf("created = %v, want post-boundary plus rewritten refs", drifted.Created)
	}
}

func TestReconcileWithoutManifestInventories(t *testing.T) {
	cfg := testConfig(t)
	seedRepos(t, cfg.Ruk.RepoRoot)
	rep, err := Reconcile(context.Background(), cfg, "")
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if rep.Checked != 1 || len(rep.Created) != 1 {
		t.Fatalf("inventory = %+v, want one created entry", rep)
	}
}
