package httpresponse

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
)

// PublicError is implemented by errors that know their own public response, for
// example the validation errors in shared/validate.
type PublicError interface {
	error
	Public() *Error
}

// Mapper turns the errors a handler returns into public responses. The zero
// value (and a nil *Mapper) is usable and applies only the built-in rules.
//
// Resolution order, first match wins:
//
//  1. an *Error anywhere in the chain is already public and is used as is;
//  2. a PublicError anywhere in the chain (validation errors);
//  3. the rules registered with Map and MapFunc, in registration order;
//  4. context.DeadlineExceeded and context.Canceled: 503 SERVICE_UNAVAILABLE;
//  5. anything else: 500 INTERNAL_ERROR with a fixed message.
//
// An unmatched error's text is never copied into the response: internal errors
// routinely embed SQL, hostnames and connection strings.
type Mapper struct {
	rules []rule
}

type rule struct {
	match    func(error) bool
	response Error
}

// NewMapper returns an empty Mapper.
func NewMapper() *Mapper { return &Mapper{} }

// Map answers target (matched with errors.Is, so wrapped errors count) with
// response. The response is copied; later changes to the argument do not affect
// the rule. It returns m for chaining.
func (m *Mapper) Map(target error, response Error) *Mapper {
	m.rules = append(m.rules, rule{match: func(err error) bool { return errors.Is(err, target) }, response: copyError(response)})
	return m
}

// MapFunc answers any error for which match returns true.
func (m *Mapper) MapFunc(match func(error) bool, response Error) *Mapper {
	m.rules = append(m.rules, rule{match: match, response: copyError(response)})
	return m
}

// Response returns the public response for err and whether it was expected. An
// unexpected error yields the generic 500 and expected == false, which callers
// should log (without the error text).
func (m *Mapper) Response(err error) (response *Error, expected bool) {
	var public *Error
	if errors.As(err, &public) && public != nil {
		return public, true
	}
	var carrier PublicError
	if errors.As(err, &carrier) {
		if resp := carrier.Public(); resp != nil {
			return resp, true
		}
	}
	if m != nil {
		for _, r := range m.rules {
			if r.match(err) {
				resp := copyError(r.response)
				return &resp, true
			}
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &Error{
			Status:  http.StatusServiceUnavailable,
			Code:    "SERVICE_UNAVAILABLE",
			Message: "The request could not be completed in time.",
		}, true
	}
	return InternalError(), false
}

func copyError(e Error) Error {
	e.Details = sanitizeDetails(e.Details)
	return e
}

// HandlerFunc is a net/http handler that reports failure by returning an error
// instead of writing one. A handler that returns a non-nil error must not have
// written anything yet.
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// Adapt converts fn into an http.HandlerFunc that writes the mapped error. An
// unexpected error is logged at error level with its Go type only (never its
// text, which may hold secrets) and answered with the generic 500. The logger
// picks up request and trace IDs from the request context. logger may be nil.
func Adapt(fn HandlerFunc, m *Mapper, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := fn(w, r)
		if err == nil {
			return
		}
		WriteError(w, ResolveAndLog(r.Context(), err, m, logger))
	}
}

// ResolveAndLog maps err to its public response and logs unexpected errors by
// type. Framework adapters (see ginhttp.Handle) use it so every entry point
// behaves the same.
func ResolveAndLog(ctx context.Context, err error, m *Mapper, logger *slog.Logger) *Error {
	response, expected := m.Response(err)
	if !expected && logger != nil {
		logger.ErrorContext(ctx, "unhandled_error", "error_type", typeName(err))
	}
	return response
}

func typeName(err error) string { return fmt.Sprintf("%T", err) }
