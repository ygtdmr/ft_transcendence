// Package sessionauth identifies the signed-in user of a public request.
//
// The ft_session cookie is the only browser credential. Auth Service decides
// whether it is valid; this service never reads the token or trusts a browser
// supplied user id.
package sessionauth

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/client/authclient"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/router"
)

const userKey = "sessionauth.user_id"

// Validator asks Auth who holds a session credential.
type Validator interface {
	ValidateSession(ctx context.Context, session string) (int64, error)
}

// Require admits only requests with a session Auth accepts.
//
// It answers 401 UNAUTHORIZED for a missing or refused session and 503
// SERVICE_UNAVAILABLE when Auth cannot answer, so an outage is never reported
// as a signed-out user.
func Require(validator Validator) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, err := c.Cookie("ft_session")
		if err != nil || raw == "" {
			router.WriteError(c, Unauthorized())
			return
		}
		userID, err := validator.ValidateSession(c.Request.Context(), raw)
		switch {
		case err == nil:
			c.Set(userKey, userID)
			c.Next()
		case errors.Is(err, authclient.ErrSessionInvalid):
			router.WriteError(c, Unauthorized())
		case errors.Is(err, authclient.ErrUnavailable):
			router.WriteError(c, &httpresponse.Error{Status: http.StatusServiceUnavailable,
				Code: "SERVICE_UNAVAILABLE", Message: "The service is temporarily unavailable."})
		default:
			_ = c.Error(err)
			router.WriteError(c, httpresponse.InternalError())
		}
	}
}

// UserID returns the user identified by Require.
func UserID(c *gin.Context) int64 {
	return c.GetInt64(userKey)
}

// Unauthorized is the response for a caller without a usable session.
func Unauthorized() *httpresponse.Error {
	return &httpresponse.Error{Status: http.StatusUnauthorized, Code: "UNAUTHORIZED",
		Message: "A valid active session is required."}
}
