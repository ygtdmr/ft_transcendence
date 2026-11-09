// Package router adapts error-returning handlers to Gin's handler chain.
package router

import (
	"errors"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
)

type Handler func(*gin.Context) error

// ErrorMapper translates a service's domain errors into public API errors.
type ErrorMapper func(error) *httpresponse.Error

// Wrap supports both endpoint handlers and middleware. Returning nil lets the
// chain continue; returning an error aborts it and writes the common error envelope.
// Explicit httpresponse.Errors are preserved; unmapped errors become INTERNAL_ERROR.
func Wrap(handler Handler, mapError ErrorMapper) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := handler(c); err != nil {
			_ = c.Error(err)
			var response *httpresponse.Error
			if !errors.As(err, &response) && mapError != nil {
				response = mapError(err)
			}
			WriteError(c, response)
		}
	}
}

// WriteError aborts the chain and writes a public error unless the response is
// already committed. A committed response cannot be replaced or appended to.
func WriteError(c *gin.Context, response *httpresponse.Error) {
	c.Abort()
	if !c.Writer.Written() {
		httpresponse.WriteError(c.Writer, response)
	}
}
