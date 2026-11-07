// Package googleclient exchanges Google authorization codes and verifies ID tokens.
package googleclient

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
)

const (
	// TokenEndpoint is Google's OAuth 2.0 token endpoint.
	TokenEndpoint = "https://oauth2.googleapis.com/token"
	// KeysEndpoint publishes the keys that sign Google ID tokens.
	KeysEndpoint = "https://www.googleapis.com/oauth2/v3/certs"

	maxResponseBody = 64 << 10
	keyCacheLife    = time.Hour
)

var googleIssuers = []string{"https://accounts.google.com", "accounts.google.com"}

var _ service.GoogleIdentityProvider = (*Client)(nil)

// Config holds the backend-only Google client settings and endpoints.
type Config struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
	// TokenEndpoint and KeysEndpoint default to Google's; tests replace them.
	TokenEndpoint string
	KeysEndpoint  string
	Timeout       time.Duration
}

// Client talks to Google's token endpoint and key set.
type Client struct {
	config Config
	http   *http.Client

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	keysUntil time.Time
	now       func() time.Time
}

// New returns a Google client.
//
// Errors:
//
//   - ClientID, ClientSecret or RedirectURI is empty.
func New(config Config) (*Client, error) {
	if config.ClientID == "" || config.ClientSecret == "" || config.RedirectURI == "" {
		return nil, errors.New("google client needs a client ID, client secret and redirect URI")
	}
	if config.TokenEndpoint == "" {
		config.TokenEndpoint = TokenEndpoint
	}
	if config.KeysEndpoint == "" {
		config.KeysEndpoint = KeysEndpoint
	}
	if config.Timeout <= 0 {
		config.Timeout = 10 * time.Second
	}

	return &Client{
		config: config,
		http: &http.Client{Timeout: config.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		now: time.Now,
	}, nil
}

// Identify exchanges code with the PKCE verifier and verifies the returned ID token.
//
// Errors:
//
//   - service.ErrGoogleUnavailable: the exchange or key download failed.
//   - service.ErrGoogleRejected: Google refused the code or the ID token is
//     invalid (signature, issuer, audience, expiry or claims).
func (c *Client) Identify(ctx context.Context, code, codeVerifier string) (service.GoogleIdentity, error) {
	raw, err := c.exchange(ctx, code, codeVerifier)
	if err != nil {
		return service.GoogleIdentity{}, err
	}

	return c.verify(ctx, raw)
}

func (c *Client) exchange(ctx context.Context, code, codeVerifier string) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", c.config.RedirectURI)
	form.Set("client_id", c.config.ClientID)
	form.Set("client_secret", c.config.ClientSecret)
	form.Set("code_verifier", codeVerifier)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")

	response, err := c.http.Do(request)
	if err != nil {
		return "", fmt.Errorf("%w: token request failed", service.ErrGoogleUnavailable)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody))
	if err != nil {
		return "", fmt.Errorf("%w: token response unreadable", service.ErrGoogleUnavailable)
	}
	if response.StatusCode >= 500 {
		return "", fmt.Errorf("%w: token endpoint status %d", service.ErrGoogleUnavailable, response.StatusCode)
	}
	if response.StatusCode != http.StatusOK {
		// The body may echo request details; only the status is reported.
		return "", fmt.Errorf("%w: token endpoint status %d", service.ErrGoogleRejected, response.StatusCode)
	}
	var tokens struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &tokens); err != nil || tokens.IDToken == "" {
		return "", fmt.Errorf("%w: token response without id_token", service.ErrGoogleRejected)
	}

	// Access and refresh tokens are never kept or logged.
	return tokens.IDToken, nil
}

type idClaims struct {
	Email         string `json:"email"`
	EmailVerified any    `json:"email_verified"`
	Name          string `json:"name"`
	jwt.RegisteredClaims
}

func (c *Client) verify(ctx context.Context, raw string) (service.GoogleIdentity, error) {
	claims := &idClaims{}
	var keyErr error
	token, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		kid, _ := token.Header["kid"].(string)
		key, err := c.key(ctx, kid)
		keyErr = err
		return key, err
	}, jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}), jwt.WithAudience(c.config.ClientID),
		jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithTimeFunc(c.now), jwt.WithLeeway(30*time.Second))
	if errors.Is(keyErr, service.ErrGoogleUnavailable) {
		return service.GoogleIdentity{}, keyErr
	}
	if err != nil || !token.Valid {
		return service.GoogleIdentity{}, fmt.Errorf("%w: invalid id token", service.ErrGoogleRejected)
	}
	validIssuer := false
	for _, issuer := range googleIssuers {
		validIssuer = validIssuer || claims.Issuer == issuer
	}
	if !validIssuer || claims.Subject == "" || len(claims.Subject) > 255 || claims.Email == "" {
		return service.GoogleIdentity{}, fmt.Errorf("%w: invalid id token claims", service.ErrGoogleRejected)
	}

	return service.GoogleIdentity{
		Subject:       claims.Subject,
		Email:         claims.Email,
		EmailVerified: claims.EmailVerified == true || claims.EmailVerified == "true",
		Name:          claims.Name,
	}, nil
}

// key returns the signing key for kid, refreshing the cached key set when it
// expired or does not know kid.
func (c *Client) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if key, ok := c.keys[kid]; ok && c.now().Before(c.keysUntil) {
		return key, nil
	}
	keys, err := c.fetchKeys(ctx)
	if err != nil {
		return nil, err
	}
	c.keys, c.keysUntil = keys, c.now().Add(keyCacheLife)
	key, ok := keys[kid]
	if !ok {
		return nil, fmt.Errorf("%w: unknown signing key", service.ErrGoogleRejected)
	}

	return key, nil
}

func (c *Client) fetchKeys(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.config.KeysEndpoint, nil)
	if err != nil {
		return nil, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: key set request failed", service.ErrGoogleUnavailable)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody))
	if err != nil || response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: key set status %d", service.ErrGoogleUnavailable, response.StatusCode)
	}
	var set struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, fmt.Errorf("%w: key set malformed", service.ErrGoogleUnavailable)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, jwk := range set.Keys {
		n, errN := base64.RawURLEncoding.DecodeString(jwk.N)
		e, errE := base64.RawURLEncoding.DecodeString(jwk.E)
		if jwk.Kty != "RSA" || jwk.Kid == "" || errN != nil || errE != nil || len(e) == 0 || len(e) > 4 {
			continue
		}
		exponent := int(new(big.Int).SetBytes(e).Int64())
		keys[jwk.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: exponent}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: key set has no RSA keys", service.ErrGoogleUnavailable)
	}

	return keys, nil
}
