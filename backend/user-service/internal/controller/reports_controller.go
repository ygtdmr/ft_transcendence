package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/middleware/sessionauth"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/router"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

// maxReportBody leaves room for 500 characters written as JSON escapes.
const maxReportBody = 8 << 10

// ReportService answers the report route.
type ReportService interface {
	File(ctx context.Context, reporterID, targetID int64, input service.ReportInput) (service.StoredReport, error)
}

// ReportsController serves the public report route.
type ReportsController struct {
	reports  ReportService
	sessions sessionauth.Validator
}

// NewReports returns the controller for /api/v1/users/{user_id}/reports.
func NewReports(reports ReportService, sessions sessionauth.Validator) *ReportsController {
	return &ReportsController{reports: reports, sessions: sessions}
}

// RegisterRoutes adds the report route behind session validation.
func (h *ReportsController) RegisterRoutes(routes gin.IRoutes) {
	routes.POST("/api/v1/users/:user_id/reports", sessionauth.Require(h.sessions), router.Wrap(h.file, mapFriendError))
}

// file takes the reporter only from the session, so a body cannot forge one.
func (h *ReportsController) file(c *gin.Context) error {
	targetID, err := pathUserID(c)
	if err != nil {
		return err
	}
	reporterID := sessionauth.UserID(c)
	if err := service.CheckReportTarget(reporterID, targetID); err != nil {
		return reportError(err, targetID)
	}
	input, err := decodeReport(c.Request.Body)
	if err != nil {
		return err
	}
	stored, err := h.reports.File(c.Request.Context(), reporterID, targetID, input)
	if err != nil {
		return reportError(err, targetID)
	}
	c.JSON(http.StatusCreated, gin.H{"data": gin.H{
		"report_id":        stored.ID,
		"reported_user_id": targetID,
		"reason":           input.Reason,
		"created_at":       stored.CreatedAt.UTC().Format(receiptTimeLayout),
	}})

	return nil
}

// decodeReport reads one JSON object. Malformed JSON is 400; a field of the
// wrong type is a field error.
func decodeReport(body io.Reader) (service.ReportInput, error) {
	var input service.ReportInput
	raw, err := io.ReadAll(io.LimitReader(body, maxReportBody+1))
	if err != nil || len(raw) > maxReportBody {
		return input, malformed("body", "Body must be one JSON object.")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return input, malformed("body", "Body must be one JSON object.")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return input, malformed("body", "Body must be one JSON object.")
	}
	if value, present := fields["reason"]; present && json.Unmarshal(value, &input.Reason) != nil {
		input.Reason = ""
	}
	if value, present := fields["details"]; present {
		if isJSONNull(value) || json.Unmarshal(value, &input.Details) != nil {
			return input, &service.ValidationError{Field: "details", Reason: "must be at most 500 characters of text"}
		}
	}
	if value, present := fields["room_id"]; present && !isJSONNull(value) {
		var roomID int64
		// A JSON number decodes into int64 only when it is a whole number.
		if json.Unmarshal(value, &roomID) != nil {
			return input, &service.ValidationError{Field: "room_id", Reason: "must be a positive integer"}
		}
		input.RoomID = &roomID
	}

	return input, nil
}

func isJSONNull(value json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}

func reportError(err error, targetID int64) error {
	var repeated *service.AlreadyReportedError
	switch {
	case errors.Is(err, service.ErrCannotReportSelf):
		return &httpresponse.Error{Status: http.StatusUnprocessableEntity, Code: "CANNOT_REPORT_SELF",
			Message: "You cannot report yourself.", Details: map[string]any{"user_id": targetID}}
	case errors.Is(err, service.ErrUserNotFound):
		return &httpresponse.Error{Status: http.StatusNotFound, Code: "USER_NOT_FOUND",
			Message: "No user exists with this ID.", Details: map[string]any{"user_id": targetID}}
	case errors.As(err, &repeated):
		return conflict("ALREADY_REPORTED", fmt.Sprintf("You already reported user %d recently.", targetID),
			map[string]any{"user_id": targetID, "retry_after_seconds": repeated.RetryAfterSeconds})
	default:
		return err
	}
}
