package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/middleware/serviceauth"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/model"
	authrouter "github.com/melmut-42/ft_transcendence/backend/auth-service/internal/router"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
)

// maxDeletionBody bounds the Begin body; a valid one is far smaller.
const maxDeletionBody = 1 << 10

var operationIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// AccountDeleter performs Auth's part of a coordinated account deletion.
type AccountDeleter interface {
	BeginAccountDeletion(ctx context.Context, operationID string, userID int64) (service.DeletionBegin, error)
	CompleteAccountDeletion(ctx context.Context, operationID string) (model.AccountDeletion, error)
}

type AccountDeletionController struct {
	deleter AccountDeleter
	auth    *serviceauth.Authenticator
}

// NewAccountDeletion returns the controller for the private deletion routes.
//
// Parameters:
//
//   - deleter: Auth's deletion participant.
//   - auth: Service authenticator enforcing each route's scope.
func NewAccountDeletion(deleter AccountDeleter, auth *serviceauth.Authenticator) *AccountDeletionController {
	return &AccountDeletionController{deleter: deleter, auth: auth}
}

// RegisterRoutes adds Begin and Complete. Register them only on the private listener.
func (h *AccountDeletionController) RegisterRoutes(routes gin.IRoutes) {
	routes.PUT("/internal/v1/account-deletions/:operation_id",
		h.auth.Require(serviceauth.ScopeAccountDeletionBegin), authrouter.Wrap(h.begin, mapDeletionError))
	routes.PUT("/internal/v1/account-deletions/:operation_id/completion",
		h.auth.Require(serviceauth.ScopeAccountDeletionComplete), authrouter.Wrap(h.complete, mapDeletionError))
}

func (h *AccountDeletionController) begin(c *gin.Context) error {
	operationID, err := deletionOperationID(c)
	if err != nil {
		return err
	}
	userID, err := decodeDeletionBody(c.Request.Body)
	if err != nil {
		return err
	}
	result, err := h.deleter.BeginAccountDeletion(c.Request.Context(), operationID, userID)
	if errors.Is(err, service.ErrNotFound) {
		return &httpresponse.Error{Status: http.StatusNotFound, Code: "USER_NOT_FOUND",
			Message: "No account exists for this user.", Details: map[string]any{"user_id": userID}}
	}
	if errors.Is(err, service.ErrDeletionConflict) {
		return &httpresponse.Error{Status: http.StatusConflict, Code: "DELETION_CONFLICT",
			Message: "Another deletion operation already exists for this user.", Details: map[string]any{"user_id": userID}}
	}
	if errors.Is(err, service.ErrDeletionOperationConflict) {
		return &httpresponse.Error{Status: http.StatusConflict, Code: "OPERATION_CONFLICT",
			Message: "This deletion operation already exists for another user.",
			Details: map[string]any{"operation_id": operationID}}
	}
	if err != nil {
		return err
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	c.JSON(status, gin.H{"data": deletionReceipt(result.Deletion)})

	return nil
}

func (h *AccountDeletionController) complete(c *gin.Context) error {
	operationID, err := deletionOperationID(c)
	if err != nil {
		return err
	}
	if hasBody(c.Request.Body) {
		return &httpresponse.Error{Status: http.StatusBadRequest, Code: "VALIDATION_ERROR",
			Message: "This request takes no body.", Details: map[string]any{"field": "body"}}
	}
	deletion, err := h.deleter.CompleteAccountDeletion(c.Request.Context(), operationID)
	if errors.Is(err, service.ErrDeletionNotFound) {
		return &httpresponse.Error{Status: http.StatusNotFound, Code: "DELETION_NOT_FOUND",
			Message: "No deletion operation has begun with this ID.", Details: map[string]any{"operation_id": operationID}}
	}
	if err != nil {
		return err
	}
	c.JSON(http.StatusOK, gin.H{"data": deletionReceipt(deletion)})

	return nil
}

// deletionOperationID normalizes the path UUID to lowercase.
func deletionOperationID(c *gin.Context) (string, error) {
	id := strings.ToLower(c.Param("operation_id"))
	if !operationIDPattern.MatchString(id) {
		return "", &httpresponse.Error{Status: http.StatusUnprocessableEntity, Code: "VALIDATION_ERROR",
			Message: "operation_id must be a UUID.", Details: map[string]any{"field": "operation_id"}}
	}

	return id, nil
}

// decodeDeletionBody accepts exactly one JSON object whose only field is a
// positive integer user_id.
func decodeDeletionBody(body io.Reader) (int64, error) {
	malformed := &httpresponse.Error{Status: http.StatusBadRequest, Code: "VALIDATION_ERROR",
		Message: "Body must be one JSON object with user_id.", Details: map[string]any{"field": "body"}}
	invalid := &httpresponse.Error{Status: http.StatusUnprocessableEntity, Code: "VALIDATION_ERROR",
		Message: "user_id must be a positive integer.", Details: map[string]any{"field": "user_id"}}

	raw, err := io.ReadAll(io.LimitReader(body, maxDeletionBody+1))
	if err != nil || len(raw) > maxDeletionBody {
		return 0, malformed
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return 0, malformed
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return 0, malformed
	}
	value, ok := fields["user_id"]
	if !ok || len(fields) != 1 {
		return 0, malformed
	}
	// json.Number also accepts a quoted string, so require a bare number.
	if trimmed := bytes.TrimSpace(value); len(trimmed) == 0 || trimmed[0] == '"' {
		return 0, invalid
	}
	numberDecoder := json.NewDecoder(bytes.NewReader(value))
	numberDecoder.UseNumber()
	var number json.Number
	if err := numberDecoder.Decode(&number); err != nil {
		return 0, invalid
	}
	userID, err := number.Int64()
	if err != nil || userID <= 0 {
		return 0, invalid
	}

	return userID, nil
}

func hasBody(body io.Reader) bool {
	if body == nil {
		return false
	}
	var probe [1]byte
	n, _ := body.Read(probe[:])

	return n > 0
}

func deletionReceipt(deletion model.AccountDeletion) gin.H {
	var completedAt any
	if deletion.CompletedAt != nil {
		completedAt = deletion.CompletedAt.UTC().Format(time.RFC3339Nano)
	}

	return gin.H{
		"operation_id": deletion.OperationID,
		"user_id":      deletion.UserID,
		"state":        deletion.State,
		"begun_at":     deletion.BegunAt.UTC().Format(time.RFC3339Nano),
		"completed_at": completedAt,
	}
}

// mapDeletionError keeps dependency failures distinct from deletion verdicts.
func mapDeletionError(err error) *httpresponse.Error {
	if errors.Is(err, service.ErrUnavailable) {
		return &httpresponse.Error{Status: http.StatusServiceUnavailable, Code: "SERVICE_UNAVAILABLE",
			Message: "The service is temporarily unavailable."}
	}

	return httpresponse.InternalError()
}
