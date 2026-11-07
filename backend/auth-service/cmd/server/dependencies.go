package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/client/googleclient"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/client/userclient"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/config"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/controller"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/middleware/serviceauth"
	authpostgres "github.com/melmut-42/ft_transcendence/backend/auth-service/internal/repository/postgres"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/security"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/server"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
	"github.com/melmut-42/ft_transcendence/backend/shared/postgres"
)

// setup initializes application dependencies and creates the HTTP server.
//
// Parameters:
//
//   - ctx: Controls database connection startup.
//   - cfg: Validated application settings.
//   - logger: Configured service logger.
//
// It returns the unstarted public and internal servers and a cleanup function
// that closes the pool. Private routes run only on the internal listener and
// accept only configured callers. The caller must invoke cleanup after HTTP
// shutdown. On failure, every value is nil.
//
// Errors:
//
//   - Token issuer errors: Invalid signing key or credential lifetimes.
//   - PostgreSQL errors: Invalid connection settings or failed connectivity checks, including context cancellation and deadlines.
func setup(ctx context.Context, cfg config.Config, logger *slog.Logger) ([]*http.Server, func(), error) {
	issuer, err := security.NewTokenIssuer([]byte(cfg.JWTSecret), cfg.AccessTokenTTL, cfg.RefreshTokenTTL)
	if err != nil {
		return nil, nil, err
	}

	pool, err := postgres.Open(ctx, postgres.Config{DSN: cfg.DatabaseURL})
	if err != nil {
		return nil, nil, err
	}

	logger.InfoContext(ctx, "PostgreSQL connection established")

	authRepository := authpostgres.NewAuth(pool)
	var options []service.Option
	if cfg.UserInternalURL != "" {
		provisioner, err := userclient.New(cfg.UserInternalURL, cfg.UserServiceCredential, cfg.UserInternalTimeout)
		if err != nil {
			pool.Close()
			return nil, nil, err
		}
		options = append(options, service.WithProfileProvisioner(provisioner))
	} else {
		logger.WarnContext(ctx, "USER_INTERNAL_URL is not set; accounts are created without User Service profiles")
	}
	authService := service.NewAuth(authRepository, issuer, options...)
	sealer, err := security.NewOAuthStateSealer([]byte(cfg.JWTSecret))
	if err != nil {
		pool.Close()
		return nil, nil, err
	}
	if cfg.GoogleClientID == "" {
		logger.WarnContext(ctx, "Google sign-in is not configured; it redirects with OAUTH_PROVIDER_ERROR")
	}
	google := service.NewGoogleOAuth(service.GoogleOAuthConfig{
		ClientID: cfg.GoogleClientID, RedirectURI: cfg.GoogleRedirectURI(),
	}, authRepository, sealer)
	if cfg.GoogleClientID != "" {
		identities, err := googleclient.New(googleclient.Config{
			ClientID: cfg.GoogleClientID, ClientSecret: cfg.GoogleClientSecret, RedirectURI: cfg.GoogleRedirectURI(),
		})
		if err != nil {
			pool.Close()
			return nil, nil, err
		}
		google.EnableCallback(identities, authService)
	}
	authController := controller.NewAuth(authService, controller.WithGoogleOAuth(google, logger))
	cleanup := startSessionRevocations(ctx, authService, pool.Close, logger)

	if len(cfg.CallerCredentials) == 0 {
		logger.WarnContext(ctx, "no service credentials configured; private routes accept no caller")
	}
	serviceAuth := serviceauth.New(cfg.CallerCredentials, serviceauth.DefaultMatrix())
	internal := server.NewInternal(cfg.InternalPort, logger, controller.NewInternalReads(authService, serviceAuth))

	return []*http.Server{server.New(cfg.Port, authController, logger), internal}, cleanup, nil
}
