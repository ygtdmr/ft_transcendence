package rest

import (
	"errors"
	"net/http"

	"game-session/room"
	"game-session/service"

	"github.com/gin-gonic/gin"
)

type apiError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

type errorEnvelope struct {
	Error apiError `json:"error"`
}

type dataEnvelope struct {
	Data any `json:"data"`
}

func writeData(c *gin.Context, status int, data any) {
	c.JSON(status, dataEnvelope{Data: data})
}

func writeError(c *gin.Context, status int, code string, message string, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	c.JSON(status, errorEnvelope{Error: apiError{Code: code, Message: message, Details: details}})
}

func writeDomainError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, room.ErrRoomNotFound):
		writeError(c, http.StatusNotFound, "ROOM_NOT_FOUND", "No active room matches this id.", nil)
	case errors.Is(err, room.ErrNotRoomMember):
		writeError(c, http.StatusForbidden, "NOT_ROOM_MEMBER", "The user is not a member of this room.", nil)
	case errors.Is(err, room.ErrAlreadyInRoom):
		writeError(c, http.StatusConflict, "ALREADY_IN_ROOM", "The user already belongs to an active room.", nil)
	case errors.Is(err, room.ErrRoomFull):
		writeError(c, http.StatusConflict, "ROOM_FULL", "The room has no free seat.", nil)
	case errors.Is(err, room.ErrRoomNotJoinable):
		writeError(c, http.StatusConflict, "ROOM_NOT_JOINABLE", "The room does not accept joins in its current state.", nil)
	case errors.Is(err, room.ErrInvalidRoomState):
		writeError(c, http.StatusConflict, "INVALID_ROOM_STATE", "The room does not allow this in its current state.", nil)
	case errors.Is(err, room.ErrInvalidPayload), errors.Is(err, room.ErrValidationError):
		writeError(c, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "The request payload is invalid.", nil)
	case errors.Is(err, service.ErrRoomCodeTaken):
		writeError(c, http.StatusServiceUnavailable, "ROOM_CODE_UNAVAILABLE", "Could not allocate a room code, retry.", nil)
	case errors.Is(err, service.ErrAccountCleanedUp):
		writeError(c, http.StatusUnauthorized, "UNAUTHORIZED", "A valid active session is required.", nil)
	case errors.Is(err, service.ErrProfileSourceUnavailable):
		writeError(c, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "User Service is unavailable, retry.", nil)
	default:
		writeError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "Unexpected server error.", nil)
	}
}
