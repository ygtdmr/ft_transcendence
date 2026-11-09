package rest

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"game-session/room"
	"game-session/service"
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

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("could not write JSON response", "err", err)
	}
}

func writeData(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, dataEnvelope{Data: data})
}

func writeError(w http.ResponseWriter, status int, code string, message string, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	writeJSON(w, status, errorEnvelope{Error: apiError{Code: code, Message: message, Details: details}})
}

func writeDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, room.ErrRoomNotFound):
		writeError(w, http.StatusNotFound, "ROOM_NOT_FOUND", "No active room matches this id.", nil)
	case errors.Is(err, room.ErrNotRoomMember):
		writeError(w, http.StatusForbidden, "NOT_ROOM_MEMBER", "The user is not a member of this room.", nil)
	case errors.Is(err, room.ErrAlreadyInRoom):
		writeError(w, http.StatusConflict, "ALREADY_IN_ROOM", "The user already belongs to an active room.", nil)
	case errors.Is(err, room.ErrRoomFull):
		writeError(w, http.StatusConflict, "ROOM_FULL", "The room has no free seat.", nil)
	case errors.Is(err, room.ErrRoomNotJoinable):
		writeError(w, http.StatusConflict, "ROOM_NOT_JOINABLE", "The room does not accept joins in its current state.", nil)
	case errors.Is(err, room.ErrInvalidPayload), errors.Is(err, room.ErrValidationError):
		writeError(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "The request payload is invalid.", nil)
	case errors.Is(err, service.ErrRoomCodeTaken):
		writeError(w, http.StatusServiceUnavailable, "ROOM_CODE_UNAVAILABLE", "Could not allocate a room code, retry.", nil)
	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unexpected server error.", nil)
	}
}
