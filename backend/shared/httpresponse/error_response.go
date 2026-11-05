// Package httpresponse defines common HTTP response envelopes without a router dependency.
//
// API conventions every service follows:
//
//   - success: {"data": ...} via WriteData; 204 has no body (WriteNoContent);
//   - failure: {"error": {"code", "message", "details"}} via WriteError, with an
//     UPPER_SNAKE_CASE code and details always an object;
//   - JSON object keys are snake_case; enum values are UPPER_SNAKE_CASE;
//   - timestamps are ISO-8601 / RFC 3339 in UTC (use the Time type).
//
// Every response carries Cache-Control: no-store and X-Content-Type-Options:
// nosniff, and error details never contain credential-like keys (see
// sanitizeDetails). Mapper and Adapt turn service errors into public errors
// without ever exposing an internal error's text.
//
// Violations checks a captured response against all of these in service tests.
package httpresponse

import (
	"encoding/json"
	"net/http"
)

// Error contains only public error information intended for the API client.
// Internal causes, credentials and database details must not be included.
type Error struct {
	Status  int            `json:"-"`
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

func (e *Error) Error() string { return e.Message }

// InternalError returns the generic response for an unexpected failure.
func InternalError() *Error {
	return &Error{
		Status:  http.StatusInternalServerError,
		Code:    "INTERNAL_ERROR",
		Message: "An internal error occurred.",
	}
}

// WriteError writes {"error":{"code":...,"message":...,"details":...}}.
// Missing details become an empty object. Invalid error definitions fall back
// to InternalError. Callers must ensure the response has not already been written.
func WriteError(writer http.ResponseWriter, response *Error) {
	if response == nil || response.Status < 400 || response.Status > 599 || response.Code == "" || response.Message == "" {
		response = InternalError()
	}
	public := *response
	public.Details = sanitizeDetails(public.Details)
	body, err := json.Marshal(struct {
		Error *Error `json:"error"`
	}{Error: &public})
	if err != nil {
		// An unencodable detail is an internal failure, not a partial public response.
		WriteError(writer, InternalError())
		return
	}
	if recorder, ok := writer.(ErrorCodeRecorder); ok {
		recorder.RecordErrorCode(public.Code)
	}
	setAPIHeaders(writer.Header())
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(public.Status)
	_, _ = writer.Write(append(body, '\n'))
}
