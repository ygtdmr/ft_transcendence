package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/client/authclient"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/config"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/controller"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/middleware/serviceauth"
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

	reads := service.NewStatsReads(statspostgres.NewStatsReads(pool))
	auth := serviceauth.New(cfg.CallerCredentials, serviceauth.DefaultMatrix())
	if len(cfg.CallerCredentials) == 0 {
		logger.WarnContext(ctx, "no service credentials configured; private routes accept no caller")
	}
	internal := server.NewInternal(cfg.InternalPort, logger, controller.NewInternalStats(reads, auth))

	var public []server.RouteRegistrar
	if cfg.AuthInternalURL != "" {
		sessions, err := authclient.New(cfg.AuthInternalURL, cfg.AuthServiceCredential, cfg.AuthInternalTimeout)
		if err != nil {
			pool.Close()
			return nil, nil, err
		}
		public = append(public, controller.NewMatches(reads, sessions))
	} else {
		logger.WarnContext(ctx, "AUTH_INTERNAL_URL is not set; match history is not served")
	}

	return []*http.Server{server.New(cfg.Port, health, logger, public...), internal}, pool.Close, nil
}
