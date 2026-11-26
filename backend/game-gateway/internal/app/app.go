// Package app assembles the Game Gateway: a public listener with the room
// socket and health routes, and a private listener with the /internal/v1
// routes. It owns startup (Report gateway started) and the shutdown order.
package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/game-gateway/internal/config"
	"github.com/melmut-42/ft_transcendence/backend/game-gateway/internal/connectivity"
	"github.com/melmut-42/ft_transcendence/backend/game-gateway/internal/gamesession"
	"github.com/melmut-42/ft_transcendence/backend/game-gateway/internal/internalapi"
	"github.com/melmut-42/ft_transcendence/backend/game-gateway/internal/socket"
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

var goingAway = socketregistry.Closure{Code: socket.CloseGoingAway, Reason: "GOING_AWAY"}

// Rooms is the Game Session client the gateway needs.
type Rooms interface {
	socket.Rooms
	ResetConnections(ctx context.Context) error
}

// Dependencies lets tests replace what production builds from the config.
type Dependencies struct {
	// Sessions validates the ft_session cookie. Nil means Auth Service's
	// session.validate through cfg.Auth.
	Sessions identity.SessionValidator
	// Rooms calls Game Session. Nil means the room.socket routes through
	// cfg.Game.
	Rooms Rooms
	// ResetBackoff is the first wait between startup reports. Default 500ms,
	// doubling up to 5s.
	ResetBackoff time.Duration
}

// Gateway is an assembled, unstarted gateway.
type Gateway struct {
	// Public serves GET /ws/v2/rooms/{room_id}, /healthz and /readyz.
	Public *http.Server
	// Internal serves /internal/v1/*.
	Internal *http.Server
	// Registry is this instance's in-memory socket registry.
	Registry *socketregistry.Registry
	// Tracker counts each member's room sockets for connectivity reports.
	Tracker *connectivity.Tracker
	// Health reports liveness and readiness.
	Health *lifecycle.Health

	cfg          config.Config
	logger       *slog.Logger
	sockets      *socket.Handler
	rooms        Rooms
	resetBackoff time.Duration
	started      atomic.Bool
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
	rooms := deps.Rooms
	if rooms == nil {
		client, err := privateclient.New(cfg.Game)
		if err != nil {
			return nil, err
		}
		rooms = gamesession.New(client)
	}
	backoff := deps.ResetBackoff
	if backoff <= 0 {
		backoff = 500 * time.Millisecond
	}

	g := &Gateway{
		Registry:     socketregistry.New(socketregistry.Options{QueueSize: cfg.Socket.SendQueue}),
		Tracker:      &connectivity.Tracker{},
		Health:       lifecycle.NewHealth(),
		cfg:          cfg,
		logger:       logger,
		rooms:        rooms,
		resetBackoff: backoff,
	}
	g.Health.Add(lifecycle.Check{Name: "game-session-start-report", Fn: func(context.Context) error {
		if !g.started.Load() {
			return errors.New("not reported yet")
		}
		return nil
	}})
	g.sockets = socket.New(socket.Options{
		Registry:     g.Registry,
		Tracker:      g.Tracker,
		Origin:       guard,
		Sessions:     sessions,
		Rooms:        rooms,
		Logger:       logger,
		PingInterval: cfg.Socket.PingInterval,
		PongWait:     cfg.Socket.PongWait,
	})

	public := baseEngine(logger)
	public.GET("/healthz", gin.WrapH(g.Health.Liveness()))
	public.GET("/readyz", gin.WrapH(g.Health.Readiness()))
	g.sockets.RegisterRoutes(public)

	private := baseEngine(logger)
	internalapi.New(serviceauth.New(cfg.Callers, config.Matrix()), g.Registry, logger).RegisterRoutes(private)

	g.Public = newServer(":"+strconv.Itoa(cfg.Port), public)
	g.Internal = newServer(cfg.InternalAddr, private)
	g.Public.RegisterOnShutdown(func() { g.Registry.CloseAll(goingAway) })

	return g, nil
}

// ReportStarted tells Game Session that this gateway holds no room socket
// (Report gateway started) and opens the socket route once it answered. It
// retries with backoff until it succeeds or ctx ends; until then every
// handshake is refused with 503 and readiness fails.
func (g *Gateway) ReportStarted(ctx context.Context) error {
	wait := g.resetBackoff
	for {
		err := g.rooms.ResetConnections(ctx)
		if err == nil {
			g.started.Store(true)
			g.sockets.Open()
			g.logger.InfoContext(ctx, "game_session_start_reported")

			return nil
		}
		g.logger.WarnContext(ctx, "game_session_start_report_failed", "retry_in", wait.String())
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		wait = min(wait*2, 5*time.Second)
	}
}

// Run reports the start to Game Session, serves both listeners until ctx is
// cancelled or one listener fails, then shuts both down: readiness fails,
// every socket closes with 1001 GOING_AWAY, and Run waits for the socket
// goroutines (each reports its member's last socket closed).
func (g *Gateway) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = g.ReportStarted(runCtx) }()

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
// waits until their goroutines finished, bounded by ctx.
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
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
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
			Message: "The requested endpoint was not found."})
	})
	engine.NoMethod(func(c *gin.Context) {
		httpresponse.WriteError(c.Writer, &httpresponse.Error{Status: http.StatusMethodNotAllowed,
			Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not allowed for this endpoint."})
	})

	return engine
}
