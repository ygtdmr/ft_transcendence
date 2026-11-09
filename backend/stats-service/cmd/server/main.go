package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/shared/logging"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/config"
)

const serviceName = "stats-service"

func main() {
	logger, _ := logging.New(logging.Config{Service: serviceName}, os.Stdout)
	slog.SetDefault(logger)
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger, err := logging.New(
		logging.Config{Format: cfg.LogFormat, Level: cfg.LogLevel,
			Service: serviceName},
		os.Stdout)

	if err != nil {
		return err
	}
	slog.SetDefault(logger)
	configureGin(cfg.Environment)

	servers, cleanup, err := setup(ctx, cfg, logger)
	if err != nil {
		return err
	}

	defer cleanup()

	return runHTTPServers(ctx, servers...)
}

func configureGin(environment string) {
	switch environment {
	case "production":
		gin.SetMode(gin.ReleaseMode)
	case "test":
		gin.SetMode(gin.TestMode)
	default:
		gin.SetMode(gin.DebugMode)
	}

}
