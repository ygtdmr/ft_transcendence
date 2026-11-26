// Package throttle rejects requests over a per-user rate limit with 429 RATE_LIMITED.
package throttle

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/shared/ratelimit"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/middleware/sessionauth"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/router"
)

// PerUser limits requests per session owner. It must run after
// sessionauth.Require, and before the body is read, so a limited request has
// no side effect. A nil limiter is not enforced.
func PerUser(limiter *ratelimit.Limiter, message string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if limiter == nil || limiter.Allow("user:"+strconv.FormatInt(sessionauth.UserID(c), 10)) {
			c.Next()
			return
		}
		router.WriteError(c, &httpresponse.Error{Status: http.StatusTooManyRequests, Code: "RATE_LIMITED", Message: message})
	}
}
