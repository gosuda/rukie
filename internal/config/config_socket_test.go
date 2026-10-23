package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSocketConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rukie.toml")
	base := "data_dir = \"/tmp/rukie-socktest\"\n" +
		"secret_key_file = \"/tmp/rukie-socktest.key\"\n" +
		"[http]\nlisten = [\"127.0.0.1:3000\"]\n" +
		"[store]\nbackend = \"sqlite\"\n"
	if err := os.WriteFile(path, []byte(base+body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestGitMaxBodyBytesConfig(t *testing.T) {
	cfg, err := Load(writeSocketConfig(t, "[git]\nmax_body_bytes = 12345\n"))
	if err != nil {
		t.Fatalf("Load with [git].max_body_bytes: %v", err)
	}
	if cfg.Git.MaxBodyBytes != 12345 {
		t.Fatalf("git.max_body_bytes = %d, want 12345", cfg.Git.MaxBodyBytes)
	}
}

func TestRukSocketSelectsExternalDaemon(t *testing.T) {
	cfg, err := Load(writeSocketConfig(t, "[ruk]\nsocket = \"/run/ruk-external.sock\"\n"))
	if err != nil {
		t.Fatalf("Load with [ruk] socket: %v", err)
	}
	if cfg.Ruk.Socket != "/run/ruk-external.sock" {
		t.Fatalf("ruk.socket = %q", cfg.Ruk.Socket)
	}
	if cfg.Ruk.SocketPath == "" {
		t.Fatal("ruk.socket_path lost its data_dir default beside socket")
	}
}

func TestRukSocketResolvesRelative(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rukie.toml")
	body := "data_dir = \"/tmp/x\"\nsecret_key_file = \"/tmp/x.key\"\n" +
		"[http]\nlisten = [\"127.0.0.1:3000\"]\n[store]\nbackend = \"sqlite\"\n" +
		"[ruk]\nsocket = \"run/external.sock\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := filepath.Join(dir, "run", "external.sock"); cfg.Ruk.Socket != want {
		t.Fatalf("ruk.socket = %q, want %q", cfg.Ruk.Socket, want)
	}
}

func TestRukSocketMayMatchSocketPath(t *testing.T) {
	cfg, err := Load(writeSocketConfig(t, "[ruk]\nsocket = \"/tmp/rukie-socktest/run/ruk.sock\"\n"+
		"socket_path = \"/tmp/rukie-socktest/run/ruk.sock\"\n"))
	if err != nil {
		t.Fatalf("Load refused a shared socket path: %v", err)
	}
	if cfg.Ruk.Socket == "" {
		t.Fatal("external socket did not survive Load")
	}
}

func TestRukSocketUnknownKeyStillFails(t *testing.T) {
	path := writeSocketConfig(t, "[ruk]\nsockett = \"/run/x.sock\"\n")
	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted unknown key ruk.sockett; unknown keys must fail startup")
	}
}

func TestRukRestartKeysAreRejected(t *testing.T) {
	for _, key := range []string{"restart_max_attempts = 3", "restart_backoff_ms = 1000"} {
		path := writeSocketConfig(t, "[ruk]\n"+key+"\n")
		if _, err := Load(path); err == nil {
			t.Fatalf("Load accepted removed key %q; O35 fixes attempts (3) and backoff (1s,2s,4s)", key)
		}
	}
	path := writeSocketConfig(t, "[ruk]\nstartup_timeout_ms = 20000\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load refused startup_timeout_ms: %v", err)
	}
	if cfg.Ruk.StartupTimeoutMs != 20000 {
		t.Fatalf("ruk.startup_timeout_ms = %d, want 20000", cfg.Ruk.StartupTimeoutMs)
	}
}
