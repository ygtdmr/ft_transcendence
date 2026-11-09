package main

import (
	"context"
	"errors"
	"net/http"
	"time"
)

const shutdownTimeout = 10 * time.Second

// runHTTPServers serves every listener until shutdown or the first listener failure.
//
// Parameters:
//
//   - ctx: Cancellation starts graceful shutdown with a separate shutdown timeout.
//   - servers: HTTP servers containing the listener settings and handlers.
//
// When one listener fails, the others are shut down too, so the service never
// keeps running with only part of its listeners.
//
// It returns nil after a normal shutdown.
//
// Errors:
//
//   - Listener errors: a server could not start or stopped unexpectedly.
//   - Shutdown errors: graceful shutdown failed or exceeded its timeout; the server is then closed.
func runHTTPServers(ctx context.Context, servers ...*http.Server) error {
	serverErrors := make(chan error, len(servers))
	for _, server := range servers {
		go func() {
			serverErrors <- server.ListenAndServe()
		}()
	}

	var failure error
	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			failure = err
		}
	case <-ctx.Done():
	}

	shutdownErr := shutdownHTTPServers(servers)
	if failure != nil {
		return failure
	}

	return shutdownErr
}

func shutdownHTTPServers(servers []*http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	var errs []error
	for _, server := range servers {
		if err := server.Shutdown(ctx); err != nil {
			_ = server.Close()
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}
