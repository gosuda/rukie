package supervisor

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/gosuda/rukie/internal/config"
)

type renderedDaemonFile struct {
	SocketPath          string   `toml:"socket_path"`
	RepoRoots           []string `toml:"repo_roots"`
	AllowedUIDs         []uint32 `toml:"allowed_uids"`
	AdminUIDs           []uint32 `toml:"admin_uids"`
	MaxConnections      uint     `toml:"max_connections"`
	BlockingPoolSize    uint     `toml:"blocking_pool_size"`
	HandshakeTimeoutMs  uint64   `toml:"handshake_timeout_ms"`
	FrameTimeoutMs      uint64   `toml:"frame_timeout_ms"`
	StreamWindowBytes   uint64   `toml:"stream_window_bytes"`
	RetentionIntervalMs uint64   `toml:"retention_interval_ms"`
}

// RenderDaemonConfig generates the daemon TOML from the rukie config.
// An empty [ruk] allowed_uids falls back to fallbackUIDs so the child
// always starts with the rukie uid pinned.
func RenderDaemonConfig(cfg config.Config, fallbackUIDs []uint32) (string, error) {
	allowed := cfg.Ruk.AllowedUIDs
	if len(allowed) == 0 {
		allowed = fallbackUIDs
	}
	doc := renderedDaemonFile{
		SocketPath:          cfg.Ruk.SocketPath,
		RepoRoots:           []string{cfg.Ruk.RepoRoot},
		AllowedUIDs:         allowed,
		AdminUIDs:           fallbackUIDs,
		MaxConnections:      cfg.Ruk.MaxConnections,
		BlockingPoolSize:    cfg.Ruk.BlockingPoolSize,
		HandshakeTimeoutMs:  cfg.Ruk.HandshakeTimeoutMs,
		FrameTimeoutMs:      cfg.Ruk.FrameTimeoutMs,
		StreamWindowBytes:   cfg.Ruk.StreamWindowBytes,
		RetentionIntervalMs: cfg.Ruk.RetentionIntervalMs,
	}
	var b strings.Builder
	if err := toml.NewEncoder(&b).Encode(doc); err != nil {
		return "", fmt.Errorf("supervisor: render daemon config: %w", err)
	}
	return b.String(), nil
}

// DaemonConfigPath returns the rendered file path under the data dir.
func DaemonConfigPath(cfg config.Config) string {
	return filepath.Join(cfg.DataDir, "run", "ruk.toml")
}

// WriteDaemonConfig renders the daemon TOML to <data_dir>/run/ruk.toml
// at mode 0600, creating run/ at 0700. It returns the written path.
func WriteDaemonConfig(cfg config.Config) (string, error) {
	runDir := filepath.Join(cfg.DataDir, "run")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return "", fmt.Errorf("supervisor: create run dir: %w", err)
	}
	fallback, err := fallbackUIDForAllowedUIDs()
	if err != nil {
		return "", err
	}
	text, err := RenderDaemonConfig(cfg, []uint32{fallback})
	if err != nil {
		return "", err
	}
	path := filepath.Join(runDir, "ruk.toml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", fmt.Errorf("supervisor: write daemon config: %w", err)
	}
	return path, nil
}

func fallbackUIDForAllowedUIDs() (uint32, error) {
	uid := os.Getuid()
	if uid < 0 || uid > math.MaxUint32 {
		return 0, fmt.Errorf("supervisor: uid %d is outside the uint32 range", uid)
	}
	return uint32(uid), nil
}

// HermeticEnv builds the child's fixed environment from base: pinned
// git isolation plus the caller's remaining variables, with every
// GIT_AUTH_* credential stripped. No credential ever reaches the daemon
// through argv, env, digests, receipts, or logs.
func HermeticEnv(base []string) []string {
	out := make([]string, 0, len(base)+3)
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_GLOBAL", "HOME":
			continue
		}
		if strings.HasPrefix(name, "GIT_AUTH_") {
			continue
		}
		out = append(out, kv)
	}
	out = append(out,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=",
		"HOME=",
	)
	return out
}
