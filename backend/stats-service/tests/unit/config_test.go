package unit_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/config"
)

const validDatabaseURL = "postgres://stats:secret@localhost:5432/stats?sslmode=disable"

func setEnvironment(t *testing.T, values map[string]string) {
	t.Helper()
	for _, name := range []string{"APP_ENV", "LOG_FORMAT", "LOG_LEVEL", "PORT", "DATABASE_URL", "MIGRATE_ON_START", "INTERNAL_PORT", "INTERNAL_HOST",
		"USER_SERVICE_CREDENTIAL_FILE", "USER_SERVICE_PREVIOUS_CREDENTIAL_FILE", "AUTH_INTERNAL_URL",
		"AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE", "AUTH_INTERNAL_TIMEOUT", "RABBITMQ_URL", "STATS_EVENTS_QUEUE"} {
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

// TestLoadBroker checks the event consumer settings.
//
// Expected output: unset is allowed outside production and refused in
// production; a full pair is kept; a partial pair, a non-AMQP URL and a queue
// name with whitespace are refused without echoing the URL.
func TestLoadBroker(t *testing.T) {
	const brokerURL = "amqp://stats:secret@rabbitmq:5672/"
	setEnvironment(t, map[string]string{"DATABASE_URL": validDatabaseURL})
	if cfg, err := config.Load(); err != nil || cfg.BrokerURL != "" {
		t.Fatalf("unset = %q, %v", cfg.EventsQueue, err)
	}
	setEnvironment(t, map[string]string{"DATABASE_URL": validDatabaseURL, "RABBITMQ_URL": brokerURL, "STATS_EVENTS_QUEUE": "stats.game-events"})
	if cfg, err := config.Load(); err != nil || cfg.BrokerURL != brokerURL || cfg.EventsQueue != "stats.game-events" {
		t.Fatalf("full = %q, %v", cfg.EventsQueue, err)
	}
	auth := filepath.Join(t.TempDir(), "auth-credential")
	if err := os.WriteFile(auth, []byte(strings.Repeat("a", 40)), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnvironment(t, map[string]string{"DATABASE_URL": validDatabaseURL, "APP_ENV": "production",
		"AUTH_INTERNAL_URL": "http://auth-service:8090", "AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE": auth})
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "RABBITMQ_URL") {
		t.Fatalf("production without broker = %v", err)
	}
	for name, values := range map[string]map[string]string{
		"queue only":       {"STATS_EVENTS_QUEUE": "stats.game-events"},
		"url only":         {"RABBITMQ_URL": brokerURL},
		"http url":         {"RABBITMQ_URL": "http://stats:secret@rabbitmq/", "STATS_EVENTS_QUEUE": "q"},
		"queue with space": {"RABBITMQ_URL": brokerURL, "STATS_EVENTS_QUEUE": "stats events"},
	} {
		values["DATABASE_URL"] = validDatabaseURL
		setEnvironment(t, values)
		if _, err := config.Load(); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("%s error = %v", name, err)
		}
	}
}

// TestLoadInternalHost checks the private listener's bind address.
//
// Expected output:
//
//   - Unset INTERNAL_HOST listens on every interface (":8090").
//   - A host name or an IP address is joined with INTERNAL_PORT.
//   - A value with a port or an invalid host is refused, naming INTERNAL_HOST.
func TestLoadInternalHost(t *testing.T) {
	for _, tt := range []struct{ host, want string }{
		{"", ":8090"}, {"stats_private", "stats_private:8090"}, {"172.20.0.4", "172.20.0.4:8090"}, {"fd00::4", "[fd00::4]:8090"},
	} {
		setEnvironment(t, map[string]string{"DATABASE_URL": validDatabaseURL, "INTERNAL_HOST": tt.host})
		cfg, err := config.Load()
		if err != nil || cfg.InternalAddress() != tt.want {
			t.Fatalf("INTERNAL_HOST=%q: address %q, %v; want %q", tt.host, cfg.InternalAddress(), err, tt.want)
		}
	}
	for _, host := range []string{"host:9999", "bad host", "-leading.dash"} {
		setEnvironment(t, map[string]string{"DATABASE_URL": validDatabaseURL, "INTERNAL_HOST": host})
		if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "INTERNAL_HOST") {
			t.Fatalf("INTERNAL_HOST=%q: error %v, want an INTERNAL_HOST error", host, err)
		}
	}
}
