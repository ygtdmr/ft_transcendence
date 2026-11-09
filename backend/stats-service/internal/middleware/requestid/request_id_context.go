package requestid

import (
	"context"

	"github.com/gin-gonic/gin"
)

type contextKey struct{}

// Get returns the request ID from Gin context, or an empty string if none is set.
func Get(c *gin.Context) string {
	return c.GetString(ginContextKey)
}

// FromContext returns the request ID, or an empty string if the middleware has not set one.
func FromContext(ctx context.Context) string {
	id, _ := ctx.Value(contextKey{}).(string)
	return id
}
