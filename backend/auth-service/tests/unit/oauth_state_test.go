package unit_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/controller"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/security"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/testutil"
)

// TestOAuthStateSealer checks the sealed ft_oauth cookie value.
//
// Expected output:
//
//   - A sealed value opens to the same state before expiry and hides the verifier.
//   - Changed, truncated, foreign-key or expired values are ErrInvalidOAuthState.
func TestOAuthStateSealer(t *testing.T) {
	sealer, err := security.NewOAuthStateSealer([]byte(testutil.TestSigningKey))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	state := security.OAuthState{State: "state-value", CodeVerifier: "verifier-value", ReturnTo: "/rooms/7", ExpiresAt: now.Add(10 * time.Minute)}
	value, err := sealer.Seal(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(value, "verifier") || strings.Contains(value, "state-value") {
		t.Fatal("sealed cookie exposes its contents")
	}
	opened, err := sealer.Open(value, now)
	if err != nil || opened.State != state.State || opened.CodeVerifier != state.CodeVerifier ||
		opened.ReturnTo != state.ReturnTo || !opened.ExpiresAt.Equal(state.ExpiresAt) {
		t.Fatalf("Open() = %+v, %v", opened, err)
	}

	other, _ := security.NewOAuthStateSealer([]byte(strings.Repeat("k", 40)))
	flipped := []byte(value)
	flipped[len(flipped)/2] ^= 1
	for name, open := range map[string]func() error{
		"changed":    func() error { _, err := sealer.Open(string(flipped), now); return err },
		"truncated":  func() error { _, err := sealer.Open(value[:10], now); return err },
		"not base64": func() error { _, err := sealer.Open("%%%", now); return err },
		"other key":  func() error { _, err := other.Open(value, now); return err },
		"expired":    func() error { _, err := sealer.Open(value, now.Add(10*time.Minute)); return err },
	} {
		if err := open(); !errors.Is(err, security.ErrInvalidOAuthState) {
			t.Errorf("%s: error = %v, want ErrInvalidOAuthState", name, err)
		}
	}
	if _, err := security.NewOAuthStateSealer([]byte("short")); err == nil {
		t.Fatal("short secret accepted")
	}
}

// TestPKCEChallenge checks S256 (base64url of SHA-256, no padding) against a
// value computed independently with Python's hashlib.
func TestPKCEChallenge(t *testing.T) {
	if got := security.PKCEChallenge("dBjftJeZ4CVP-mJ92K9XKcbqpBOt7ZhrNOk5XydI3p8"); got != "yyBKk1_Wh7eSP8Z5azvY2mZ9L5PDxAn5xVEy8XBCDT0" {
		t.Fatalf("PKCEChallenge() = %q", got)
	}
	secret, err := security.NewOAuthSecret()
	if err != nil || len(secret) != 43 {
		t.Fatalf("NewOAuthSecret() = %q, %v", secret, err)
	}
}

// TestSafeReturnTo checks which return_to values survive.
func TestSafeReturnTo(t *testing.T) {
	for raw, want := range map[string]string{
		"":                             "/lobby",
		"/lobby":                       "/lobby",
		"/rooms/42?tab=chat":           "/rooms/42?tab=chat",
		"//evil.example":               "/lobby",
		"/\\evil.example":              "/lobby",
		"https://evil.example/":        "/lobby",
		"lobby":                        "/lobby",
		"/a b":                         "/lobby",
		"/tab\tname":                   "/lobby",
		"/" + strings.Repeat("a", 200): "/lobby",
		"/" + strings.Repeat("a", 199): "/" + strings.Repeat("a", 199),
	} {
		if got := service.SafeReturnTo(raw); got != want {
			t.Errorf("SafeReturnTo(%q) = %q, want %q", raw, got, want)
		}
	}
}

type failingStates struct{}

func (failingStates) CreateOAuthState(context.Context, string, time.Time) error {
	return errors.New("database down")
}

// TestGoogleAuthorizeFailures checks that sign-in failures redirect with OAUTH_PROVIDER_ERROR.
//
// Expected output:
//
//   - Missing configuration and a failing state store both answer 302 to
//     /?oauth_error=OAUTH_PROVIDER_ERROR, clear ft_oauth and never reach Google.
func TestGoogleAuthorizeFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sealer, _ := security.NewOAuthStateSealer([]byte(testutil.TestSigningKey))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for name, google := range map[string]*service.GoogleOAuth{
		"not configured": service.NewGoogleOAuth(service.GoogleOAuthConfig{}, failingStates{}, sealer),
		"store failure": service.NewGoogleOAuth(service.GoogleOAuthConfig{
			ClientID: "client", RedirectURI: "https://example.com/api/v1/auth/oauth/google/callback",
		}, failingStates{}, sealer),
	} {
		t.Run(name, func(t *testing.T) {
			engine := gin.New()
			controller.NewAuth(&service.AuthService{}, controller.WithGoogleOAuth(google, logger)).RegisterRoutes(engine.Group("/api/v1/auth"))
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/google/authorize?return_to=/lobby", nil))

			cookie := recorder.Header().Get("Set-Cookie")
			if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/?oauth_error=OAUTH_PROVIDER_ERROR" ||
				!strings.Contains(cookie, "ft_oauth=;") || !strings.Contains(cookie, "Max-Age=0") {
				t.Fatalf("got %d Location=%q Set-Cookie=%q", recorder.Code, recorder.Header().Get("Location"), cookie)
			}
		})
	}
}
