package controller

import (
	"context"
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

// GoogleStarter prepares the redirect to Google.
type GoogleStarter interface {
	StartGoogle(ctx context.Context, returnTo string) (service.GoogleAuthorization, error)
}

// WithGoogleOAuth adds GET /oauth/google/authorize to the auth routes.
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
