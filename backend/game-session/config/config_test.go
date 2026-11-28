package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadWithHost(t *testing.T, host string) (Config, error) {
	t.Helper()
	return loadWithHosts(t, host, "")
}

func loadWithHosts(t *testing.T, internalHost, publicHost string) (Config, error) {
	t.Helper()
	credential := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(credential, []byte(strings.Repeat("c", 40)), 0o600); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"DATABASE_URL": "postgres://test@localhost/test", "PORT": "", "PUBLIC_HOST": publicHost, "INTERNAL_PORT": "8081", "INTERNAL_HOST": internalHost,
		"TICK_INTERVAL": "", "USER_SERVICE_INTERNAL_URL": "http://user-service:8081",
		"GAME_TO_USER_SERVICE_CREDENTIAL_FILE": credential,
		"AUTH_SERVICE_CREDENTIAL_FILE":         "", "USER_SERVICE_CREDENTIAL_FILE": "", "GAME_GATEWAY_CREDENTIAL_FILE": "",
		"LEAVE_PENALTY_BASE": "", "LEAVE_PENALTY_OPERATIVE_MULTIPLIER": "", "LEAVE_PENALTY_SPYMASTER_MULTIPLIER": "",
	} {
		t.Setenv(key, value)
	}

	return Load()
}

// TestInternalHostSelectsThePrivateListenerAddress checks INTERNAL_HOST.
//
// Expected output:
//
//   - Unset, the private listener binds every interface on INTERNAL_PORT.
//   - A valid IP address or host name is joined with INTERNAL_PORT.
//   - A value with a port, a scheme or whitespace is refused by name.
func TestInternalHostSelectsThePrivateListenerAddress(t *testing.T) {
	for host, want := range map[string]string{"": ":8081", "172.29.11.5": "172.29.11.5:8081", "game.private": "game.private:8081", "::1": "[::1]:8081"} {
		cfg, err := loadWithHost(t, host)
		if err != nil || cfg.InternalAddress() != want {
			t.Errorf("INTERNAL_HOST=%q: address %q, error %v, want %q", host, cfg.InternalAddress(), err, want)
		}
	}
	for _, host := range []string{"172.29.11.5:8081", "http://game", "game session"} {
		if _, err := loadWithHost(t, host); err == nil || !strings.Contains(err.Error(), "INTERNAL_HOST") {
			t.Errorf("INTERNAL_HOST=%q: error %v, want one naming INTERNAL_HOST", host, err)
		}
	}
}

// TestPublicHostSelectsThePublicListenerAddress checks PUBLIC_HOST.
//
// Expected output:
//
//   - Unset, the public listener binds every interface on PORT (8083 by default).
//   - A valid IP address or host name is joined with PORT.
//   - A value with a port, a scheme or whitespace is refused by name.
//   - PUBLIC_HOST and INTERNAL_HOST are independent: each listener has its own.
func TestPublicHostSelectsThePublicListenerAddress(t *testing.T) {
	for host, want := range map[string]string{"": ":8083", "172.29.12.13": "172.29.12.13:8083", "game.rest": "game.rest:8083", "::1": "[::1]:8083"} {
		cfg, err := loadWithHosts(t, "", host)
		if err != nil || cfg.PublicAddress() != want {
			t.Errorf("PUBLIC_HOST=%q: address %q, error %v, want %q", host, cfg.PublicAddress(), err, want)
		}
	}
	for _, host := range []string{"172.29.12.13:8083", "http://game", "game session"} {
		if _, err := loadWithHosts(t, "", host); err == nil || !strings.Contains(err.Error(), "PUBLIC_HOST") {
			t.Errorf("PUBLIC_HOST=%q: error %v, want one naming PUBLIC_HOST", host, err)
		}
	}
	cfg, err := loadWithHosts(t, "172.29.11.13", "172.29.12.13")
	if err != nil || cfg.InternalAddress() != "172.29.11.13:8081" || cfg.PublicAddress() != "172.29.12.13:8083" {
		t.Errorf("both hosts set: internal %q, public %q, error %v", cfg.InternalAddress(), cfg.PublicAddress(), err)
	}
}
