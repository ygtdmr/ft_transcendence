package integration_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/controller"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/middleware/throttle"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/model"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/security"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/server"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
	"github.com/melmut-42/ft_transcendence/backend/shared/ratelimit"
)

// countingAuth answers every registration and login with INVALID_CREDENTIALS
// or success and counts how many requests reached the service.
type countingAuth struct {
	registrations atomic.Int32
	logins        atomic.Int32
}

func (a *countingAuth) Register(context.Context, service.RegisterInput) (service.AuthResult, error) {
	a.registrations.Add(1)
	return service.AuthResult{}, service.ErrEmailTaken
}

func (a *countingAuth) Login(_ context.Context, input service.LoginInput) (service.AuthResult, error) {
	a.logins.Add(1)
	if input.Password != "Correct-Horse-1" {
		return service.AuthResult{}, service.ErrInvalidCredentials
	}
	return service.AuthResult{User: model.User{ID: 42, Email: input.Email, Username: "player_one"},
		Credentials: security.Credentials{AccessToken: "access", RefreshToken: "refresh",
			AccessExpiresAt: time.Now().Add(time.Minute), Refresh: model.RefreshToken{ExpiresAt: time.Now().Add(time.Hour)}}}, nil
}

func (a *countingAuth) Authenticate(context.Context, string) (service.Identity, error) {
	return service.Identity{}, service.ErrUnauthorized
}

func (a *countingAuth) CurrentSession(context.Context, service.Identity) (service.SessionResult, error) {
	return service.SessionResult{}, service.ErrUnauthorized
}

func (a *countingAuth) Refresh(context.Context, string) (security.Credentials, error) {
	return security.Credentials{}, service.ErrSessionExpired
}

func (a *countingAuth) Logout(context.Context, service.Identity) error {
	return service.ErrUnauthorized
}

func limiter(t *testing.T, count int) *ratelimit.Limiter {
	t.Helper()
	l, err := ratelimit.New(ratelimit.Config{Limit: count, Window: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func limitedHandler(t *testing.T, auth *countingAuth, limits throttle.Limits) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	return server.New("0", controller.NewAuth(auth, controller.WithRateLimits(limits)), testLogger(t, "text", "info", io.Discard)).Handler
}

func send(handler http.Handler, path, body, peer string, headers ...string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = peer
	for i := 0; i+1 < len(headers); i += 2 {
		request.Header.Set(headers[i], headers[i+1])
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertRateLimited(t *testing.T, response *httptest.ResponseRecorder, message string) {
	t.Helper()
	var body struct {
		Error struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if response.Code != http.StatusTooManyRequests || json.Unmarshal(response.Body.Bytes(), &body) != nil ||
		body.Error.Code != "RATE_LIMITED" || body.Error.Message != message || body.Error.Details == nil {
		t.Fatalf("response = %d %s, want 429 RATE_LIMITED %q", response.Code, response.Body.String(), message)
	}
	if response.Header().Get("Set-Cookie") != "" {
		t.Fatal("a limited request set a cookie")
	}
}

func loginBody(email, password string) string {
	raw, _ := json.Marshal(map[string]string{"email": email, "password": password})
	return string(raw)
}

// TestRegisterRateLimitHTTP checks the registration limit per client address.
//
// Expected output:
//
//   - The third registration from one address is 429 RATE_LIMITED before the
//     body is read: even a malformed body gets 429 and the service is not called.
//   - Another client address keeps its own allowance.
func TestRegisterRateLimitHTTP(t *testing.T) {
	auth := &countingAuth{}
	handler := limitedHandler(t, auth, throttle.Limits{RegisterClient: limiter(t, 2)})
	body := `{"email":"p@example.com","username":"player_one","password":"Correct-Horse-1"}`

	for i := 0; i < 2; i++ {
		if response := send(handler, "/api/v1/auth/register", body, "198.51.100.7:4000"); response.Code != http.StatusConflict {
			t.Fatalf("registration %d = %d %s", i, response.Code, response.Body.String())
		}
	}
	assertRateLimited(t, send(handler, "/api/v1/auth/register", body, "198.51.100.7:4001"), "Too many registration attempts; try again later.")
	assertRateLimited(t, send(handler, "/api/v1/auth/register", `{`, "198.51.100.7:4002"), "Too many registration attempts; try again later.")
	if got := auth.registrations.Load(); got != 2 {
		t.Fatalf("service saw %d registrations, want 2", got)
	}
	if response := send(handler, "/api/v1/auth/register", body, "203.0.113.9:4000"); response.Code != http.StatusConflict {
		t.Fatalf("another client = %d", response.Code)
	}
}

// TestLoginRateLimitHTTP checks the login limits per client address and per email.
//
// Expected output:
//
//   - Failed attempts count: once one address is over its limit, even the
//     correct password is 429 and the service is not called.
//   - Attempts on one email from many addresses share one limit, regardless
//     of letter case; another email is unaffected.
//   - A malformed login body is still 400 while under the limits.
func TestLoginRateLimitHTTP(t *testing.T) {
	const message = "Too many login attempts; try again later."
	t.Run("per client", func(t *testing.T) {
		auth := &countingAuth{}
		handler := limitedHandler(t, auth, throttle.Limits{LoginClient: limiter(t, 3), LoginEmail: limiter(t, 100)})
		for i := 0; i < 3; i++ {
			email := "player" + string(rune('a'+i)) + "@example.com"
			if response := send(handler, "/api/v1/auth/login", loginBody(email, "wrong"), "198.51.100.7:4000"); response.Code != http.StatusUnauthorized {
				t.Fatalf("attempt %d = %d", i, response.Code)
			}
		}
		assertRateLimited(t, send(handler, "/api/v1/auth/login", loginBody("playerz@example.com", "Correct-Horse-1"), "198.51.100.7:4000"), message)
		if got := auth.logins.Load(); got != 3 {
			t.Fatalf("service saw %d logins, want 3", got)
		}
		if response := send(handler, "/api/v1/auth/login", loginBody("playerz@example.com", "Correct-Horse-1"), "203.0.113.9:4000"); response.Code != http.StatusOK {
			t.Fatalf("another client = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("per email", func(t *testing.T) {
		auth := &countingAuth{}
		handler := limitedHandler(t, auth, throttle.Limits{LoginClient: limiter(t, 100), LoginEmail: limiter(t, 2)})
		for i, email := range []string{"victim@example.com", "Victim@Example.com"} {
			peer := "198.51.100." + string(rune('1'+i)) + ":4000"
			if response := send(handler, "/api/v1/auth/login", loginBody(email, "wrong"), peer); response.Code != http.StatusUnauthorized {
				t.Fatalf("attempt %d = %d", i, response.Code)
			}
		}
		assertRateLimited(t, send(handler, "/api/v1/auth/login", loginBody("VICTIM@example.com", "wrong"), "198.51.100.9:4000"), message)
		if response := send(handler, "/api/v1/auth/login", loginBody("other@example.com", "wrong"), "198.51.100.9:4000"); response.Code != http.StatusUnauthorized {
			t.Fatalf("another email = %d", response.Code)
		}
		if response := send(handler, "/api/v1/auth/login", `{"email":`, "198.51.100.9:4000"); response.Code != http.StatusBadRequest {
			t.Fatalf("malformed login = %d", response.Code)
		}
	})
}

// TestRateLimitTrustedProxiesHTTP checks how the client address is found.
//
// Expected output:
//
//   - Behind a trusted proxy, clients named in X-Forwarded-For have their own
//     allowances.
//   - From an untrusted peer, X-Forwarded-For is ignored, so a client cannot
//     escape its limit by inventing addresses.
func TestRateLimitTrustedProxiesHTTP(t *testing.T) {
	proxies, err := ratelimit.ParseTrustedProxies([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	auth := &countingAuth{}
	handler := limitedHandler(t, auth, throttle.Limits{RegisterClient: limiter(t, 1), Proxies: proxies})
	body := `{"email":"p@example.com","username":"player_one","password":"Correct-Horse-1"}`

	for _, client := range []string{"198.51.100.7", "203.0.113.9"} {
		if response := send(handler, "/api/v1/auth/register", body, "10.0.0.5:4000", "X-Forwarded-For", client); response.Code != http.StatusConflict {
			t.Fatalf("client %s behind the proxy = %d", client, response.Code)
		}
	}
	assertRateLimited(t, send(handler, "/api/v1/auth/register", body, "10.0.0.5:4000", "X-Forwarded-For", "198.51.100.7"),
		"Too many registration attempts; try again later.")

	if response := send(handler, "/api/v1/auth/register", body, "192.0.2.1:4000", "X-Forwarded-For", "192.0.2.50"); response.Code != http.StatusConflict {
		t.Fatalf("first direct request = %d", response.Code)
	}
	assertRateLimited(t, send(handler, "/api/v1/auth/register", body, "192.0.2.1:4000", "X-Forwarded-For", "192.0.2.51"),
		"Too many registration attempts; try again later.")
}
