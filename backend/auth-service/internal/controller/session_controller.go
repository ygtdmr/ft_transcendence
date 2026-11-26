package controller

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/middleware/authentication"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/middleware/throttle"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/middleware/validation"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/router"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/security"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
)

// SessionService provides cookie-backed authentication and session operations.
type SessionService interface {
	Login(context.Context, service.LoginInput) (service.AuthResult, error)
	Authenticate(context.Context, string) (service.Identity, error)
	CurrentSession(context.Context, service.Identity) (service.SessionResult, error)
	Refresh(context.Context, string) (security.Credentials, error)
	Logout(context.Context, service.Identity) error
}

type Option func(*AuthController)

// WithSessions enables login, refresh, current-session and logout routes.
func WithSessions(sessions SessionService) Option {
	return func(controller *AuthController) {
		controller.sessions = sessions
	}
}

// WithRateLimits enforces the login and registration limits.
func WithRateLimits(limits throttle.Limits) Option {
	return func(controller *AuthController) {
		controller.limits = limits
	}
}

func (authController *AuthController) registerSessionRoutes(routes gin.IRoutes) {
	guard := authentication.Middleware(authController.sessions)
	routes.POST("/login", router.Wrap(authController.limits.LoginByClient(), mapAuthError),
		router.Wrap(validation.ValidateLogin, mapAuthError), router.Wrap(authController.limits.LoginByEmail(), mapAuthError),
		router.Wrap(authController.Login, mapAuthError))
	routes.POST("/refresh", router.Wrap(authController.Refresh, mapAuthError))
	routes.GET("/session", guard, router.Wrap(authController.CurrentSession, mapAuthError))
	routes.DELETE("/session", guard, router.Wrap(authController.Logout, mapAuthError))
}

// Login verifies credentials and issues a fresh session through secure cookies.
func (authController *AuthController) Login(c *gin.Context) error {
	input, ok := validation.LoginInput(c)
	if !ok {
		return errors.New("validated login input is missing")
	}
	result, err := authController.sessions.Login(c.Request.Context(), input)
	if err != nil {
		return err
	}
	setCredentials(c.Writer, result.Credentials)
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"user": result.User, "access_token_expires_at": result.Credentials.AccessExpiresAt,
	}})
	return nil
}

// CurrentSession returns the authenticated profile and live room membership.
func (authController *AuthController) CurrentSession(c *gin.Context) error {
	identity, ok := authentication.Get(c)
	if !ok {
		return service.ErrUnauthorized
	}
	result, err := authController.sessions.CurrentSession(c.Request.Context(), identity)
	if err != nil {
		return err
	}
	c.JSON(http.StatusOK, gin.H{"data": result})
	return nil
}

// Refresh rotates the refresh cookie and reissues both credentials.
func (authController *AuthController) Refresh(c *gin.Context) error {
	if err := requireEmptyBody(c.Request); err != nil {
		return err
	}
	raw, err := c.Cookie("ft_refresh")
	if err != nil || raw == "" {
		return service.ErrSessionExpired
	}
	credentials, err := authController.sessions.Refresh(c.Request.Context(), raw)
	if err != nil {
		return err
	}
	setCredentials(c.Writer, credentials)
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"access_token_expires_at": credentials.AccessExpiresAt}})
	return nil
}

// Logout revokes the current session and clears cookies on their original paths.
func (authController *AuthController) Logout(c *gin.Context) error {
	identity, ok := authentication.Get(c)
	if !ok {
		return service.ErrUnauthorized
	}
	if err := authController.sessions.Logout(c.Request.Context(), identity); err != nil {
		return err
	}
	clearCredentials(c.Writer)
	c.Status(http.StatusNoContent)
	return nil
}

func requireEmptyBody(request *http.Request) error {
	if request.Body == nil {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, 1))
	if err != nil || len(body) != 0 {
		return validation.ErrInvalidLoginRequest
	}
	return nil
}
