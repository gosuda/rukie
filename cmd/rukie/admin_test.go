package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/gosuda/rukie/internal/store"
	"github.com/gosuda/rukie/internal/sysops"
)

func writeTestConfig(t *testing.T, dataDir, extra string) string {
	t.Helper()
	keyPath := filepath.Join(dataDir, "secret.key")
	if err := os.WriteFile(keyPath, []byte(strings.Repeat("k", 32)), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	body := "data_dir = " + quoteForTest(dataDir) + "\n" +
		"secret_key_file = " + quoteForTest(keyPath) + "\n" +
		"[http]\nlisten = [\"127.0.0.1:0\"]\n" +
		"[store]\nbackend = \"sqlite\"\n" + extra
	path := filepath.Join(dataDir, "rukie.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func quoteForTest(s string) string {
	return "\"" + strings.ReplaceAll(s, "\"", "\\\"") + "\""
}

func TestCheckConfigFailsFast(t *testing.T) {
	dir := t.TempDir()
	bad := writeTestConfig(t, dir, "[nope]\nx = 1\n")
	start := time.Now()
	code := run([]string{"serve", "--check-config", "--config", bad})
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("check-config took %s, want a fast fail without opening ports or starting the daemon", elapsed)
	}
	if code != 2 {
		t.Fatalf("check-config on a bad file = %d, want 2", code)
	}
	good := writeTestConfig(t, dir, "")
	if code := run([]string{"serve", "--check-config", "--config", good}); code != 0 {
		t.Fatalf("check-config on a good file = %d, want 0", code)
	}
	if code := run([]string{"serve", "--check-config", "--config", filepath.Join(dir, "missing.toml")}); code != 2 {
		t.Fatalf("check-config on a missing file = %d, want 2", code)
	}
}

func TestAdminMigrateDowngradeIsExit3(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "")
	dbPath := filepath.Join(dir, "meta.sqlite")
	st, err := store.Open(t.Context(), store.Options{Path: dbPath})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if cerr := st.Close(t.Context()); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	code, err := sysops.CodeSchemaVersion()
	if err != nil {
		t.Fatalf("code schema: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations (version, checksum, applied_at) VALUES (?, ?, 1)`,
		code+1, strings.Repeat("0", 64)); err != nil {
		_ = db.Close()
		t.Fatalf("fabricate newer version: %v", err)
	}
	_ = db.Close()
	if code := run([]string{"admin", "migrate", "--config", cfgPath}); code != 3 {
		t.Fatalf("admin migrate on a newer database = %d, want 3 (downgrade refused)", code)
	}
	if code := run([]string{"admin", "migrate", "--dry-run", "--config", cfgPath}); code != 3 {
		t.Fatalf("admin migrate --dry-run on a newer database = %d, want 3", code)
	}
	if code := run([]string{"admin", "status", "--config", cfgPath}); code != 0 {
		t.Fatalf("admin status on a newer database = %d, want 0: read-only status survives downgrade", code)
	}
}

func TestAdminSecretGenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "keys", "secret.key")
	if code := run([]string{"admin", "secret-gen", "--out", out}); code != 0 {
		t.Fatalf("secret-gen = %d, want 0", code)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("key length = %d, want 32", len(raw))
	}
	if code := run([]string{"admin", "secret-gen", "--out", out}); code == 0 {
		t.Fatal("second secret-gen succeeded, want overwrite refusal")
	}
	if code := run([]string{"admin", "secret-gen", "--out", out, "--force"}); code != 0 {
		t.Fatalf("secret-gen --force = %d, want 0", code)
	}
}

func TestRestoreAcceptsFlagAfterDir(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "")
	missing := filepath.Join(dir, "no-backup")
	if code := run([]string{"restore", "--config", cfgPath, missing, "--force"}); code == exitUsage {
		t.Fatal("restore <dir> --force died in flag parsing, want the runtime to judge the missing dir")
	}
	if code := run([]string{"restore", "--force", "--config", cfgPath, missing}); code == exitUsage {
		t.Fatal("restore --force <dir> died in flag parsing")
	}
}

func TestAdminLeavesFallThrough(t *testing.T) {
	if code, ok := tryHandWrittenAdmin([]string{"user", "list"}); ok {
		t.Fatalf("tryHandWrittenAdmin claimed the derived leaf admin user list with code %d", code)
	}
	if code, ok := tryHandWrittenAdmin(nil); ok {
		t.Fatalf("tryHandWrittenAdmin claimed bare admin with code %d", code)
	}
	if _, ok := tryHandWrittenAdmin([]string{"status"}); !ok {
		t.Fatal("tryHandWrittenAdmin did not claim the owned leaf admin status")
	}
}
