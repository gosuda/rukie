package supervisor

import (
	"fmt"
	"log/slog"

	"github.com/gosuda/rukie/internal/config"
	"github.com/gosuda/rukie/internal/logging"
)

// ReloadLogLevel re-reads the config file and rebuilds the logger with
// the file's level. Format and source stay as passed so SIGHUP reloads
// the log level only; every other key change requires a restart and is
// ignored here. It returns the applied level name.
func ReloadLogLevel(format string, source bool, configPath string) (string, *slog.Logger, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return "", nil, fmt.Errorf("supervisor: reload log level: %w", err)
	}
	logger, lerr := logging.New(cfg.Log.Level, format, source)
	if lerr != nil {
		return "", nil, fmt.Errorf("supervisor: reload log level: %w", lerr)
	}
	return cfg.Log.Level, logger, nil
}
