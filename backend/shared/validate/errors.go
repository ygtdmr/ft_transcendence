// Package validate checks the shape of an incoming request before any domain
// code runs: it decodes JSON bodies strictly, validates path and query values,
// and turns failures into the public error the API contract defines.
//
// Only syntax and shape live here (type, length, range, enum, format). Domain
// rules, such as "this username is taken" or "you cannot friend yourself", stay
// in the service layer.
//
// Status codes follow the contract:
//
//   - 400 VALIDATION_ERROR, no details: malformed JSON or unexpected structure
//     (wrong media type, unknown or duplicate fields, trailing data, oversized
//     body). The message is generic and never echoes the request.
//   - 422 VALIDATION_ERROR with details.field: syntactically valid input whose
//     value fails a constraint, including path and query values.
//
// Messages are built from field names in code and constants, never from request
// data, so an error response cannot reflect attacker-controlled text.
package validate

import (
	"errors"
	"net/http"

	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
)

// FieldError reports that the named field's value violates a constraint.
type FieldError struct {
	Field   string
	Message string
}

func (e *FieldError) Error() string { return e.Message }

// Public returns the 422 VALIDATION_ERROR response for e.
func (e *FieldError) Public() *httpresponse.Error {
	return &httpresponse.Error{
		Status:  http.StatusUnprocessableEntity,
		Code:    "VALIDATION_ERROR",
		Message: e.Message,
		Details: map[string]any{"field": e.Field},
	}
}

// BodyError reports a request body that is malformed or has an unexpected
// structure. The cause is deliberately not recorded: it could echo client data.
type BodyError struct{}

func (*BodyError) Error() string { return bodyMessage }

const bodyMessage = "The request body must be a single JSON object with the documented fields."

// Public returns the 400 VALIDATION_ERROR response.
func (*BodyError) Public() *httpresponse.Error {
	return &httpresponse.Error{
		Status:  http.StatusBadRequest,
		Code:    "VALIDATION_ERROR",
		Message: bodyMessage,
	}
}

// Response returns the public error for a validation failure produced by this
// package and true, or nil and false for any other error.
func Response(err error) (*httpresponse.Error, bool) {
	var field *FieldError
	if errors.As(err, &field) {
		return field.Public(), true
	}
	var body *BodyError
	if errors.As(err, &body) {
		return body.Public(), true
	}
	return nil, false
}

// First returns the first non-nil error, so a DTO's Validate reports one
// deterministic failure in the order its checks are written.
func First(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
