// Package controller translates HTTP requests into service calls and public responses.
package controller

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/router"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/service"
)

// Readiness reports whether the service can reach everything it depends on.
type Readiness interface {
	Ready(ctx context.Context) error
}

type HealthController struct {
	readiness Readiness
}

// NewHealth returns a controller serving the liveness and readiness endpoints.
func NewHealth(readiness Readiness) *HealthController {
	return &HealthController{readiness: readiness}
}

// RegisterRoutes adds GET /healthz and GET /readyz to the supplied router.
//
// The paths sit outside /api/v1 on purpose: they are for the orchestrator and
// local tooling, and the public reverse proxy must not expose them.
func (h *HealthController) RegisterRoutes(routes gin.IRoutes) {
	routes.GET("/healthz", router.Wrap(h.live, nil))
	routes.GET("/readyz", router.Wrap(h.ready, mapHealthError))
}

// live answers as long as the process can serve HTTP; it never touches a dependency.
func (h *HealthController) live(c *gin.Context) error {
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"status": "OK"}})
	return nil
}

// ready answers 200 only while the database is reachable.
func (h *HealthController) ready(c *gin.Context) error {
	if err := h.readiness.Ready(c.Request.Context()); err != nil {
		return err
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"status": "OK"}})
	return nil
}

func mapHealthError(err error) *httpresponse.Error {
	if errors.Is(err, service.ErrDatabaseUnavailable) {
		return &httpresponse.Error{
			Status:  http.StatusServiceUnavailable,
			Code:    "DEPENDENCY_UNAVAILABLE",
			Message: "A required dependency is unavailable.",
			Details: map[string]any{"dependency": "postgres"},
		}
	}

	return httpresponse.InternalError()
}
