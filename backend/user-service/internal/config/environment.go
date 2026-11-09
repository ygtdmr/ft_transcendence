package config

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/logging"
)

const (
	defaultPort         = "8080"
	defaultInternalPort = "8090"
)

type Config struct {
	Environment  string
	LogFormat    string
	LogLevel     string
	Port         string
	InternalPort string
	DatabaseURL  string `json:"-"`
	// AvatarBaseURL is the public HTTPS origin prefixed to avatar paths, or
	// empty for root-relative avatar URLs.
	AvatarBaseURL string
	// AvatarStorageDir is the absolute directory holding uploaded avatars.
	// Required in production; when empty, avatar changes are not served.
	AvatarStorageDir string
	// CallerCredentials maps each private caller to its accepted service
	// credentials, read from mounted secret files.
	CallerCredentials map[string][]string `json:"-"`
	// AuthInternalURL, AuthServiceCredential and AuthInternalTimeout reach
	// Auth's private listener to validate browser sessions.
	AuthInternalURL       string
	AuthServiceCredential string `json:"-"`
	AuthInternalTimeout   time.Duration
	// ReportWindow is how long a reporter must wait before reporting the same
	// user again.
	ReportWindow   time.Duration
	MigrateOnStart bool
}

// Load reads and validates application settings from the process environment.
//
// It takes no parameters and does not load .env files. Missing optional settings
// use the default port, migration behavior and environment-specific logging settings.
// Private routes are served on INTERNAL_PORT. Service credentials are read from
// the files named by each caller's credential variables; a caller without a
// configured file is accepted by no private route.
//
// It returns the validated Config, or a zero Config on failure.
//
// Errors:
//
//   - Missing settings: DATABASE_URL was not supplied.
//   - Malformed values: PORT, DATABASE_URL, AVATAR_BASE_URL, AVATAR_STORAGE_DIR or MIGRATE_ON_START could not be parsed.
//   - Logging settings: APP_ENV, LOG_FORMAT or LOG_LEVEL has an unsupported value.
//   - Invalid bounds: PORT or INTERNAL_PORT is outside the valid TCP range, or both are equal.
//   - Credentials: a configured credential file is unreadable, too short, or
//     holds a credential already assigned to another caller.
//   - Auth settings: see loadAuthService.
func Load() (Config, error) {
	cfg := Config{
		Environment:      strings.TrimSpace(os.Getenv("APP_ENV")),
		LogFormat:        strings.TrimSpace(os.Getenv("LOG_FORMAT")),
		LogLevel:         strings.TrimSpace(os.Getenv("LOG_LEVEL")),
		Port:             strings.TrimSpace(os.Getenv("PORT")),
		InternalPort:     strings.TrimSpace(os.Getenv("INTERNAL_PORT")),
		AvatarBaseURL:    strings.TrimRight(strings.TrimSpace(os.Getenv("AVATAR_BASE_URL")), "/"),
		DatabaseURL:      strings.TrimSpace(os.Getenv("DATABASE_URL")),
		MigrateOnStart:   true,
		AvatarStorageDir: strings.TrimSpace(os.Getenv("AVATAR_STORAGE_DIR")),
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
	if err := cfg.loadReportWindow(); err != nil {
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

// configureLogging applies environment defaults and validates logging settings.
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
	if err := (logging.Config{Format: cfg.LogFormat, Level: cfg.LogLevel}).Validate(); err != nil {
		return err
	}

	return nil
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

	if cfg.AvatarBaseURL != "" {
		base, err := url.Parse(cfg.AvatarBaseURL)
		if err != nil || base.Scheme != "https" || base.Host == "" || base.Path != "" || base.RawQuery != "" || base.Fragment != "" {
			return errors.New("AVATAR_BASE_URL must be an https:// origin without a path")
		}
	}

	if cfg.AvatarStorageDir == "" && cfg.Environment == "production" {
		return errors.New("AVATAR_STORAGE_DIR is required in production")
	}
	if cfg.AvatarStorageDir != "" && !filepath.IsAbs(cfg.AvatarStorageDir) {
		return errors.New("AVATAR_STORAGE_DIR must be an absolute path")
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
