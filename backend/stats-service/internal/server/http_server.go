package server

import (
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/controller"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/middleware/requestid"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/router"
)

const (
	readHeaderTimeout = 5 * time.Second
	idleTimeout       = 60 * time.Second
)

// RouteRegistrar adds a controller's routes to the router.
type RouteRegistrar interface {
	RegisterRoutes(routes gin.IRoutes)
}

// New assembles the stats-service HTTP server without opening its listener.
//
// Parameters:
//
//   - port: Port used by the caller when starting the listener.
//   - health: Controller registering the liveness and readiness routes.
//   - logger: Service logger receiving access and panic records.
//   - extra: Further controllers whose routes are added after the health routes.
//
// It returns an unstarted server with health routes, request IDs, access logging
// and panic recovery. The caller owns startup and shutdown.
func New(port string, health *controller.HealthController, logger *slog.Logger, extra ...RouteRegistrar) *http.Server {

	return &http.Server{
		Addr:              ":" + port,
		Handler:           newRouter(health, logger, extra...),
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
	}
}

// NewInternal assembles the private listener. It serves only the given routes,
// so attach INTERNAL_PORT only to the private service network.
func NewInternal(port string, logger *slog.Logger, routes ...RouteRegistrar) *http.Server {
	engine := baseEngine(logger)
	for _, registrar := range routes {
		registrar.RegisterRoutes(engine)
	}

	return &http.Server{
		Addr:              ":" + port,
		Handler:           engine,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
	}
}

// newRouter wires the public routes and the common HTTP middleware.
func newRouter(health *controller.HealthController, logger *slog.Logger, extra ...RouteRegistrar) *gin.Engine {
	engine := baseEngine(logger)
	health.RegisterRoutes(engine)
	for _, registrar := range extra {
		registrar.RegisterRoutes(engine)
	}

	return engine
}

// baseEngine returns a Gin engine with the common middleware and error envelopes.
func baseEngine(logger *slog.Logger) *gin.Engine {
	engine := gin.New()
	engine.Use(requestid.Middleware(), requestLogger(logger), gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, _ any) {
		logger.ErrorContext(c.Request.Context(), "http_panic", "request_id", requestid.Get(c))
		router.WriteError(c, httpresponse.InternalError())
	}))
	_ = engine.SetTrustedProxies(nil)
	engine.NoRoute(router.Wrap(func(c *gin.Context) error {
		return &httpresponse.Error{Status: http.StatusNotFound, Code: "NOT_FOUND", Message: "The requested endpoint was not found."}
	}, nil))
	engine.HandleMethodNotAllowed = true
	engine.NoMethod(router.Wrap(func(c *gin.Context) error {
		return &httpresponse.Error{Status: http.StatusMethodNotAllowed, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not allowed for this endpoint."}
	}, nil))

	return engine
}
