package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/melmut-42/ft_transcendence/backend/shared/rabbitmq"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/client/authclient"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/config"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/consumer"
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
	internal := server.NewInternal(cfg.InternalAddress(), logger, controller.NewInternalStats(reads, auth),
		controller.NewInternalCleanup(service.NewAccountCleanup(statspostgres.NewAccountCleanup(pool)), auth))

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

	cleanup := pool.Close
	if cfg.BrokerURL != "" {
		handler := consumer.NewHandler(service.NewMatchIngest(statspostgres.NewMatches(pool)),
			service.NewPenaltyIngest(statspostgres.NewPenalties(pool)))
		runner := consumer.NewRunner(rabbitmq.Config{URL: cfg.BrokerURL, Name: "stats-service"}, cfg.EventsQueue,
			handler, logger)
		cleanup = startConsumer(ctx, runner, cleanup)
	} else {
		logger.WarnContext(ctx, "RABBITMQ_URL is not set; finalized matches and penalties are not ingested")
	}

	return []*http.Server{server.New(cfg.Port, health, logger, public...), internal}, cleanup, nil
}

// startConsumer runs the event consumer until shutdown. The returned cleanup
// stops the consumer, waits for the delivery in progress, then runs next.
func startConsumer(ctx context.Context, runner *consumer.Runner, next func()) func() {
	consumerCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	go func() {
		defer close(done)
		runner.Run(consumerCtx)
	}()

	return func() {
		cancel()
		<-done
		next()
	}
}
