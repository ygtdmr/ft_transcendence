package rest

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"game-session/room"
	"game-session/service"

	"github.com/gin-gonic/gin"
)

type membershipView struct {
	UserID     int64   `json:"user_id"`
	RoomID     *int64  `json:"active_room_id"`
	APIVersion *string `json:"active_room_api_version"`
}

func (h *Handler) membership(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || userID <= 0 {
		writeError(c, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "user_id must be a positive integer.", map[string]any{"field": "user_id"})
		return
	}
	var roomID int64
	var ok bool
	err = h.store.ReadTransaction(func(tx service.RoomStore) error {
		var err error
		roomID, ok, err = h.svc.ActiveRoom(tx, userID)
		return err
	})
	if err != nil {
		writeError(c, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Database unavailable.", nil)
		return
	}
	view := membershipView{UserID: userID}
	if ok {
		version := "v2"
		view.RoomID = &roomID
		view.APIVersion = &version
	}
	writeData(c, http.StatusOK, view)
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type internalUserRequest struct {
	UserID *int64 `json:"user_id"`
}

type accountCleanupView struct {
	OperationID string    `json:"operation_id"`
	UserID      int64     `json:"user_id"`
	State       string    `json:"state"`
	CompletedAt time.Time `json:"completed_at"`
}

func readInternalUserRequest(c *gin.Context, idParamName string) (string, int64, bool) {
	id := strings.ToLower(c.Param(idParamName))
	if !uuidPattern.MatchString(id) {
		writeError(c, http.StatusUnprocessableEntity, "VALIDATION_ERROR", idParamName+" must be a UUID.", map[string]any{"field": idParamName})
		return "", 0, false
	}

	const maxBodyBytes = 64 * 1024 //64kb
	var req internalUserRequest
	decoder := json.NewDecoder(io.LimitReader(c.Request.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "VALIDATION_ERROR", "Body must be a JSON object with user_id.", map[string]any{"field": "body"})
		return "", 0, false
	}
	if decoder.More() {
		writeError(c, http.StatusBadRequest, "VALIDATION_ERROR", "Body must contain one JSON value.", map[string]any{"field": "body"})
		return "", 0, false
	}
	if req.UserID == nil {
		writeError(c, http.StatusBadRequest, "VALIDATION_ERROR", "user_id is required.", map[string]any{"field": "body"})
		return "", 0, false
	}
	userID := *req.UserID
	if userID <= 0 {
		writeError(c, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "user_id must be a positive integer.", map[string]any{"field": "user_id"})
		return "", 0, false
	}

	return id, userID, true
}

func (h *Handler) performAccountCleanup(operationID string, userID int64) (service.AccountCleanup, bool, error) {
	now := h.now()
	var receipt service.AccountCleanup
	var replayed bool
	err := h.store.Transaction(func(tx service.RoomStore) error {
		existing, found, err := tx.LookupAccountCleanup(userID)
		if err != nil {
			return err
		}
		if found {
			if existing.OperationID != operationID {
				return service.ErrAccountCleanupUserConflict
			}
			receipt = existing
			replayed = true
			return nil
		}

		roomID, inRoom, err := tx.ActiveRoomOf(userID)
		if err != nil {
			return err
		}
		if inRoom {
			if _, err := h.actions.Leave(tx, roomID, userID, room.DepartureAccountDeleted, now); err != nil {
				return err
			}
		}

		receipt = service.AccountCleanup{OperationID: operationID, UserID: userID, CompletedAt: now}
		return tx.SaveAccountCleanup(receipt)
	})
	return receipt, replayed, err
}

func (h *Handler) accountCleanup(c *gin.Context) {
	operationID, userID, valid := readInternalUserRequest(c, "operation_id")
	if !valid {
		return
	}

	receipt, replayed, err := h.performAccountCleanup(operationID, userID)
	switch {
	case errors.Is(err, service.ErrAccountCleanupOperationConflict):
		writeError(c, http.StatusConflict, "OPERATION_CONFLICT", "operation_id already belongs to another user.", nil)
		return
	case errors.Is(err, service.ErrAccountCleanupUserConflict):
		writeError(c, http.StatusConflict, "DELETION_CONFLICT", "The user was already cleaned up by another operation.", nil)
		return
	case err != nil:
		writeDomainError(c, err)
		return
	}

	view := accountCleanupView{OperationID: receipt.OperationID, UserID: receipt.UserID, State: "COMPLETED", CompletedAt: receipt.CompletedAt}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	writeData(c, status, view)
}

type sessionDepartureView struct {
	SessionID   string    `json:"session_id"`
	UserID      int64     `json:"user_id"`
	State       string    `json:"state"`
	CompletedAt time.Time `json:"completed_at"`
}

func (h *Handler) performSessionDeparture(sessionID string, userID int64) (service.SessionDeparture, bool, error) {
	now := h.now()
	var receipt service.SessionDeparture
	var replayed bool
	err := h.store.Transaction(func(tx service.RoomStore) error {
		existing, found, err := tx.LookupSessionDeparture(sessionID)
		if err != nil {
			return err
		}
		if found {
			if existing.UserID != userID {
				return service.ErrSessionDepartureConflict
			}
			receipt = existing
			replayed = true
			return nil
		}

		roomID, inRoom, err := tx.ActiveRoomOf(userID)
		if err != nil {
			return err
		}
		if inRoom {
			if _, err := h.actions.Leave(tx, roomID, userID, room.DepartureSessionEnded, now); err != nil {
				return err
			}
		}

		receipt = service.SessionDeparture{SessionID: sessionID, UserID: userID, CompletedAt: now}
		return tx.SaveSessionDeparture(receipt)
	})
	return receipt, replayed, err
}

func (h *Handler) sessionDeparture(c *gin.Context) {
	sessionID, userID, valid := readInternalUserRequest(c, "session_id")
	if !valid {
		return
	}

	receipt, replayed, err := h.performSessionDeparture(sessionID, userID)
	switch {
	case errors.Is(err, service.ErrSessionDepartureConflict):
		writeError(c, http.StatusConflict, "OPERATION_CONFLICT", "This session departure was recorded for another user.", nil)
		return
	case err != nil:
		writeDomainError(c, err)
		return
	}

	view := sessionDepartureView{SessionID: receipt.SessionID, UserID: receipt.UserID, State: "COMPLETED", CompletedAt: receipt.CompletedAt}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	writeData(c, status, view)
}
