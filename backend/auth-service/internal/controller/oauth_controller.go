package controller

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
)

const (
	oauthCookieName = "ft_oauth"
	oauthCookiePath = "/api/v1/auth/oauth/google"
)

// GoogleStarter prepares the redirect to Google and completes the callback.
type GoogleStarter interface {
	StartGoogle(ctx context.Context, returnTo string) (service.GoogleAuthorization, error)
	CompleteGoogle(ctx context.Context, callback service.GoogleCallback) (service.AuthResult, string, error)
}

// WithGoogleOAuth adds GET /oauth/google/authorize and /oauth/google/callback.
func WithGoogleOAuth(starter GoogleStarter, logger *slog.Logger) Option {
	return func(c *AuthController) {
		c.google = starter
		c.logger = logger
	}
}

// startGoogle answers 302 to Google with the ft_oauth cookie, or 302 to
// /?oauth_error=OAUTH_PROVIDER_ERROR when sign-in cannot start. It is a
// browser navigation, so errors are redirects, never JSON.
func (authController *AuthController) startGoogle(c *gin.Context) {
	authorization, err := authController.google.StartGoogle(c.Request.Context(), c.Query("return_to"))
	if err != nil {
		if authController.logger != nil {
			authController.logger.WarnContext(c.Request.Context(), "oauth_google_authorize_failed", "error", err)
		}
		clearCredentialCookie(c.Writer, oauthCookieName, oauthCookiePath)
		c.Redirect(http.StatusFound, "/?oauth_error="+url.QueryEscape("OAUTH_PROVIDER_ERROR"))
		return
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name: oauthCookieName, Value: authorization.Cookie, Path: oauthCookiePath,
		Expires: authorization.ExpiresAt, MaxAge: int(service.OAuthStateLifetime.Seconds()),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	c.Redirect(http.StatusFound, authorization.RedirectURL)
}

// completeGoogle finishes Google sign-in. Success sets ft_session and
// ft_refresh like Login, clears ft_oauth and answers 302 to return_to.
// Every failure clears ft_oauth and answers 302 to /?oauth_error=<code>
// without session cookies. No token ever appears in the URL or body.
func (authController *AuthController) completeGoogle(c *gin.Context) {
	cookie, _ := c.Cookie(oauthCookieName)
	result, returnTo, err := authController.google.CompleteGoogle(c.Request.Context(), service.GoogleCallback{
		Cookie: cookie, State: c.Query("state"), Code: c.Query("code"), Error: c.Query("error"),
	})
	clearCredentialCookie(c.Writer, oauthCookieName, oauthCookiePath)
	if err != nil {
		code := oauthErrorCode(err)
		if authController.logger != nil && code == "OAUTH_PROVIDER_ERROR" {
			authController.logger.WarnContext(c.Request.Context(), "oauth_google_callback_failed", "error", err)
		}
		c.Redirect(http.StatusFound, "/?oauth_error="+url.QueryEscape(code))
		return
	}
	setCredentials(c.Writer, result.Credentials)
	c.Redirect(http.StatusFound, returnTo)
}

func oauthErrorCode(err error) string {
	switch {
	case errors.Is(err, service.ErrOAuthCancelled):
		return "OAUTH_CANCELLED"
	case errors.Is(err, service.ErrOAuthStateInvalid):
		return "OAUTH_STATE_INVALID"
	case errors.Is(err, service.ErrOAuthEmailUnverified):
		return "OAUTH_EMAIL_UNVERIFIED"
	default:
		return "OAUTH_PROVIDER_ERROR"
	}
}
