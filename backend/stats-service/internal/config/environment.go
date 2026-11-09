// Package config reads the Stats Service's settings from the environment.
package config

import (
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/melmut-42/ft_transcendence/backend/shared/logging"
)

const defaultPort = "8080"

// Config holds validated application settings.
type Config struct {
	Environment    string
	LogFormat      string
	LogLevel       string
	Port           string
	DatabaseURL    string `json:"-"`
	MigrateOnStart bool
}

// Load reads and validates settings from the process environment. It does not
// load .env files.
//
// Errors:
//
//   - DATABASE_URL is missing or is not a PostgreSQL URL.
//   - PORT or MIGRATE_ON_START has an invalid value.
//   - APP_ENV, LOG_FORMAT or LOG_LEVEL has an unsupported value.
func Load() (Config, error) {
	cfg := Config{
		Environment:    strings.TrimSpace(os.Getenv("APP_ENV")),
		LogFormat:      strings.TrimSpace(os.Getenv("LOG_FORMAT")),
		LogLevel:       strings.TrimSpace(os.Getenv("LOG_LEVEL")),
		Port:           strings.TrimSpace(os.Getenv("PORT")),
		DatabaseURL:    strings.TrimSpace(os.Getenv("DATABASE_URL")),
		MigrateOnStart: true,
	}
	if err := cfg.configureLogging(); err != nil {
		return Config{}, err
	}
	if cfg.Port == "" {
		cfg.Port = defaultPort
	}
	if raw := strings.TrimSpace(os.Getenv("MIGRATE_ON_START")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, errors.New("MIGRATE_ON_START must be true or false")
		}
		cfg.MigrateOnStart = value
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (cfg *Config) configureLogging() error {
	if cfg.Environment == "" {
		cfg.Environment = "development"
	}
	format, level := "text", "debug"
	switch cfg.Environment {
	case "development":
	case "production":
		format, level = "json", "info"
	case "test":
		level = "error"
	default:
		return errors.New("APP_ENV must be development, production or test")
	}
	if cfg.LogFormat == "" {
		cfg.LogFormat = format
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = level
	}

	return (logging.Config{Format: cfg.LogFormat, Level: cfg.LogLevel}).Validate()
}

func (cfg Config) validate() error {
	port, err := strconv.Atoi(cfg.Port)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("PORT must be an integer between 1 and 65535")
	}
	if cfg.DatabaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	parsed, err := url.Parse(cfg.DatabaseURL)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Host == "" {
		return errors.New("DATABASE_URL must be a postgres:// or postgresql:// URL")
	}

	return nil
}
