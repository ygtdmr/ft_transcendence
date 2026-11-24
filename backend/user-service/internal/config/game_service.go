package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

const defaultGameInternalTimeout = 5 * time.Second

// loadGameService reads the settings for calling Game Session's private
// listener.
//
// GAME_INTERNAL_URL and GAME_SERVICE_OUTBOUND_CREDENTIAL_FILE are required in
// production and must be set together elsewhere. Without them the own profile
// reports no active room.
//
// Errors:
//
//   - Only one of the two settings is present, or neither is set in production.
//   - The URL is not an http(s) origin, the timeout is invalid, or the
//     credential file is unreadable, too short or reused for another service.
func (cfg *Config) loadGameService() error {
	cfg.GameInternalURL = strings.TrimRight(strings.TrimSpace(os.Getenv("GAME_INTERNAL_URL")), "/")
	path := strings.TrimSpace(os.Getenv("GAME_SERVICE_OUTBOUND_CREDENTIAL_FILE"))

	cfg.GameInternalTimeout = defaultGameInternalTimeout
	if raw := strings.TrimSpace(os.Getenv("GAME_INTERNAL_TIMEOUT")); raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil || timeout <= 0 {
			return errors.New("GAME_INTERNAL_TIMEOUT must be a positive duration")
		}
		cfg.GameInternalTimeout = timeout
	}

	switch {
	case cfg.GameInternalURL == "" && path == "":
		if cfg.Environment == "production" {
			return errors.New("GAME_INTERNAL_URL and GAME_SERVICE_OUTBOUND_CREDENTIAL_FILE are required in production")
		}
		return nil
	case cfg.GameInternalURL == "" || path == "":
		return errors.New("GAME_INTERNAL_URL and GAME_SERVICE_OUTBOUND_CREDENTIAL_FILE must be set together")
	}

	parsed, err := url.Parse(cfg.GameInternalURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Path != "" {
		return errors.New("GAME_INTERNAL_URL must be an http(s) origin without a path")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return errors.New("GAME_SERVICE_OUTBOUND_CREDENTIAL_FILE: credential file cannot be read")
	}
	credential := strings.TrimSpace(string(raw))
	if len(credential) < minCredentialLength {
		return fmt.Errorf("GAME_SERVICE_OUTBOUND_CREDENTIAL_FILE: credential must be at least %d characters", minCredentialLength)
	}
	used := credential == cfg.AuthServiceCredential || credential == cfg.StatsServiceCredential
	for _, gateway := range cfg.Gateways {
		used = used || credential == gateway.Credential
	}
	for _, inbound := range cfg.CallerCredentials {
		for _, value := range inbound {
			used = used || value == credential
		}
	}
	if used {
		return errors.New("the outbound Game Session credential must differ from every other service credential")
	}
	cfg.GameServiceCredential = credential

	return nil
}
