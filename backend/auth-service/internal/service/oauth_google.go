package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/security"
)

const (
	// OAuthStateLifetime bounds the ft_oauth cookie and the stored state.
	OAuthStateLifetime = 10 * time.Minute
	// DefaultOAuthReturnTo is the landing path when return_to is absent or unsafe.
	DefaultOAuthReturnTo = "/lobby"
	// GoogleAuthorizationEndpoint is Google's OAuth 2.0 authorization endpoint.
	GoogleAuthorizationEndpoint = "https://accounts.google.com/o/oauth2/v2/auth"

	maxReturnToLength = 200
)

// ErrOAuthNotConfigured reports that Google sign-in has no client configuration.
var ErrOAuthNotConfigured = errors.New("google sign-in is not configured")

// OAuthStateStore keeps single-use state hashes for the callback.
type OAuthStateStore interface {
	// CreateOAuthState stores a state hash until expiresAt and removes
	// expired rows.
	CreateOAuthState(ctx context.Context, stateHash string, expiresAt time.Time) error
}

// GoogleOAuthConfig holds the backend-only Google client settings.
type GoogleOAuthConfig struct {
	ClientID string
	// RedirectURI is {public base URL}/api/v1/auth/oauth/google/callback.
	RedirectURI string
	// AuthorizationEndpoint defaults to GoogleAuthorizationEndpoint.
	AuthorizationEndpoint string
}

// GoogleOAuth starts Google sign-in with a single-use state and PKCE.
type GoogleOAuth struct {
	config GoogleOAuthConfig
	states OAuthStateStore
	sealer *security.OAuthStateSealer
	now    func() time.Time
}

// NewGoogleOAuth returns the Google sign-in starter.
//
// An empty ClientID or RedirectURI yields a starter that always answers
// ErrOAuthNotConfigured, so the route stays available and fails safely.
func NewGoogleOAuth(config GoogleOAuthConfig, states OAuthStateStore, sealer *security.OAuthStateSealer) *GoogleOAuth {
	if config.AuthorizationEndpoint == "" {
		config.AuthorizationEndpoint = GoogleAuthorizationEndpoint
	}

	return &GoogleOAuth{config: config, states: states, sealer: sealer, now: time.Now}
}

// GoogleAuthorization is the redirect target and the sealed ft_oauth cookie value.
type GoogleAuthorization struct {
	RedirectURL string
	Cookie      string
	ExpiresAt   time.Time
}

// StartGoogle prepares the redirect to Google.
//
// Flow:
//
//  1. Keep return_to only if it is a safe same-origin path; otherwise use /lobby.
//  2. Generate a random state and PKCE code verifier.
//  3. Store the state's hash with a 10-minute expiry for single-use checks.
//  4. Seal state, verifier and return_to into the ft_oauth cookie value.
//  5. Build Google's URL with only the S256 code challenge.
//
// Errors:
//
//   - ErrOAuthNotConfigured: no client ID, redirect URI or state store.
//   - Store or randomness errors: nothing usable was issued.
func (g *GoogleOAuth) StartGoogle(ctx context.Context, returnTo string) (GoogleAuthorization, error) {
	if g.config.ClientID == "" || g.config.RedirectURI == "" || g.states == nil || g.sealer == nil {
		return GoogleAuthorization{}, ErrOAuthNotConfigured
	}
	state, err := security.NewOAuthSecret()
	if err != nil {
		return GoogleAuthorization{}, fmt.Errorf("generate oauth state: %w", err)
	}
	verifier, err := security.NewOAuthSecret()
	if err != nil {
		return GoogleAuthorization{}, fmt.Errorf("generate pkce verifier: %w", err)
	}
	expiresAt := g.now().UTC().Add(OAuthStateLifetime).Truncate(time.Second)
	if err := g.states.CreateOAuthState(ctx, security.OAuthStateHash(state), expiresAt); err != nil {
		return GoogleAuthorization{}, err
	}
	cookie, err := g.sealer.Seal(security.OAuthState{
		State: state, CodeVerifier: verifier, ReturnTo: SafeReturnTo(returnTo), ExpiresAt: expiresAt,
	})
	if err != nil {
		return GoogleAuthorization{}, fmt.Errorf("seal oauth state: %w", err)
	}

	query := url.Values{}
	query.Set("client_id", g.config.ClientID)
	query.Set("redirect_uri", g.config.RedirectURI)
	query.Set("response_type", "code")
	query.Set("scope", "openid email profile")
	query.Set("state", state)
	query.Set("code_challenge", security.PKCEChallenge(verifier))
	query.Set("code_challenge_method", "S256")

	return GoogleAuthorization{
		RedirectURL: g.config.AuthorizationEndpoint + "?" + query.Encode(),
		Cookie:      cookie,
		ExpiresAt:   expiresAt,
	}, nil
}

// SafeReturnTo keeps a same-origin SPA path and replaces anything else with /lobby.
//
// A safe path starts with exactly one "/", is at most 200 characters, and has
// no backslash, control or whitespace character. Browsers treat "//host" and
// "/\host" as other origins, so both are rejected.
func SafeReturnTo(raw string) string {
	if raw == "" || len(raw) > maxReturnToLength || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return DefaultOAuthReturnTo
	}
	for _, r := range raw {
		if r == '\\' || unicode.IsControl(r) || unicode.IsSpace(r) {
			return DefaultOAuthReturnTo
		}
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.User != nil {
		return DefaultOAuthReturnTo
	}

	return raw
}
