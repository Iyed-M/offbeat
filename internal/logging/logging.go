package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

type Level slog.Level

const (
	LevelDebug Level = Level(slog.LevelDebug)
	LevelInfo  Level = Level(slog.LevelInfo)
	LevelWarn  Level = Level(slog.LevelWarn)
	LevelError Level = Level(slog.LevelError)
)

func ParseLevel(s string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return LevelDebug, nil
	case "", "info":
		return LevelInfo, nil
	case "warn", "warning":
		return LevelWarn, nil
	case "error":
		return LevelError, nil
	default:
		return LevelInfo, &levelError{s: s}
	}
}

type levelError struct{ s string }

func (e *levelError) Error() string { return "unknown log level: " + e.s }

type Format int

const (
	FormatText Format = iota
	FormatJSON
)

func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "text":
		return FormatText, nil
	case "json":
		return FormatJSON, nil
	default:
		return FormatText, &formatError{s: s}
	}
}

type formatError struct{ s string }

func (e *formatError) Error() string { return "unknown log format: " + e.s }

type Config struct {
	Level  Level
	Format Format
	Writer io.Writer
}

func New(cfg Config) *slog.Logger {
	if cfg.Writer == nil {
		cfg.Writer = os.Stderr
	}
	opts := &slog.HandlerOptions{Level: slog.Level(cfg.Level)}
	var h slog.Handler
	switch cfg.Format {
	case FormatJSON:
		h = slog.NewJSONHandler(cfg.Writer, opts)
	default:
		h = slog.NewTextHandler(cfg.Writer, opts)
	}
	return slog.New(h)
}
