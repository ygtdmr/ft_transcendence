package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

const defaultGatewayInternalTimeout = 5 * time.Second

// Gateway is one Gateway kind's running instances and this service's
// credential for them.
type Gateway struct {
	URLs       []string
	Credential string `json:"-"`
}

// gatewaySettings names each Gateway kind's instance list and credential file.
var gatewaySettings = []struct{ name, urls, credential string }{
	{"game", "GAME_GATEWAY_INTERNAL_URLS", "GAME_GATEWAY_OUTBOUND_CREDENTIAL_FILE"},
	{"chat", "CHAT_GATEWAY_INTERNAL_URLS", "CHAT_GATEWAY_OUTBOUND_CREDENTIAL_FILE"},
}

// loadGateways reads the settings for querying Gateway socket registries.
//
// Presence needs every registry, so the four settings are set together:
// comma-separated private origins of every running Game Gateway and Chat
// Gateway instance, and one credential file per Gateway kind. They are
// required in production. Elsewhere, when none is set, cfg.Gateways stays nil
// and presence reports every user as offline.
//
// Errors:
//
//   - Some but not all settings are present, or none is set in production.
//   - A URL is not an http(s) origin, is repeated, or the timeout is invalid.
//   - A credential file is unreadable or too short, or a credential is reused
//     for another Gateway, the Auth call or an inbound caller.
func (cfg *Config) loadGateways() error {
	cfg.GatewayInternalTimeout = defaultGatewayInternalTimeout
	if raw := strings.TrimSpace(os.Getenv("GATEWAY_INTERNAL_TIMEOUT")); raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil || timeout <= 0 {
			return errors.New("GATEWAY_INTERNAL_TIMEOUT must be a positive duration")
		}
		cfg.GatewayInternalTimeout = timeout
	}

	set := 0
	for _, s := range gatewaySettings {
		for _, variable := range []string{s.urls, s.credential} {
			if strings.TrimSpace(os.Getenv(variable)) != "" {
				set++
			}
		}
	}
	switch {
	case set == 0 && cfg.Environment == "production":
		return errors.New("GAME_GATEWAY_* and CHAT_GATEWAY_* registry settings are required in production")
	case set == 0:
		return nil
	case set != 2*len(gatewaySettings):
		return errors.New("GAME_GATEWAY_INTERNAL_URLS, GAME_GATEWAY_OUTBOUND_CREDENTIAL_FILE, " +
			"CHAT_GATEWAY_INTERNAL_URLS and CHAT_GATEWAY_OUTBOUND_CREDENTIAL_FILE must be set together")
	}

	used := map[string]bool{cfg.AuthServiceCredential: true}
	for _, inbound := range cfg.CallerCredentials {
		for _, value := range inbound {
			used[value] = true
		}
	}
	seen := map[string]bool{}
	for _, s := range gatewaySettings {
		var gateway Gateway
		for _, raw := range strings.Split(os.Getenv(s.urls), ",") {
			origin := strings.TrimRight(strings.TrimSpace(raw), "/")
			parsed, err := url.Parse(origin)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Path != "" {
				return fmt.Errorf("%s must list http(s) origins without a path", s.urls)
			}
			if seen[origin] {
				return fmt.Errorf("%s repeats an instance", s.urls)
			}
			seen[origin] = true
			gateway.URLs = append(gateway.URLs, origin)
		}
		raw, err := os.ReadFile(strings.TrimSpace(os.Getenv(s.credential)))
		if err != nil {
			return fmt.Errorf("%s: credential file cannot be read", s.credential)
		}
		gateway.Credential = strings.TrimSpace(string(raw))
		if len(gateway.Credential) < minCredentialLength {
			return fmt.Errorf("%s: credential must be at least %d characters", s.credential, minCredentialLength)
		}
		if used[gateway.Credential] {
			return fmt.Errorf("%s: each outbound Gateway credential must differ from every other service credential", s.credential)
		}
		used[gateway.Credential] = true
		cfg.Gateways = append(cfg.Gateways, gateway)
	}

	return nil
}
