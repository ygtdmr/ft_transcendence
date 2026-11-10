package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

const defaultStatsInternalTimeout = 5 * time.Second

// loadStatsService reads the settings for calling Stats Service's private
// listener.
//
// STATS_INTERNAL_URL and STATS_SERVICE_OUTBOUND_CREDENTIAL_FILE are required in
// production and must be set together elsewhere. Without them the public
// profile route is not registered.
//
// Errors:
//
//   - Only one of the two settings is present, or neither is set in production.
//   - The URL is not an http(s) origin, the timeout is invalid, or the
//     credential file is unreadable, too short or reused for another service.
func (cfg *Config) loadStatsService() error {
	cfg.StatsInternalURL = strings.TrimRight(strings.TrimSpace(os.Getenv("STATS_INTERNAL_URL")), "/")
	path := strings.TrimSpace(os.Getenv("STATS_SERVICE_OUTBOUND_CREDENTIAL_FILE"))

	cfg.StatsInternalTimeout = defaultStatsInternalTimeout
	if raw := strings.TrimSpace(os.Getenv("STATS_INTERNAL_TIMEOUT")); raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil || timeout <= 0 {
			return errors.New("STATS_INTERNAL_TIMEOUT must be a positive duration")
		}
		cfg.StatsInternalTimeout = timeout
	}

	switch {
	case cfg.StatsInternalURL == "" && path == "":
		if cfg.Environment == "production" {
			return errors.New("STATS_INTERNAL_URL and STATS_SERVICE_OUTBOUND_CREDENTIAL_FILE are required in production")
		}
		return nil
	case cfg.StatsInternalURL == "" || path == "":
		return errors.New("STATS_INTERNAL_URL and STATS_SERVICE_OUTBOUND_CREDENTIAL_FILE must be set together")
	}

	parsed, err := url.Parse(cfg.StatsInternalURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Path != "" {
		return errors.New("STATS_INTERNAL_URL must be an http(s) origin without a path")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return errors.New("STATS_SERVICE_OUTBOUND_CREDENTIAL_FILE: credential file cannot be read")
	}
	credential := strings.TrimSpace(string(raw))
	if len(credential) < minCredentialLength {
		return fmt.Errorf("STATS_SERVICE_OUTBOUND_CREDENTIAL_FILE: credential must be at least %d characters", minCredentialLength)
	}
	used := credential == cfg.AuthServiceCredential
	for _, gateway := range cfg.Gateways {
		used = used || credential == gateway.Credential
	}
	for _, inbound := range cfg.CallerCredentials {
		for _, value := range inbound {
			used = used || value == credential
		}
	}
	if used {
		return errors.New("the outbound Stats Service credential must differ from every other service credential")
	}
	cfg.StatsServiceCredential = credential

	return nil
}
