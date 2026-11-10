package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/middleware/sessionauth"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/router"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

// PublicProfileService reads one user's public profile for a viewer.
type PublicProfileService interface {
	Read(ctx context.Context, viewerID, userID int64) (service.PublicProfile, error)
}

// PublicProfileController serves GET /api/v1/users/{user_id}.
type PublicProfileController struct {
	profiles PublicProfileService
	sessions sessionauth.Validator
}

// NewPublicProfile returns the public profile controller.
func NewPublicProfile(profiles PublicProfileService, sessions sessionauth.Validator) *PublicProfileController {
	return &PublicProfileController{profiles: profiles, sessions: sessions}
}

// RegisterRoutes adds the public profile route behind session validation.
func (h *PublicProfileController) RegisterRoutes(routes gin.IRoutes) {
	routes.GET("/api/v1/users/:user_id", sessionauth.Require(h.sessions), router.Wrap(h.read, mapFriendError))
}

func (h *PublicProfileController) read(c *gin.Context) error {
	userID, err := pathUserID(c)
	if err != nil {
		return err
	}
	profile, err := h.profiles.Read(c.Request.Context(), sessionauth.UserID(c), userID)
	if errors.Is(err, service.ErrUserNotFound) {
		return &httpresponse.Error{Status: http.StatusNotFound, Code: "USER_NOT_FOUND",
			Message: fmt.Sprintf("User %d was not found.", userID), Details: map[string]any{"user_id": userID}}
	}
	if err != nil {
		return err
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"user_id":           profile.UserID,
		"username":          profile.Username,
		"avatar_url":        profile.AvatarURL,
		"level":             profile.Stats.Level,
		"wins":              profile.Stats.Wins,
		"losses":            profile.Stats.Losses,
		"matches_played":    profile.Stats.MatchesPlayed,
		"is_online":         profile.IsOnline,
		"relationship":      profile.Relationship,
		"friend_request_id": profile.FriendRequestID,
	}})

	return nil
}
