package service

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const maxReportDetails = 500

// reportReasons maps each public reason to its stored value.
var reportReasons = map[string]string{
	"HARASSMENT":         "harassment",
	"CHEATING":           "cheating",
	"OFFENSIVE_USERNAME": "offensive_username",
	"SPAM":               "spam",
	"OTHER":              "other",
}

// ErrCannotReportSelf reports a report aimed at the reporter.
var ErrCannotReportSelf = errors.New("cannot report yourself")

// AlreadyReportedError reports a repeat inside the configured window.
type AlreadyReportedError struct {
	RetryAfterSeconds int64
}

func (e *AlreadyReportedError) Error() string { return "already reported within the window" }

// ReportInput is what the reporter submits. The reporter's identity is never
// part of it.
type ReportInput struct {
	Reason  string
	Details string
	RoomID  *int64
}

// NewReport is a validated report ready to store.
type NewReport struct {
	ReporterID int64
	TargetID   int64
	Reason     string
	Details    string
	RoomID     *int64
	Window     time.Duration
}

// StoredReport identifies a stored report.
type StoredReport struct {
	ID        int64
	CreatedAt time.Time
}

// ReportStore stores reports.
type ReportStore interface {
	// FileReport stores report unless the reporter already reported the target
	// inside the window. It returns ErrUserNotFound when the target is missing
	// or being deleted, and *AlreadyReportedError for a repeat.
	FileReport(ctx context.Context, report NewReport) (StoredReport, error)
}

// Reports answers the report route.
type Reports struct {
	store  ReportStore
	window time.Duration
}

// NewReports returns the report service.
func NewReports(store ReportStore, window time.Duration) *Reports {
	return &Reports{store: store, window: window}
}

// CheckReportTarget validates who is reported.
//
// Errors:
//
//   - *ValidationError: targetID is not positive.
//   - ErrCannotReportSelf: targetID equals reporterID.
func CheckReportTarget(reporterID, targetID int64) error {
	if targetID <= 0 {
		return &ValidationError{Field: "user_id", Reason: "must be a positive integer"}
	}
	if targetID == reporterID {
		return ErrCannotReportSelf
	}

	return nil
}

// File stores a report from reporterID. The reported user is not notified, so
// no event is recorded.
//
// Errors:
//
//   - *ValidationError: a field is invalid. ErrCannotReportSelf: see CheckReportTarget.
//   - ErrUserNotFound, *AlreadyReportedError: the report is refused.
//   - ErrUnavailable or other database errors.
func (s *Reports) File(ctx context.Context, reporterID, targetID int64, input ReportInput) (StoredReport, error) {
	if err := CheckReportTarget(reporterID, targetID); err != nil {
		return StoredReport{}, err
	}
	reason, known := reportReasons[input.Reason]
	if !known {
		return StoredReport{}, &ValidationError{Field: "reason", Reason: "must be HARASSMENT, CHEATING, OFFENSIVE_USERNAME, SPAM or OTHER"}
	}
	details := strings.TrimSpace(input.Details)
	if utf8.RuneCountInString(details) > maxReportDetails || strings.ContainsRune(details, 0) {
		return StoredReport{}, &ValidationError{Field: "details", Reason: "must be at most 500 characters of text"}
	}
	if input.RoomID != nil && *input.RoomID <= 0 {
		return StoredReport{}, &ValidationError{Field: "room_id", Reason: "must be a positive integer"}
	}

	return s.store.FileReport(ctx, NewReport{ReporterID: reporterID, TargetID: targetID, Reason: reason,
		Details: details, RoomID: input.RoomID, Window: s.window})
}
