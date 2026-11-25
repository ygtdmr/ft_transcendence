// Package skeleton is the reference template for a backend service (#83).
//
// It is compiled and tested with the shared module, so it cannot drift from the
// shared library, but no service imports it: a new service copies it into
// backend/<service>/ and renames it. backend/shared/SKELETON.md lists the
// conventions it demonstrates and how to instantiate it.
//
// In working code it shows:
//   - configuration through config.Reader: the common settings (APP_ENV,
//     LOG_FORMAT, LOG_LEVEL, PORT), the private listener (INTERNAL_HOST,
//     INTERNAL_PORT), caller credentials as files (<CALLER>_CREDENTIAL_FILE and
//     <CALLER>_PREVIOUS_CREDENTIAL_FILE) and shutdown bounds (SHUTDOWN_*), with
//     every problem reported at once by variable name and never by value;
//   - a public listener (health and public routes) and a private listener
//     (/internal/v1 only), both with request ids, trace context, access logs,
//     panic recovery and the contract's error envelope;
//   - one private route behind serviceauth, to show the caller/scope pattern;
//   - liveness, readiness and graceful shutdown through lifecycle;
//   - structured logs carrying the service and environment.
package skeleton

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/shared/config"
	"github.com/melmut-42/ft_transcendence/backend/shared/ginhttp"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/shared/lifecycle"
	"github.com/melmut-42/ft_transcendence/backend/shared/logging"
	"github.com/melmut-42/ft_transcendence/backend/shared/serviceauth"
)

// ServiceName is the fixed service attribute of every log record. A real
// service uses its directory name, such as "user-service".
const ServiceName = "skeleton-service"

// Default ports: public 8080, private 8081, the same for every service inside
// its own container.
const (
	DefaultPort         = 8080
	DefaultInternalPort = 8081
)

// The template accepts one private caller with one scope, to show the pattern.
// A real service lists its rows of the private authorization matrix.
const (
	ExampleCaller = "user-service"
	ExampleScope  = "skeleton.read"
	ExampleRoute  = "/internal/v1/skeleton/caller"
)

var callerFiles = map[string]serviceauth.CallerFiles{
	ExampleCaller: {Current: "USER_SERVICE_CREDENTIAL_FILE", Previous: "USER_SERVICE_PREVIOUS_CREDENTIAL_FILE"},
}

// Config is the validated configuration.
type Config struct {
	config.Common

	// InternalAddr is the private listener address (INTERNAL_HOST:INTERNAL_PORT).
	InternalAddr string
	// Callers holds each private caller's accepted credentials, current and
	// previous.
	Callers map[string][]string
	// Lifecycle bounds shutdown (SHUTDOWN_TIMEOUT, SHUTDOWN_DRAIN_DELAY,
	// SHUTDOWN_CLOSE_TIMEOUT).
	Lifecycle lifecycle.Options
}

// LoadConfig reads and validates the configuration through lookup
// (os.LookupEnv in production). The error lists every problem by variable
// name; it never contains a value, a file path or a secret.
func LoadConfig(lookup config.LookupFunc) (Config, error) {
	r := config.NewReader(lookup)
	cfg := Config{Common: r.Common(ServiceName, DefaultPort)}
	cfg.InternalAddr = r.InternalAddress(DefaultInternalPort)
	cfg.Callers = serviceauth.LoadCredentials(r, callerFiles)
	cfg.Lifecycle = lifecycle.OptionsFromEnv(r)
	if err := r.Err(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// Service is an assembled, unstarted service.
type Service struct {
	// Public serves /healthz, /readyz and the service's public routes.
	Public *http.Server
	// Internal serves /internal/v1/* only.
	Internal *http.Server
	// Health reports liveness and readiness.
	Health *lifecycle.Health

	cfg    Config
	logger *slog.Logger
}

// New assembles the service without opening a listener.
func New(cfg Config, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	health := lifecycle.NewHealth()
	// A real service registers its required dependencies here, for example:
	//   health.Add(lifecycle.Check{Name: "postgres", Fn: func(ctx context.Context) error {
	//       return postgres.Check(ctx, pool, 0)
	//   }})

	public := NewEngine(logger)
	public.GET("/healthz", gin.WrapH(health.Liveness()))
	public.GET("/readyz", gin.WrapH(health.Readiness()))

	private := NewEngine(logger)
	auth := serviceauth.New(cfg.Callers, serviceauth.Matrix{ExampleCaller: {ExampleScope}})
	private.GET(ExampleRoute, ginhttp.Wrap(auth.Require(ExampleScope)), func(c *gin.Context) {
		httpresponse.WriteData(c.Writer, http.StatusOK, gin.H{"caller": serviceauth.Caller(c.Request.Context())})
	})

	return &Service{
		Public:   NewServer(":"+strconv.Itoa(cfg.Port), public),
		Internal: NewServer(cfg.InternalAddr, private),
		Health:   health,
		cfg:      cfg,
		logger:   logger,
	}
}

// Run serves both listeners until ctx is cancelled or one of them fails, then
// shuts both down: readiness fails first, in-flight requests finish within
// SHUTDOWN_TIMEOUT, and components close in reverse order.
func (s *Service) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	opts := s.cfg.Lifecycle
	opts.Health = s.Health
	opts.Logger = s.logger
	// A real service lists what it must release, last registered closes first:
	//   opts.Components = []lifecycle.Component{lifecycle.Func("postgres", pool.Close)}

	errs := make(chan error, 2)
	for _, srv := range []*http.Server{s.Public, s.Internal} {
		go func() {
			errs <- lifecycle.Serve(runCtx, srv, opts)
			cancel()
		}()
	}

	return errors.Join(<-errs, <-errs)
}

// Main is the whole startup sequence a service's main function runs: load the
// configuration, build the logger, set the Gin mode, assemble and run. stdout
// receives the logs.
func Main(ctx context.Context, lookup config.LookupFunc, stdout io.Writer) error {
	cfg, err := LoadConfig(lookup)
	if err != nil {
		return err
	}
	logger, err := logging.New(cfg.Logging, stdout)
	if err != nil {
		return err
	}
	setGinMode(cfg.Environment)
	for caller := range callerFiles {
		if len(cfg.Callers[caller]) == 0 {
			logger.WarnContext(ctx, "private caller without a credential is refused", "caller", caller)
		}
	}
	logger.InfoContext(ctx, "server_started", "port", cfg.Port, "internal_addr", cfg.InternalAddr)

	return New(cfg, logger).Run(ctx)
}

func setGinMode(env config.Environment) {
	switch env {
	case config.Production:
		gin.SetMode(gin.ReleaseMode)
	case config.Test:
		gin.SetMode(gin.TestMode)
	default:
		gin.SetMode(gin.DebugMode)
	}
}

// NewEngine returns a Gin engine with the middleware every listener uses:
// request ids and trace context, credential-free access logs, panic recovery,
// and the contract's NOT_FOUND / METHOD_NOT_ALLOWED envelopes.
func NewEngine(logger *slog.Logger) *gin.Engine {
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

// NewServer returns an unstarted HTTP server with the shared timeouts. A
// service whose handlers hijack connections (WebSockets) closes those itself at
// shutdown (srv.RegisterOnShutdown); the library clears these deadlines when it
// takes over the connection.
func NewServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}
