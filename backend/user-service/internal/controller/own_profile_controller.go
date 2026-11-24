package controller

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/middleware/sessionauth"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/router"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

// OwnProfileService reads the session owner's profile.
type OwnProfileService interface {
	Read(ctx context.Context, userID int64) (service.OwnProfile, error)
}

// OwnProfileController serves GET /api/v1/users/me.
type OwnProfileController struct {
	profiles OwnProfileService
	sessions sessionauth.Validator
}

// NewOwnProfile returns the own profile controller.
func NewOwnProfile(profiles OwnProfileService, sessions sessionauth.Validator) *OwnProfileController {
	return &OwnProfileController{profiles: profiles, sessions: sessions}
}

// RegisterRoutes adds the own profile route behind session validation. The
// static /me segment takes precedence over /api/v1/users/{user_id}.
func (h *OwnProfileController) RegisterRoutes(routes gin.IRoutes) {
	routes.GET("/api/v1/users/me", sessionauth.Require(h.sessions), router.Wrap(h.read, mapFriendError))
}

func (h *OwnProfileController) read(c *gin.Context) error {
	profile, err := h.profiles.Read(c.Request.Context(), sessionauth.UserID(c))
	if errors.Is(err, service.ErrOwnerInactive) {
		return sessionauth.Unauthorized()
	}
	if err != nil {
		return err
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"user_id":                 profile.UserID,
		"username":                profile.Username,
		"email":                   profile.Email,
		"avatar_url":              profile.AvatarURL,
		"level":                   profile.Stats.Level,
		"wins":                    profile.Stats.Wins,
		"losses":                  profile.Stats.Losses,
		"matches_played":          profile.Stats.MatchesPlayed,
		"penalty_points":          profile.Stats.PenaltyPoints,
		"active_room_id":          profile.Membership.RoomID,
		"active_room_api_version": profile.Membership.APIVersion,
		"created_at":              profile.CreatedAt.UTC().Format(receiptTimeLayout),
	}})

	return nil
}
