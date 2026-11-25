// Command server runs the service skeleton. A real service keeps this file as
// it is and only changes the import path.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/melmut-42/ft_transcendence/backend/shared/logging"
	"github.com/melmut-42/ft_transcendence/backend/shared/skeleton"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := skeleton.Main(ctx, os.LookupEnv, os.Stdout)
	stop()
	if err != nil {
		// The configuration may be invalid, so log with a fixed bootstrap logger.
		// The error names variables only, never values.
		logger, _ := logging.New(logging.Config{Service: skeleton.ServiceName}, os.Stdout)
		logger.Error("server_stopped", slog.String("error", err.Error()))
		os.Exit(1)
	}
}
