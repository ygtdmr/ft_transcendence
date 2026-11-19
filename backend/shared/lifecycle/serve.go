package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/config"
)

const (
	defaultShutdownTimeout = 10 * time.Second
	defaultCloseTimeout    = 5 * time.Second
)

// ErrShutdownTimeout reports that in-flight requests did not finish within the
// shutdown timeout and their connections were closed forcibly.
var ErrShutdownTimeout = errors.New("lifecycle: shutdown timed out; remaining connections were closed")

// Component is something that must be released at shutdown (a connection pool,
// a broker connection, a background worker).
type Component struct {
	Name  string
	Close func(ctx context.Context) error
}

// Func adapts a plain close function such as pool.Close.
func Func(name string, closeFn func()) Component {
	return Component{Name: name, Close: func(context.Context) error { closeFn(); return nil }}
}

// Options configures Serve. The zero value is valid.
type Options struct {
	// Health, when set, is marked draining as soon as shutdown starts.
	Health *Health
	// ShutdownTimeout bounds how long in-flight requests may take. Default 10s.
	ShutdownTimeout time.Duration
	// DrainDelay is how long the server keeps serving, reporting not ready,
	// before it stops accepting connections. It gives a load balancer or
	// orchestrator time to notice the failing readiness probe. Default 0.
	DrainDelay time.Duration
	// CloseTimeout bounds each component's Close. Default 5s.
	CloseTimeout time.Duration
	// Components are closed in reverse order (last registered first) after the
	// HTTP server has stopped, so a worker registered after the pool stops first.
	Components []Component
	// Logger receives lifecycle records. Default slog.Default().
	Logger *slog.Logger
}

// OptionsFromEnv reads the shutdown timings through r:
//
//	SHUTDOWN_TIMEOUT        default 10s, 1s-5m
//	SHUTDOWN_DRAIN_DELAY    default 0, up to 2m
//	SHUTDOWN_CLOSE_TIMEOUT  default 5s, 1s-2m
func OptionsFromEnv(r *config.Reader) Options {
	return Options{
		ShutdownTimeout: r.Duration("SHUTDOWN_TIMEOUT", defaultShutdownTimeout, time.Second, 5*time.Minute),
		DrainDelay:      r.Duration("SHUTDOWN_DRAIN_DELAY", 0, 0, 2*time.Minute),
		CloseTimeout:    r.Duration("SHUTDOWN_CLOSE_TIMEOUT", defaultCloseTimeout, time.Second, 2*time.Minute),
	}
}

// Serve runs srv until ctx is cancelled (typically by SIGTERM through
// signal.NotifyContext) or the listener fails, then shuts down in this order:
//
//  1. mark Health draining, so readiness fails (liveness keeps answering 200);
//  2. wait DrainDelay while still serving;
//  3. stop accepting connections and wait for in-flight requests, at most
//     ShutdownTimeout; on timeout the remaining connections are closed and
//     ErrShutdownTimeout is reported;
//  4. close Components in reverse order, each bounded by CloseTimeout.
//
// Components are closed even when the listener fails to start. Serve returns nil
// after a clean shutdown, otherwise the joined errors. Hijacked connections
// (WebSockets) are not tracked by net/http; register srv.RegisterOnShutdown to
// close those.
func Serve(ctx context.Context, srv *http.Server, opts Options) error {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	shutdownTimeout := orDefault(opts.ShutdownTimeout, defaultShutdownTimeout)
	closeTimeout := orDefault(opts.CloseTimeout, defaultCloseTimeout)

	listenErr := make(chan error, 1)
	go func() { listenErr <- srv.ListenAndServe() }()

	var errs []error
	select {
	case err := <-listenErr:
		if !errors.Is(err, http.ErrServerClosed) {
			errs = append(errs, fmt.Errorf("lifecycle: listener failed: %w", err))
		}
	case <-ctx.Done():
		logger.Info("shutdown_started", "drain_delay", opts.DrainDelay.String(), "timeout", shutdownTimeout.String())
		if opts.Health != nil {
			opts.Health.SetDraining()
		}
		if opts.DrainDelay > 0 {
			time.Sleep(opts.DrainDelay)
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		if err := srv.Shutdown(shutdownCtx); err != nil {
			_ = srv.Close()
			errs = append(errs, ErrShutdownTimeout)
			logger.Error("shutdown_forced", "timeout", shutdownTimeout.String())
		}
		cancel()
	}

	for i := len(opts.Components) - 1; i >= 0; i-- {
		component := opts.Components[i]
		closeCtx, cancel := context.WithTimeout(context.Background(), closeTimeout)
		err := closeComponent(closeCtx, component)
		cancel()
		if err != nil {
			logger.Error("component_close_failed", "component", component.Name)
			errs = append(errs, fmt.Errorf("lifecycle: closing %s failed", component.Name))
			continue
		}
		logger.Info("component_closed", "component", component.Name)
	}
	if len(errs) == 0 {
		logger.Info("shutdown_complete")
	}
	return errors.Join(errs...)
}

// closeComponent runs Close and enforces ctx even if Close ignores it. The Close
// error is dropped because it can carry host or credential details.
func closeComponent(ctx context.Context, c Component) (err error) {
	if c.Close == nil {
		return nil
	}
	done := make(chan error, 1)
	go func() {
		defer func() {
			if recover() != nil {
				done <- errors.New("panic")
			}
		}()
		done <- c.Close(ctx)
	}()
	select {
	case closeErr := <-done:
		return closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func orDefault(d, fallback time.Duration) time.Duration {
	if d <= 0 {
		return fallback
	}
	return d
}
