// Package config reads the Stats Service's settings from the environment.
package config

import (
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/logging"
)

const (
	defaultPort         = "8080"
	defaultInternalPort = "8090"
)

// Config holds validated application settings.
type Config struct {
	Environment    string
	LogFormat      string
	LogLevel       string
	Port           string
	InternalPort   string
	DatabaseURL    string `json:"-"`
	MigrateOnStart bool
	// CallerCredentials maps each private caller to its accepted credentials.
	CallerCredentials map[string][]string `json:"-"`
	// AuthInternalURL, AuthServiceCredential and AuthInternalTimeout reach
	// Auth's private listener to validate browser sessions.
	AuthInternalURL       string
	AuthServiceCredential string `json:"-"`
	AuthInternalTimeout   time.Duration
}

// Load reads and validates settings from the process environment. It does not
// load .env files.
//
// Errors:
//
//   - DATABASE_URL is missing or is not a PostgreSQL URL.
//   - PORT, INTERNAL_PORT or MIGRATE_ON_START has an invalid value.
//   - A credential file is unreadable or too short, or Auth settings are incomplete.
//   - APP_ENV, LOG_FORMAT or LOG_LEVEL has an unsupported value.
func Load() (Config, error) {
	cfg := Config{
		Environment:    strings.TrimSpace(os.Getenv("APP_ENV")),
		LogFormat:      strings.TrimSpace(os.Getenv("LOG_FORMAT")),
		LogLevel:       strings.TrimSpace(os.Getenv("LOG_LEVEL")),
		Port:           strings.TrimSpace(os.Getenv("PORT")),
		InternalPort:   strings.TrimSpace(os.Getenv("INTERNAL_PORT")),
		DatabaseURL:    strings.TrimSpace(os.Getenv("DATABASE_URL")),
		MigrateOnStart: true,
	}
	if err := cfg.configureLogging(); err != nil {
		return Config{}, err
	}
	if cfg.Port == "" {
		cfg.Port = defaultPort
	}
	if cfg.InternalPort == "" {
		cfg.InternalPort = defaultInternalPort
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
	credentials, err := loadCallerCredentials()
	if err != nil {
		return Config{}, err
	}
	cfg.CallerCredentials = credentials
	if err := cfg.loadAuthService(); err != nil {
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
	if !validPort(cfg.Port) {
		return errors.New("PORT must be an integer between 1 and 65535")
	}
	if !validPort(cfg.InternalPort) {
		return errors.New("INTERNAL_PORT must be an integer between 1 and 65535")
	}
	if cfg.Port == cfg.InternalPort {
		return errors.New("INTERNAL_PORT must differ from PORT")
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

func validPort(raw string) bool {
	port, err := strconv.Atoi(raw)

	return err == nil && port >= 1 && port <= 65535
}
