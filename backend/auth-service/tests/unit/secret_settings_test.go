package unit_test

import (
	"strings"
	"testing"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/config"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/testutil"
)

// setAuthBaseline sets the settings config.Load needs and clears the ones these
// tests change.
func setAuthBaseline(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{
		"APP_ENV": "development", "LOG_FORMAT": "", "LOG_LEVEL": "", "PORT": "", "INTERNAL_PORT": "", "INTERNAL_HOST": "",
		"DATABASE_URL": "postgres://test@localhost/test", "JWT_SECRET": "", "JWT_SECRET_FILE": "",
		"GOOGLE_CLIENT_ID": "", "GOOGLE_CLIENT_SECRET": "", "GOOGLE_CLIENT_SECRET_FILE": "", "PUBLIC_BASE_URL": "",
	} {
		t.Setenv(key, value)
	}
}

// TestSecretFileSettings checks that the signing key and the Google client
// secret can come from mounted files.
//
// Expected output:
//
//   - JWT_SECRET_FILE and GOOGLE_CLIENT_SECRET_FILE supply the trimmed file contents.
//   - Setting a value and its file together is refused.
//   - A too-short key file fails the existing 32-byte rule.
//   - No error contains the file path or the secret.
func TestSecretFileSettings(t *testing.T) {
	setAuthBaseline(t)
	keyFile := writeSecret(t, testutil.TestSigningKey+"\n")
	googleFile := writeSecret(t, "google-client-secret-value\n")
	t.Setenv("JWT_SECRET_FILE", keyFile)
	t.Setenv("GOOGLE_CLIENT_ID", "client-id")
	t.Setenv("GOOGLE_CLIENT_SECRET_FILE", googleFile)
	t.Setenv("PUBLIC_BASE_URL", "https://localhost:3000")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.JWTSecret != testutil.TestSigningKey || cfg.GoogleClientSecret != "google-client-secret-value" {
		t.Fatal("secrets were not read from their files")
	}

	for _, tt := range []struct{ name, value, want string }{
		{"JWT_SECRET", testutil.TestSigningKey, "JWT_SECRET and JWT_SECRET_FILE"},
		{"GOOGLE_CLIENT_SECRET", "google-client-secret-value", "GOOGLE_CLIENT_SECRET and GOOGLE_CLIENT_SECRET_FILE"},
	} {
		t.Run("both "+tt.name, func(t *testing.T) {
			t.Setenv(tt.name, tt.value)
			if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want %q", err, tt.want)
			}
		})
	}

	t.Run("short key file", func(t *testing.T) {
		t.Setenv("JWT_SECRET_FILE", writeSecret(t, "short"))
		_, err := config.Load()
		if err == nil || !strings.Contains(err.Error(), "JWT_SECRET") {
			t.Fatalf("Load() error = %v, want a JWT_SECRET error", err)
		}
	})

	t.Run("unreadable file", func(t *testing.T) {
		missing := keyFile + ".missing"
		t.Setenv("JWT_SECRET_FILE", missing)
		_, err := config.Load()
		if err == nil || !strings.Contains(err.Error(), "JWT_SECRET_FILE") || strings.Contains(err.Error(), missing) {
			t.Fatalf("Load() error = %v, want a JWT_SECRET_FILE error without the path", err)
		}
	})
}

// TestInternalHostSetting checks the private listener's bind address.
//
// Expected output: unset INTERNAL_HOST gives ":8090"; a host is joined with the port.
func TestInternalHostSetting(t *testing.T) {
	setAuthBaseline(t)
	t.Setenv("JWT_SECRET", testutil.TestSigningKey)
	for host, want := range map[string]string{"": ":8090", "auth_private": "auth_private:8090", "172.20.0.2": "172.20.0.2:8090"} {
		t.Setenv("INTERNAL_HOST", host)
		cfg, err := config.Load()
		if err != nil || cfg.InternalAddress() != want {
			t.Fatalf("INTERNAL_HOST=%q: address %q, %v; want %q", host, cfg.InternalAddress(), err, want)
		}
	}
}
