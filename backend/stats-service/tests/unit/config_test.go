package unit_test

import (
	"strings"
	"testing"

	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/config"
)

const validDatabaseURL = "postgres://stats:secret@localhost:5432/stats?sslmode=disable"

func setEnvironment(t *testing.T, values map[string]string) {
	t.Helper()
	for _, name := range []string{"APP_ENV", "LOG_FORMAT", "LOG_LEVEL", "PORT", "DATABASE_URL", "MIGRATE_ON_START", "INTERNAL_PORT",
		"USER_SERVICE_CREDENTIAL_FILE", "USER_SERVICE_PREVIOUS_CREDENTIAL_FILE", "AUTH_INTERNAL_URL",
		"AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE", "AUTH_INTERNAL_TIMEOUT"} {
		t.Setenv(name, values[name])
	}
}

// TestLoadDefaults checks the values used when only DATABASE_URL is set.
//
// Expected output: development defaults on port 8080 with migrations on.
func TestLoadDefaults(t *testing.T) {
	setEnvironment(t, map[string]string{"DATABASE_URL": validDatabaseURL})
	cfg, err := config.Load()
	if err != nil || cfg.Environment != "development" || cfg.Port != "8080" || cfg.LogFormat != "text" || !cfg.MigrateOnStart {
		t.Fatalf("Load = %+v, %v", cfg, err)
	}
}

// TestLoadRejectsInvalidSettings checks every refused setting.
//
// Expected output: each error names its setting and never contains the URL.
func TestLoadRejectsInvalidSettings(t *testing.T) {
	for _, tt := range []struct {
		name   string
		values map[string]string
		want   string
	}{
		{"missing database", map[string]string{}, "DATABASE_URL"},
		{"wrong scheme", map[string]string{"DATABASE_URL": "mysql://stats:secret@host/stats"}, "DATABASE_URL"},
		{"bad port", map[string]string{"DATABASE_URL": validDatabaseURL, "PORT": "70000"}, "PORT"},
		{"bad environment", map[string]string{"DATABASE_URL": validDatabaseURL, "APP_ENV": "staging"}, "APP_ENV"},
		{"production without auth", map[string]string{"DATABASE_URL": validDatabaseURL, "APP_ENV": "production"}, "AUTH_INTERNAL_URL"},
		{"same internal port", map[string]string{"DATABASE_URL": validDatabaseURL, "PORT": "8090"}, "INTERNAL_PORT"},
		{"auth url alone", map[string]string{"DATABASE_URL": validDatabaseURL, "AUTH_INTERNAL_URL": "http://auth:8090"}, "set together"},
		{"bad migrate flag", map[string]string{"DATABASE_URL": validDatabaseURL, "MIGRATE_ON_START": "maybe"}, "MIGRATE_ON_START"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setEnvironment(t, tt.values)
			_, err := config.Load()
			if err == nil || !strings.Contains(err.Error(), tt.want) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("Load error = %v, want it to mention %s", err, tt.want)
			}
		})
	}
}
