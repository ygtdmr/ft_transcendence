// Package logging defines common log formats for backend services.
package logging

import (
	"context"
	"log/slog"
	"time"
)

// AccessLog contains the metadata for a completed HTTP request.
// Path must contain only the URL path, without query parameters.
// Request bodies, cookies, tokens and headers are not part of this format.
type AccessLog struct {
	Time      time.Time
	RequestID string
	Status    int
	Latency   time.Duration
	ClientIP  string
	Method    string
	Path      string
	// ErrorCode is the REST error code written for this response, if any.
	ErrorCode string
}

// LogAccess writes a completed request using the logger's format and level.
// Successes and redirects use info, 4xx responses warn, and 5xx responses error.
func LogAccess(ctx context.Context, logger *slog.Logger, entry AccessLog) {
	level := slog.LevelInfo
	if entry.Status >= 500 {
		level = slog.LevelError
	} else if entry.Status >= 400 {
		level = slog.LevelWarn
	}
	if !logger.Enabled(ctx, level) {
		return
	}
	record := slog.NewRecord(entry.Time, level, "http_request", 0)
	record.AddAttrs(
		slog.String("request_id", entry.RequestID),
		slog.Int("status", entry.Status),
		slog.String("latency", entry.Latency.String()),
		slog.String("client_ip", entry.ClientIP),
		slog.String("method", entry.Method),
		slog.String("path", entry.Path),
	)
	if entry.ErrorCode != "" {
		record.AddAttrs(slog.String("error_code", entry.ErrorCode))
	}
	_ = logger.Handler().Handle(ctx, record)
}
