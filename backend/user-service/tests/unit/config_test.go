package unit_test

import (
	"maps"
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
	for _, name := range []string{"APP_ENV", "LOG_FORMAT", "LOG_LEVEL", "PORT", "INTERNAL_PORT", "INTERNAL_HOST", "DATABASE_URL", "MIGRATE_ON_START",
		"AUTH_SERVICE_CREDENTIAL_FILE", "AUTH_SERVICE_PREVIOUS_CREDENTIAL_FILE", "AVATAR_BASE_URL", "AVATAR_STORAGE_DIR", "REPORT_WINDOW",
		"GAME_SESSION_CREDENTIAL_FILE", "GAME_SESSION_PREVIOUS_CREDENTIAL_FILE",
		"AUTH_INTERNAL_URL", "AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE", "AUTH_INTERNAL_TIMEOUT",
		"WEB_BFF_CREDENTIAL_FILE", "WEB_BFF_PREVIOUS_CREDENTIAL_FILE",
		"GAME_GATEWAY_INTERNAL_URLS", "GAME_GATEWAY_OUTBOUND_CREDENTIAL_FILE",
		"NOTIFICATION_GATEWAY_INTERNAL_URLS", "NOTIFICATION_GATEWAY_OUTBOUND_CREDENTIAL_FILE", "GATEWAY_INTERNAL_TIMEOUT",
		"STATS_INTERNAL_URL", "STATS_SERVICE_OUTBOUND_CREDENTIAL_FILE", "STATS_INTERNAL_TIMEOUT",
		"GAME_INTERNAL_URL", "GAME_SERVICE_OUTBOUND_CREDENTIAL_FILE", "GAME_INTERNAL_TIMEOUT",
		"ACCOUNT_DELETION_RATE_LIMIT", "REPORT_RATE_LIMIT"} {
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
				"AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE":         writeSecret(t, strings.Repeat("o", 40)),
				"AVATAR_STORAGE_DIR":                            "/var/lib/user-service/avatars",
				"GAME_GATEWAY_INTERNAL_URLS":                    "http://game-gateway:8090",
				"GAME_GATEWAY_OUTBOUND_CREDENTIAL_FILE":         writeSecret(t, strings.Repeat("g", 40)),
				"NOTIFICATION_GATEWAY_INTERNAL_URLS":            "http://notification-gateway:8090",
				"NOTIFICATION_GATEWAY_OUTBOUND_CREDENTIAL_FILE": writeSecret(t, strings.Repeat("c", 40)),
				"STATS_INTERNAL_URL":                            "http://stats-service:8090",
				"STATS_SERVICE_OUTBOUND_CREDENTIAL_FILE":        writeSecret(t, strings.Repeat("t", 40)),
				"GAME_INTERNAL_URL":                             "http://game-session:8093",
				"GAME_SERVICE_OUTBOUND_CREDENTIAL_FILE":         writeSecret(t, strings.Repeat("m", 40)),
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
		{"auth required in production", map[string]string{"DATABASE_URL": validDatabaseURL, "APP_ENV": "production", "AVATAR_STORAGE_DIR": "/data/avatars"}, "AUTH_INTERNAL_URL"},
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

// TestLoadReportWindow checks the report window setting.
//
// Expected output: 24 hours when absent, a valid duration is kept, and a
// malformed, zero or negative value is refused.
func TestLoadReportWindow(t *testing.T) {
	setEnvironment(t, map[string]string{"DATABASE_URL": validDatabaseURL})
	cfg, err := config.Load()
	if err != nil || cfg.ReportWindow != 24*time.Hour {
		t.Fatalf("default = %v, %v", cfg.ReportWindow, err)
	}

	setEnvironment(t, map[string]string{"DATABASE_URL": validDatabaseURL, "REPORT_WINDOW": "90m"})
	cfg, err = config.Load()
	if err != nil || cfg.ReportWindow != 90*time.Minute {
		t.Fatalf("configured = %v, %v", cfg.ReportWindow, err)
	}

	for _, bad := range []string{"soon", "0s", "-1h", "24"} {
		setEnvironment(t, map[string]string{"DATABASE_URL": validDatabaseURL, "REPORT_WINDOW": bad})
		if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "REPORT_WINDOW") {
			t.Fatalf("%q: err = %v", bad, err)
		}
	}
}

// TestLoadRateLimits checks the per-user deletion and report limits.
//
// Expected output: the defaults are 3/1m and 5/1m, "<count>/<duration>"
// overrides are read, and a malformed, zero or negative value is refused
// with the variable's name.
func TestLoadRateLimits(t *testing.T) {
	setEnvironment(t, map[string]string{"DATABASE_URL": validDatabaseURL})
	cfg, err := config.Load()
	if err != nil || cfg.AccountDeletionRateLimit != (config.RateLimit{Count: 3, Window: time.Minute}) ||
		cfg.ReportRateLimit != (config.RateLimit{Count: 5, Window: time.Minute}) {
		t.Fatalf("defaults = %+v %+v, %v", cfg.AccountDeletionRateLimit, cfg.ReportRateLimit, err)
	}

	setEnvironment(t, map[string]string{"DATABASE_URL": validDatabaseURL, "ACCOUNT_DELETION_RATE_LIMIT": "1/30s", "REPORT_RATE_LIMIT": " 20 / 1h "})
	cfg, err = config.Load()
	if err != nil || cfg.AccountDeletionRateLimit != (config.RateLimit{Count: 1, Window: 30 * time.Second}) ||
		cfg.ReportRateLimit != (config.RateLimit{Count: 20, Window: time.Hour}) {
		t.Fatalf("configured = %+v %+v, %v", cfg.AccountDeletionRateLimit, cfg.ReportRateLimit, err)
	}

	for _, variable := range []string{"ACCOUNT_DELETION_RATE_LIMIT", "REPORT_RATE_LIMIT"} {
		for _, bad := range []string{"3", "0/1m", "-1/1m", "x/1m", "3/0s", "3/soon"} {
			setEnvironment(t, map[string]string{"DATABASE_URL": validDatabaseURL, variable: bad})
			if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), variable) {
				t.Fatalf("%s=%q: err = %v", variable, bad, err)
			}
		}
	}
}

// TestLoadAvatarStorage checks the avatar directory setting.
//
// Expected output: a relative path is refused, an absolute path is kept, and
// production refuses a missing setting.
func TestLoadAvatarStorage(t *testing.T) {
	setEnvironment(t, map[string]string{"DATABASE_URL": validDatabaseURL, "AVATAR_STORAGE_DIR": "relative/dir"})
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "AVATAR_STORAGE_DIR") {
		t.Fatalf("relative directory: err = %v", err)
	}

	setEnvironment(t, map[string]string{"DATABASE_URL": validDatabaseURL, "AVATAR_STORAGE_DIR": "/data/avatars"})
	cfg, err := config.Load()
	if err != nil || cfg.AvatarStorageDir != "/data/avatars" {
		t.Fatalf("absolute directory: %q, %v", cfg.AvatarStorageDir, err)
	}

	setEnvironment(t, map[string]string{"DATABASE_URL": validDatabaseURL, "APP_ENV": "production",
		"AUTH_INTERNAL_URL": "http://auth:8090", "AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE": writeSecret(t, strings.Repeat("o", 40))})
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "AVATAR_STORAGE_DIR") {
		t.Fatalf("production without directory: err = %v", err)
	}
}

// TestLoadGameSessionCallerCredentials checks the Game Session caller.
//
// Expected output:
//
//   - Game Session loads its current and previous credentials.
//   - Channel Service credential variables are ignored; no such caller exists.
//   - One credential configured for two callers is refused.
func TestLoadGameSessionCallerCredentials(t *testing.T) {
	game, gamePrevious := strings.Repeat("g", 40), strings.Repeat("j", 40)
	setEnvironment(t, map[string]string{
		"DATABASE_URL":                          validDatabaseURL,
		"GAME_SESSION_CREDENTIAL_FILE":          writeSecret(t, game),
		"GAME_SESSION_PREVIOUS_CREDENTIAL_FILE": writeSecret(t, gamePrevious),
	})
	t.Setenv("CHANNEL_SERVICE_CREDENTIAL_FILE", writeSecret(t, strings.Repeat("h", 40)))
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	got := cfg.CallerCredentials["game-session"]
	if len(got) != 2 || !slices.Contains(got, game) || !slices.Contains(got, gamePrevious) {
		t.Fatalf("game-session credentials = %d values, want current and previous", len(got))
	}
	if _, ok := cfg.CallerCredentials["channel-service"]; ok {
		t.Fatal("channel-service credentials were loaded")
	}

	shared := writeSecret(t, game)
	setEnvironment(t, map[string]string{"DATABASE_URL": validDatabaseURL,
		"WEB_BFF_CREDENTIAL_FILE": shared, "GAME_SESSION_CREDENTIAL_FILE": shared})
	if _, err := config.Load(); err == nil {
		t.Fatal("Load() accepted one credential for two callers")
	}
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

// TestLoadGateways checks the Gateway registry settings.
//
// Expected output: none set leaves presence unconfigured outside production
// and fails in production; a full set lists every instance; a partial set,
// a URL with a path, a repeated instance and a reused credential are refused.
func TestLoadGateways(t *testing.T) {
	game := writeSecret(t, strings.Repeat("g", 40))
	notification := writeSecret(t, strings.Repeat("c", 40))
	full := map[string]string{"DATABASE_URL": validDatabaseURL,
		"GAME_GATEWAY_INTERNAL_URLS": "http://game-gateway-1:8090, http://game-gateway-2:8090/", "GAME_GATEWAY_OUTBOUND_CREDENTIAL_FILE": game,
		"NOTIFICATION_GATEWAY_INTERNAL_URLS": "http://notification-gateway:8090", "NOTIFICATION_GATEWAY_OUTBOUND_CREDENTIAL_FILE": notification}

	setEnvironment(t, map[string]string{"DATABASE_URL": validDatabaseURL})
	if cfg, err := config.Load(); err != nil || cfg.Gateways != nil || cfg.GatewayInternalTimeout != 5*time.Second {
		t.Fatalf("unset = %v, %v", cfg.Gateways, err)
	}
	setEnvironment(t, map[string]string{"DATABASE_URL": validDatabaseURL, "APP_ENV": "production", "AVATAR_STORAGE_DIR": "/data",
		"AUTH_INTERNAL_URL": "http://auth-service:8090", "AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE": writeSecret(t, strings.Repeat("o", 40))})
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "GATEWAY") {
		t.Fatalf("production without gateways = %v", err)
	}

	setEnvironment(t, full)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Gateways) != 2 || !slices.Equal(cfg.Gateways[0].URLs, []string{"http://game-gateway-1:8090", "http://game-gateway-2:8090"}) ||
		cfg.Gateways[0].Credential != strings.Repeat("g", 40) || !slices.Equal(cfg.Gateways[1].URLs, []string{"http://notification-gateway:8090"}) {
		t.Fatalf("gateways = %+v", cfg.Gateways)
	}

	for name, change := range map[string]map[string]string{
		"partial":        {"NOTIFICATION_GATEWAY_OUTBOUND_CREDENTIAL_FILE": ""},
		"path":           {"NOTIFICATION_GATEWAY_INTERNAL_URLS": "http://notification-gateway:8090/x"},
		"repeated":       {"NOTIFICATION_GATEWAY_INTERNAL_URLS": "http://game-gateway-1:8090"},
		"shared":         {"NOTIFICATION_GATEWAY_OUTBOUND_CREDENTIAL_FILE": game},
		"inbound reused": {"WEB_BFF_CREDENTIAL_FILE": notification},
		"timeout":        {"GATEWAY_INTERNAL_TIMEOUT": "0s"},
	} {
		values := maps.Clone(full)
		maps.Copy(values, change)
		setEnvironment(t, values)
		if _, err := config.Load(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

// TestLoadStatsService checks the Stats settings.
//
// Expected output: unset is allowed outside production; a full pair is kept
// with a 5s default timeout; a partial pair, a URL with a path and a reused
// credential are refused.
func TestLoadStatsService(t *testing.T) {
	stats := writeSecret(t, strings.Repeat("t", 40))
	setEnvironment(t, map[string]string{"DATABASE_URL": validDatabaseURL})
	if cfg, err := config.Load(); err != nil || cfg.StatsInternalURL != "" {
		t.Fatalf("unset = %q, %v", cfg.StatsInternalURL, err)
	}

	full := map[string]string{"DATABASE_URL": validDatabaseURL, "STATS_INTERNAL_URL": "http://stats-service:8090/",
		"STATS_SERVICE_OUTBOUND_CREDENTIAL_FILE": stats}
	setEnvironment(t, full)
	cfg, err := config.Load()
	if err != nil || cfg.StatsInternalURL != "http://stats-service:8090" || cfg.StatsServiceCredential != strings.Repeat("t", 40) ||
		cfg.StatsInternalTimeout != 5*time.Second {
		t.Fatalf("stats = %q %v, %v", cfg.StatsInternalURL, cfg.StatsInternalTimeout, err)
	}

	for name, change := range map[string]map[string]string{
		"partial":        {"STATS_SERVICE_OUTBOUND_CREDENTIAL_FILE": ""},
		"path":           {"STATS_INTERNAL_URL": "http://stats-service:8090/x"},
		"inbound reused": {"WEB_BFF_CREDENTIAL_FILE": stats},
		"timeout":        {"STATS_INTERNAL_TIMEOUT": "-1s"},
	} {
		values := maps.Clone(full)
		maps.Copy(values, change)
		setEnvironment(t, values)
		if _, err := config.Load(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

// TestLoadGameService checks the Game Session settings.
//
// Expected output: unset is allowed outside production with a 5s timeout and
// refused in production; a full pair is kept; a partial pair, a URL with a
// path, an invalid timeout and a credential shared with an inbound caller or
// Stats are refused.
func TestLoadGameService(t *testing.T) {
	game := writeSecret(t, strings.Repeat("m", 40))
	setEnvironment(t, map[string]string{"DATABASE_URL": validDatabaseURL})
	if cfg, err := config.Load(); err != nil || cfg.GameInternalURL != "" || cfg.GameInternalTimeout != 5*time.Second {
		t.Fatalf("unset = %q, %v", cfg.GameInternalURL, err)
	}
	setEnvironment(t, map[string]string{"DATABASE_URL": validDatabaseURL, "APP_ENV": "production", "AVATAR_STORAGE_DIR": "/data",
		"AUTH_INTERNAL_URL": "http://auth-service:8090", "AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE": writeSecret(t, strings.Repeat("o", 40)),
		"GAME_GATEWAY_INTERNAL_URLS": "http://game-gateway:8090", "GAME_GATEWAY_OUTBOUND_CREDENTIAL_FILE": writeSecret(t, strings.Repeat("g", 40)),
		"NOTIFICATION_GATEWAY_INTERNAL_URLS":            "http://notification-gateway:8090",
		"NOTIFICATION_GATEWAY_OUTBOUND_CREDENTIAL_FILE": writeSecret(t, strings.Repeat("c", 40)),
		"STATS_INTERNAL_URL":                            "http://stats-service:8090", "STATS_SERVICE_OUTBOUND_CREDENTIAL_FILE": writeSecret(t, strings.Repeat("t", 40))})
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "GAME_INTERNAL_URL") {
		t.Fatalf("production without Game Session = %v", err)
	}

	full := map[string]string{"DATABASE_URL": validDatabaseURL, "GAME_INTERNAL_URL": "http://game-session:8093/",
		"GAME_SERVICE_OUTBOUND_CREDENTIAL_FILE": game}
	setEnvironment(t, full)
	cfg, err := config.Load()
	if err != nil || cfg.GameInternalURL != "http://game-session:8093" || cfg.GameServiceCredential != strings.Repeat("m", 40) ||
		cfg.GameInternalTimeout != 5*time.Second {
		t.Fatalf("game = %q %v, %v", cfg.GameInternalURL, cfg.GameInternalTimeout, err)
	}

	for name, change := range map[string]map[string]string{
		"partial":        {"GAME_SERVICE_OUTBOUND_CREDENTIAL_FILE": ""},
		"path":           {"GAME_INTERNAL_URL": "http://game-session:8093/x"},
		"inbound reused": {"GAME_SESSION_CREDENTIAL_FILE": game},
		"stats reused":   {"STATS_INTERNAL_URL": "http://stats-service:8090", "STATS_SERVICE_OUTBOUND_CREDENTIAL_FILE": game},
		"timeout":        {"GAME_INTERNAL_TIMEOUT": "0s"},
	} {
		values := maps.Clone(full)
		maps.Copy(values, change)
		setEnvironment(t, values)
		if _, err := config.Load(); err == nil || strings.Contains(err.Error(), strings.Repeat("m", 40)) {
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
		{"", ":8090"}, {"user_private", "user_private:8090"}, {"172.20.0.4", "172.20.0.4:8090"}, {"fd00::4", "[fd00::4]:8090"},
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
