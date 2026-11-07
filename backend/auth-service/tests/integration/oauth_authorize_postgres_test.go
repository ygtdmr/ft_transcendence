package integration_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/controller"
	repository "github.com/melmut-42/ft_transcendence/backend/auth-service/internal/repository/postgres"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/security"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/server"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/testutil"
)

const googleRedirectURI = "https://example.com/api/v1/auth/oauth/google/callback"

// TestGoogleAuthorizePostgres checks the authorize redirect, cookie and stored state.
//
// Expected output:
//
//   - 302 to Google with client_id, redirect_uri, response_type=code,
//     scope "openid email profile", state and an S256 challenge; no verifier,
//     secret or session data in the URL.
//   - ft_oauth is HttpOnly, Secure, SameSite=Lax, Path=/api/v1/auth/oauth/google,
//     10 minutes; it opens to the same state, the matching verifier and the
//     safe return_to.
//   - Only the state's hash is stored, expiring with the cookie; expired rows
//     are removed; each start issues a new state.
func TestGoogleAuthorizePostgres(t *testing.T) {
	pool := registrationTestPool(t)
	gin.SetMode(gin.TestMode)
	sealer, err := security.NewOAuthStateSealer([]byte(testutil.TestSigningKey))
	if err != nil {
		t.Fatal(err)
	}
	repo := repository.NewAuth(pool)
	google := service.NewGoogleOAuth(service.GoogleOAuthConfig{ClientID: "client-123", RedirectURI: googleRedirectURI}, repo, sealer)
	handler := server.New("0", controller.NewAuth(service.NewAuth(repo, testutil.TokenIssuer(t)),
		controller.WithGoogleOAuth(google, slog.New(slog.NewTextHandler(io.Discard, nil)))),
		slog.New(slog.NewTextHandler(io.Discard, nil))).Handler
	if _, err := pool.Exec(context.Background(), `INSERT INTO oauth_states (state_hash, created_at, expires_at)
		VALUES (repeat('a', 64), now() - interval '1 hour', now() - interval '30 minutes')`); err != nil {
		t.Fatal(err)
	}

	start := func(returnTo string) (*url.URL, *http.Cookie) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
			"/api/v1/auth/oauth/google/authorize?return_to="+url.QueryEscape(returnTo), nil))
		if recorder.Code != http.StatusFound || recorder.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("authorize = %d %v", recorder.Code, recorder.Header())
		}
		location, err := url.Parse(recorder.Header().Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		cookies := recorder.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatalf("cookies = %v", cookies)
		}
		return location, cookies[0]
	}

	location, cookie := start("/rooms/42")
	query := location.Query()
	if location.Scheme+"://"+location.Host+location.Path != service.GoogleAuthorizationEndpoint ||
		query.Get("client_id") != "client-123" || query.Get("redirect_uri") != googleRedirectURI ||
		query.Get("response_type") != "code" || query.Get("scope") != "openid email profile" ||
		query.Get("code_challenge_method") != "S256" || query.Get("state") == "" || len(query) != 7 {
		t.Fatalf("Google URL = %s", location)
	}
	if cookie.Name != "ft_oauth" || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode ||
		cookie.Path != "/api/v1/auth/oauth/google" || cookie.MaxAge != 600 {
		t.Fatalf("cookie = %+v", cookie)
	}
	opened, err := sealer.Open(cookie.Value, time.Now())
	if err != nil || opened.State != query.Get("state") || security.PKCEChallenge(opened.CodeVerifier) != query.Get("code_challenge") ||
		opened.ReturnTo != "/rooms/42" || time.Until(opened.ExpiresAt) > 10*time.Minute || time.Until(opened.ExpiresAt) < 9*time.Minute {
		t.Fatalf("opened cookie = %+v, %v", opened, err)
	}
	if location.Query().Has("code_verifier") || location.Query().Has("client_secret") {
		t.Fatal("secret material in the Google URL")
	}

	var stored int
	var expires time.Time
	if err := pool.QueryRow(context.Background(), `SELECT count(*), max(expires_at) FROM oauth_states WHERE state_hash = $1`,
		security.OAuthStateHash(opened.State)).Scan(&stored, &expires); err != nil || stored != 1 || !expires.Equal(opened.ExpiresAt) {
		t.Fatalf("stored state = %d %v, %v", stored, expires, err)
	}
	var expired int
	_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM oauth_states WHERE expires_at < now()`).Scan(&expired)
	if expired != 0 {
		t.Fatal("expired states were not removed")
	}

	_, unsafe := start("//evil.example/steal")
	again, err := sealer.Open(unsafe.Value, time.Now())
	if err != nil || again.ReturnTo != "/lobby" || again.State == opened.State {
		t.Fatalf("second start = %+v, %v", again, err)
	}
}
