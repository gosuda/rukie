package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/gosuda/rukie/internal/config"
)

func testConfig() config.Config {
	cfg := config.Defaults()
	cfg.DataDir = "/var/lib/rukie"
	cfg.Ruk.RepoRoot = "/var/lib/rukie/repos"
	cfg.Ruk.SocketPath = "/var/lib/rukie/run/ruk.sock"
	return cfg
}

func TestRenderDaemonConfigPinsUID(t *testing.T) {
	cfg := testConfig()
	text, err := RenderDaemonConfig(cfg, []uint32{993})
	if err != nil {
		t.Fatalf("RenderDaemonConfig: %v", err)
	}
	var doc map[string]any
	if _, err := toml.Decode(text, &doc); err != nil {
		t.Fatalf("rendered daemon TOML does not parse: %v\n%s", err, text)
	}
	if got := doc["socket_path"]; got != "/var/lib/rukie/run/ruk.sock" {
		t.Fatalf("socket_path = %v", got)
	}
	roots, ok := doc["repo_roots"].([]any)
	if !ok || len(roots) != 1 || roots[0] != "/var/lib/rukie/repos" {
		t.Fatalf("repo_roots = %v, want exactly the repo root", doc["repo_roots"])
	}
	uids, ok := doc["allowed_uids"].([]any)
	if !ok || len(uids) != 1 {
		t.Fatalf("allowed_uids = %v, want the fallback uid", doc["allowed_uids"])
	}
}

func TestRenderDaemonConfigKeepsExplicitUIDs(t *testing.T) {
	cfg := testConfig()
	cfg.Ruk.AllowedUIDs = []uint32{1001, 1002}
	text, err := RenderDaemonConfig(cfg, []uint32{993})
	if err != nil {
		t.Fatalf("RenderDaemonConfig: %v", err)
	}
	doc := map[string]any{}
	if _, uerr := toml.Decode(text, &doc); uerr != nil {
		t.Fatalf("rendered daemon TOML does not parse: %v\n%s", uerr, text)
	}
	uids, ok := doc["allowed_uids"].([]any)
	if !ok || len(uids) != 2 || uids[0] != int64(1001) || uids[1] != int64(1002) {
		t.Fatalf("allowed_uids = %v, want [1001 1002]", doc["allowed_uids"])
	}
	admins, ok := doc["admin_uids"].([]any)
	if !ok || len(admins) != 1 || admins[0] != int64(993) {
		t.Fatalf("admin_uids = %v, want the fallback supervisor uid", doc["admin_uids"])
	}
}

func TestWriteDaemonConfigPermissions(t *testing.T) {
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	cfg.Ruk.RepoRoot = filepath.Join(cfg.DataDir, "repos")
	cfg.Ruk.SocketPath = filepath.Join(cfg.DataDir, "run", "ruk.sock")
	path, err := WriteDaemonConfig(cfg)
	if err != nil {
		t.Fatalf("WriteDaemonConfig: %v", err)
	}
	if want := filepath.Join(cfg.DataDir, "run", "ruk.toml"); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("daemon config mode = %o, want 600", info.Mode().Perm())
	}
	runInfo, err := os.Stat(filepath.Join(cfg.DataDir, "run"))
	if err != nil {
		t.Fatalf("stat run: %v", err)
	}
	if runInfo.Mode().Perm() != 0o700 {
		t.Fatalf("run dir mode = %o, want 700", runInfo.Mode().Perm())
	}
}

func TestHermeticEnvStripsCredentials(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"HOME=/root",
		"GIT_CONFIG_GLOBAL=/root/.gitconfig",
		"GIT_CONFIG_NOSYSTEM=0",
		"GIT_AUTH_TOKEN=topsecret",
		"GIT_AUTH_USER=ada",
		"RUKIE_CONFIG=/etc/rukie/config.toml",
	}
	env := HermeticEnv(base)
	has := func(prefix string) (string, bool) {
		for _, kv := range env {
			if kv == prefix || strings.HasPrefix(kv, prefix+"=") {
				return kv, true
			}
		}
		return "", false
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, "GIT_AUTH_") {
			t.Fatalf("hermetic env carries %q", kv)
		}
	}
	if kv, ok := has("GIT_CONFIG_NOSYSTEM"); !ok || kv != "GIT_CONFIG_NOSYSTEM=1" {
		t.Fatalf("GIT_CONFIG_NOSYSTEM = %q, want pinned 1", kv)
	}
	if kv, ok := has("GIT_CONFIG_GLOBAL"); !ok || kv != "GIT_CONFIG_GLOBAL=" {
		t.Fatalf("GIT_CONFIG_GLOBAL = %q, want empty", kv)
	}
	if kv, ok := has("HOME"); !ok || kv != "HOME=" {
		t.Fatalf("HOME = %q, want empty", kv)
	}
	if _, ok := has("PATH=/usr/bin"); !ok {
		t.Fatal("unrelated vars must survive the hermetic filter")
	}
	if _, ok := has("RUKIE_CONFIG"); !ok {
		t.Fatal("non-git vars must survive the hermetic filter")
	}
}

func TestHermeticEnvKeepsNoAuthPrefix(t *testing.T) {
	env := HermeticEnv([]string{"GIT_AUTH_=x", "GIT_AUTHOR_NAME=ada"})
	for _, kv := range env {
		if strings.HasPrefix(kv, "GIT_AUTH_") {
			t.Fatalf("hermetic env carries %q", kv)
		}
	}
	found := false
	for _, kv := range env {
		if kv == "GIT_AUTHOR_NAME=ada" {
			found = true
		}
	}
	if !found {
		t.Fatal("GIT_AUTHOR_NAME is not a credential and must survive")
	}
}
