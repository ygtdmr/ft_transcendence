package controller

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/middleware/sessionauth"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/router"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

// UserSearchService answers the username search route.
type UserSearchService interface {
	Search(ctx context.Context, viewerID int64, query string, limit, offset int) (service.UserSearchPage, error)
}

// UserSearchController serves the public username search route.
type UserSearchController struct {
	search   UserSearchService
	sessions sessionauth.Validator
}

// NewUserSearch returns the controller for /api/v1/users/search.
func NewUserSearch(search UserSearchService, sessions sessionauth.Validator) *UserSearchController {
	return &UserSearchController{search: search, sessions: sessions}
}

// RegisterRoutes adds the search route behind session validation.
func (h *UserSearchController) RegisterRoutes(routes gin.IRoutes) {
	routes.GET("/api/v1/users/search", sessionauth.Require(h.sessions), router.Wrap(h.read, mapFriendError))
}

func (h *UserSearchController) read(c *gin.Context) error {
	query := c.Request.URL.Query()
	values := query["q"]
	if len(values) != 1 {
		return &service.ValidationError{Field: "q", Reason: "must be 1 to 20 characters"}
	}
	limit, err := pageParameter(query, "limit", service.DefaultUserSearchLimit)
	if err != nil {
		return err
	}
	offset, err := pageParameter(query, "offset", 0)
	if err != nil {
		return err
	}
	page, err := h.search.Search(c.Request.Context(), sessionauth.UserID(c), values[0], limit, offset)
	if err != nil {
		return err
	}
	results := make([]gin.H, 0, len(page.Results))
	for _, result := range page.Results {
		results = append(results, gin.H{
			"user_id":           result.UserID,
			"username":          result.Username,
			"avatar_url":        result.AvatarURL,
			"relationship":      result.Relationship,
			"friend_request_id": result.FriendRequestID,
		})
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"limit":    page.Limit,
		"offset":   page.Offset,
		"has_more": page.HasMore,
		"results":  results,
	}})

	return nil
}
