package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/config"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/controller"
	statspostgres "github.com/melmut-42/ft_transcendence/backend/stats-service/internal/repository/postgres"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/server"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/service"
)

// setup applies pending migrations when cfg.MigrateOnStart is set, opens the
// schema-scoped pool and creates the HTTP server.
//
// It returns the unstarted server and a cleanup function that closes the pool.
//
// Errors:
//
//   - Migration errors: a migration could not be applied.
//   - PostgreSQL errors: invalid settings or failed connectivity checks.
func setup(ctx context.Context, cfg config.Config, logger *slog.Logger) ([]*http.Server, func(), error) {
	if cfg.MigrateOnStart {
		if err := statspostgres.Migrate(ctx, cfg.DatabaseURL); err != nil {
			return nil, nil, err
		}
		logger.InfoContext(ctx, "database migrations applied")
	}

	pool, err := statspostgres.OpenPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}
	logger.InfoContext(ctx, "PostgreSQL connection established")

	health := controller.NewHealth(service.NewHealth(statspostgres.NewHealth(pool)))

	return []*http.Server{server.New(cfg.Port, health, logger)}, pool.Close, nil
}
