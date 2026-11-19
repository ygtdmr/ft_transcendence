package logging

import (
	"errors"
	"io"
	"log/slog"
	"time"
)

// Config selects the output format, minimum severity and service name.
// Empty Format and Level values default to text and info.
type Config struct {
	Format  string
	Level   string
	Service string
	// Environment, when set, is added to every record as "environment" so logs
	// from different deployment tiers can be told apart without the log pipeline.
	Environment string
}

// Validate rejects unsupported formats and levels before creating a logger.
func (cfg Config) Validate() error {
	if cfg.Format != "" && cfg.Format != "text" && cfg.Format != "json" {
		return errors.New("LOG_FORMAT must be text or json")
	}
	_, err := cfg.level()
	return err
}

func (cfg Config) level() (slog.Level, error) {
	switch cfg.Level {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, errors.New("LOG_LEVEL must be debug, info, warn or error")
	}
}

// New creates a concurrency-safe logger using only the standard library.
// Both formats use UTC RFC3339Nano timestamps and a fixed service attribute.
//
// Every logger New returns also (1) redacts the values of attributes whose keys
// look like credentials (password, token, cookie, authorization, ...) at any
// depth, and (2) adds request_id to records written with a request context, so
// logger.InfoContext(ctx, ...) correlates with the access log without the
// caller passing the ID by hand.
func New(cfg Config, output io.Writer) (*slog.Logger, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	level, _ := cfg.level()
	options := &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, attr slog.Attr) slog.Attr {
			if len(groups) == 0 && attr.Key == slog.TimeKey {
				return slog.String(slog.TimeKey, attr.Value.Time().UTC().Format(time.RFC3339Nano))
			}
			return attr
		},
	}
	var handler slog.Handler
	if cfg.Format == "json" {
		handler = slog.NewJSONHandler(output, options)
	} else {
		handler = slog.NewTextHandler(output, options)
	}
	logger := slog.New(contextHandler{next: handler}).With("service", cfg.Service)
	if cfg.Environment != "" {
		logger = logger.With("environment", cfg.Environment)
	}
	return logger, nil
}
