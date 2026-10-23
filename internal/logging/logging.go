package logging

import (
	"fmt"
	"log/slog"
	"os"
)

func ParseLevel(level string) (slog.Level, error) {
	switch level {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("unknown log level %q", level)
	}
}

func New(level, format string, source bool) (*slog.Logger, error) {
	lvl, err := ParseLevel(level)
	if err != nil {
		return nil, err
	}
	shared := &slog.LevelVar{}
	shared.Set(lvl)
	return NewWithVar(shared, format, source), nil
}

func NewWithVar(lvl *slog.LevelVar, format string, source bool) *slog.Logger {
	opts := &slog.HandlerOptions{Level: lvl, AddSource: source}
	switch format {
	case "text":
		return slog.New(slog.NewTextHandler(os.Stderr, opts))
	default:
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
}
