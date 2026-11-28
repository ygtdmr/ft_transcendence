// Command server runs the Web BFF.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/gin-gonic/gin"
	sharedconfig "github.com/melmut-42/ft_transcendence/backend/shared/config"
	"github.com/melmut-42/ft_transcendence/backend/shared/logging"
	"github.com/melmut-42/ft_transcendence/backend/web-bff/internal/app"
	"github.com/melmut-42/ft_transcendence/backend/web-bff/internal/config"
)

func main() {
	bootstrap, _ := logging.New(logging.Config{Service: config.ServiceName}, os.Stdout)
	slog.SetDefault(bootstrap)
	if err := run(); err != nil {
		// The error names variables only, never values.
		slog.Error("server_stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		return err
	}
	logger, err := logging.New(cfg.Logging, os.Stdout)
	if err != nil {
		return err
	}
	slog.SetDefault(logger)
	if cfg.Environment == sharedconfig.Production {
		gin.SetMode(gin.ReleaseMode)
	} else {
		gin.SetMode(gin.TestMode)
	}

	bff, err := app.New(cfg, logger, app.Dependencies{})
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "server_started", "port", cfg.Port)

	return bff.Run(ctx)
}
