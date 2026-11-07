package controller

import (
	"errors"
	"net/http"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/middleware/validation"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
)

func mapAuthError(err error) *httpresponse.Error {
	status, code, message := http.StatusInternalServerError, "INTERNAL_ERROR", "An internal error occurred."
	details := map[string]any{}
	switch {
	case errors.Is(err, validation.ErrInvalidLoginRequest):
		status, code, message = http.StatusBadRequest, "VALIDATION_ERROR", validation.ErrInvalidLoginRequest.Error()
	case errors.Is(err, validation.ErrInvalidRequest):
		status, code, message = http.StatusBadRequest, "VALIDATION_ERROR", validation.ErrInvalidRequest.Error()
	case errors.Is(err, service.ErrInvalidCredentials):
		status, code, message = http.StatusUnauthorized, "INVALID_CREDENTIALS", "Email or password is incorrect."
	case errors.Is(err, service.ErrUnauthorized):
		status, code, message = http.StatusUnauthorized, "UNAUTHORIZED", "A valid active session is required."
	case errors.Is(err, service.ErrSessionExpired):
		status, code, message = http.StatusUnauthorized, "SESSION_EXPIRED", "The refresh token is invalid or expired. Log in again."
	case errors.Is(err, service.ErrServiceUnavailable):
		status, code, message = http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "The service is temporarily unavailable. Try again later."
	default:
		return mapRegistrationError(err)
	}
	return &httpresponse.Error{Status: status, Code: code, Message: message, Details: details}
}

func mapRegistrationError(err error) *httpresponse.Error {
	status, code, message := http.StatusInternalServerError, "INTERNAL_ERROR", "An internal error occurred."
	details := map[string]any{}
	switch {
	case errors.Is(err, service.ErrInvalidEmail):
		status, code, message = http.StatusUnprocessableEntity, "INVALID_EMAIL", service.ErrInvalidEmail.Error()
		details["field"] = "email"
	case errors.Is(err, service.ErrInvalidUsername):
		status, code, message = http.StatusUnprocessableEntity, "INVALID_USERNAME", service.ErrInvalidUsername.Error()
		details["field"] = "username"
	case errors.Is(err, service.ErrInvalidPassword):
		status, code, message = http.StatusUnprocessableEntity, "INVALID_PASSWORD", service.ErrInvalidPassword.Error()
		details["field"] = "password"
	case errors.Is(err, service.ErrEmailTaken):
		status, code, message = http.StatusConflict, "EMAIL_TAKEN", service.ErrEmailTaken.Error()
		details["field"] = "email"
	case errors.Is(err, service.ErrUsernameTaken):
		status, code, message = http.StatusConflict, "USERNAME_TAKEN", service.ErrUsernameTaken.Error()
		details["field"] = "username"
	}

	return &httpresponse.Error{Status: status, Code: code, Message: message, Details: details}
}
