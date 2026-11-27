// Package app assembles the Web BFF: one public listener that forwards each
// browser REST route to its owning service after the same-origin check and
// session enforcement the route requires.
//
// The BFF serves no /internal routes and accepts no private caller, so it has
// no private listener.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/shared/ginhttp"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/shared/identity"
	"github.com/melmut-42/ft_transcendence/backend/shared/lifecycle"
	"github.com/melmut-42/ft_transcendence/backend/shared/originguard"
	"github.com/melmut-42/ft_transcendence/backend/shared/privateclient"
	"github.com/melmut-42/ft_transcendence/backend/shared/sessionclient"
	"github.com/melmut-42/ft_transcendence/backend/web-bff/internal/config"
	"github.com/melmut-42/ft_transcendence/backend/web-bff/internal/proxy"
	"github.com/melmut-42/ft_transcendence/backend/web-bff/internal/routes"
)

// ContractVersion is the REST API v1 contract revision GET /api/v1/health
// reports.
const ContractVersion = "1.2.0-dev"

// Dependencies replaces production collaborators in tests. Nil fields use the
// real implementations built from the configuration.
type Dependencies struct {
	// Sessions validates ft_session; nil uses Auth's session.validate route.
	Sessions identity.SessionValidator
	// Transport sends forwarded requests; nil uses a transport bounded by
	// UPSTREAM_TIMEOUT.
	Transport http.RoundTripper
	// Now returns the current time for GET /api/v1/health; nil uses time.Now.
	Now func() time.Time
}

// BFF is an assembled, unstarted Web BFF.
type BFF struct {
	// Public serves /healthz, /readyz, GET /api/v1/health and the forwarded routes.
	Public *http.Server
	// Health reports liveness and readiness.
	Health *lifecycle.Health

	cfg    config.Config
	logger *slog.Logger
}

// New assembles the BFF without opening its listener.
func New(cfg config.Config, logger *slog.Logger, deps Dependencies) (*BFF, error) {
	if logger == nil {
		logger = slog.Default()
	}
	guard, err := originguard.New(cfg.PublicOrigin)
	if err != nil {
		return nil, fmt.Errorf("web-bff: public origin: %w", err)
	}
	sessions := deps.Sessions
	if sessions == nil {
		client, err := privateclient.New(cfg.Auth)
		if err != nil {
			return nil, fmt.Errorf("web-bff: auth client: %w", err)
		}
		sessions = sessionclient.New(client)
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}

	health := lifecycle.NewHealth()
	engine := NewEngine(logger)
	engine.GET("/healthz", gin.WrapH(health.Liveness()))
	engine.GET("/readyz", gin.WrapH(health.Readiness()))
	engine.GET("/api/v1/health", func(c *gin.Context) {
		httpresponse.WriteData(c.Writer, http.StatusOK, gin.H{
			"status":           "OK",
			"contract_version": ContractVersion,
			"server_time":      now().UTC().Format(time.RFC3339),
		})
	})

	targets := map[routes.Upstream]proxyTarget{
		routes.Auth:  {target: cfg.Upstreams.Auth},
		routes.User:  {target: cfg.Upstreams.User},
		routes.Stats: {target: cfg.Upstreams.Stats},
		routes.Game:  {target: cfg.Upstreams.Game, forwardIdentity: true},
	}
	handlers := map[routes.Upstream]gin.HandlerFunc{}
	for name, t := range targets {
		handlers[name] = gin.WrapH(proxy.New(proxy.Options{
			Name: string(name), Target: t.target, ForwardIdentity: t.forwardIdentity,
			Transport: deps.Transport, ResponseTimeout: cfg.UpstreamTimeout, Logger: logger,
		}))
	}

	csrf := ginhttp.Wrap(guard.CSRF())
	authenticate := ginhttp.Wrap(identity.Authenticate(sessions, identity.Options{Logger: logger}))
	for _, route := range routes.Table() {
		var chain []gin.HandlerFunc
		switch route.Access {
		case routes.Public:
		case routes.RefreshCookie:
			chain = append(chain, csrf)
		case routes.Session:
			// The origin is checked before the session, as the contract requires.
			chain = append(chain, csrf, authenticate)
		default:
			return nil, fmt.Errorf("web-bff: route %s %s has no access rule", route.Method, route.Path)
		}
		chain = append(chain, handlers[route.Upstream])
		engine.Handle(route.Method, route.Path, chain...)
	}

	return &BFF{
		Public: NewServer(":"+strconv.Itoa(cfg.Port), engine),
		Health: health,
		cfg:    cfg,
		logger: logger,
	}, nil
}

type proxyTarget struct {
	target          *url.URL
	forwardIdentity bool
}

// Run serves the public listener until ctx is cancelled or it fails, then shuts
// it down: readiness fails first and in-flight requests finish within
// SHUTDOWN_TIMEOUT.
func (b *BFF) Run(ctx context.Context) error {
	opts := b.cfg.Lifecycle
	opts.Health = b.Health
	opts.Logger = b.logger

	return lifecycle.Serve(ctx, b.Public, opts)
}

// NewEngine returns a Gin engine with request ids and trace context,
// credential-free access logs, panic recovery and the contract's NOT_FOUND /
// METHOD_NOT_ALLOWED envelopes.
func NewEngine(logger *slog.Logger) *gin.Engine {
	engine := gin.New()
	engine.HandleMethodNotAllowed = true
	_ = engine.SetTrustedProxies(nil)
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

// NewServer returns an unstarted HTTP server with the shared timeouts. The
// write timeout leaves room for the upstream wait and an avatar upload.
func NewServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      45 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}
