package rest

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"

	"github.com/gin-gonic/gin"
)

func userIDParam(c *gin.Context) (int64, bool) {
	userID, err := strconv.ParseInt(c.GetHeader("X-User-Id"), 10, 64)
	if err != nil || userID <= 0 {
		writeError(c, http.StatusUnauthorized, "UNAUTHORIZED", "A valid active session is required.", nil)
		return 0, false
	}
	return userID, true
}

func roomIDParam(c *gin.Context) (int64, bool) {
	roomID, err := strconv.ParseInt(c.Param("room_id"), 10, 64)
	if err != nil || roomID <= 0 {
		errorDetails := map[string]any{"field": "room_id"}
		writeError(c, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "room_id must be a positive integer.", errorDetails)
		return 0, false
	}
	return roomID, true
}

var roomCodePattern = regexp.MustCompile(`^[A-Za-z0-9]{6}$`)

func roomCodeParam(c *gin.Context) (string, bool) {
	code := c.Param("room_code")
	if !roomCodePattern.MatchString(code) {
		errorDetails := map[string]any{"field": "room_code"}
		writeError(c, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "room_code must match ^[A-Za-z0-9]{6}$.", errorDetails)
		return "", false
	}
	return code, true
}

type createRoomRequest struct {
	MaxPlayers *int `json:"max_players"`
}

func readCreateRoomRequest(c *gin.Context) (createRoomRequest, bool) {
	const maxBodyBytes = 64 * 1024 //64kb
	var req createRoomRequest
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBodyBytes+1))
	if err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_BODY", "Could not read request body.", nil)
		return createRoomRequest{}, false
	}
	if len(body) > maxBodyBytes {
		writeError(c, http.StatusRequestEntityTooLarge, "INVALID_BODY", "Request body must not exceed 64 KB.", nil)
		return createRoomRequest{}, false
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			errorDetails := map[string]any{"field": "body"}
			if !json.Valid(body) {
				writeError(c, http.StatusBadRequest, "VALIDATION_ERROR", "Body must be valid JSON.", errorDetails)
				return createRoomRequest{}, false
			}
			var typeErr *json.UnmarshalTypeError
			if errors.As(err, &typeErr) {
				errorDetails["field"] = typeErr.Field
				writeError(c, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "Field has the wrong type.", errorDetails)
				return createRoomRequest{}, false
			}
			writeError(c, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "Body must be a JSON object.", errorDetails)
			return createRoomRequest{}, false
		}
	}
	return req, true
}
