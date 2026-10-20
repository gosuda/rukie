package supervisor

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rukie.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

const logTestBase = `
data_dir = "/tmp/rukie-k10-logtest"
secret_key_file = "/tmp/rukie-k10-logtest.key"

[http]
listen = ["127.0.0.1:3000"]

[store]
backend = "sqlite"
`

func TestReloadLogLevelAppliesFileLevel(t *testing.T) {
	path := writeConfig(t, logTestBase+"\n[log]\nlevel = \"debug\"\n")
	level, logger, err := ReloadLogLevel("json", false, path)
	if err != nil {
		t.Fatalf("ReloadLogLevel: %v", err)
	}
	if level != "debug" {
		t.Fatalf("level = %q, want debug", level)
	}
	if !logger.Enabled(t.Context(), slog.LevelDebug) {
		t.Fatal("reloaded logger does not enable debug")
	}
	if logger.Enabled(t.Context(), slog.Level(-10)) {
		t.Fatal("reloaded logger enables below-debug; level table is wrong")
	}
}

func TestReloadLogLevelRejectsBadFile(t *testing.T) {
	path := writeConfig(t, logTestBase+"\n[log]\nlevel = \"verbose\"\n")
	if _, _, err := ReloadLogLevel("json", false, path); err == nil {
		t.Fatal("ReloadLogLevel accepted level verbose, want an error")
	}
	unknown := writeConfig(t, logTestBase+"\n[log]\nlevel = \"info\"\n[nope]\nx = 1\n")
	if _, _, err := ReloadLogLevel("json", false, unknown); err == nil {
		t.Fatal("ReloadLogLevel accepted an unknown key, want an error")
	}
	if _, _, err := ReloadLogLevel("json", false, filepath.Join(t.TempDir(), "missing.toml")); err == nil {
		t.Fatal("ReloadLogLevel accepted a missing file, want an error")
	}
}
