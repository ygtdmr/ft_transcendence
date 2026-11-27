package unit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/shared/identity"
	"github.com/melmut-42/ft_transcendence/backend/web-bff/internal/app"
	"github.com/melmut-42/ft_transcendence/backend/web-bff/internal/config"
)

const (
	origin       = "https://app.test"
	validCookie  = "valid-session-credential"
	downCookie   = "verifier-down-credential"
	refreshValue = "refresh-credential"
)

// fakeSessions accepts validCookie as user 42 and reports downCookie as an
// unavailable verdict. It counts calls so tests can prove the order of checks.
type fakeSessions struct {
	mu    sync.Mutex
	calls int
}

func (f *fakeSessions) Validate(_ context.Context, credential string) (identity.Identity, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	switch credential {
	case validCookie:
		return identity.Identity{UserID: 42, SessionID: "3f9b2c71-5d4e-4a86-9c11-000000000001", ExpiresAt: time.Now().Add(time.Hour)}, nil
	case downCookie:
		return identity.Identity{}, identity.ErrUnavailable
	default:
		return identity.Identity{}, identity.ErrInvalidSession
	}
}

func (f *fakeSessions) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// upstream records the last request a fake service received.
type upstream struct {
	name   string
	server *httptest.Server
	mu     sync.Mutex
	last   *http.Request
	body   string
	hits   int
}

func newUpstream(t *testing.T, name string, handler func(w http.ResponseWriter, r *http.Request)) *upstream {
	t.Helper()
	u := &upstream{name: name}
	u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.last, u.body, u.hits = r.Clone(context.Background()), string(raw), u.hits+1
		u.mu.Unlock()
		if handler != nil {
			handler(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "upstream-echo")
		_, _ = w.Write([]byte(`{"data":{"served_by":"` + name + `"}}`))
	}))
	t.Cleanup(u.server.Close)
	return u
}

func (u *upstream) url(t *testing.T) *url.URL {
	t.Helper()
	parsed, err := url.Parse(u.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func (u *upstream) snapshot() (*http.Request, string, int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.last, u.body, u.hits
}

type harness struct {
	handler                 http.Handler
	sessions                *fakeSessions
	auth, user, stats, game *upstream
	logs                    *bytes.Buffer
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &harness{sessions: &fakeSessions{}, logs: &bytes.Buffer{}}
	h.auth = newUpstream(t, "auth-service", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "ft_session", Value: "issued", Path: "/", HttpOnly: true, Secure: true})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"user_id":42}}`))
		case "/api/v1/auth/oauth/google/callback":
			http.Redirect(w, r, "/lobby", http.StatusFound)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"served_by":"auth-service"}}`))
		}
	})
	h.user = newUpstream(t, "user-service", nil)
	h.stats = newUpstream(t, "stats-service", nil)
	h.game = newUpstream(t, "game-session", nil)

	cfg := config.Config{PublicOrigin: origin, UpstreamTimeout: 5 * time.Second}
	cfg.Upstreams = config.Upstreams{Auth: h.auth.url(t), User: h.user.url(t), Stats: h.stats.url(t), Game: h.game.url(t)}
	logger := slog.New(slog.NewJSONHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	bff, err := app.New(cfg, logger, app.Dependencies{
		Sessions: h.sessions,
		Now:      func() time.Time { return time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	h.handler = bff.Public.Handler
	return h
}

func (h *harness) do(t *testing.T, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = "172.20.0.10:51000"
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("response is not an error envelope: %q", rec.Body.String())
	}
	if envelope.Error.Details == nil {
		t.Fatalf("error details must be an object: %q", rec.Body.String())
	}
	return envelope.Error.Code
}

func sessionCookie(value string) map[string]string {
	return map[string]string{"Cookie": "ft_session=" + value}
}

// TestProtectedRoutesRequireSession checks that a missing, invalid or
// unverifiable session never reaches a service.
//
// Expected output: 401 UNAUTHORIZED for a missing or invalid cookie, 503
// SERVICE_UNAVAILABLE when Auth cannot answer, and no upstream request.
func TestProtectedRoutesRequireSession(t *testing.T) {
	h := newHarness(t)
	for _, tt := range []struct {
		name    string
		headers map[string]string
		status  int
		code    string
	}{
		{"missing cookie", nil, http.StatusUnauthorized, "UNAUTHORIZED"},
		{"invalid cookie", sessionCookie("expired-or-revoked"), http.StatusUnauthorized, "UNAUTHORIZED"},
		{"two cookies", map[string]string{"Cookie": "ft_session=" + validCookie + "; ft_session=other"}, http.StatusUnauthorized, "UNAUTHORIZED"},
		{"verifier unavailable", sessionCookie(downCookie), http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := h.do(t, http.MethodGet, "/api/v1/users/me", "", tt.headers)
			if rec.Code != tt.status || errorCode(t, rec) != tt.code {
				t.Fatalf("status %d body %s, want %d %s", rec.Code, rec.Body.String(), tt.status, tt.code)
			}
		})
	}
	if _, _, hits := h.user.snapshot(); hits != 0 {
		t.Fatalf("user-service received %d requests without a valid session", hits)
	}
}

// TestCredentialsOutsideTheCookieAreIgnored checks that a valid credential in a
// query parameter, header or body does not authenticate.
//
// Expected output: 401 UNAUTHORIZED for each carrier.
func TestCredentialsOutsideTheCookieAreIgnored(t *testing.T) {
	h := newHarness(t)
	for _, tt := range []struct {
		name, method, target, body string
		headers                    map[string]string
	}{
		{"query", http.MethodGet, "/api/v1/users/me?ft_session=" + validCookie, "", nil},
		{"authorization header", http.MethodGet, "/api/v1/users/me", "", map[string]string{"Authorization": "Bearer " + validCookie}},
		{"custom header", http.MethodGet, "/api/v1/users/me", "", map[string]string{"X-Session": validCookie, "X-User-Id": "42"}},
		{"json body", http.MethodPatch, "/api/v1/users/me", `{"ft_session":"` + validCookie + `"}`, map[string]string{"Content-Type": "application/json"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := h.do(t, tt.method, tt.target, tt.body, tt.headers)
			if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "UNAUTHORIZED" {
				t.Fatalf("status %d body %s, want 401 UNAUTHORIZED", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestAuthorizedRequestsReachTheOwningService checks the route table end to end.
//
// Expected output: each path reaches its owner with method, path, query and
// body intact.
func TestAuthorizedRequestsReachTheOwningService(t *testing.T) {
	h := newHarness(t)
	for _, tt := range []struct {
		method, target, body string
		want                 *upstream
	}{
		{http.MethodGet, "/api/v1/auth/session", "", h.auth},
		{http.MethodGet, "/api/v1/users/me/matches?limit=20", "", h.stats},
		{http.MethodGet, "/api/v1/users/me", "", h.user},
		{http.MethodGet, "/api/v1/users/search?q=red&limit=20&offset=0", "", h.user},
		{http.MethodGet, "/api/v1/users/43", "", h.user},
		{http.MethodPut, "/api/v1/users/45/block", "", h.user},
		{http.MethodPost, "/api/v1/friends/requests", `{"user_id":43}`, h.user},
		{http.MethodDelete, "/api/v1/friends/requests/701", "", h.user},
		{http.MethodDelete, "/api/v1/friends/43", "", h.user},
		{http.MethodGet, "/api/v1/avatars/presets", "", h.user},
		{http.MethodPost, "/api/v2/rooms", `{"max_players":8}`, h.game},
		{http.MethodGet, "/api/v2/rooms/lookup/ABCD12", "", h.game},
		{http.MethodGet, "/api/v2/rooms/1001", "", h.game},
		{http.MethodDelete, "/api/v2/rooms/1001/members/me", "", h.game},
	} {
		t.Run(tt.method+" "+tt.target, func(t *testing.T) {
			headers := sessionCookie(validCookie)
			headers["Origin"] = origin
			rec := h.do(t, tt.method, tt.target, tt.body, headers)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tt.want.name) {
				t.Fatalf("status %d body %s, want %s", rec.Code, rec.Body.String(), tt.want.name)
			}
			got, body, _ := tt.want.snapshot()
			if got.Method != tt.method || got.URL.RequestURI() != tt.target || body != tt.body {
				t.Fatalf("%s received %s %s %q", tt.want.name, got.Method, got.URL.RequestURI(), body)
			}
			if values := rec.Header().Values("X-Request-ID"); len(values) != 1 {
				t.Fatalf("X-Request-ID values = %v, want exactly one", values)
			}
		})
	}
}

// TestForwardedHeaders checks what each service receives besides the request.
//
// Expected output:
//
//   - Auth, User and Stats keep the browser's cookie and never receive
//     Authorization, X-User-Id or X-Session-Id;
//   - Game Session receives X-User-Id of the validated session, never the
//     client's value, and no cookie;
//   - every service receives the BFF's X-Request-ID and the forwarded chain.
func TestForwardedHeaders(t *testing.T) {
	h := newHarness(t)
	headers := map[string]string{
		"Cookie":          "ft_session=" + validCookie + "; theme=dark",
		"Authorization":   "Bearer forged",
		"X-User-Id":       "999",
		"X-Session-Id":    "forged",
		"X-Request-ID":    "client-request-0001",
		"X-Forwarded-For": "203.0.113.7",
		"Origin":          origin,
	}

	rec := h.do(t, http.MethodGet, "/api/v1/users/me", "", headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("user route status %d", rec.Code)
	}
	got, _, _ := h.user.snapshot()
	if c, err := got.Cookie("ft_session"); err != nil || c.Value != validCookie {
		t.Fatal("user-service did not receive the session cookie")
	}
	for _, name := range []string{"Authorization", "X-User-Id", "X-Session-Id"} {
		if got.Header.Get(name) != "" {
			t.Fatalf("user-service received %s", name)
		}
	}
	if got.Header.Get("X-Request-ID") != "client-request-0001" || rec.Header().Get("X-Request-ID") != "client-request-0001" {
		t.Fatalf("request id not propagated: upstream %q, response %q", got.Header.Get("X-Request-ID"), rec.Header().Get("X-Request-ID"))
	}
	if xff := got.Header.Get("X-Forwarded-For"); xff != "203.0.113.7, 172.20.0.10" {
		t.Fatalf("X-Forwarded-For = %q", xff)
	}

	rec = h.do(t, http.MethodPost, "/api/v2/rooms/1001/members", "", headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("game route status %d", rec.Code)
	}
	got, _, _ = h.game.snapshot()
	if got.Header.Get("X-User-Id") != "42" {
		t.Fatalf("game-session X-User-Id = %q, want 42", got.Header.Get("X-User-Id"))
	}
	if got.Header.Get("Cookie") != "" || got.Header.Get("Authorization") != "" || got.Header.Get("X-Session-Id") != "" {
		t.Fatal("game-session received a browser credential or a forged header")
	}
}

// TestPublicAuthRoutes checks that sign-in routes are not blocked by session
// enforcement and that cookies and redirects come back unchanged.
func TestPublicAuthRoutes(t *testing.T) {
	h := newHarness(t)

	rec := h.do(t, http.MethodPost, "/api/v1/auth/login", `{"email":"a@b.c","password":"x"}`,
		map[string]string{"Content-Type": "application/json", "Origin": "https://elsewhere.test"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Set-Cookie"), "ft_session=issued") {
		t.Fatalf("login: status %d, Set-Cookie %q", rec.Code, rec.Header().Get("Set-Cookie"))
	}

	rec = h.do(t, http.MethodGet, "/api/v1/auth/oauth/google/callback?code=c&state=s", "", nil)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/lobby" {
		t.Fatalf("callback: status %d, Location %q", rec.Code, rec.Header().Get("Location"))
	}

	rec = h.do(t, http.MethodPost, "/api/v1/auth/refresh", "", map[string]string{"Cookie": "ft_refresh=" + refreshValue, "Origin": origin})
	if got, _, _ := h.auth.snapshot(); rec.Code != http.StatusOK || got.URL.Path != "/api/v1/auth/refresh" {
		t.Fatalf("refresh: status %d", rec.Code)
	}
	if h.sessions.count() != 0 {
		t.Fatal("public routes consulted the session verifier")
	}

	rec = h.do(t, http.MethodGet, "/api/v1/health", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"contract_version":"1.2.0-dev"`) ||
		!strings.Contains(rec.Body.String(), `"server_time":"2026-11-27T12:00:00Z"`) {
		t.Fatalf("health: status %d body %s", rec.Code, rec.Body.String())
	}
}

// TestCrossOriginWritesAreRefusedFirst checks the same-origin rule.
//
// Expected output: 403 FORBIDDEN_ORIGIN before the session is checked, for a
// session route and for refresh; reads are not checked.
func TestCrossOriginWritesAreRefusedFirst(t *testing.T) {
	h := newHarness(t)
	for _, tt := range []struct {
		method, target string
		headers        map[string]string
	}{
		{http.MethodPost, "/api/v2/rooms", map[string]string{"Cookie": "ft_session=" + validCookie, "Origin": "https://evil.test"}},
		{http.MethodDelete, "/api/v1/auth/session", map[string]string{"Cookie": "ft_session=" + validCookie, "Sec-Fetch-Site": "cross-site"}},
		{http.MethodPost, "/api/v1/auth/refresh", map[string]string{"Cookie": "ft_refresh=" + refreshValue, "Origin": "null"}},
	} {
		rec := h.do(t, tt.method, tt.target, "", tt.headers)
		if rec.Code != http.StatusForbidden || errorCode(t, rec) != "FORBIDDEN_ORIGIN" {
			t.Fatalf("%s %s: status %d body %s", tt.method, tt.target, rec.Code, rec.Body.String())
		}
	}
	if h.sessions.count() != 0 {
		t.Fatal("the session was checked before the origin")
	}
	if rec := h.do(t, http.MethodGet, "/api/v1/users/me", "", map[string]string{"Cookie": "ft_session=" + validCookie, "Origin": "https://evil.test"}); rec.Code != http.StatusOK {
		t.Fatalf("cross-origin read status %d, want 200", rec.Code)
	}
}

// TestUnroutedPaths checks that only listed routes are forwarded.
//
// Expected output: legacy rooms and unknown paths are 404 NOT_FOUND and a wrong
// method is 405 METHOD_NOT_ALLOWED, with no upstream request.
func TestUnroutedPaths(t *testing.T) {
	h := newHarness(t)
	for _, tt := range []struct {
		method, target string
		status         int
		code           string
	}{
		{http.MethodPost, "/api/v1/rooms", http.StatusNotFound, "NOT_FOUND"},
		{http.MethodGet, "/api/v1/rooms/1001", http.StatusNotFound, "NOT_FOUND"},
		{http.MethodGet, "/internal/v1/sessions/current", http.StatusNotFound, "NOT_FOUND"},
		{http.MethodGet, "/api/v1/unknown", http.StatusNotFound, "NOT_FOUND"},
		{http.MethodPut, "/api/v1/users/me", http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED"},
	} {
		rec := h.do(t, tt.method, tt.target, "", sessionCookie(validCookie))
		if rec.Code != tt.status || errorCode(t, rec) != tt.code {
			t.Fatalf("%s %s: status %d body %s", tt.method, tt.target, rec.Code, rec.Body.String())
		}
	}
	for _, u := range []*upstream{h.auth, h.user, h.stats, h.game} {
		if _, _, hits := u.snapshot(); hits != 0 {
			t.Fatalf("%s received an unrouted request", u.name)
		}
	}
}

// TestUnavailableService checks that an unreachable service is an availability
// failure, never a missing route or a logout.
func TestUnavailableService(t *testing.T) {
	h := newHarness(t)
	h.stats.server.Close()
	rec := h.do(t, http.MethodGet, "/api/v1/users/me/matches", "", sessionCookie(validCookie))
	if rec.Code != http.StatusServiceUnavailable || errorCode(t, rec) != "SERVICE_UNAVAILABLE" {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), h.stats.server.URL) || strings.Contains(h.logs.String(), h.stats.server.URL) {
		t.Fatal("the service address leaked into the response or the logs")
	}
}

// TestLogsCarryNoCredentials checks access and failure logs.
func TestLogsCarryNoCredentials(t *testing.T) {
	h := newHarness(t)
	h.do(t, http.MethodGet, "/api/v1/users/me?ft_session="+validCookie, "", sessionCookie(validCookie))
	h.do(t, http.MethodGet, "/api/v1/users/me", "", sessionCookie(downCookie))
	h.do(t, http.MethodPost, "/api/v1/auth/refresh", "", map[string]string{"Cookie": "ft_refresh=" + refreshValue})
	logs := h.logs.String()
	if logs == "" {
		t.Fatal("no access log written")
	}
	for _, secret := range []string{validCookie, downCookie, refreshValue, "ft_session=", "ft_refresh="} {
		if strings.Contains(logs, secret) {
			t.Fatalf("logs contain %q", secret)
		}
	}
}
