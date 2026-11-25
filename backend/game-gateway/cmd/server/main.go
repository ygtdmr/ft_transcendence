// Command server runs the Game Gateway.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/game-gateway/internal/app"
	"github.com/melmut-42/ft_transcendence/backend/game-gateway/internal/config"
	sharedconfig "github.com/melmut-42/ft_transcendence/backend/shared/config"
	"github.com/melmut-42/ft_transcendence/backend/shared/logging"
)

func main() {
	bootstrap, _ := logging.New(logging.Config{Service: config.ServiceName}, os.Stdout)
	slog.SetDefault(bootstrap)
	if err := run(); err != nil {
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
	switch cfg.Environment {
	case sharedconfig.Production:
		gin.SetMode(gin.ReleaseMode)
	case sharedconfig.Test:
		gin.SetMode(gin.TestMode)
	default:
		gin.SetMode(gin.DebugMode)
	}
	if missing := cfg.MissingCallers(); len(missing) > 0 {
		logger.WarnContext(ctx, "private callers without a credential are refused", "callers", missing)
	}

	gateway, err := app.New(cfg, logger, app.Dependencies{})
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "server_started", "port", cfg.Port, "internal_addr", cfg.InternalAddr)

	return gateway.Run(ctx)
}
