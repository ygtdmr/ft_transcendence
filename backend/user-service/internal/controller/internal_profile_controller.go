package controller

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/middleware/serviceauth"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/router"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

// CanonicalProfileReader returns the current profile for one user.
type CanonicalProfileReader interface {
	CanonicalProfile(ctx context.Context, userID int64) (service.CanonicalProfile, error)
}

type InternalProfileController struct {
	profiles CanonicalProfileReader
	auth     *serviceauth.Authenticator
}

// NewInternalProfile returns the controller for the private canonical profile read.
func NewInternalProfile(profiles CanonicalProfileReader, auth *serviceauth.Authenticator) *InternalProfileController {
	return &InternalProfileController{profiles: profiles, auth: auth}
}

// RegisterRoutes adds GET /internal/v1/profiles/{user_id}. Register it only on
// the private listener.
func (h *InternalProfileController) RegisterRoutes(routes gin.IRoutes) {
	routes.GET("/internal/v1/profiles/:user_id",
		h.auth.Require(serviceauth.ScopeProfileRead), router.Wrap(h.read, mapProfileReadError))
}

// read returns user_id, username, avatar_url and created_at. It never returns
// credentials, the login email, presence, statistics or relationships.
func (h *InternalProfileController) read(c *gin.Context) error {
	userID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || userID <= 0 {
		return &service.ValidationError{Field: "user_id", Reason: "must be a positive integer"}
	}
	profile, err := h.profiles.CanonicalProfile(c.Request.Context(), userID)
	if errors.Is(err, service.ErrUserNotFound) {
		return &httpresponse.Error{Status: http.StatusNotFound, Code: "USER_NOT_FOUND",
			Message: "No profile exists for this user.", Details: map[string]any{"user_id": userID}}
	}
	if err != nil {
		return err
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"user_id":    profile.UserID,
		"username":   profile.Username,
		"avatar_url": profile.AvatarURL,
		"created_at": profile.CreatedAt.UTC().Format(receiptTimeLayout),
	}})

	return nil
}

// mapProfileReadError reuses the private contract's common error responses.
func mapProfileReadError(err error) *httpresponse.Error {
	return mapProvisioningError(err)
}
