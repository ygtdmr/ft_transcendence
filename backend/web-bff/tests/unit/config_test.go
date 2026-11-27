package unit_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/web-bff/internal/config"
)

func lookup(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := values[name]
		return v, ok
	}
}

func validSettings(t *testing.T) map[string]string {
	t.Helper()
	credential := filepath.Join(t.TempDir(), "web-bff-to-auth-service")
	if err := os.WriteFile(credential, []byte(strings.Repeat("c", 64)+"\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"PUBLIC_BASE_URL":                       "https://localhost:8443",
		"AUTH_INTERNAL_URL":                     "http://auth-service:8090",
		"AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE": credential,
		"AUTH_INTERNAL_TIMEOUT":                 "3s",
		"AUTH_PUBLIC_URL":                       "http://auth-service:8080",
		"USER_PUBLIC_URL":                       "http://user-service:8080",
		"STATS_PUBLIC_URL":                      "http://stats-service:8080",
		"GAME_PUBLIC_URL":                       "http://game-session:8080/",
	}
}

// TestLoadConfig checks defaults and the upstream origins.
//
// Expected output: port 8080, a 10s upstream timeout and origins without a path.
func TestLoadConfig(t *testing.T) {
	cfg, err := config.Load(lookup(validSettings(t)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 8080 || cfg.UpstreamTimeout != 10*time.Second || cfg.Upstreams.Game.String() != "http://game-session:8080" ||
		cfg.Upstreams.Stats.Host != "stats-service:8080" {
		t.Fatalf("config = %+v", cfg)
	}
}

// TestLoadConfigRejects checks that every problem names its variable, never a
// value or a path.
func TestLoadConfigRejects(t *testing.T) {
	for _, tt := range []struct{ name, value string }{
		{"PUBLIC_BASE_URL", ""},
		{"PUBLIC_BASE_URL", "https://localhost:8443/app"},
		{"AUTH_INTERNAL_URL", ""},
		{"AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE", "/nonexistent/secret-path"},
		{"USER_PUBLIC_URL", ""},
		{"USER_PUBLIC_URL", "http://user-service:8080/api"},
		{"STATS_PUBLIC_URL", "ftp://stats-service"},
		{"GAME_PUBLIC_URL", "http://user:pass@game-session:8080"},
		{"AUTH_INTERNAL_TIMEOUT", ""},
		{"UPSTREAM_TIMEOUT", "2h"},
	} {
		t.Run(tt.name+"="+tt.value, func(t *testing.T) {
			settings := validSettings(t)
			settings[tt.name] = tt.value
			_, err := config.Load(lookup(settings))
			if err == nil || !strings.Contains(err.Error(), tt.name) {
				t.Fatalf("error = %v, want one naming %s", err, tt.name)
			}
			if tt.value != "" && strings.Contains(err.Error(), tt.value) {
				t.Fatalf("error contains the value: %v", err)
			}
		})
	}
}
