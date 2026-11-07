package config

import (
	"errors"
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

type Config struct {
	Environment     string
	LogFormat       string
	LogLevel        string
	Port            string
	InternalPort    string
	DatabaseURL     string
	JWTSecret       string `json:"-"`
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	// CallerCredentials maps each private caller to its accepted service
	// credentials, read from mounted secret files.
	CallerCredentials map[string][]string `json:"-"`
	// Google sign-in client settings; empty when Google sign-in is disabled.
	GoogleClientID     string
	GoogleClientSecret string `json:"-"`
	PublicBaseURL      string
}

// Load reads and validates application settings from the process environment.
//
// It takes no parameters and does not load .env files. Missing optional settings
// use the default ports, token lifetimes and environment-specific logging settings.
// Private routes are served on INTERNAL_PORT; each caller's service credential
// is read from the file its credential variable names.
//
// It returns the validated Config, or a zero Config on failure.
//
// Errors:
//
//   - Missing settings: DATABASE_URL or a valid JWT_SECRET was not supplied.
//   - Malformed values: PORT or token lifetime settings could not be parsed.
//   - Logging settings: APP_ENV, LOG_FORMAT or LOG_LEVEL has an unsupported value.
//   - Invalid bounds: Ports, signing key length or token lifetimes violate configuration constraints.
//   - Credentials: a credential file is unreadable, too short or shared by two callers.
func Load() (Config, error) {
	cfg := Config{
		Environment:  strings.TrimSpace(os.Getenv("APP_ENV")),
		LogFormat:    strings.TrimSpace(os.Getenv("LOG_FORMAT")),
		LogLevel:     strings.TrimSpace(os.Getenv("LOG_LEVEL")),
		Port:         strings.TrimSpace(os.Getenv("PORT")),
		InternalPort: strings.TrimSpace(os.Getenv("INTERNAL_PORT")),
		DatabaseURL:  strings.TrimSpace(os.Getenv("DATABASE_URL")),
		JWTSecret:    strings.TrimSpace(os.Getenv("JWT_SECRET")),
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

	cfg, err := cfg.withTokenLifetimes()
	if err != nil {
		return Config{}, err
	}
	credentials, err := loadCallerCredentials()
	if err != nil {
		return Config{}, err
	}
	cfg.CallerCredentials = credentials
	if err := cfg.loadGoogleOAuth(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// withTokenLifetimes loads token lifetimes and validates the completed settings.
func (cfg Config) withTokenLifetimes() (Config, error) {
	var err error
	cfg.AccessTokenTTL, err = duration("ACCESS_TOKEN_TTL", 30*time.Minute)
	if err != nil {
		return Config{}, err
	}

	cfg.RefreshTokenTTL, err = duration("REFRESH_TOKEN_TTL", 30*24*time.Hour)
	if err != nil {
		return Config{}, err
	}

	if err := cfg.validate(); err != nil {
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

	if cfg.DatabaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}

	if len(cfg.JWTSecret) < 32 {
		return errors.New("JWT_SECRET must contain at least 32 bytes")
	}

	if cfg.AccessTokenTTL < time.Second || cfg.RefreshTokenTTL <= cfg.AccessTokenTTL {
		return errors.New("token TTLs must satisfy 1s <= ACCESS_TOKEN_TTL < REFRESH_TOKEN_TTL")
	}

	return nil
}

func validPort(raw string) bool {
	port, err := strconv.Atoi(raw)

	return err == nil && port >= 1 && port <= 65535
}

func duration(name string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}

	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, errors.New(name + " must be a valid Go duration")
	}

	return value, nil
}
