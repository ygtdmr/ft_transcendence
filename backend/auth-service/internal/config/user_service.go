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

// outboundService names the settings of one private receiver Auth calls.
type outboundService struct {
	urlVar, fileVar, timeoutVar string
	fallbackTimeout             time.Duration
	// label names the receiver in error messages.
	label string
}

// outboundSettings holds one receiver's validated settings. All fields are
// empty when the receiver is not configured.
type outboundSettings struct {
	url, credential string
	timeout         time.Duration
}

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
	settings, err := cfg.loadOutbound(outboundService{
		urlVar: "USER_INTERNAL_URL", fileVar: "USER_SERVICE_OUTBOUND_CREDENTIAL_FILE",
		timeoutVar: "USER_INTERNAL_TIMEOUT", fallbackTimeout: defaultUserInternalTimeout, label: "User Service",
	})
	if err != nil {
		return err
	}
	cfg.UserInternalURL, cfg.UserServiceCredential, cfg.UserInternalTimeout = settings.url, settings.credential, settings.timeout

	return nil
}

// loadOutbound validates one receiver's URL, credential file and timeout.
func (cfg *Config) loadOutbound(service outboundService) (outboundSettings, error) {
	rawURL := strings.TrimRight(strings.TrimSpace(os.Getenv(service.urlVar)), "/")
	path := strings.TrimSpace(os.Getenv(service.fileVar))
	timeout, err := duration(service.timeoutVar, service.fallbackTimeout)
	if err != nil {
		return outboundSettings{}, err
	}
	if timeout <= 0 {
		return outboundSettings{}, errors.New(service.timeoutVar + " must be positive")
	}

	switch {
	case rawURL == "" && path == "":
		if cfg.Environment == "production" {
			return outboundSettings{}, fmt.Errorf("%s and %s are required in production", service.urlVar, service.fileVar)
		}
		return outboundSettings{timeout: timeout}, nil
	case rawURL == "" || path == "":
		return outboundSettings{}, fmt.Errorf("%s and %s must be set together", service.urlVar, service.fileVar)
	}

	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Path != "" {
		return outboundSettings{}, errors.New(service.urlVar + " must be an http(s) origin without a path")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return outboundSettings{}, errors.New(service.fileVar + ": credential file cannot be read")
	}
	credential := strings.TrimSpace(string(raw))
	if len(credential) < minCredentialLength {
		return outboundSettings{}, fmt.Errorf("%s: credential must be at least %d characters", service.fileVar, minCredentialLength)
	}
	for _, inbound := range cfg.CallerCredentials {
		for _, value := range inbound {
			if value == credential {
				return outboundSettings{}, fmt.Errorf("the outbound %s credential must differ from every inbound credential", service.label)
			}
		}
	}

	return outboundSettings{url: rawURL, credential: credential, timeout: timeout}, nil
}
