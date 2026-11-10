package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/client/authclient"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/client/gatewayclient"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/client/statsclient"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/config"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/controller"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/middleware/serviceauth"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/repository/avatarfiles"
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
	presence, err := newPresence(cfg)
	if err != nil {
		pool.Close()
		return nil, nil, err
	}
	if cfg.Gateways == nil {
		logger.WarnContext(ctx, "no Gateway registry configured; every user is reported offline")
	}
	profileService := service.NewProfile(userpostgres.NewProfile(pool))
	profileReads := service.NewProfileRead(userpostgres.NewProfile(pool), service.AvatarURLs{Base: cfg.AvatarBaseURL})
	internal := server.NewInternal(cfg.InternalPort, logger,
		controller.NewInternalProvisioning(profileService, auth),
		controller.NewInternalProfile(profileReads, auth),
		controller.NewInternalFriendshipVerdict(service.NewFriendshipVerdicts(userpostgres.NewFriendshipVerdicts(pool)), auth),
		controller.NewInternalPresence(presence, auth))

	cleanup := pool.Close
	var avatarStorage *avatarfiles.Store
	var avatarReader controller.AvatarFileReader
	if cfg.AvatarStorageDir != "" {
		avatarStorage, err = avatarfiles.Open(cfg.AvatarStorageDir)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		avatarReader = avatarStorage
		cleanup = func() {
			_ = avatarStorage.Close()
			pool.Close()
		}
	}

	public := []server.RouteRegistrar{controller.NewAvatarFiles(avatarReader)}
	if cfg.AuthInternalURL != "" {
		sessions, err := authclient.New(cfg.AuthInternalURL, cfg.AuthServiceCredential, cfg.AuthInternalTimeout)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		avatars := service.AvatarURLs{Base: cfg.AvatarBaseURL}
		requests := service.NewFriendRequests(userpostgres.NewFriendRequests(pool), avatars, presence)
		blocks := service.NewBlocks(userpostgres.NewBlocks(pool), avatars)
		friends := service.NewFriends(userpostgres.NewFriends(pool), avatars, presence)
		public = append(public, controller.NewFriendRequests(requests, sessions), controller.NewBlocks(blocks, sessions),
			controller.NewFriends(friends, sessions),
			controller.NewUserSearch(service.NewUserSearch(userpostgres.NewUserSearch(pool), avatars), sessions),
			controller.NewReports(service.NewReports(userpostgres.NewReports(pool), cfg.ReportWindow), sessions))
		if cfg.StatsInternalURL != "" {
			stats, err := statsclient.New(cfg.StatsInternalURL, cfg.StatsServiceCredential, cfg.StatsInternalTimeout)
			if err != nil {
				cleanup()
				return nil, nil, err
			}
			profiles := service.NewPublicProfiles(userpostgres.NewPublicProfiles(pool), publicStats{stats}, presence, avatars)
			public = append(public, controller.NewPublicProfile(profiles, sessions))
		} else {
			logger.WarnContext(ctx, "STATS_INTERNAL_URL is not set; the public profile route is not served")
		}
		if avatarStorage != nil {
			logCleanup := func(key string, err error) {
				logger.WarnContext(ctx, "replaced avatar file could not be removed", "key", key, "error", err)
			}
			avatarService := service.NewAvatars(userpostgres.NewAvatars(pool), avatarStorage, avatars, logCleanup)
			public = append(public, controller.NewAvatars(avatarService, sessions))
		} else {
			logger.WarnContext(ctx, "AVATAR_STORAGE_DIR is not set; avatar changes are not served")
		}
	} else {
		logger.WarnContext(ctx, "AUTH_INTERNAL_URL is not set; routes that need a signed-in user are not served")
	}

	return []*http.Server{server.New(cfg.Port, healthController, logger, public...), internal}, cleanup, nil
}

// newPresence returns presence over every configured Gateway instance, or
// NoPresence when cfg has no Gateway registry, which Load allows only outside
// production.
//
// Errors:
//
//   - A Gateway client cannot be created; see gatewayclient.New.
func newPresence(cfg config.Config) (service.PresenceReader, error) {
	if cfg.Gateways == nil {
		return service.NoPresence{}, nil
	}
	var instances []service.ConnectionReader
	for _, gateway := range cfg.Gateways {
		for _, origin := range gateway.URLs {
			client, err := gatewayclient.New(origin, gateway.Credential, cfg.GatewayInternalTimeout)
			if err != nil {
				return nil, err
			}
			instances = append(instances, client)
		}
	}

	return service.NewPresence(instances...), nil
}

// publicStats adapts the Stats client to the counters a public profile shows.
type publicStats struct{ client *statsclient.Client }

// PublicStats implements service.StatsReader. Penalty points are dropped.
func (p publicStats) PublicStats(ctx context.Context, userID int64) (service.PublicStats, error) {
	stats, err := p.client.UserStats(ctx, userID)
	if err != nil {
		return service.PublicStats{}, err
	}

	return service.PublicStats{Level: stats.Level, Wins: stats.Wins, Losses: stats.Losses,
		MatchesPlayed: stats.MatchesPlayed}, nil
}
