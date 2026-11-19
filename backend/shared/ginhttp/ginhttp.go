// Package ginhttp holds the Gin middleware every backend service shares:
// request correlation IDs and trace context, a response recorder that learns the
// REST error code, an optional request timeout, and structured access logging.
// It replaces the copies each service carries.
//
// Order matters; register them in this order:
//
//	router.Use(ginhttp.Trace(), ginhttp.AccessLog(logger), metrics.Middleware(service))
//	api := router.Group("/api/v1", ginhttp.Timeout(10*time.Second)) // not on WebSocket routes
package ginhttp

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/shared/logging"
	"github.com/melmut-42/ft_transcendence/backend/shared/requestcontext"
	"github.com/melmut-42/ft_transcendence/backend/shared/requestid"
	"github.com/melmut-42/ft_transcendence/backend/shared/tracecontext"
)

const (
	requestIDKey = "request_id"
	traceKey     = "ginhttp.trace"
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

// Trace prepares the request context (see requestcontext.Begin): it assigns the
// correlation ID (accepting one valid X-Request-ID, otherwise generating one),
// starts this service's span under a valid traceparent or a new root trace,
// echoes the ID in the response, stores everything in the Gin and request
// contexts, cancels the context when the request ends, and installs the
// error-code recorder. Register it first.
func Trace() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel, id, trace := requestcontext.Begin(c.Request, 0)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Set(requestIDKey, id)
		c.Set(traceKey, trace)
		c.Header(requestid.Header, id)

		rec := &recorder{ResponseWriter: c.Writer}
		c.Writer = rec
		c.Set(recorderKey, rec)
		c.Next()
	}
}

// Timeout cancels the request context after d. Handlers and everything they call
// with c.Request.Context() (database, Transport clients, publishes) stop when it
// passes. If the deadline passed and nothing was written, Timeout answers 503
// SERVICE_UNAVAILABLE instead of letting Gin send an empty 200. d <= 0 disables
// it. The duration is set by the service, never by the client. Do not apply it to
// long-lived routes such as WebSocket upgrades.
func Timeout(d time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		if d <= 0 {
			c.Next()
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), d)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
		if ctx.Err() == context.DeadlineExceeded && !c.Writer.Written() {
			c.Abort()
			httpresponse.WriteError(c.Writer, &httpresponse.Error{
				Status:  http.StatusServiceUnavailable,
				Code:    "SERVICE_UNAVAILABLE",
				Message: "The request could not be completed in time.",
			})
		}
	}
}

// TraceContext returns this request's span, or false when Trace has not run.
func TraceContext(c *gin.Context) (tracecontext.Context, bool) {
	if v, ok := c.Get(traceKey); ok {
		return v.(tracecontext.Context), true
	}
	return tracecontext.Context{}, false
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

// Handle adapts a Gin handler that reports failure by returning an error. The
// error is mapped to its public response (see httpresponse.Mapper): validation
// errors and *httpresponse.Error pass through, registered errors are translated,
// and anything unexpected becomes the generic 500 and is logged by Go type only,
// never by text, which may hold secrets. m and logger may be nil.
//
// The handler must not have written a response when it returns an error. The
// raw error is deliberately not attached with c.Error, so no Gin-level output
// can print it.
func Handle(fn func(*gin.Context) error, m *httpresponse.Mapper, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		err := fn(c)
		if err == nil {
			return
		}
		response := httpresponse.ResolveAndLog(c.Request.Context(), err, m, logger)
		c.Abort()
		if !c.Writer.Written() {
			httpresponse.WriteError(c.Writer, response)
		}
	}
}

// Wrap adapts a net/http middleware (serviceauth.Require, identity.Authenticate,
// identity.Authorize, ratelimit.Middleware) to Gin. If the middleware answers the
// request itself (a 401, 403, 429...) the Gin chain is aborted; if it lets the
// request through, the possibly updated request (carrying the identity or caller
// in its context) is what the rest of the chain sees. Errors are written through
// c.Writer, so ErrorCode and the metrics still see them.
func Wrap(mw func(http.Handler) http.Handler) gin.HandlerFunc {
	return func(c *gin.Context) {
		passed := false
		next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			passed = true
			c.Request = r
			c.Next()
		})
		mw(next).ServeHTTP(c.Writer, c.Request)
		if !passed {
			c.Abort()
		}
	}
}
