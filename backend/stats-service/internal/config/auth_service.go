package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

const defaultAuthInternalTimeout = 5 * time.Second

// loadAuthService reads the settings for calling Auth's private listener.
//
// AUTH_INTERNAL_URL and AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE are required in
// production and must be set together elsewhere. Without them the routes that
// need a signed-in user are not registered.
//
// Errors:
//
//   - Only one of the two settings is present, or neither is set in production.
//   - The URL is not an http(s) origin, the timeout is invalid, or the
//     credential file is unreadable, too short or reused for an inbound caller.
func (cfg *Config) loadAuthService() error {
	cfg.AuthInternalURL = strings.TrimRight(strings.TrimSpace(os.Getenv("AUTH_INTERNAL_URL")), "/")
	path := strings.TrimSpace(os.Getenv("AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE"))

	cfg.AuthInternalTimeout = defaultAuthInternalTimeout
	if raw := strings.TrimSpace(os.Getenv("AUTH_INTERNAL_TIMEOUT")); raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil || timeout <= 0 {
			return errors.New("AUTH_INTERNAL_TIMEOUT must be a positive duration")
		}
		cfg.AuthInternalTimeout = timeout
	}

	switch {
	case cfg.AuthInternalURL == "" && path == "":
		if cfg.Environment == "production" {
			return errors.New("AUTH_INTERNAL_URL and AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE are required in production")
		}
		return nil
	case cfg.AuthInternalURL == "" || path == "":
		return errors.New("AUTH_INTERNAL_URL and AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE must be set together")
	}

	parsed, err := url.Parse(cfg.AuthInternalURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Path != "" {
		return errors.New("AUTH_INTERNAL_URL must be an http(s) origin without a path")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return errors.New("AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE: credential file cannot be read")
	}
	credential := strings.TrimSpace(string(raw))
	if len(credential) < minCredentialLength {
		return fmt.Errorf("AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE: credential must be at least %d characters", minCredentialLength)
	}
	for _, inbound := range cfg.CallerCredentials {
		for _, value := range inbound {
			if value == credential {
				return errors.New("the outbound Auth Service credential must differ from every inbound credential")
			}
		}
	}
	cfg.AuthServiceCredential = credential

	return nil
}
