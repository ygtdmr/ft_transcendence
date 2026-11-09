package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/middleware/sessionauth"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/router"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

const maxFriendRequestBody = 1 << 10

// FriendRequestService answers the friend request routes.
type FriendRequestService interface {
	Send(ctx context.Context, senderID, recipientID int64) (service.FriendRequest, error)
	List(ctx context.Context, userID int64) (incoming, outgoing []service.FriendRequest, err error)
	Accept(ctx context.Context, requestID, callerID int64) (service.Friend, error)
	Decline(ctx context.Context, requestID, callerID int64) error
	Cancel(ctx context.Context, requestID, callerID int64) error
}

// FriendRequestsController serves the public friend request routes.
type FriendRequestsController struct {
	requests FriendRequestService
	sessions sessionauth.Validator
}

// NewFriendRequests returns the controller for /api/v1/friends/requests.
func NewFriendRequests(requests FriendRequestService, sessions sessionauth.Validator) *FriendRequestsController {
	return &FriendRequestsController{requests: requests, sessions: sessions}
}

// RegisterRoutes adds the friend request routes behind session validation.
func (h *FriendRequestsController) RegisterRoutes(routes gin.IRoutes) {
	const path = "/api/v1/friends/requests"
	auth := sessionauth.Require(h.sessions)
	routes.POST(path, auth, router.Wrap(h.send, mapFriendError))
	routes.GET(path, auth, router.Wrap(h.list, mapFriendError))
	routes.POST(path+"/:request_id/accept", auth, router.Wrap(h.accept, mapFriendError))
	routes.POST(path+"/:request_id/decline", auth, router.Wrap(h.decline, mapFriendError))
	routes.DELETE(path+"/:request_id", auth, router.Wrap(h.cancel, mapFriendError))
}

func (h *FriendRequestsController) send(c *gin.Context) error {
	recipientID, err := decodeRecipient(c.Request.Body)
	if err != nil {
		return err
	}
	request, err := h.requests.Send(c.Request.Context(), sessionauth.UserID(c), recipientID)
	if err != nil {
		return sendError(err, recipientID)
	}
	c.JSON(http.StatusCreated, gin.H{"data": friendRequestJSON(request)})

	return nil
}

// sendError maps the refusals whose details name the recipient.
func sendError(err error, recipientID int64) error {
	var pending *service.PendingRequestError
	switch {
	case errors.As(err, &pending) && pending.Outgoing:
		return conflict("REQUEST_ALREADY_SENT", fmt.Sprintf("A friend request to user %d is already pending.", recipientID),
			map[string]any{"user_id": recipientID, "request_id": pending.RequestID})
	case errors.As(err, &pending):
		return conflict("REQUEST_ALREADY_RECEIVED", fmt.Sprintf("User %d has already sent you a friend request.", recipientID),
			map[string]any{"user_id": recipientID, "request_id": pending.RequestID})
	case errors.Is(err, service.ErrAlreadyFriends):
		return conflict("ALREADY_FRIENDS", "You are already friends with this user.", map[string]any{"user_id": recipientID})
	case errors.Is(err, service.ErrUserNotFound):
		return &httpresponse.Error{Status: http.StatusNotFound, Code: "USER_NOT_FOUND",
			Message: "No user exists with this ID.", Details: map[string]any{"user_id": recipientID}}
	case errors.Is(err, service.ErrRelationshipBlocked):
		return &httpresponse.Error{Status: http.StatusForbidden, Code: "RELATIONSHIP_BLOCKED",
			Message: "You cannot send a friend request to this user.", Details: map[string]any{"user_id": recipientID}}
	default:
		return err
	}
}

func (h *FriendRequestsController) list(c *gin.Context) error {
	incoming, outgoing, err := h.requests.List(c.Request.Context(), sessionauth.UserID(c))
	if err != nil {
		return err
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"incoming": friendRequestsJSON(incoming),
		"outgoing": friendRequestsJSON(outgoing),
	}})

	return nil
}

func (h *FriendRequestsController) accept(c *gin.Context) error {
	requestID, err := resolutionTarget(c)
	if err != nil {
		return err
	}
	friend, err := h.requests.Accept(c.Request.Context(), requestID, sessionauth.UserID(c))
	if err != nil {
		return resolveError(err, requestID, "for you")
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"user_id":    friend.UserID,
		"username":   friend.Username,
		"avatar_url": friend.AvatarURL,
		"is_online":  friend.IsOnline,
	}})

	return nil
}

func (h *FriendRequestsController) decline(c *gin.Context) error {
	requestID, err := resolutionTarget(c)
	if err != nil {
		return err
	}
	if err := h.requests.Decline(c.Request.Context(), requestID, sessionauth.UserID(c)); err != nil {
		return resolveError(err, requestID, "for you")
	}
	c.Status(http.StatusNoContent)
	c.Writer.WriteHeaderNow()

	return nil
}

func (h *FriendRequestsController) cancel(c *gin.Context) error {
	requestID, err := resolutionTarget(c)
	if err != nil {
		return err
	}
	if err := h.requests.Cancel(c.Request.Context(), requestID, sessionauth.UserID(c)); err != nil {
		return resolveError(err, requestID, "from you")
	}
	c.Status(http.StatusNoContent)
	c.Writer.WriteHeaderNow()

	return nil
}

// resolutionTarget validates the path id and the required empty body.
func resolutionTarget(c *gin.Context) (int64, error) {
	requestID, err := strconv.ParseInt(c.Param("request_id"), 10, 64)
	if err != nil || requestID <= 0 {
		return 0, &service.ValidationError{Field: "request_id", Reason: "must be a positive integer"}
	}
	if err := rejectBody(c); err != nil {
		return 0, err
	}

	return requestID, nil
}

// rejectBody refuses a request that must have no body.
func rejectBody(c *gin.Context) error {
	var probe [1]byte
	if n, _ := c.Request.Body.Read(probe[:]); n > 0 {
		return malformed("body", "This request takes no body.")
	}

	return nil
}

func resolveError(err error, requestID int64, side string) error {
	switch {
	case errors.Is(err, service.ErrRequestNotFound):
		return &httpresponse.Error{Status: http.StatusNotFound, Code: "REQUEST_NOT_FOUND",
			Message: fmt.Sprintf("Friend request %d is not pending %s.", requestID, side),
			Details: map[string]any{"request_id": requestID}}
	case errors.Is(err, service.ErrRelationshipBlocked):
		return &httpresponse.Error{Status: http.StatusForbidden, Code: "RELATIONSHIP_BLOCKED",
			Message: "You cannot accept this friend request.", Details: map[string]any{"request_id": requestID}}
	default:
		return err
	}
}

// decodeRecipient reads one JSON object holding user_id. Malformed JSON is 400;
// a missing or non-integer user_id is a field error.
func decodeRecipient(body io.Reader) (int64, error) {
	invalid := &service.ValidationError{Field: "user_id", Reason: "must be a positive integer"}
	raw, err := io.ReadAll(io.LimitReader(body, maxFriendRequestBody+1))
	if err != nil || len(raw) > maxFriendRequestBody {
		return 0, malformed("body", "Body must be one JSON object.")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return 0, malformed("body", "Body must be one JSON object.")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return 0, malformed("body", "Body must be one JSON object.")
	}
	value, ok := fields["user_id"]
	// A JSON number decodes into int64 only when it is a whole number.
	var userID int64
	if !ok || json.Unmarshal(value, &userID) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return 0, invalid
	}

	return userID, nil
}

func friendRequestsJSON(requests []service.FriendRequest) []gin.H {
	result := make([]gin.H, 0, len(requests))
	for _, request := range requests {
		result = append(result, friendRequestJSON(request))
	}

	return result
}

func friendRequestJSON(request service.FriendRequest) gin.H {
	return gin.H{
		"request_id": request.ID,
		"from_user":  request.From,
		"to_user":    request.To,
		"status":     request.Status,
		"created_at": request.CreatedAt.UTC().Format(receiptTimeLayout),
	}
}

// mapFriendError reuses the common validation and availability responses.
func mapFriendError(err error) *httpresponse.Error {
	return mapProvisioningError(err)
}
