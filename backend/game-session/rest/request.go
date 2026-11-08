package rest

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
)

func userIDParam(w http.ResponseWriter, r *http.Request) (int64, bool) {
	userID, err := strconv.ParseInt(r.Header.Get("X-User-Id"), 10, 64)
	if err != nil || userID <= 0 {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "A valid active session is required.", nil)
		return 0, false
	}
	return userID, true
}

func roomIDParam(w http.ResponseWriter, r *http.Request) (int64, bool) {
	roomID, err := strconv.ParseInt(r.PathValue("room_id"), 10, 64)
	if err != nil || roomID <= 0 {
		errorDetails := map[string]any{"field": "room_id"}
		writeError(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "room_id must be a positive integer.", errorDetails)
		return 0, false
	}
	return roomID, true
}

var roomCodePattern = regexp.MustCompile(`^[A-Za-z0-9]{6}$`)

func roomCodeParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	code := r.PathValue("room_code")
	if !roomCodePattern.MatchString(code) {
		errorDetails := map[string]any{"field": "room_code"}
		writeError(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "room_code must match ^[A-Za-z0-9]{6}$.", errorDetails)
		return "", false
	}
	return code, true
}

type createRoomRequest struct {
	MaxPlayers *int `json:"max_players"`
}

func readCreateRoomRequest(w http.ResponseWriter, r *http.Request) (createRoomRequest, bool) {
	const maxBodyBytes = 64 * 1024 //64kb
	var req createRoomRequest
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Could not read request body.", nil)
		return createRoomRequest{}, false
	}
	if len(body) > maxBodyBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "INVALID_BODY", "Request body must not exceed 64 KB.", nil)
		return createRoomRequest{}, false
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			errorDetails := map[string]any{"field": "body"}
			if !json.Valid(body) {
				writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Body must be valid JSON.", errorDetails)
				return createRoomRequest{}, false
			}
			var typeErr *json.UnmarshalTypeError
			if errors.As(err, &typeErr) {
				errorDetails["field"] = typeErr.Field
				writeError(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "Field has the wrong type.", errorDetails)
				return createRoomRequest{}, false
			}
			writeError(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "Body must be a JSON object.", errorDetails)
			return createRoomRequest{}, false
		}
	}
	return req, true
}
