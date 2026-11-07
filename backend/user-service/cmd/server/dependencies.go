package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/config"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/controller"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/middleware/serviceauth"
	userpostgres "github.com/melmut-42/ft_transcendence/backend/user-service/internal/repository/postgres"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/server"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

// setup initializes application dependencies and creates the HTTP server.
//
// Parameters:
//
//   - ctx: Controls migration and database connection startup.
//   - cfg: Validated application settings.
//   - logger: Configured service logger.
//
// It applies pending migrations when cfg.MigrateOnStart is set, opens the
// schema-scoped connection pool and wires the layers together. Private routes
// are served only on the internal listener and accept only configured callers.
//
// It returns the unstarted public and internal servers and a cleanup function
// that closes the pool. The caller must invoke cleanup after HTTP shutdown.
// On failure, every value is nil.
//
// Errors:
//
//   - Migration errors: A migration could not be applied; see repository Migrate.
//   - PostgreSQL errors: Invalid connection settings or failed connectivity checks, including context cancellation and deadlines.
func setup(ctx context.Context, cfg config.Config, logger *slog.Logger) ([]*http.Server, func(), error) {
	if cfg.MigrateOnStart {
		if err := userpostgres.Migrate(ctx, cfg.DatabaseURL); err != nil {
			return nil, nil, err
		}
		logger.InfoContext(ctx, "database migrations applied")
	}

	pool, err := userpostgres.OpenPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}

	logger.InfoContext(ctx, "PostgreSQL connection established")

	healthRepository := userpostgres.NewHealth(pool)
	healthService := service.NewHealth(healthRepository)
	healthController := controller.NewHealth(healthService)

	if len(cfg.CallerCredentials) == 0 {
		logger.WarnContext(ctx, "no service credentials configured; private routes accept no caller")
	}
	auth := serviceauth.New(cfg.CallerCredentials, serviceauth.DefaultMatrix())
	profileService := service.NewProfile(userpostgres.NewProfile(pool))
	profileReads := service.NewProfileRead(userpostgres.NewProfile(pool), service.AvatarURLs{Base: cfg.AvatarBaseURL})
	internal := server.NewInternal(cfg.InternalPort, logger,
		controller.NewInternalProvisioning(profileService, auth),
		controller.NewInternalProfile(profileReads, auth))

	return []*http.Server{server.New(cfg.Port, healthController, logger), internal}, pool.Close, nil
}
