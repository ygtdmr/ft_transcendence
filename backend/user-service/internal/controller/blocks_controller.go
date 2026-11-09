package controller

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/middleware/sessionauth"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/router"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

// BlockService answers the block routes.
type BlockService interface {
	Block(ctx context.Context, blockerID, targetID int64) (time.Time, error)
	Unblock(ctx context.Context, blockerID, targetID int64) error
}

// BlocksController serves the public block routes.
type BlocksController struct {
	blocks   BlockService
	sessions sessionauth.Validator
}

// NewBlocks returns the controller for /api/v1/users/{user_id}/block.
func NewBlocks(blocks BlockService, sessions sessionauth.Validator) *BlocksController {
	return &BlocksController{blocks: blocks, sessions: sessions}
}

// RegisterRoutes adds the block routes behind session validation.
func (h *BlocksController) RegisterRoutes(routes gin.IRoutes) {
	const path = "/api/v1/users/:user_id/block"
	auth := sessionauth.Require(h.sessions)
	routes.PUT(path, auth, router.Wrap(h.block, mapFriendError))
	routes.DELETE(path, auth, router.Wrap(h.unblock, mapFriendError))
}

func (h *BlocksController) block(c *gin.Context) error {
	targetID, err := blockTarget(c)
	if err != nil {
		return err
	}
	if err := rejectBody(c); err != nil {
		return err
	}
	blockedAt, err := h.blocks.Block(c.Request.Context(), sessionauth.UserID(c), targetID)
	if err != nil {
		return blockError(err, targetID)
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"user_id":    targetID,
		"blocked_at": blockedAt.UTC().Format(receiptTimeLayout),
	}})

	return nil
}

func (h *BlocksController) unblock(c *gin.Context) error {
	targetID, err := blockTarget(c)
	if err != nil {
		return err
	}
	if err := h.blocks.Unblock(c.Request.Context(), sessionauth.UserID(c), targetID); err != nil {
		return blockError(err, targetID)
	}
	c.Status(http.StatusNoContent)
	c.Writer.WriteHeaderNow()

	return nil
}

func blockTarget(c *gin.Context) (int64, error) {
	targetID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || targetID <= 0 {
		return 0, &service.ValidationError{Field: "user_id", Reason: "must be a positive integer"}
	}

	return targetID, nil
}

func blockError(err error, targetID int64) error {
	switch {
	case errors.Is(err, service.ErrCannotBlockSelf):
		return &httpresponse.Error{Status: http.StatusUnprocessableEntity, Code: "VALIDATION_ERROR",
			Message: "You cannot block yourself.", Details: map[string]any{"field": "user_id"}}
	case errors.Is(err, service.ErrUserNotFound):
		return &httpresponse.Error{Status: http.StatusNotFound, Code: "USER_NOT_FOUND",
			Message: "No user exists with this ID.", Details: map[string]any{"user_id": targetID}}
	default:
		return err
	}
}
