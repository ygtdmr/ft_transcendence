package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/client/gameclient"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/client/gatewayclient"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/client/googleclient"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/client/sessionlifecycle"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/client/userclient"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/config"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/controller"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/middleware/serviceauth"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/middleware/throttle"
	authpostgres "github.com/melmut-42/ft_transcendence/backend/auth-service/internal/repository/postgres"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/security"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/server"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
	"github.com/melmut-42/ft_transcendence/backend/shared/postgres"
	"github.com/melmut-42/ft_transcendence/backend/shared/ratelimit"
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
		options = append(options, service.WithProfileProvisioner(provisioner), service.WithProfileReader(provisioner))
	} else {
		logger.WarnContext(ctx, "USER_INTERNAL_URL is not set; accounts are created without User Service profiles or username uniqueness")
	}
	var lifecycle sessionlifecycle.Lifecycle
	if cfg.GameInternalURL != "" {
		game, err := gameclient.New(cfg.GameInternalURL, cfg.GameServiceCredential, cfg.GameInternalTimeout)
		if err != nil {
			pool.Close()
			return nil, nil, err
		}
		options = append(options, service.WithMembershipReader(game))
		lifecycle.Departures = game
	} else {
		logger.WarnContext(ctx, "GAME_INTERNAL_URL is not set; current-session reports no active room and logout records no Game departure")
	}
	if len(cfg.Gateways) > 0 {
		var instances []gatewayclient.Instance
		for _, gateway := range cfg.Gateways {
			for _, origin := range gateway.URLs {
				instances = append(instances, gatewayclient.Instance{URL: origin, Credential: gateway.Credential})
			}
		}
		gateways, err := gatewayclient.New(instances, cfg.GatewayInternalTimeout)
		if err != nil {
			pool.Close()
			return nil, nil, err
		}
		lifecycle.Gateways = gateways
	} else {
		logger.WarnContext(ctx, "gateway revocation settings are not set; revoked sessions keep their open sockets")
	}
	if lifecycle.Configured() {
		options = append(options, service.WithSessionLifecycle(lifecycle))
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
	limits, err := rateLimits(cfg)
	if err != nil {
		pool.Close()
		return nil, nil, err
	}
	authController := controller.NewAuth(authService, controller.WithGoogleOAuth(google, logger), controller.WithRateLimits(limits))
	healthController := controller.NewHealth(service.NewHealth(authpostgres.NewHealth(pool)))
	cleanup := startSessionRevocations(ctx, authService, pool.Close, logger)

	if len(cfg.CallerCredentials) == 0 {
		logger.WarnContext(ctx, "no service credentials configured; private routes accept no caller")
	}
	serviceAuth := serviceauth.New(cfg.CallerCredentials, serviceauth.DefaultMatrix())
	internal := server.NewInternal(cfg.InternalAddress(), logger, controller.NewInternalReads(authService, serviceAuth),
		controller.NewAccountDeletion(authService, serviceAuth))

	return []*http.Server{server.New(cfg.Port, authController, logger, healthController), internal}, cleanup, nil
}

// rateLimits builds the login and registration limiters from validated settings.
func rateLimits(cfg config.Config) (throttle.Limits, error) {
	proxies, err := ratelimit.ParseTrustedProxies(cfg.TrustedProxies)
	if err != nil {
		return throttle.Limits{}, err
	}
	limits := throttle.Limits{Proxies: proxies}
	for _, setting := range []struct {
		limit  config.RateLimit
		target **ratelimit.Limiter
	}{
		{cfg.LoginClientRateLimit, &limits.LoginClient},
		{cfg.LoginEmailRateLimit, &limits.LoginEmail},
		{cfg.RegisterClientRateLimit, &limits.RegisterClient},
	} {
		limiter, err := ratelimit.New(ratelimit.Config{Limit: setting.limit.Count, Window: setting.limit.Window})
		if err != nil {
			return throttle.Limits{}, err
		}
		*setting.target = limiter
	}

	return limits, nil
}
