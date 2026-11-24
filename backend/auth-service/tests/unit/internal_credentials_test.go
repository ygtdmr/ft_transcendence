package unit_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/config"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/testutil"
)

// writeSecret stores value in a temporary file and returns its path.
func writeSecret(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// TestInternalListenerConfig checks the private port and caller credential files.
//
// Expected output:
//
//   - INTERNAL_PORT defaults to 8090 and no caller is configured by default.
//   - Each caller's current and previous credentials are read and trimmed.
//   - Short credentials and one credential shared by two callers are rejected
//     without echoing the credential or its path.
func TestInternalListenerConfig(t *testing.T) {
	base := map[string]string{
		"APP_ENV": "test", "PORT": "", "INTERNAL_PORT": "", "DATABASE_URL": "postgres://test@localhost/test",
		"JWT_SECRET": testutil.TestSigningKey, "ACCESS_TOKEN_TTL": "", "REFRESH_TOKEN_TTL": "",
		"LOG_FORMAT": "", "LOG_LEVEL": "",
	}
	for _, caller := range []string{"WEB_BFF", "GAME_GATEWAY", "NOTIFICATION_GATEWAY", "USER_SERVICE"} {
		base[caller+"_CREDENTIAL_FILE"], base[caller+"_PREVIOUS_CREDENTIAL_FILE"] = "", ""
	}
	set := func(values map[string]string) {
		for key, value := range base {
			t.Setenv(key, value)
		}
		for key, value := range values {
			t.Setenv(key, value)
		}
	}

	set(nil)
	cfg, err := config.Load()
	if err != nil || cfg.InternalPort != "8090" || len(cfg.CallerCredentials) != 0 {
		t.Fatalf("defaults = %q, %d callers, %v", cfg.InternalPort, len(cfg.CallerCredentials), err)
	}

	bff, previous, user := strings.Repeat("b", 40), strings.Repeat("p", 40), strings.Repeat("u", 40)
	set(map[string]string{
		"WEB_BFF_CREDENTIAL_FILE":          writeSecret(t, bff+"\n"),
		"WEB_BFF_PREVIOUS_CREDENTIAL_FILE": writeSecret(t, previous),
		"USER_SERVICE_CREDENTIAL_FILE":     writeSecret(t, user),
	})
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := cfg.CallerCredentials["web-bff"]; len(got) != 2 || !slices.Contains(got, bff) || !slices.Contains(got, previous) {
		t.Fatalf("web-bff has %d credentials, want current and previous", len(got))
	}
	if got := cfg.CallerCredentials["user-service"]; len(got) != 1 || got[0] != user {
		t.Fatal("user-service credential not loaded")
	}

	for name, values := range map[string]map[string]string{
		"short":  {"USER_SERVICE_CREDENTIAL_FILE": writeSecret(t, "too-short-secret")},
		"shared": {"WEB_BFF_CREDENTIAL_FILE": writeSecret(t, bff), "GAME_GATEWAY_CREDENTIAL_FILE": writeSecret(t, bff)},
	} {
		t.Run(name, func(t *testing.T) {
			set(values)
			_, err := config.Load()
			if err == nil {
				t.Fatal("invalid credentials accepted")
			}
			if strings.Contains(err.Error(), "too-short-secret") || strings.Contains(err.Error(), bff) ||
				strings.Contains(err.Error(), os.TempDir()) {
				t.Fatalf("error %q leaks a credential or path", err)
			}
		})
	}
}

// TestUserServiceConfig checks the outbound User Service provisioning settings.
//
// Expected output:
//
//   - Production without the settings fails; development without them is allowed.
//   - Valid settings load the URL, credential and default 5s timeout.
//   - An outbound credential equal to an inbound one is rejected.
func TestUserServiceConfig(t *testing.T) {
	base := map[string]string{
		"APP_ENV": "development", "PORT": "", "INTERNAL_PORT": "", "DATABASE_URL": "postgres://test@localhost/test",
		"JWT_SECRET": testutil.TestSigningKey, "ACCESS_TOKEN_TTL": "", "REFRESH_TOKEN_TTL": "",
		"LOG_FORMAT": "", "LOG_LEVEL": "", "USER_INTERNAL_URL": "", "USER_SERVICE_OUTBOUND_CREDENTIAL_FILE": "",
		"USER_INTERNAL_TIMEOUT": "", "GAME_INTERNAL_URL": "", "GAME_SERVICE_OUTBOUND_CREDENTIAL_FILE": "", "GAME_INTERNAL_TIMEOUT": "",
	}
	for _, caller := range []string{"WEB_BFF", "GAME_GATEWAY", "NOTIFICATION_GATEWAY", "USER_SERVICE"} {
		base[caller+"_CREDENTIAL_FILE"], base[caller+"_PREVIOUS_CREDENTIAL_FILE"] = "", ""
	}
	set := func(values map[string]string) {
		for key, value := range base {
			t.Setenv(key, value)
		}
		for key, value := range values {
			t.Setenv(key, value)
		}
	}

	set(nil)
	if cfg, err := config.Load(); err != nil || cfg.UserInternalURL != "" {
		t.Fatalf("development without User Service = %q, %v", cfg.UserInternalURL, err)
	}
	set(map[string]string{"APP_ENV": "production", "GAME_INTERNAL_URL": "http://game-session:8093",
		"GAME_SERVICE_OUTBOUND_CREDENTIAL_FILE": writeSecret(t, strings.Repeat("g", 40))})
	if _, err := config.Load(); err == nil {
		t.Fatal("production without User Service settings accepted")
	}

	outbound := strings.Repeat("o", 40)
	set(map[string]string{"USER_INTERNAL_URL": "http://user-service:8090/", "USER_SERVICE_OUTBOUND_CREDENTIAL_FILE": writeSecret(t, outbound)})
	cfg, err := config.Load()
	if err != nil || cfg.UserInternalURL != "http://user-service:8090" || cfg.UserServiceCredential != outbound ||
		cfg.UserInternalTimeout.String() != "5s" {
		t.Fatalf("User Service settings = %q %v, %v", cfg.UserInternalURL, cfg.UserInternalTimeout, err)
	}

	set(map[string]string{
		"USER_INTERNAL_URL":                     "http://user-service:8090",
		"USER_SERVICE_OUTBOUND_CREDENTIAL_FILE": writeSecret(t, outbound),
		"USER_SERVICE_CREDENTIAL_FILE":          writeSecret(t, outbound),
	})
	if _, err := config.Load(); err == nil || strings.Contains(err.Error(), outbound) {
		t.Fatalf("reused credential error = %v", err)
	}
}

// TestGameServiceConfig checks the Game Session membership settings.
//
// Expected output:
//
//   - Development without the settings loads with no Game URL; production fails.
//   - Valid settings load the URL without a trailing slash, the credential and
//     the default 5s timeout.
//   - Only one setting, an inbound credential or the User Service credential
//     reused as the Game credential is rejected without echoing the value.
func TestGameServiceConfig(t *testing.T) {
	base := map[string]string{
		"APP_ENV": "development", "PORT": "", "INTERNAL_PORT": "", "DATABASE_URL": "postgres://test@localhost/test",
		"JWT_SECRET": testutil.TestSigningKey, "ACCESS_TOKEN_TTL": "", "REFRESH_TOKEN_TTL": "",
		"LOG_FORMAT": "", "LOG_LEVEL": "", "USER_INTERNAL_URL": "", "USER_SERVICE_OUTBOUND_CREDENTIAL_FILE": "",
		"USER_INTERNAL_TIMEOUT": "", "GAME_INTERNAL_URL": "", "GAME_SERVICE_OUTBOUND_CREDENTIAL_FILE": "", "GAME_INTERNAL_TIMEOUT": "",
	}
	for _, caller := range []string{"WEB_BFF", "GAME_GATEWAY", "NOTIFICATION_GATEWAY", "USER_SERVICE"} {
		base[caller+"_CREDENTIAL_FILE"], base[caller+"_PREVIOUS_CREDENTIAL_FILE"] = "", ""
	}
	set := func(values map[string]string) {
		for key, value := range base {
			t.Setenv(key, value)
		}
		for key, value := range values {
			t.Setenv(key, value)
		}
	}

	set(nil)
	if cfg, err := config.Load(); err != nil || cfg.GameInternalURL != "" {
		t.Fatalf("development without Game Session = %q, %v", cfg.GameInternalURL, err)
	}
	set(map[string]string{"APP_ENV": "production", "USER_INTERNAL_URL": "http://user-service:8090",
		"USER_SERVICE_OUTBOUND_CREDENTIAL_FILE": writeSecret(t, strings.Repeat("o", 40))})
	if _, err := config.Load(); err == nil {
		t.Fatal("production without Game Session settings accepted")
	}

	game := strings.Repeat("g", 40)
	set(map[string]string{"GAME_INTERNAL_URL": "http://game-session:8093/", "GAME_SERVICE_OUTBOUND_CREDENTIAL_FILE": writeSecret(t, game)})
	cfg, err := config.Load()
	if err != nil || cfg.GameInternalURL != "http://game-session:8093" || cfg.GameServiceCredential != game ||
		cfg.GameInternalTimeout.String() != "5s" {
		t.Fatalf("Game Session settings = %q %v, %v", cfg.GameInternalURL, cfg.GameInternalTimeout, err)
	}

	set(map[string]string{"GAME_INTERNAL_URL": "http://game-session:8093"})
	if _, err := config.Load(); err == nil {
		t.Fatal("Game URL without a credential file accepted")
	}
	for name, values := range map[string]map[string]string{
		"inbound reuse": {"GAME_INTERNAL_URL": "http://game-session:8093", "GAME_SERVICE_OUTBOUND_CREDENTIAL_FILE": writeSecret(t, game),
			"USER_SERVICE_CREDENTIAL_FILE": writeSecret(t, game)},
		"User credential reuse": {"GAME_INTERNAL_URL": "http://game-session:8093", "GAME_SERVICE_OUTBOUND_CREDENTIAL_FILE": writeSecret(t, game),
			"USER_INTERNAL_URL": "http://user-service:8090", "USER_SERVICE_OUTBOUND_CREDENTIAL_FILE": writeSecret(t, game)},
	} {
		set(values)
		if _, err := config.Load(); err == nil || strings.Contains(err.Error(), game) {
			t.Fatalf("%s error = %v", name, err)
		}
	}
}
