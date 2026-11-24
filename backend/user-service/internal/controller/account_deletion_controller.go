package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/middleware/sessionauth"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/router"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

const (
	maxDeletionBody = 1 << 10
	// deletionRequestTimeout bounds the workflow run inside the request. The
	// worker resumes an operation that does not finish in time.
	deletionRequestTimeout = 20 * time.Second
)

// AccountDeletionService coordinates the deletion of the session owner.
type AccountDeletionService interface {
	Request(ctx context.Context, userID int64, confirmUsername string) error
}

// AccountDeletionController serves DELETE /api/v1/users/me.
type AccountDeletionController struct {
	deletions AccountDeletionService
	sessions  sessionauth.Validator
}

// NewAccountDeletion returns the account deletion controller.
func NewAccountDeletion(deletions AccountDeletionService, sessions sessionauth.Validator) *AccountDeletionController {
	return &AccountDeletionController{deletions: deletions, sessions: sessions}
}

// RegisterRoutes adds the account deletion route behind session validation.
func (h *AccountDeletionController) RegisterRoutes(routes gin.IRoutes) {
	routes.DELETE("/api/v1/users/me", sessionauth.Require(h.sessions), router.Wrap(h.remove, mapFriendError))
}

type deletionRequest struct {
	ConfirmUsername *string `json:"confirm_username"`
}

// remove answers 204 once every participant has finished. When the operation
// is recorded but not finished it answers 503; both answers clear the session
// cookies, because Auth revokes every session when the deletion begins.
func (h *AccountDeletionController) remove(c *gin.Context) error {
	var body deletionRequest
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, maxDeletionBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return malformed("body", "Body must be one JSON object with confirm_username.")
	}
	confirm := ""
	if body.ConfirmUsername != nil {
		confirm = *body.ConfirmUsername
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), deletionRequestTimeout)
	defer cancel()
	err := h.deletions.Request(ctx, sessionauth.UserID(c), confirm)
	switch {
	case errors.Is(err, service.ErrConfirmationMismatch):
		return &httpresponse.Error{Status: http.StatusUnprocessableEntity, Code: "CONFIRMATION_MISMATCH",
			Message: "confirm_username must match your current username.", Details: map[string]any{"field": "confirm_username"}}
	case errors.Is(err, service.ErrDeletionInProgress):
		clearSessionCookies(c)
		return &httpresponse.Error{Status: http.StatusServiceUnavailable, Code: "SERVICE_UNAVAILABLE",
			Message: "Account deletion has started and will finish shortly."}
	case err != nil:
		return err
	}
	clearSessionCookies(c)
	c.Status(http.StatusNoContent)
	c.Writer.WriteHeaderNow()

	return nil
}

// clearSessionCookies expires ft_session and ft_refresh on their original paths.
func clearSessionCookies(c *gin.Context) {
	for _, cookie := range []struct{ name, path string }{{"ft_session", "/"}, {"ft_refresh", "/api/v1/auth/refresh"}} {
		http.SetCookie(c.Writer, &http.Cookie{Name: cookie.name, Value: "", Path: cookie.path, MaxAge: -1,
			HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	}
}
