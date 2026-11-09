package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/middleware/sessionauth"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/router"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

// FriendService answers the friend list and removal routes.
type FriendService interface {
	List(ctx context.Context, userID int64, limit, offset int) (service.FriendPage, error)
	Remove(ctx context.Context, userID, friendID int64) error
}

// FriendsController serves the public friend list and removal routes.
type FriendsController struct {
	friends  FriendService
	sessions sessionauth.Validator
}

// NewFriends returns the controller for /api/v1/friends.
func NewFriends(friends FriendService, sessions sessionauth.Validator) *FriendsController {
	return &FriendsController{friends: friends, sessions: sessions}
}

// RegisterRoutes adds the friend routes behind session validation.
func (h *FriendsController) RegisterRoutes(routes gin.IRoutes) {
	const path = "/api/v1/friends"
	auth := sessionauth.Require(h.sessions)
	routes.GET(path, auth, router.Wrap(h.list, mapFriendError))
	routes.DELETE(path+"/:user_id", auth, router.Wrap(h.remove, mapFriendError))
}

func (h *FriendsController) list(c *gin.Context) error {
	query := c.Request.URL.Query()
	limit, err := pageParameter(query, "limit", service.DefaultFriendPageLimit)
	if err != nil {
		return err
	}
	offset, err := pageParameter(query, "offset", 0)
	if err != nil {
		return err
	}
	page, err := h.friends.List(c.Request.Context(), sessionauth.UserID(c), limit, offset)
	if err != nil {
		return err
	}
	friends := make([]gin.H, 0, len(page.Friends))
	for _, friend := range page.Friends {
		friends = append(friends, gin.H{
			"user_id":    friend.UserID,
			"username":   friend.Username,
			"avatar_url": friend.AvatarURL,
			"is_online":  friend.IsOnline,
		})
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"friend_count": page.Total,
		"limit":        page.Limit,
		"offset":       page.Offset,
		"has_more":     page.HasMore,
		"friends":      friends,
	}})

	return nil
}

func (h *FriendsController) remove(c *gin.Context) error {
	friendID, err := pathUserID(c)
	if err != nil {
		return err
	}
	if err := h.friends.Remove(c.Request.Context(), sessionauth.UserID(c), friendID); err != nil {
		if errors.Is(err, service.ErrNotFriends) {
			return &httpresponse.Error{Status: http.StatusNotFound, Code: "NOT_FRIENDS",
				Message: fmt.Sprintf("User %d is not a friend.", friendID), Details: map[string]any{"user_id": friendID}}
		}
		return err
	}
	c.Status(http.StatusNoContent)
	c.Writer.WriteHeaderNow()

	return nil
}

// pageParameter returns fallback when name is absent. An empty, repeated or
// non-integer value is a field error.
func pageParameter(query url.Values, name string, fallback int) (int, error) {
	values, present := query[name]
	if !present {
		return fallback, nil
	}
	invalid := &service.ValidationError{Field: name, Reason: "must be one integer"}
	if len(values) != 1 {
		return 0, invalid
	}
	value, err := strconv.Atoi(values[0])
	if err != nil {
		return 0, invalid
	}

	return value, nil
}
