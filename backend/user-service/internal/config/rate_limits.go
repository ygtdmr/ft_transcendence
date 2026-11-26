package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// RateLimit allows at most Count requests per Window for one user.
type RateLimit struct {
	Count  int
	Window time.Duration
}

// loadRateLimits reads the per-user limits of account deletion and reports.
//
// Each limit is written as "<count>/<duration>", such as "3/1m"; an empty
// value uses the default: ACCOUNT_DELETION_RATE_LIMIT 3/1m and
// REPORT_RATE_LIMIT 5/1m.
//
// Errors:
//
//   - A limit is not "<positive count>/<positive duration>".
func (cfg *Config) loadRateLimits() error {
	var err error
	if cfg.AccountDeletionRateLimit, err = rateLimit("ACCOUNT_DELETION_RATE_LIMIT", RateLimit{3, time.Minute}); err != nil {
		return err
	}
	if cfg.ReportRateLimit, err = rateLimit("REPORT_RATE_LIMIT", RateLimit{5, time.Minute}); err != nil {
		return err
	}

	return nil
}

func rateLimit(variable string, fallback RateLimit) (RateLimit, error) {
	raw := strings.TrimSpace(os.Getenv(variable))
	if raw == "" {
		return fallback, nil
	}
	invalid := fmt.Errorf("%s must be <positive count>/<positive duration>, such as 3/1m", variable)
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
