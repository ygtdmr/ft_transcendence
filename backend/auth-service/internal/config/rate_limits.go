package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/ratelimit"
)

// RateLimit allows at most Count requests per Window for one key.
type RateLimit struct {
	Count  int
	Window time.Duration
}

// rateLimitSettings names each public route limit and its default.
var rateLimitSettings = []struct {
	variable string
	fallback RateLimit
	target   func(*Config) *RateLimit
}{
	{"LOGIN_CLIENT_RATE_LIMIT", RateLimit{10, time.Minute}, func(c *Config) *RateLimit { return &c.LoginClientRateLimit }},
	{"LOGIN_EMAIL_RATE_LIMIT", RateLimit{5, time.Minute}, func(c *Config) *RateLimit { return &c.LoginEmailRateLimit }},
	{"REGISTER_CLIENT_RATE_LIMIT", RateLimit{5, 10 * time.Minute}, func(c *Config) *RateLimit { return &c.RegisterClientRateLimit }},
}

// loadRateLimits reads the login and registration limits and the trusted proxies.
//
// Each limit is written as "<count>/<duration>", such as "10/1m"; an empty
// value uses the default. TRUSTED_PROXIES lists the reverse proxies, as
// comma-separated addresses or CIDR ranges, whose X-Forwarded-For is believed
// when attributing a request to a client address; empty trusts none.
//
// Errors:
//
//   - A limit is not "<positive count>/<positive duration>".
//   - TRUSTED_PROXIES contains an entry that is neither an address nor a CIDR range.
func (cfg *Config) loadRateLimits() error {
	for _, setting := range rateLimitSettings {
		limit, err := rateLimit(setting.variable, setting.fallback)
		if err != nil {
			return err
		}
		*setting.target(cfg) = limit
	}
	cfg.TrustedProxies = nil
	for _, entry := range strings.Split(os.Getenv("TRUSTED_PROXIES"), ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			cfg.TrustedProxies = append(cfg.TrustedProxies, entry)
		}
	}
	if _, err := ratelimit.ParseTrustedProxies(cfg.TrustedProxies); err != nil {
		return errors.New("TRUSTED_PROXIES must list addresses or CIDR ranges")
	}

	return nil
}

func rateLimit(variable string, fallback RateLimit) (RateLimit, error) {
	raw := strings.TrimSpace(os.Getenv(variable))
	if raw == "" {
		return fallback, nil
	}
	invalid := fmt.Errorf("%s must be <positive count>/<positive duration>, such as 10/1m", variable)
	count, window, found := strings.Cut(raw, "/")
	if !found {
		return RateLimit{}, invalid
	}
	n, err := strconv.Atoi(strings.TrimSpace(count))
	if err != nil || n <= 0 {
		return RateLimit{}, invalid
	}
	d, err := time.ParseDuration(strings.TrimSpace(window))
	if err != nil || d <= 0 {
		return RateLimit{}, invalid
	}

	return RateLimit{Count: n, Window: d}, nil
}
