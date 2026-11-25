// Package app assembles the Notification Gateway: a public listener with the
// notification socket and health routes, and a private listener with the
// /internal/v1 routes. It owns startup and the shutdown order.
package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/notification-gateway/internal/config"
	"github.com/melmut-42/ft_transcendence/backend/notification-gateway/internal/internalapi"
	"github.com/melmut-42/ft_transcendence/backend/notification-gateway/internal/socket"
	"github.com/melmut-42/ft_transcendence/backend/shared/ginhttp"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/shared/identity"
	"github.com/melmut-42/ft_transcendence/backend/shared/lifecycle"
	"github.com/melmut-42/ft_transcendence/backend/shared/originguard"
	"github.com/melmut-42/ft_transcendence/backend/shared/privateclient"
	"github.com/melmut-42/ft_transcendence/backend/shared/serviceauth"
	"github.com/melmut-42/ft_transcendence/backend/shared/sessionclient"
	"github.com/melmut-42/ft_transcendence/backend/shared/socketregistry"
)

const (
	readHeaderTimeout = 5 * time.Second
	idleTimeout       = 60 * time.Second
)

// goingAway closes every socket at shutdown.
var goingAway = socketregistry.Closure{Code: socket.CloseGoingAway, Reason: "GOING_AWAY"}

// Dependencies lets tests replace what production builds from the config.
type Dependencies struct {
	// Sessions validates the ft_session cookie. Nil means Auth Service's
	// session.validate through cfg.Auth.
	Sessions identity.SessionValidator
	// Now replaces the clock of event timestamps. Nil means time.Now.
	Now func() time.Time
}

// Gateway is an assembled, unstarted gateway.
type Gateway struct {
	// Public serves GET /ws/v2/notifications, /healthz and /readyz.
	Public *http.Server
	// Internal serves /internal/v1/*.
	Internal *http.Server
	// Registry is this instance's in-memory socket registry.
	Registry *socketregistry.Registry
	// Health reports liveness and readiness.
	Health *lifecycle.Health

	cfg     config.Config
	logger  *slog.Logger
	sockets *socket.Handler
}

// New assembles the gateway without opening a listener.
func New(cfg config.Config, logger *slog.Logger, deps Dependencies) (*Gateway, error) {
	if logger == nil {
		logger = slog.Default()
	}
	guard, err := originguard.New(cfg.PublicOrigin)
	if err != nil {
		return nil, err
	}
	sessions := deps.Sessions
	if sessions == nil {
		client, err := privateclient.New(cfg.Auth)
		if err != nil {
			return nil, err
		}
		sessions = sessionclient.New(client)
	}

	registry := socketregistry.New(socketregistry.Options{QueueSize: cfg.Socket.SendQueue})
	health := lifecycle.NewHealth()
	sockets := socket.New(socket.Options{
		Registry:     registry,
		Origin:       guard,
		Sessions:     sessions,
		Logger:       logger,
		PingInterval: cfg.Socket.PingInterval,
		PongWait:     cfg.Socket.PongWait,
		Now:          deps.Now,
	})

	public := baseEngine(logger)
	public.GET("/healthz", gin.WrapH(health.Liveness()))
	public.GET("/readyz", gin.WrapH(health.Readiness()))
	sockets.RegisterRoutes(public)

	private := baseEngine(logger)
	internalapi.New(serviceauth.New(cfg.Callers, config.Matrix()), registry, logger).RegisterRoutes(private)

	g := &Gateway{
		Public:   newServer(":"+strconv.Itoa(cfg.Port), public),
		Internal: newServer(cfg.InternalAddr, private),
		Registry: registry,
		Health:   health,
		cfg:      cfg,
		logger:   logger,
		sockets:  sockets,
	}
	// net/http does not track hijacked connections, so shutdown closes the
	// sockets itself as soon as it starts.
	g.Public.RegisterOnShutdown(func() { registry.CloseAll(goingAway) })

	return g, nil
}

// Run serves both listeners until ctx is cancelled or one listener fails, then
// shuts both down: readiness fails first, new connections stop, every socket
// is closed with 1001 GOING_AWAY, and Run waits for the socket goroutines.
func (g *Gateway) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	opts := g.cfg.Lifecycle
	opts.Health = g.Health
	opts.Logger = g.logger
	publicOpts := opts
	publicOpts.Components = []lifecycle.Component{{Name: "sockets", Close: g.CloseSockets}}

	errs := make(chan error, 2)
	go func() {
		errs <- lifecycle.Serve(runCtx, g.Public, publicOpts)
		cancel()
	}()
	go func() {
		errs <- lifecycle.Serve(runCtx, g.Internal, opts)
		cancel()
	}()

	return errors.Join(<-errs, <-errs)
}

// CloseSockets closes every socket with 1001 GOING_AWAY, refuses new ones and
// waits until their goroutines finished, bounded by ctx. Run calls it at
// shutdown.
func (g *Gateway) CloseSockets(ctx context.Context) error {
	g.Registry.CloseAll(goingAway)
	if !g.sockets.Wait(ctx.Done()) {
		return ctx.Err()
	}

	return nil
}

func newServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       idleTimeout,
	}
}

// baseEngine has request ids and trace context, access logs without
// credentials, panic recovery and the contract's 404/405 envelopes.
func baseEngine(logger *slog.Logger) *gin.Engine {
	engine := gin.New()
	engine.HandleMethodNotAllowed = true
	engine.Use(ginhttp.Trace(), ginhttp.AccessLog(logger), gin.CustomRecovery(func(c *gin.Context, _ any) {
		logger.ErrorContext(c.Request.Context(), "panic_recovered")
		httpresponse.WriteError(c.Writer, httpresponse.InternalError())
		c.Abort()
	}))
	engine.NoRoute(func(c *gin.Context) {
		httpresponse.WriteError(c.Writer, &httpresponse.Error{Status: http.StatusNotFound, Code: "NOT_FOUND",
			Message: "The requested resource does not exist."})
	})
	engine.NoMethod(func(c *gin.Context) {
		httpresponse.WriteError(c.Writer, &httpresponse.Error{Status: http.StatusMethodNotAllowed,
			Code: "METHOD_NOT_ALLOWED", Message: "The method is not allowed for this resource."})
	})

	return engine
}
