package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

const defaultUserInternalTimeout = 5 * time.Second

// loadUserService reads the User Service provisioning settings.
//
// USER_INTERNAL_URL and USER_SERVICE_OUTBOUND_CREDENTIAL_FILE are required in
// production and must be set together elsewhere. Without them, development and
// test accounts are created ACTIVE without a User Service profile.
//
// Errors:
//
//   - Only one of the two settings is present, or neither is set in production.
//   - The URL is not an http(s) origin, the timeout is invalid, or the
//     credential file is unreadable, too short or reused for an inbound caller.
func (cfg *Config) loadUserService() error {
	cfg.UserInternalURL = strings.TrimRight(strings.TrimSpace(os.Getenv("USER_INTERNAL_URL")), "/")
	path := strings.TrimSpace(os.Getenv("USER_SERVICE_OUTBOUND_CREDENTIAL_FILE"))
	timeout, err := duration("USER_INTERNAL_TIMEOUT", defaultUserInternalTimeout)
	if err != nil {
		return err
	}
	if timeout <= 0 {
		return errors.New("USER_INTERNAL_TIMEOUT must be positive")
	}
	cfg.UserInternalTimeout = timeout

	switch {
	case cfg.UserInternalURL == "" && path == "":
		if cfg.Environment == "production" {
			return errors.New("USER_INTERNAL_URL and USER_SERVICE_OUTBOUND_CREDENTIAL_FILE are required in production")
		}
		return nil
	case cfg.UserInternalURL == "" || path == "":
		return errors.New("USER_INTERNAL_URL and USER_SERVICE_OUTBOUND_CREDENTIAL_FILE must be set together")
	}

	parsed, err := url.Parse(cfg.UserInternalURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Path != "" {
		return errors.New("USER_INTERNAL_URL must be an http(s) origin without a path")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return errors.New("USER_SERVICE_OUTBOUND_CREDENTIAL_FILE: credential file cannot be read")
	}
	credential := strings.TrimSpace(string(raw))
	if len(credential) < minCredentialLength {
		return fmt.Errorf("USER_SERVICE_OUTBOUND_CREDENTIAL_FILE: credential must be at least %d characters", minCredentialLength)
	}
	for _, inbound := range cfg.CallerCredentials {
		for _, value := range inbound {
			if value == credential {
				return errors.New("the outbound User Service credential must differ from every inbound credential")
			}
		}
	}
	cfg.UserServiceCredential = credential

	return nil
}
