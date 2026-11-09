package unit_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/config"
)

const validDatabaseURL = "postgres://user:secret@localhost:5432/user?sslmode=disable"

// setEnvironment replaces every setting config.Load reads.
//
// Parameters:
//
//   - t: Go test runner; values are restored when the test ends.
//   - values: Setting names and values; names missing here are set to empty.
func setEnvironment(t *testing.T, values map[string]string) {
	t.Helper()
	for _, name := range []string{"APP_ENV", "LOG_FORMAT", "LOG_LEVEL", "PORT", "INTERNAL_PORT", "DATABASE_URL", "MIGRATE_ON_START",
		"AUTH_SERVICE_CREDENTIAL_FILE", "AUTH_SERVICE_PREVIOUS_CREDENTIAL_FILE", "AVATAR_BASE_URL",
		"AUTH_INTERNAL_URL", "AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE", "AUTH_INTERNAL_TIMEOUT"} {
		t.Setenv(name, values[name])
	}
}

// TestLoadDefaults checks the defaults applied when only DATABASE_URL is supplied.
//
// Parameters:
//
//   - t: Go test runner used for assertions and failure reporting.
//
// Expected output:
//
//   - Development environment, port 8080, internal port 8090, text logs at debug
//     level, migrations on and no service credentials.
func TestLoadDefaults(t *testing.T) {
	setEnvironment(t, map[string]string{"DATABASE_URL": validDatabaseURL})

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Environment != "development" || cfg.Port != "8080" || cfg.InternalPort != "8090" ||
		len(cfg.CallerCredentials) != 0 || cfg.LogFormat != "text" ||
		cfg.LogLevel != "debug" || !cfg.MigrateOnStart || cfg.DatabaseURL != validDatabaseURL {
		t.Fatalf("Load() = %+v, want development defaults", cfg)
	}
}

// TestLoadEnvironmentDefaults checks logging defaults for production and test.
//
// Parameters:
//
//   - t: Go test runner used for subtests and assertions.
//
// Expected output:
//
//   - Production logs JSON at info; test logs text at error.
func TestLoadEnvironmentDefaults(t *testing.T) {
	tests := []struct {
		environment, format, level string
	}{
		{"production", "json", "info"},
		{"test", "text", "error"},
	}
	for _, tt := range tests {
		t.Run(tt.environment, func(t *testing.T) {
			setEnvironment(t, map[string]string{
				"APP_ENV": tt.environment, "DATABASE_URL": validDatabaseURL, "AUTH_INTERNAL_URL": "http://auth-service:8090",
				"AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE": writeSecret(t, strings.Repeat("o", 40)),
			})

			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.LogFormat != tt.format || cfg.LogLevel != tt.level {
				t.Fatalf("logging = %s/%s, want %s/%s", cfg.LogFormat, cfg.LogLevel, tt.format, tt.level)
			}
		})
	}
}

// TestLoadMigrateOnStart checks that MIGRATE_ON_START can switch migrations off.
//
// Parameters:
//
//   - t: Go test runner used for subtests and assertions.
//
// Expected output:
//
//   - "false" disables migrations, "true" keeps them, other values are rejected.
func TestLoadMigrateOnStart(t *testing.T) {
	tests := []struct {
		value   string
		want    bool
		wantErr bool
	}{
		{"false", false, false},
		{"true", true, false},
		{"maybe", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			setEnvironment(t, map[string]string{"DATABASE_URL": validDatabaseURL, "MIGRATE_ON_START": tt.value})

			cfg, err := config.Load()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Load() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && cfg.MigrateOnStart != tt.want {
				t.Fatalf("MigrateOnStart = %v, want %v", cfg.MigrateOnStart, tt.want)
			}
		})
	}
}

// TestLoadRejectsInvalidSettings checks startup validation failures.
//
// Parameters:
//
//   - t: Go test runner used for subtests and assertions.
//
// Expected output:
//
//   - A zero Config and an error naming the offending setting, never the database password.
func TestLoadRejectsInvalidSettings(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]string
		want   string
	}{
		{"missing database url", map[string]string{}, "DATABASE_URL is required"},
		{"not a postgres url", map[string]string{"DATABASE_URL": "mysql://user:secret@localhost/db"}, "postgres://"},
		{"url without host", map[string]string{"DATABASE_URL": "postgres:///db"}, "postgres://"},
		{"port not a number", map[string]string{"DATABASE_URL": validDatabaseURL, "PORT": "http"}, "PORT"},
		{"port too large", map[string]string{"DATABASE_URL": validDatabaseURL, "PORT": "70000"}, "PORT"},
		{"avatar base over http", map[string]string{"DATABASE_URL": validDatabaseURL, "AVATAR_BASE_URL": "http://example.com"}, "AVATAR_BASE_URL"},
		{"avatar base with path", map[string]string{"DATABASE_URL": validDatabaseURL, "AVATAR_BASE_URL": "https://example.com/app"}, "AVATAR_BASE_URL"},
		{"internal port not a number", map[string]string{"DATABASE_URL": validDatabaseURL, "INTERNAL_PORT": "x"}, "INTERNAL_PORT"},
		{"internal port equals port", map[string]string{"DATABASE_URL": validDatabaseURL, "PORT": "9000", "INTERNAL_PORT": "9000"}, "INTERNAL_PORT"},
		{"missing credential file", map[string]string{"DATABASE_URL": validDatabaseURL, "AUTH_SERVICE_CREDENTIAL_FILE": "/nonexistent/secret"}, "AUTH_SERVICE_CREDENTIAL_FILE"},
		{"auth url without credential", map[string]string{"DATABASE_URL": validDatabaseURL, "AUTH_INTERNAL_URL": "http://auth:8090"}, "set together"},
		{"auth credential without url", map[string]string{"DATABASE_URL": validDatabaseURL, "AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE": "/nonexistent/secret"}, "set together"},
		{"auth required in production", map[string]string{"DATABASE_URL": validDatabaseURL, "APP_ENV": "production"}, "AUTH_INTERNAL_URL"},
		{"auth timeout invalid", map[string]string{"DATABASE_URL": validDatabaseURL, "AUTH_INTERNAL_TIMEOUT": "soon"}, "AUTH_INTERNAL_TIMEOUT"},
		{"unknown environment", map[string]string{"DATABASE_URL": validDatabaseURL, "APP_ENV": "staging"}, "APP_ENV"},
		{"unknown log format", map[string]string{"DATABASE_URL": validDatabaseURL, "LOG_FORMAT": "xml"}, "LOG_FORMAT"},
		{"unknown log level", map[string]string{"DATABASE_URL": validDatabaseURL, "LOG_LEVEL": "loud"}, "LOG_LEVEL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnvironment(t, tt.values)

			cfg, err := config.Load()
			if err == nil {
				t.Fatalf("Load() = %+v, want error", cfg)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want it to mention %q", err, tt.want)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("error %q leaks the database password", err)
			}
		})
	}
}

// TestLoadAuthService checks the settings for calling Auth's private listener.
//
// Expected output:
//
//   - The URL, credential and timeout are loaded; the timeout defaults to 5s.
//   - A credential shared with an inbound caller is refused.
//   - A URL with a path is refused.
func TestLoadAuthService(t *testing.T) {
	outbound := writeSecret(t, strings.Repeat("o", 40))
	setEnvironment(t, map[string]string{"DATABASE_URL": validDatabaseURL, "AUTH_INTERNAL_URL": "http://auth-service:8090/",
		"AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE": outbound})
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthInternalURL != "http://auth-service:8090" || cfg.AuthServiceCredential != strings.Repeat("o", 40) || cfg.AuthInternalTimeout != 5*time.Second {
		t.Fatalf("auth settings = %q %v", cfg.AuthInternalURL, cfg.AuthInternalTimeout)
	}

	setEnvironment(t, map[string]string{"DATABASE_URL": validDatabaseURL, "AUTH_INTERNAL_URL": "http://auth-service:8090",
		"AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE": outbound, "AUTH_SERVICE_CREDENTIAL_FILE": outbound})
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "must differ") {
		t.Fatalf("shared credential = %v", err)
	}

	setEnvironment(t, map[string]string{"DATABASE_URL": validDatabaseURL, "AUTH_INTERNAL_URL": "http://auth-service:8090/x",
		"AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE": outbound})
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "AUTH_INTERNAL_URL") {
		t.Fatalf("url with path = %v", err)
	}
}

// writeSecret stores value in a temporary file and returns its path.
func writeSecret(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// TestLoadCallerCredentials checks reading service credentials from secret files.
//
// Expected output:
//
//   - Current and previous credentials are trimmed and both accepted.
//   - A credential shorter than 32 characters is rejected without echoing it or its path.
func TestLoadCallerCredentials(t *testing.T) {
	current := strings.Repeat("c", 40)
	previous := strings.Repeat("p", 40)
	setEnvironment(t, map[string]string{
		"DATABASE_URL":                          validDatabaseURL,
		"AUTH_SERVICE_CREDENTIAL_FILE":          writeSecret(t, current+"\n"),
		"AUTH_SERVICE_PREVIOUS_CREDENTIAL_FILE": writeSecret(t, previous),
	})
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	got := cfg.CallerCredentials["auth-service"]
	if len(got) != 2 || !slices.Contains(got, current) || !slices.Contains(got, previous) {
		t.Fatalf("auth-service credentials = %d values, want current and previous", len(got))
	}

	short := writeSecret(t, "too-short-secret")
	setEnvironment(t, map[string]string{"DATABASE_URL": validDatabaseURL, "AUTH_SERVICE_CREDENTIAL_FILE": short})
	_, err = config.Load()
	if err == nil || !strings.Contains(err.Error(), "AUTH_SERVICE_CREDENTIAL_FILE") {
		t.Fatalf("error = %v, want a short credential rejected", err)
	}
	if strings.Contains(err.Error(), "too-short-secret") || strings.Contains(err.Error(), short) {
		t.Fatalf("error %q leaks the credential or its path", err)
	}
}

// TestLoadAvatarBaseURL checks that a trailing slash is removed from AVATAR_BASE_URL.
func TestLoadAvatarBaseURL(t *testing.T) {
	setEnvironment(t, map[string]string{"DATABASE_URL": validDatabaseURL, "AVATAR_BASE_URL": "https://example.com/"})
	cfg, err := config.Load()
	if err != nil || cfg.AvatarBaseURL != "https://example.com" {
		t.Fatalf("AvatarBaseURL = %q, %v", cfg.AvatarBaseURL, err)
	}
}
