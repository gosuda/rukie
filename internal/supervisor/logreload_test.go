package supervisor

import (
	"io"
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
	lv := &slog.LevelVar{}
	lv.Set(slog.LevelInfo)
	level, err := ReloadLogLevel(lv, path)
	if err != nil {
		t.Fatalf("ReloadLogLevel: %v", err)
	}
	if level != "debug" {
		t.Fatalf("level = %q, want debug", level)
	}
	if lv.Level() != slog.LevelDebug {
		t.Fatalf("level var = %v, want debug", lv.Level())
	}
}

func TestReloadLogLevelRejectsBadFile(t *testing.T) {
	lv := &slog.LevelVar{}
	lv.Set(slog.LevelInfo)
	path := writeConfig(t, logTestBase+"\n[log]\nlevel = \"verbose\"\n")
	if _, err := ReloadLogLevel(lv, path); err == nil {
		t.Fatal("ReloadLogLevel accepted level verbose, want an error")
	}
	if lv.Level() != slog.LevelInfo {
		t.Fatalf("level var moved to %v on a rejected reload, want info", lv.Level())
	}
	unknown := writeConfig(t, logTestBase+"\n[log]\nlevel = \"info\"\n[nope]\nx = 1\n")
	if _, err := ReloadLogLevel(lv, unknown); err == nil {
		t.Fatal("ReloadLogLevel accepted an unknown key, want an error")
	}
	if _, err := ReloadLogLevel(lv, filepath.Join(t.TempDir(), "missing.toml")); err == nil {
		t.Fatal("ReloadLogLevel accepted a missing file, want an error")
	}
}

func TestReloadLogLevelReachesComponentLoggers(t *testing.T) {
	path := writeConfig(t, logTestBase+"\n[log]\nlevel = \"info\"\n")
	lv := &slog.LevelVar{}
	lv.Set(slog.LevelInfo)
	component := slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: lv}))
	if component.Enabled(t.Context(), slog.LevelDebug) {
		t.Fatal("component logger enables debug at info")
	}
	relaxed := writeConfig(t, logTestBase+"\n[log]\nlevel = \"debug\"\n")
	if _, err := ReloadLogLevel(lv, relaxed); err != nil {
		t.Fatalf("ReloadLogLevel: %v", err)
	}
	if !component.Enabled(t.Context(), slog.LevelDebug) {
		t.Fatal("component logger obtained before the reload ignores debug; it keeps a stale level")
	}
	strict := writeConfig(t, logTestBase+"\n[log]\nlevel = \"warn\"\n")
	if _, err := ReloadLogLevel(lv, strict); err != nil {
		t.Fatalf("ReloadLogLevel: %v", err)
	}
	if component.Enabled(t.Context(), slog.LevelInfo) {
		t.Fatal("component logger still enables info after a warn reload")
	}
	if _, err := ReloadLogLevel(lv, path); err != nil {
		t.Fatalf("ReloadLogLevel: %v", err)
	}
}
