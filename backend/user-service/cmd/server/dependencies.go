package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/shared/ratelimit"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/client/authclient"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/client/gameclient"
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
		sweep := service.NewAvatarSweep(avatarStorage, userpostgres.NewAvatars(pool))
		sweep.OnError = func(name string, err error) {
			logger.Warn("unreferenced avatar file could not be removed", "name", name, "error", err)
		}
		cleanup = startAvatarSweep(ctx, sweep, cleanup, logger)
	}

	reportLimiter, err := ratelimit.New(ratelimit.Config{Limit: cfg.ReportRateLimit.Count, Window: cfg.ReportRateLimit.Window})
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	deletionLimiter, err := ratelimit.New(ratelimit.Config{Limit: cfg.AccountDeletionRateLimit.Count, Window: cfg.AccountDeletionRateLimit.Window})
	if err != nil {
		cleanup()
		return nil, nil, err
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
			controller.NewReports(service.NewReports(userpostgres.NewReports(pool), cfg.ReportWindow), sessions).
				WithRateLimit(reportLimiter))
		if cfg.StatsInternalURL != "" {
			stats, err := statsclient.New(cfg.StatsInternalURL, cfg.StatsServiceCredential, cfg.StatsInternalTimeout)
			if err != nil {
				cleanup()
				return nil, nil, err
			}
			profiles := service.NewPublicProfiles(userpostgres.NewPublicProfiles(pool), publicStats{stats}, presence, avatars)
			memberships, err := newMemberships(cfg)
			if err != nil {
				cleanup()
				return nil, nil, err
			}
			if cfg.GameInternalURL == "" {
				logger.WarnContext(ctx, "GAME_INTERNAL_URL is not set; the own profile reports no active room")
			}
			own := service.NewOwnProfiles(profileReads, accounts{sessions}, ownStats{stats}, memberships)
			deletions, err := newAccountDeletions(cfg, pool, sessions, stats, avatarStorage, logger)
			if err != nil {
				cleanup()
				return nil, nil, err
			}
			usernames := service.NewUsernames(userpostgres.NewUsernames(pool), avatars)
			public = append(public, controller.NewPublicProfile(profiles, sessions), controller.NewOwnProfile(own, sessions),
				controller.NewProfileUpdate(usernames, own, sessions), controller.NewAccountDeletion(deletions, sessions).WithRateLimit(deletionLimiter))
			cleanup = startDeletionWorker(ctx, deletions, cleanup, logger)
		} else {
			logger.WarnContext(ctx, "STATS_INTERNAL_URL is not set; the public and own profile routes are not served")
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

// ownStats adapts the Stats client to the counters an own profile shows.
type ownStats struct{ client *statsclient.Client }

// OwnStats implements service.OwnStatsReader, keeping penalty points.
func (o ownStats) OwnStats(ctx context.Context, userID int64) (service.OwnStats, error) {
	stats, err := o.client.UserStats(ctx, userID)
	if err != nil {
		return service.OwnStats{}, err
	}

	return service.OwnStats{PublicStats: service.PublicStats{Level: stats.Level, Wins: stats.Wins, Losses: stats.Losses,
		MatchesPlayed: stats.MatchesPlayed}, PenaltyPoints: stats.PenaltyPoints}, nil
}

// accounts adapts the Auth client to the account metadata an own profile shows.
type accounts struct{ client *authclient.Client }

// Account implements service.AccountReader.
func (a accounts) Account(ctx context.Context, userID int64) (service.Account, error) {
	account, err := a.client.AccountMetadata(ctx, userID)
	switch {
	case errors.Is(err, authclient.ErrAccountNotFound):
		return service.Account{}, service.ErrAccountNotFound
	case errors.Is(err, authclient.ErrUnavailable):
		return service.Account{}, fmt.Errorf("%w: auth: %w", service.ErrUnavailable, err)
	case err != nil:
		return service.Account{}, err
	}

	return service.Account{Email: account.Email, Status: account.Status, CreatedAt: account.CreatedAt}, nil
}

// newMemberships returns the Game Session membership reader, or NoMembership
// when cfg has no Game Session settings, which Load allows only outside
// production.
//
// Errors:
//
//   - The Game client cannot be created; see gameclient.New.
func newMemberships(cfg config.Config) (service.MembershipReader, error) {
	if cfg.GameInternalURL == "" {
		return service.NoMembership{}, nil
	}

	return gameclient.New(cfg.GameInternalURL, cfg.GameServiceCredential, cfg.GameInternalTimeout)
}

// newAccountDeletions returns the deletion coordinator. Without Game Session
// settings, which Load allows only outside production, the Game step is
// skipped.
//
// Errors:
//
//   - The Game client cannot be created; see gameclient.New.
func newAccountDeletions(cfg config.Config, pool *pgxpool.Pool, auth *authclient.Client, stats *statsclient.Client,
	files *avatarfiles.Store, logger *slog.Logger) (*service.AccountDeletions, error) {
	var game service.AccountCleaner
	if cfg.GameInternalURL != "" {
		client, err := gameclient.New(cfg.GameInternalURL, cfg.GameServiceCredential, cfg.GameInternalTimeout)
		if err != nil {
			return nil, err
		}
		game = client
	} else {
		logger.Warn("GAME_INTERNAL_URL is not set; account deletion skips the Game Session step")
	}
	var remover service.FileRemover
	if files != nil {
		remover = files
	}
	deletions := service.NewAccountDeletions(userpostgres.NewDeletions(pool), userpostgres.NewProfile(pool),
		deletionAuth{auth}, game, statsCleaner{stats}, remover)
	deletions.OnFileError = func(key string, err error) {
		logger.Warn("deleted account's avatar file could not be removed", "key", key, "error", err)
	}

	return deletions, nil
}

// deletionWorkerInterval is how often the worker looks for due operations.
const deletionWorkerInterval = 10 * time.Second

// startDeletionWorker resumes unfinished deletions until shutdown. The
// returned cleanup stops the worker, waits for it, then runs next.
func startDeletionWorker(ctx context.Context, deletions *service.AccountDeletions, next func(),
	logger *slog.Logger) func() {
	workerCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(deletionWorkerInterval)
		defer ticker.Stop()
		for {
			if _, err := deletions.ResumeDue(workerCtx, 20); err != nil && workerCtx.Err() == nil {
				logger.ErrorContext(workerCtx, "account_deletion_resume_failed", "error", err)
			}
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()

	return func() {
		cancel()
		<-done
		next()
	}
}

// deletionAuth maps Auth client errors to the coordinator's error classes.
type deletionAuth struct{ client *authclient.Client }

func (d deletionAuth) BeginDeletion(ctx context.Context, operationID string, userID int64) error {
	return participantError(d.client.BeginDeletion(ctx, operationID, userID), authclient.ErrUnavailable)
}

func (d deletionAuth) CompleteDeletion(ctx context.Context, operationID string) error {
	return participantError(d.client.CompleteDeletion(ctx, operationID), authclient.ErrUnavailable)
}

// statsCleaner maps Stats client errors to the coordinator's error classes.
type statsCleaner struct{ client *statsclient.Client }

func (s statsCleaner) CleanUpAccount(ctx context.Context, operationID string, userID int64) error {
	return participantError(s.client.CleanUpAccount(ctx, operationID, userID), statsclient.ErrUnavailable)
}

// participantError keeps an outage retryable and turns every other failure
// into a permanent rejection.
func participantError(err, unavailable error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unavailable):
		return fmt.Errorf("%w: %w", service.ErrUnavailable, err)
	default:
		return fmt.Errorf("%w: %w", service.ErrParticipantRejected, err)
	}
}

// avatarSweepInterval is how often unreferenced avatar files are removed.
const avatarSweepInterval = time.Hour

// startAvatarSweep removes unreferenced avatar files at startup and then every
// avatarSweepInterval until shutdown. The returned cleanup stops the sweep,
// waits for it, then runs next.
func startAvatarSweep(ctx context.Context, sweep *service.AvatarSweep, next func(), logger *slog.Logger) func() {
	sweepCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(avatarSweepInterval)
		defer ticker.Stop()
		for {
			removed, err := sweep.Sweep(sweepCtx, time.Now())
			switch {
			case err != nil && sweepCtx.Err() == nil:
				logger.ErrorContext(sweepCtx, "avatar_sweep_failed", "error", err)
			case removed > 0:
				logger.InfoContext(sweepCtx, "unreferenced avatar files removed", "count", removed)
			}
			select {
			case <-sweepCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()

	return func() {
		cancel()
		<-done
		next()
	}
}
