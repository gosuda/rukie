package sysops

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/gosuda/rukie/internal/config"
	"github.com/gosuda/rukie/internal/store"
)

func testConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	cfg.Store.SQLite.Path = filepath.Join(cfg.DataDir, "meta.sqlite")
	cfg.SecretKeyFile = filepath.Join(cfg.DataDir, "secret.key")
	cfg.Ruk.RepoRoot = filepath.Join(cfg.DataDir, "repos")
	if err := EnsureDataDir(cfg.DataDir); err != nil {
		t.Fatalf("EnsureDataDir: %v", err)
	}
	if _, err := GenerateSecretKey(cfg.SecretKeyFile); err != nil {
		t.Fatalf("GenerateSecretKey: %v", err)
	}
	return cfg
}

func openTestStore(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), store.Options{Path: path})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	return st
}

// fabricateNewerDB writes a database whose schema_migrations claims a
// version beyond this binary, simulating a downgrade.
func fabricateNewerDB(t *testing.T, path string) int64 {
	t.Helper()
	st, err := store.Open(context.Background(), store.Options{Path: path})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if cerr := st.Close(context.Background()); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	code, err := CodeSchemaVersion()
	if err != nil {
		t.Fatalf("CodeSchemaVersion: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	newer := code + 1
	if _, err := db.Exec(`INSERT INTO schema_migrations (version, checksum, applied_at) VALUES (?, '`+strings.Repeat("0", 64)+`', 1)`, newer); err != nil {
		t.Fatalf("fabricate newer version: %v", err)
	}
	return newer
}

func downgradeConfig(t *testing.T, dbPath string) config.Config {
	t.Helper()
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	cfg.Store.SQLite.Path = dbPath
	return cfg
}

func TestDowngradeRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "newer.sqlite")
	fabricateNewerDB(t, path)
	if err := CheckDowngrade(downgradeConfig(t, path)); err == nil {
		t.Fatal("CheckDowngrade passed a newer database, want refusal")
	} else if !IsDowngrade(err) {
		t.Fatalf("CheckDowngrade error = %v, want a downgrade refusal", err)
	}
	if _, err := store.Open(context.Background(), store.Options{Path: path}); err == nil {
		t.Fatal("store.Open passed a newer database, want ErrSchemaTooNew")
	} else if !IsDowngrade(err) {
		t.Fatalf("store.Open error = %v, want a downgrade refusal", err)
	}
}

func TestDowngradeCheckPassesCurrentDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "current.sqlite")
	openTestStore(t, path)
	if err := CheckDowngrade(downgradeConfig(t, path)); err != nil {
		t.Fatalf("CheckDowngrade on a current database: %v", err)
	}
	if err := CheckDowngrade(downgradeConfig(t, filepath.Join(t.TempDir(), "missing.sqlite"))); err != nil {
		t.Fatalf("CheckDowngrade on a missing database: %v", err)
	}
}

func TestDowngradeRefusedByMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "current.sqlite")
	openTestStore(t, path)
	code, err := CodeSchemaVersion()
	if err != nil {
		t.Fatalf("CodeSchemaVersion: %v", err)
	}
	cfg := downgradeConfig(t, path)
	if err := WriteMetaVersion(cfg.DataDir, code+1); err != nil {
		t.Fatalf("WriteMetaVersion: %v", err)
	}
	if err := CheckDowngrade(cfg); !IsDowngrade(err) {
		t.Fatalf("CheckDowngrade passed a newer marker, want refusal (got %v)", err)
	}
	if _, err := PendingMigrations(cfg); !IsDowngrade(err) {
		t.Fatalf("PendingMigrations passed a newer marker, want refusal (got %v)", err)
	}
}

func TestMigrateWritesMarker(t *testing.T) {
	cfg := testConfig(t)
	res, err := Migrate(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if !res.Applied {
		t.Fatal("Migrate reported no work on a fresh database")
	}
	marked, ok := ReadMetaVersion(cfg.DataDir)
	if !ok {
		t.Fatal("Migrate left no meta.version marker")
	}
	if marked != res.AppliedMax {
		t.Fatalf("marker = %d, applied = %d", marked, res.AppliedMax)
	}
}

func TestCodeSchemaVersionMatchesMigrations(t *testing.T) {
	code, err := CodeSchemaVersion()
	if err != nil {
		t.Fatalf("CodeSchemaVersion: %v", err)
	}
	entries, err := os.ReadDir("../store/migrations")
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	if int64(len(entries)) != code {
		t.Fatalf("CodeSchemaVersion = %d, want %d migration files", code, len(entries))
	}
	if code < 1 {
		t.Fatal("CodeSchemaVersion is zero; no migration applied to the probe store")
	}
}

func TestPendingMigrationsEmptyOnCurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "current.sqlite")
	openTestStore(t, path)
	cfg := config.Defaults()
	cfg.Store.SQLite.Path = path
	res, err := PendingMigrations(cfg)
	if err != nil {
		t.Fatalf("PendingMigrations: %v", err)
	}
	if len(res.Pending) != 0 {
		t.Fatalf("pending = %v on a current database, want none", res.Pending)
	}
	if res.AppliedMax != res.CodeMax {
		t.Fatalf("applied %d != code %d on a current database", res.AppliedMax, res.CodeMax)
	}
}

func TestPendingMigrationsRefusesNewer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "newer.sqlite")
	fabricateNewerDB(t, path)
	cfg := config.Defaults()
	cfg.Store.SQLite.Path = path
	if _, err := PendingMigrations(cfg); !IsDowngrade(err) {
		t.Fatalf("PendingMigrations on newer db = %v, want downgrade refusal", err)
	}
	if _, err := Migrate(context.Background(), cfg); !IsDowngrade(err) {
		t.Fatalf("Migrate on newer db = %v, want downgrade refusal", err)
	}
}
