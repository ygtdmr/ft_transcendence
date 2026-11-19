package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/middleware/sessionauth"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/router"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

const maxProfileUpdateBody = 1 << 10

// UsernameService renames the session owner.
type UsernameService interface {
	Rename(ctx context.Context, userID int64, username string) error
}

// ProfileUpdateController serves PATCH /api/v1/users/me.
type ProfileUpdateController struct {
	usernames UsernameService
	profiles  OwnProfileService
	sessions  sessionauth.Validator
}

// NewProfileUpdate returns the profile update controller. profiles composes
// the response, which has the Get own profile shape.
func NewProfileUpdate(usernames UsernameService, profiles OwnProfileService,
	sessions sessionauth.Validator) *ProfileUpdateController {
	return &ProfileUpdateController{usernames: usernames, profiles: profiles, sessions: sessions}
}

// RegisterRoutes adds the profile update route behind session validation.
func (h *ProfileUpdateController) RegisterRoutes(routes gin.IRoutes) {
	routes.PATCH("/api/v1/users/me", sessionauth.Require(h.sessions), router.Wrap(h.update, mapFriendError))
}

// update applies an optional username change, then answers with the own
// profile. Only username is editable here: any other field is 400.
func (h *ProfileUpdateController) update(c *gin.Context) error {
	var body struct {
		Username json.RawMessage `json:"username"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, maxProfileUpdateBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return malformed("body", "Body must be one JSON object with an optional username.")
	}
	userID := sessionauth.UserID(c)
	if body.Username != nil {
		var username string
		if bytes.Equal(body.Username, []byte("null")) || json.Unmarshal(body.Username, &username) != nil {
			return mapFriendError(service.ErrInvalidUsername)
		}
		if err := h.usernames.Rename(c.Request.Context(), userID, username); err != nil {
			return renameError(err)
		}
	}
	profile, err := h.profiles.Read(c.Request.Context(), userID)
	if errors.Is(err, service.ErrOwnerInactive) {
		return sessionauth.Unauthorized()
	}
	if err != nil {
		return err
	}
	writeOwnProfile(c, profile)

	return nil
}

// renameError answers 401 for an owner without an active profile and 503 for
// a commit whose outcome is unknown; a retry of the same rename is safe.
func renameError(err error) error {
	var deleted *service.UserDeletedError
	switch {
	case errors.As(err, &deleted), errors.Is(err, service.ErrUserNotFound):
		return sessionauth.Unauthorized()
	case errors.Is(err, service.ErrOutcomeUnknown):
		return &httpresponse.Error{Status: http.StatusServiceUnavailable, Code: "SERVICE_UNAVAILABLE",
			Message: "The service is temporarily unavailable."}
	}

	return err
}
