package unit_test

import (
	"strings"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/config"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/testutil"
)

// TestAuthConfig checks environment defaults, overrides and invalid settings.
//
// Parameters:
//
//   - t: Go test runner used for subtests, assertions and failure reporting.
//
// Inputs:
//
//   - Controlled environment variables with a test database URL and signing key; no developer
//     .env is loaded.
//   - development, production and test modes; explicit text/warn overrides; malformed or
//     missing required settings.
//
// Flow:
//
//  1. Set the baseline environment and call config.Load.
//  2. Replace one setting per invalid case and require an error.
//  3. Load each environment mode, then reload with explicit logging overrides.
//
// Expected output:
//
//   - Baseline: port 8080, development, text/debug, access TTL=30 minutes and refresh TTL=30
//     days.
//   - Logging defaults: production=json/info; test=text/error; explicit text/warn takes
//     precedence.
//   - Invalid environment, logging, key, port, database URL or TTL settings return errors.
func TestAuthConfig(t *testing.T) {
	defaults := map[string]string{
		"APP_ENV": "", "LOG_FORMAT": "", "LOG_LEVEL": "",
		"PORT": "", "DATABASE_URL": "postgres://test@localhost/test", "JWT_SECRET": testutil.TestSigningKey,
		"ACCESS_TOKEN_TTL": "", "REFRESH_TOKEN_TTL": "", "INTERNAL_PORT": "",
		"WEB_BFF_CREDENTIAL_FILE": "", "WEB_BFF_PREVIOUS_CREDENTIAL_FILE": "",
		"GAME_GATEWAY_CREDENTIAL_FILE": "", "GAME_GATEWAY_PREVIOUS_CREDENTIAL_FILE": "",
		"NOTIFICATION_GATEWAY_CREDENTIAL_FILE": "", "NOTIFICATION_GATEWAY_PREVIOUS_CREDENTIAL_FILE": "",
		"USER_SERVICE_CREDENTIAL_FILE": "", "USER_SERVICE_PREVIOUS_CREDENTIAL_FILE": "",
		"USER_INTERNAL_URL": "", "USER_SERVICE_OUTBOUND_CREDENTIAL_FILE": "", "USER_INTERNAL_TIMEOUT": "",
		"GAME_INTERNAL_URL": "", "GAME_SERVICE_OUTBOUND_CREDENTIAL_FILE": "", "GAME_INTERNAL_TIMEOUT": "",
		"GAME_GATEWAY_INTERNAL_URLS": "", "GAME_GATEWAY_OUTBOUND_CREDENTIAL_FILE": "",
		"NOTIFICATION_GATEWAY_INTERNAL_URLS": "", "NOTIFICATION_GATEWAY_OUTBOUND_CREDENTIAL_FILE": "", "GATEWAY_INTERNAL_TIMEOUT": "",
		"LOGIN_CLIENT_RATE_LIMIT": "", "LOGIN_EMAIL_RATE_LIMIT": "", "REGISTER_CLIENT_RATE_LIMIT": "", "TRUSTED_PROXIES": "",
	}

	for key, value := range defaults {
		t.Setenv(key, value)
	}

	checkConfigDefaults(t)

	for _, tt := range []struct{ key, value string }{
		{"APP_ENV", "unknown"}, {"LOG_FORMAT", "xml"}, {"LOG_LEVEL", "trace"},
		{"JWT_SECRET", ""}, {"JWT_SECRET", "short"}, {"PORT", "0"}, {"DATABASE_URL", ""},
		{"ACCESS_TOKEN_TTL", "bad"}, {"ACCESS_TOKEN_TTL", "0s"},
		{"REFRESH_TOKEN_TTL", "30days"}, {"REFRESH_TOKEN_TTL", "5m"},
		{"INTERNAL_PORT", "x"}, {"INTERNAL_PORT", "8080"},
		{"USER_SERVICE_CREDENTIAL_FILE", "/nonexistent/credential"},
		{"USER_INTERNAL_URL", "http://user-service:8090"}, {"USER_SERVICE_OUTBOUND_CREDENTIAL_FILE", "/nonexistent/credential"},
		{"USER_INTERNAL_TIMEOUT", "-1s"},
		{"GAME_INTERNAL_URL", "http://game-session:8093"}, {"GAME_SERVICE_OUTBOUND_CREDENTIAL_FILE", "/nonexistent/credential"},
		{"GAME_INTERNAL_TIMEOUT", "0s"},
		{"GAME_GATEWAY_INTERNAL_URLS", "http://game-gateway:8091"}, {"GATEWAY_INTERNAL_TIMEOUT", "0s"},
		{"LOGIN_CLIENT_RATE_LIMIT", "10"}, {"LOGIN_EMAIL_RATE_LIMIT", "0/1m"}, {"REGISTER_CLIENT_RATE_LIMIT", "5/0s"},
		{"LOGIN_CLIENT_RATE_LIMIT", "x/1m"}, {"REGISTER_CLIENT_RATE_LIMIT", "5/ten"}, {"TRUSTED_PROXIES", "10.0.0.0/8,proxy"},
	} {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
			t.Setenv(tt.key, tt.value)
			if _, err := config.Load(); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	checkLoggingEnvironments(t)
}

func checkLoggingEnvironments(t *testing.T) {
	t.Helper()
	for _, tt := range []struct{ env, format, level string }{
		{"development", "text", "debug"}, {"production", "json", "info"}, {"test", "text", "error"},
	} {
		t.Run(tt.env, func(t *testing.T) {
			t.Setenv("APP_ENV", tt.env)
			if tt.env == "production" {
				// Production requires User Service, Game Session and Gateway settings.
				t.Setenv("USER_INTERNAL_URL", "http://user-service:8090")
				t.Setenv("USER_SERVICE_OUTBOUND_CREDENTIAL_FILE", writeSecret(t, strings.Repeat("o", 40)))
				t.Setenv("GAME_INTERNAL_URL", "http://game-session:8093")
				t.Setenv("GAME_SERVICE_OUTBOUND_CREDENTIAL_FILE", writeSecret(t, strings.Repeat("g", 40)))
				t.Setenv("GAME_GATEWAY_INTERNAL_URLS", "http://game-gateway:8091")
				t.Setenv("GAME_GATEWAY_OUTBOUND_CREDENTIAL_FILE", writeSecret(t, strings.Repeat("w", 40)))
				t.Setenv("NOTIFICATION_GATEWAY_INTERNAL_URLS", "http://notification-gateway:8092")
				t.Setenv("NOTIFICATION_GATEWAY_OUTBOUND_CREDENTIAL_FILE", writeSecret(t, strings.Repeat("n", 40)))
			}
			cfg, err := config.Load()
			if err != nil || cfg.LogFormat != tt.format || cfg.LogLevel != tt.level {
				t.Fatalf("environment defaults: %+v, %v", cfg, err)
			}
			t.Setenv("LOG_FORMAT", "text")
			t.Setenv("LOG_LEVEL", "warn")
			cfg, err = config.Load()
			if err != nil || cfg.LogFormat != "text" || cfg.LogLevel != "warn" {
				t.Fatalf("explicit overrides: %+v, %v", cfg, err)
			}
		})
	}
}

func checkConfigDefaults(t *testing.T) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil || cfg.Port != "8080" || cfg.AccessTokenTTL != 30*time.Minute || cfg.RefreshTokenTTL != 30*24*time.Hour ||
		cfg.Environment != "development" || cfg.LogFormat != "text" || cfg.LogLevel != "debug" {
		t.Fatalf("defaults: %v", err)
	}
}

// TestRateLimitConfig checks the login and registration limit defaults and overrides.
//
// Expected output:
//
//   - Defaults: login 10/1m per client and 5/1m per email, registration 5/10m
//     per client, no trusted proxy.
//   - "<count>/<duration>" overrides and a comma-separated TRUSTED_PROXIES list are read.
func TestRateLimitConfig(t *testing.T) {
	for key, value := range map[string]string{
		"APP_ENV": "development", "DATABASE_URL": "postgres://test@localhost/test", "JWT_SECRET": testutil.TestSigningKey,
		"LOGIN_CLIENT_RATE_LIMIT": "", "LOGIN_EMAIL_RATE_LIMIT": "", "REGISTER_CLIENT_RATE_LIMIT": "", "TRUSTED_PROXIES": "",
	} {
		t.Setenv(key, value)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LoginClientRateLimit != (config.RateLimit{Count: 10, Window: time.Minute}) ||
		cfg.LoginEmailRateLimit != (config.RateLimit{Count: 5, Window: time.Minute}) ||
		cfg.RegisterClientRateLimit != (config.RateLimit{Count: 5, Window: 10 * time.Minute}) || cfg.TrustedProxies != nil {
		t.Fatalf("defaults = %+v %+v %+v %v", cfg.LoginClientRateLimit, cfg.LoginEmailRateLimit, cfg.RegisterClientRateLimit, cfg.TrustedProxies)
	}

	t.Setenv("LOGIN_CLIENT_RATE_LIMIT", " 20 / 30s ")
	t.Setenv("TRUSTED_PROXIES", "10.0.0.0/8, 172.18.0.5")
	cfg, err = config.Load()
	if err != nil || cfg.LoginClientRateLimit != (config.RateLimit{Count: 20, Window: 30 * time.Second}) ||
		strings.Join(cfg.TrustedProxies, ",") != "10.0.0.0/8,172.18.0.5" {
		t.Fatalf("overrides = %+v %v, %v", cfg.LoginClientRateLimit, cfg.TrustedProxies, err)
	}
}
