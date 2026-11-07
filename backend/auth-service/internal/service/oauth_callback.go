package service

import (
	"context"
	"crypto/subtle"
	"errors"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/security"
)

// Google sign-in callback outcomes. Each maps to one oauth_error code.
var (
	ErrOAuthCancelled       = errors.New("google sign-in was cancelled")
	ErrOAuthStateInvalid    = errors.New("google sign-in state is invalid")
	ErrOAuthEmailUnverified = errors.New("google email is not verified")
	ErrOAuthProviderError   = errors.New("google sign-in failed")
)

// Errors a GoogleIdentityProvider returns.
var (
	ErrGoogleUnavailable = errors.New("google is unavailable")
	ErrGoogleRejected    = errors.New("google rejected the sign-in")
)

// GoogleIdentity is the verified identity from a Google ID token.
type GoogleIdentity struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
}

// GoogleIdentityProvider exchanges an authorization code and verifies the ID token.
type GoogleIdentityProvider interface {
	Identify(ctx context.Context, code, codeVerifier string) (GoogleIdentity, error)
}

// OAuthStateConsumer marks a stored state as used, once.
type OAuthStateConsumer interface {
	// ConsumeOAuthState reports whether an unexpired, unused state with this
	// hash existed and is now consumed.
	ConsumeOAuthState(ctx context.Context, stateHash string) (bool, error)
}

// GoogleCallback is the request Google redirects back with.
type GoogleCallback struct {
	Cookie string
	State  string
	Code   string
	Error  string
}

// EnableCallback lets the starter also complete sign-in with provider and auth.
func (g *GoogleOAuth) EnableCallback(provider GoogleIdentityProvider, auth *AuthService) *GoogleOAuth {
	g.provider, g.auth = provider, auth

	return g
}

// CompleteGoogle verifies the callback and signs the user in.
//
// Flow:
//
//  1. Open ft_oauth, compare its state with the query state, and consume the
//     stored state once; any failure is OAUTH_STATE_INVALID.
//  2. A Google error is OAUTH_CANCELLED for access_denied, else a provider error.
//  3. Exchange the code with the PKCE verifier and verify the ID token.
//  4. Require email_verified, then resolve or create the account and session.
//
// It returns the session, the return_to path and nil, or one of the
// ErrOAuth* errors. Nothing is created or changed on failure before step 4.
func (g *GoogleOAuth) CompleteGoogle(ctx context.Context, callback GoogleCallback) (AuthResult, string, error) {
	if g.provider == nil || g.auth == nil || g.sealer == nil {
		return AuthResult{}, "", ErrOAuthProviderError
	}
	stored, err := g.sealer.Open(callback.Cookie, g.now())
	if err != nil || callback.State == "" ||
		subtle.ConstantTimeCompare([]byte(stored.State), []byte(callback.State)) != 1 {
		return AuthResult{}, "", ErrOAuthStateInvalid
	}
	consumer, ok := g.states.(OAuthStateConsumer)
	if !ok {
		return AuthResult{}, "", ErrOAuthProviderError
	}
	consumed, err := consumer.ConsumeOAuthState(ctx, security.OAuthStateHash(stored.State))
	if err != nil {
		return AuthResult{}, "", errors.Join(ErrOAuthProviderError, err)
	}
	if !consumed {
		return AuthResult{}, "", ErrOAuthStateInvalid
	}
	if callback.Error != "" {
		if callback.Error == "access_denied" {
			return AuthResult{}, "", ErrOAuthCancelled
		}
		return AuthResult{}, "", ErrOAuthProviderError
	}
	if callback.Code == "" {
		return AuthResult{}, "", ErrOAuthProviderError
	}

	identity, err := g.provider.Identify(ctx, callback.Code, stored.CodeVerifier)
	if err != nil {
		return AuthResult{}, "", errors.Join(ErrOAuthProviderError, err)
	}
	if !identity.EmailVerified {
		return AuthResult{}, "", ErrOAuthEmailUnverified
	}
	result, err := g.auth.SignInWithGoogle(ctx, identity)
	if err != nil {
		return AuthResult{}, "", errors.Join(ErrOAuthProviderError, err)
	}

	return result, SafeReturnTo(stored.ReturnTo), nil
}
