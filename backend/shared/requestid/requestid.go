// Package requestid carries one correlation ID through an HTTP request.
//
// The ID is accepted from a single valid X-Request-ID header or generated, echoed
// in the response, and stored in the request context. shared/logging reads it
// from the context, so any record written with a request context (for example
// logger.InfoContext(ctx, ...)) carries the same request_id. IDs are correlation
// metadata only; they are never proof of identity or authorization.
package requestid

import (
	"context"
	"crypto/rand"
	"net/http"
)

// Header is the request and response header carrying the correlation ID.
const Header = "X-Request-ID"

// MaxLength is the longest accepted ID.
const MaxLength = 128

type contextKey struct{}

// Valid reports whether id is 1 to MaxLength ASCII letters, digits, hyphens,
// underscores or dots. The restricted alphabet keeps IDs safe to log and to
// echo into headers (no CR/LF, spaces or control characters).
func Valid(id string) bool {
	if len(id) == 0 || len(id) > MaxLength {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' {
			continue
		}
		return false
	}
	return true
}

// New returns a fresh random ID.
func New() string { return rand.Text() }

// Select returns the header's ID when exactly one valid value is present, and a
// new ID when the header is missing, invalid or repeated. An invalid ID is
// replaced, never rejected, so a client cannot fail a request by sending one.
func Select(h http.Header) string {
	if values := h.Values(Header); len(values) == 1 && Valid(values[0]) {
		return values[0]
	}
	return New()
}

// WithID returns ctx carrying id.
func WithID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// FromContext returns the request ID in ctx, or "" when none was set.
func FromContext(ctx context.Context) string {
	id, _ := ctx.Value(contextKey{}).(string)
	return id
}

// Middleware assigns the correlation ID, echoes it in the response header and
// stores it in the request context before calling next.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := Select(r.Header)
		w.Header().Set(Header, id)
		next.ServeHTTP(w, r.WithContext(WithID(r.Context(), id)))
	})
}
