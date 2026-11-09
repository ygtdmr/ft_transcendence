// Package requestid correlates HTTP responses, request contexts and access logs.
package requestid

import (
	"context"

	"github.com/gin-gonic/gin"
)

// Header is the request and response header used for correlation IDs.
const Header = "X-Request-ID"

const ginContextKey = "request_id"

// Middleware assigns a correlation ID before running downstream handlers.
//
// It returns a Gin handler that accepts one valid X-Request-ID header or generates
// a random ID when the header is missing, invalid or repeated. Accepted IDs contain
// 1 to 128 ASCII letters, digits, hyphens, underscores or dots.
//
// The ID is written to the response header and both Gin and request contexts.
// Invalid IDs are replaced without rejecting the request. IDs are correlation
// metadata, not proof of identity or authorization.
func Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := selectID(c.Request.Header)
		ctx := context.WithValue(c.Request.Context(), contextKey{}, id)
		c.Request = c.Request.WithContext(ctx)

		c.Set(ginContextKey, id)
		c.Header(Header, id)
		c.Next()
	}
}
