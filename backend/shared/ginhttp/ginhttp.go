// Package ginhttp holds the Gin middleware every backend service shares:
// request correlation IDs, a response recorder that learns the REST error code,
// and structured access logging. It replaces the copies each service carries.
//
// Order matters; register them in this order:
//
//	router.Use(ginhttp.Trace(), ginhttp.AccessLog(logger), metrics.Middleware(service))
package ginhttp

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/melmut-42/ft_transcendence/backend/shared/logging"
	"github.com/melmut-42/ft_transcendence/backend/shared/requestid"
)

const (
	requestIDKey = "request_id"
	recorderKey  = "ginhttp.recorder"
)

// recorder wraps Gin's writer so httpresponse.WriteError can report the error
// code it sends. Every other ResponseWriter capability (Flush, Hijack for
// WebSocket upgrades, Pusher, status and size) passes through unchanged.
type recorder struct {
	gin.ResponseWriter
	code string
}

// RecordErrorCode implements httpresponse.ErrorCodeRecorder.
func (r *recorder) RecordErrorCode(code string) { r.code = code }

// Trace assigns the correlation ID (accepting one valid X-Request-ID, otherwise
// generating one), echoes it in the response, stores it in the Gin and request
// contexts, and installs the error-code recorder. Register it first.
func Trace() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := requestid.Select(c.Request.Header)
		c.Request = c.Request.WithContext(requestid.WithID(c.Request.Context(), id))
		c.Set(requestIDKey, id)
		c.Header(requestid.Header, id)

		rec := &recorder{ResponseWriter: c.Writer}
		c.Writer = rec
		c.Set(recorderKey, rec)
		c.Next()
	}
}

// RequestID returns the correlation ID for c, or "" when Trace has not run.
func RequestID(c *gin.Context) string { return c.GetString(requestIDKey) }

// ErrorCode returns the REST error code written for c's response, or "" when
// the response was not an error written through httpresponse.WriteError.
func ErrorCode(c *gin.Context) string {
	if rec, ok := c.Get(recorderKey); ok {
		return rec.(*recorder).code
	}
	return ""
}

// AccessLog writes one structured record per completed request containing the
// request ID, status, latency, client IP, method, path without query string and,
// for error responses, the REST error code. It never logs headers, cookies,
// query strings or bodies.
func AccessLog(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		logging.LogAccess(c.Request.Context(), logger, logging.AccessLog{
			Time:      time.Now(),
			RequestID: RequestID(c),
			Status:    c.Writer.Status(),
			Latency:   time.Since(start),
			ClientIP:  c.ClientIP(),
			Method:    c.Request.Method,
			Path:      c.Request.URL.Path,
			ErrorCode: ErrorCode(c),
		})
	}
}
