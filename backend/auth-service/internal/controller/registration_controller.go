package controller

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/middleware/validation"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/router"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
)

type RegistrationService interface {
	Register(context.Context, service.RegisterInput) (service.AuthResult, error)
}

type AuthController struct {
	service  RegistrationService
	sessions SessionService
	google   GoogleStarter
	logger   *slog.Logger
}

// NewAuth returns a controller backed by an auth service.
func NewAuth(service RegistrationService, options ...Option) *AuthController {
	controller := &AuthController{service: service}
	controller.sessions, _ = service.(SessionService)
	for _, option := range options {
		option(controller)
	}
	return controller
}

// RegisterRoutes installs auth routes on a group mounted at /api/v1/auth.
func (authController *AuthController) RegisterRoutes(routes gin.IRoutes) {
	routes.Use(disableAuthCaching)
	routes.POST("/register", router.Wrap(validation.ValidateRegister, mapAuthError),
		router.Wrap(authController.Register, mapAuthError))
	if authController.sessions != nil {
		authController.registerSessionRoutes(routes)
	}
	if authController.google != nil {
		routes.GET("/oauth/google/authorize", authController.startGoogle)
		routes.GET("/oauth/google/callback", authController.completeGoogle)
	}
}

func disableAuthCaching(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
}

// Register handles account registration and writes the HTTP response.
//
// Parameters:
//
//   - c: Gin context containing input from ValidateRegister middleware and the response writer; credentials are sent as cookies after commit.
//
// It writes HTTP 201 with profile data and returns nil on success.
// Errors are returned to the router wrapper, which writes the HTTP error response.
//
// Errors:
//
//   - 422 INVALID_EMAIL: Email is missing or invalid.
//   - 422 INVALID_USERNAME: Username is missing or invalid.
//   - 422 INVALID_PASSWORD: Password is missing or invalid.
//   - 409 EMAIL_TAKEN: Another account already uses the email.
//   - 409 USERNAME_TAKEN: Another account already uses the username.
//   - 500 INTERNAL_ERROR: An unexpected failure occurred; internal details are omitted.
func (authController *AuthController) Register(c *gin.Context) error {
	input, ok := validation.RegisterInput(c)
	if !ok {
		return errors.New("validated registration input is missing")
	}

	result, err := authController.service.Register(c.Request.Context(), input)
	if err != nil {
		return err
	}

	setCredentials(c.Writer, result.Credentials)

	c.JSON(http.StatusCreated, gin.H{"data": gin.H{
		"user": result.User, "access_token_expires_at": result.Credentials.AccessExpiresAt,
	}})
	return nil
}

func setCredentialCookie(writer http.ResponseWriter, name, value, path string, expires time.Time) {
	http.SetCookie(writer, &http.Cookie{
		Name: name, Value: value, Path: path, Expires: expires,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
}
