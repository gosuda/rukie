package supervisor

import (
	"fmt"
	"log/slog"

	"github.com/gosuda/rukie/internal/config"
	"github.com/gosuda/rukie/internal/logging"
)

// ReloadLogLevel re-reads the config file and applies the file's level to
// the shared level var. Every logger built from that var — the serve
// logger, the store logger, and the request auditor — honors the new
// level at once; nothing keeps a stale startup logger. Format, source,
// and every other key change still require a restart and are ignored
// here. It returns the applied level name.
func ReloadLogLevel(lv *slog.LevelVar, configPath string) (string, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return "", fmt.Errorf("supervisor: reload log level: %w", err)
	}
	lvl, lerr := logging.ParseLevel(cfg.Log.Level)
	if lerr != nil {
		return "", fmt.Errorf("supervisor: reload log level: %w", lerr)
	}
	lv.Set(lvl)
	return cfg.Log.Level, nil
}
