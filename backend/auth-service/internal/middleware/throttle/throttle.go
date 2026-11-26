// Package throttle rejects requests over a rate limit with 429 RATE_LIMITED.
package throttle

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/middleware/validation"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/shared/ratelimit"
)

// Limits holds the public auth route limits. A nil limiter is not enforced,
// which only tests use; Load always configures every limit.
type Limits struct {
	// LoginClient bounds login attempts from one client address.
	LoginClient *ratelimit.Limiter
	// LoginEmail bounds login attempts for one email address, from any client.
	LoginEmail *ratelimit.Limiter
	// RegisterClient bounds registrations from one client address.
	RegisterClient *ratelimit.Limiter
	// Proxies resolves the client address behind the trusted reverse proxies;
	// nil trusts none and uses the connection's peer address.
	Proxies *ratelimit.TrustedProxies
}

const (
	loginMessage    = "Too many login attempts; try again later."
	registerMessage = "Too many registration attempts; try again later."
)

// LoginByClient limits login attempts per client address. It runs before the
// body is read, so a flood costs no decoding or password hashing.
func (l Limits) LoginByClient() func(*gin.Context) error {
	return limit(l.LoginClient, l.clientKey, loginMessage)
}

// LoginByEmail limits login attempts per email address, so guessing one
// account's password from many addresses is bounded too. It must run after
// validation.ValidateLogin.
func (l Limits) LoginByEmail() func(*gin.Context) error {
	return limit(l.LoginEmail, func(c *gin.Context) string {
		input, ok := validation.LoginInput(c)
		if !ok {
			return ""
		}
		return "email:" + strings.ToLower(input.Email)
	}, loginMessage)
}

// RegisterByClient limits registrations per client address, before the body is read.
func (l Limits) RegisterByClient() func(*gin.Context) error {
	return limit(l.RegisterClient, l.clientKey, registerMessage)
}

func (l Limits) clientKey(c *gin.Context) string {
	return "client:" + ratelimit.KeyByClientIP(l.Proxies)(c.Request)
}

func limit(limiter *ratelimit.Limiter, key func(*gin.Context) string, message string) func(*gin.Context) error {
	return func(c *gin.Context) error {
		if limiter == nil || limiter.Allow(key(c)) {
			return nil
		}
		return &httpresponse.Error{Status: http.StatusTooManyRequests, Code: "RATE_LIMITED", Message: message}
	}
}
