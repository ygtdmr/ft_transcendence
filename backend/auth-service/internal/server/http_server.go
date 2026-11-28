package server

import (
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/controller"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/middleware/requestid"
	authrouter "github.com/melmut-42/ft_transcendence/backend/auth-service/internal/router"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
)

const (
	readHeaderTimeout = 5 * time.Second
	idleTimeout       = 60 * time.Second
)

// New assembles the auth HTTP server without opening its listener.
//
// Parameters:
//
//   - port: Port used by the caller when starting the listener.
//   - auth: Controller registering the auth routes.
//   - logger: Service logger receiving access and panic records.
//   - extra: Controllers whose routes are added at the root, such as the liveness
//     and readiness endpoints.
//
// It returns an unstarted server with auth routes, request IDs, access logging
// and panic recovery. The caller owns startup and shutdown.
func New(port string, auth *controller.AuthController, logger *slog.Logger, extra ...RouteRegistrar) *http.Server {

	return &http.Server{
		Addr:              ":" + port,
		Handler:           newRouter(auth, logger, extra...),
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
	}
}

// RouteRegistrar adds a controller's routes to a router.
type RouteRegistrar interface {
	RegisterRoutes(routes gin.IRoutes)
}

// NewInternal assembles the private listener for service-to-service routes.
//
// Parameters:
//
//   - address: Private listener address (INTERNAL_HOST:INTERNAL_PORT). Bind it only to the private service network.
//   - logger: Service logger receiving access and panic records.
//   - routes: Controllers whose /internal/v1 routes this listener serves.
//
// It returns an unstarted server with the same middleware and error envelope
// as the public server, but no public routes.
func NewInternal(address string, logger *slog.Logger, routes ...RouteRegistrar) *http.Server {
	router := baseRouter(logger)
	for _, registrar := range routes {
		registrar.RegisterRoutes(router)
	}

	return &http.Server{
		Addr:              address,
		Handler:           router,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
	}
}

// newRouter wires auth routes and the common HTTP middleware.
func newRouter(auth *controller.AuthController, logger *slog.Logger, extra ...RouteRegistrar) *gin.Engine {
	router := baseRouter(logger)
	auth.RegisterRoutes(router.Group("/api/v1/auth"))
	for _, registrar := range extra {
		registrar.RegisterRoutes(router)
	}

	return router
}

// baseRouter returns a Gin engine with the common middleware and error envelopes.
func baseRouter(logger *slog.Logger) *gin.Engine {
	router := gin.New()
	router.Use(requestid.Middleware(), requestLogger(logger), gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, _ any) {
		logger.ErrorContext(c.Request.Context(), "http_panic", "request_id", requestid.Get(c))
		authrouter.WriteError(c, httpresponse.InternalError())
	}))
	_ = router.SetTrustedProxies(nil)
	router.NoRoute(authrouter.Wrap(func(c *gin.Context) error {
		return &httpresponse.Error{Status: http.StatusNotFound, Code: "NOT_FOUND", Message: "The requested endpoint was not found."}
	}, nil))
	router.HandleMethodNotAllowed = true
	router.NoMethod(authrouter.Wrap(func(c *gin.Context) error {
		return &httpresponse.Error{Status: http.StatusMethodNotAllowed, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not allowed for this endpoint."}
	}, nil))

	return router
}
