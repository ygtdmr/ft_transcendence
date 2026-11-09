package config

import (
	"errors"
	"os"
	"strings"
	"time"
)

const defaultReportWindow = 24 * time.Hour

// loadReportWindow reads how long one reporter must wait before reporting the
// same user again.
func (cfg *Config) loadReportWindow() error {
	cfg.ReportWindow = defaultReportWindow
	raw := strings.TrimSpace(os.Getenv("REPORT_WINDOW"))
	if raw == "" {
		return nil
	}
	window, err := time.ParseDuration(raw)
	if err != nil || window <= 0 {
		return errors.New("REPORT_WINDOW must be a positive Go duration")
	}
	cfg.ReportWindow = window

	return nil
}
