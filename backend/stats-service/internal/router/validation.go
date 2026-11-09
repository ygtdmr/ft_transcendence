package router

import (
	"net/http"

	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
)

// ValidationError builds the public 400 response for invalid client input.
//
// Parameters:
//
//   - fields: Field name to a short, public reason. Nil or empty is allowed.
//
// Expected output:
//
//   - A VALIDATION_ERROR with status 400 whose details hold {"fields": fields}
//     when there are any. The map is copied, so later changes by the caller do
//     not alter the response.
func ValidationError(fields map[string]string) *httpresponse.Error {
	response := &httpresponse.Error{
		Status:  http.StatusBadRequest,
		Code:    "VALIDATION_ERROR",
		Message: "The request is invalid.",
	}
	if len(fields) > 0 {
		copied := make(map[string]string, len(fields))
		for name, reason := range fields {
			copied[name] = reason
		}
		response.Details = map[string]any{"fields": copied}
	}

	return response
}
