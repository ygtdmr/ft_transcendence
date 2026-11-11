package rest

import (
	"net/http"
	"strconv"

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
	roomID, ok, err := h.svc.ActiveRoom(userID)
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
