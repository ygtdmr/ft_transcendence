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
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/middleware/serviceauth"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/router"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/service"
)

const (
	maxCleanupBody    = 1 << 10
	receiptTimeLayout = "2006-01-02T15:04:05.000000Z07:00"
)

// AccountCleaner applies account deletion cleanup.
type AccountCleaner interface {
	CleanUp(ctx context.Context, operationID string, userID int64) (service.CleanupReceipt, bool, error)
}

// InternalCleanupController serves the private cleanup route.
type InternalCleanupController struct {
	cleanup AccountCleaner
	auth    *serviceauth.Authenticator
}

// NewInternalCleanup returns the private cleanup controller.
func NewInternalCleanup(cleanup AccountCleaner, auth *serviceauth.Authenticator) *InternalCleanupController {
	return &InternalCleanupController{cleanup: cleanup, auth: auth}
}

// RegisterRoutes adds PUT /internal/v1/account-cleanups/{operation_id}.
func (h *InternalCleanupController) RegisterRoutes(routes gin.IRoutes) {
	routes.PUT("/internal/v1/account-cleanups/:operation_id", h.auth.Require(serviceauth.ScopeAccountCleanup),
		router.Wrap(h.cleanUp, mapCleanupError))
}

func (h *InternalCleanupController) cleanUp(c *gin.Context) error {
	userID, err := decodeCleanupBody(c.Request.Body)
	if err != nil {
		return err
	}
	receipt, created, err := h.cleanup.CleanUp(c.Request.Context(), c.Param("operation_id"), userID)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	c.JSON(status, gin.H{"data": gin.H{
		"operation_id": receipt.OperationID, "user_id": receipt.UserID, "state": "COMPLETED",
		"completed_at": receipt.CompletedAt.UTC().Format(receiptTimeLayout),
	}})

	return nil
}

// decodeCleanupBody requires one JSON object holding only user_id.
func decodeCleanupBody(body io.Reader) (int64, error) {
	malformed := &httpresponse.Error{Status: http.StatusBadRequest, Code: "VALIDATION_ERROR",
		Message: "Body must be one JSON object with user_id.", Details: map[string]any{"field": "body"}}
	raw, err := io.ReadAll(io.LimitReader(body, maxCleanupBody+1))
	if err != nil || len(raw) > maxCleanupBody {
		return 0, malformed
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil || len(fields) != 1 || fields["user_id"] == nil {
		return 0, malformed
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return 0, malformed
	}
	var userID int64
	if err := json.Unmarshal(fields["user_id"], &userID); err != nil || userID <= 0 {
		return 0, invalidField("user_id", "user_id must be a positive integer.")
	}

	return userID, nil
}

func mapCleanupError(err error) *httpresponse.Error {
	switch {
	case errors.Is(err, service.ErrInvalidOperationID):
		return invalidField("operation_id", "operation_id must be a UUID.")
	case errors.Is(err, service.ErrOperationConflict):
		return &httpresponse.Error{Status: http.StatusConflict, Code: "OPERATION_CONFLICT",
			Message: "This operation belongs to another user.", Details: map[string]any{}}
	case errors.Is(err, service.ErrDeletionConflict):
		return &httpresponse.Error{Status: http.StatusConflict, Code: "DELETION_CONFLICT",
			Message: "This user was cleaned up by another operation.", Details: map[string]any{}}
	}

	return mapStatsError(err)
}
