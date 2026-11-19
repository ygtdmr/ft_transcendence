package integration_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/melmut-42/ft_transcendence/backend/shared/ginhttp"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/shared/identity"
	"github.com/melmut-42/ft_transcendence/backend/shared/logging"
	"github.com/melmut-42/ft_transcendence/backend/shared/metrics"
	"github.com/melmut-42/ft_transcendence/backend/shared/ratelimit"
	"github.com/melmut-42/ft_transcendence/backend/shared/serviceauth"
)

const (
	acGatewayCredential = "gateway-credential-0123456789abcdef-0123456789"
	acSession           = "browser-session-jwt-abc123.payload.sig"
)

type stubValidator struct{ calls atomic.Int32 }

func (s *stubValidator) Validate(_ context.Context, credential string) (identity.Identity, error) {
	s.calls.Add(1)
	switch credential {
	case acSession:
		return identity.Identity{UserID: 42, SessionID: "8f14e45f-ceea-4e6b-9c3a-2d1b5f7a9e01", ExpiresAt: time.Now().Add(time.Hour)}, nil
	case "revoked-session":
		return identity.Identity{}, identity.ErrInvalidSession
	case "auth-is-down":
		return identity.Identity{}, identity.ErrUnavailable
	}
	return identity.Identity{}, identity.ErrInvalidSession
}

var errNotMember = errors.New("not a member")

// accessServer wires the shared guards the way an adopter would.
func accessServer(t *testing.T, limit int) (srv *httptest.Server, logs *lockedBuffer, service string, handlerRuns *atomic.Int32, validator *stubValidator) {
	t.Helper()
	service = "itest-" + strings.ToLower(strings.NewReplacer("/", "-", "_", "-").Replace(t.Name()))
	logs = newLockedBuffer()
	logger, err := logging.New(logging.Config{Format: "json", Level: "debug", Service: service}, logs)
	if err != nil {
		t.Fatal(err)
	}
	validator = &stubValidator{}
	handlerRuns = &atomic.Int32{}
	auth := serviceauth.New(map[string][]string{"game-gateway": {acGatewayCredential}}, serviceauth.Matrix{"game-gateway": {"session.validate"}})
	limiter, err := ratelimit.New(ratelimit.Config{Limit: limit, Window: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	mapper := httpresponse.NewMapper().Map(errNotMember, httpresponse.Error{Status: 403, Code: "NOT_ROOM_MEMBER", Message: "You are not a member of this room."})

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(ginhttp.Trace(), ginhttp.AccessLog(logger), metrics.Middleware(service))
	router.GET("/metrics", metrics.Handler())

	router.GET("/internal/v1/ping", ginhttp.Wrap(auth.Require("session.validate")), func(c *gin.Context) {
		handlerRuns.Add(1)
		httpresponse.WriteData(c.Writer, 200, map[string]any{"caller": serviceauth.Caller(c.Request.Context())})
	})
	router.GET("/internal/v1/other", ginhttp.Wrap(auth.Require("account.metadata.read")), func(c *gin.Context) { handlerRuns.Add(1) })

	users := router.Group("/api/v1", ginhttp.Wrap(identity.Authenticate(validator, identity.Options{Logger: logger})),
		ginhttp.Wrap(ratelimit.Middleware(limiter, ratelimit.KeyByUser, "Too many requests.")))
	users.GET("/me", func(c *gin.Context) {
		handlerRuns.Add(1)
		id, _ := identity.FromContext(c.Request.Context())
		httpresponse.WriteData(c.Writer, 200, map[string]any{"user_id": id.UserID, "session_id": id.SessionID})
	})
	users.GET("/rooms/:id", ginhttp.Wrap(identity.Authorize(func(r *http.Request, id identity.Identity) error {
		if strings.HasSuffix(r.URL.Path, "/7") {
			return errNotMember
		}
		return nil
	}, mapper, identity.Options{Logger: logger})), func(c *gin.Context) {
		handlerRuns.Add(1)
		httpresponse.WriteData(c.Writer, 200, map[string]any{"room_id": c.Param("id")})
	})

	srv = httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv, logs, service, handlerRuns, validator
}

func getWith(t *testing.T, url string, header map[string]string, cookies ...*http.Cookie) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp, sb.String()
}

func sessionCookieValue(v string) *http.Cookie { return &http.Cookie{Name: "ft_session", Value: v} }

func TestServiceAuthOverRealHTTP(t *testing.T) {
	srv, logs, _, runs, _ := accessServer(t, 100)

	resp, body := getWith(t, srv.URL+"/internal/v1/ping", map[string]string{"Authorization": "Bearer " + acGatewayCredential})
	if resp.StatusCode != 200 || !strings.Contains(body, `"caller":"game-gateway"`) {
		t.Fatalf("authorized call: %d %s", resp.StatusCode, body)
	}

	resp, body = getWith(t, srv.URL+"/internal/v1/ping", nil)
	if resp.StatusCode != 401 || resp.Header.Get("WWW-Authenticate") != "Bearer" || !strings.Contains(body, "SERVICE_UNAUTHENTICATED") {
		t.Errorf("no credential: %d www=%q %s", resp.StatusCode, resp.Header.Get("WWW-Authenticate"), body)
	}
	resp, body = getWith(t, srv.URL+"/internal/v1/ping", map[string]string{"Authorization": "Bearer wrong-" + acGatewayCredential})
	if resp.StatusCode != 401 || strings.Contains(body, "wrong-") {
		t.Errorf("wrong credential: %d %s", resp.StatusCode, body)
	}
	resp, body = getWith(t, srv.URL+"/internal/v1/other", map[string]string{"Authorization": "Bearer " + acGatewayCredential})
	if resp.StatusCode != 403 || !strings.Contains(body, "SERVICE_FORBIDDEN") {
		t.Errorf("missing scope: %d %s", resp.StatusCode, body)
	}
	for _, b := range []string{body} {
		if v := httpresponse.Violations(403, []byte(b)); len(v) != 0 {
			t.Errorf("%v", v)
		}
	}
	if runs.Load() != 1 {
		t.Errorf("a handler ran %d times; only the one authorized call may reach it", runs.Load())
	}
	if out := logs.String(); strings.Contains(out, acGatewayCredential) || strings.Contains(out, "wrong-") {
		t.Errorf("a service credential reached the logs:\n%s", out)
	}
}

func TestUserAuthenticationOverRealHTTP(t *testing.T) {
	srv, logs, _, runs, validator := accessServer(t, 100)

	resp, body := getWith(t, srv.URL+"/api/v1/me", nil, sessionCookieValue(acSession))
	if resp.StatusCode != 200 || !strings.Contains(body, `"user_id":42`) {
		t.Fatalf("valid session: %d %s", resp.StatusCode, body)
	}

	for name, tc := range map[string]struct {
		url     string
		header  map[string]string
		cookies []*http.Cookie
		status  int
		code    string
	}{
		"no credential":        {"/api/v1/me", nil, nil, 401, "UNAUTHORIZED"},
		"revoked session":      {"/api/v1/me", nil, []*http.Cookie{sessionCookieValue("revoked-session")}, 401, "UNAUTHORIZED"},
		"credential in query":  {"/api/v1/me?ft_session=" + acSession, nil, nil, 401, "UNAUTHORIZED"},
		"credential as bearer": {"/api/v1/me", map[string]string{"Authorization": "Bearer " + acSession}, nil, 401, "UNAUTHORIZED"},
		"credential in header": {"/api/v1/me", map[string]string{"X-Session": acSession}, nil, 401, "UNAUTHORIZED"},
		"spoofed identity":     {"/api/v1/me", map[string]string{"X-User-ID": "42"}, nil, 401, "UNAUTHORIZED"},
		"auth service down":    {"/api/v1/me", nil, []*http.Cookie{sessionCookieValue("auth-is-down")}, 503, "SERVICE_UNAVAILABLE"},
		"two session cookies":  {"/api/v1/me", nil, []*http.Cookie{sessionCookieValue(acSession), sessionCookieValue(acSession)}, 401, "UNAUTHORIZED"},
	} {
		resp, body := getWith(t, srv.URL+tc.url, tc.header, tc.cookies...)
		if resp.StatusCode != tc.status || !strings.Contains(body, `"code":"`+tc.code+`"`) {
			t.Errorf("%s: %d %s, want %d %s", name, resp.StatusCode, body, tc.status, tc.code)
		}
		if v := httpresponse.Violations(resp.StatusCode, []byte(body)); len(v) != 0 {
			t.Errorf("%s: %v", name, v)
		}
		if len(resp.Header["Set-Cookie"]) != 0 {
			t.Errorf("%s: the guard must not touch cookies (an outage is not a logout)", name)
		}
	}
	if runs.Load() != 1 {
		t.Errorf("a protected handler ran %d times; only the valid session may reach it", runs.Load())
	}
	if validator.calls.Load() != 3 { // the valid call, the revoked one, and the auth-down one
		t.Errorf("the validator was asked %d times; requests with the credential in the wrong place must never reach it", validator.calls.Load())
	}
	if out := logs.String(); strings.Contains(out, acSession) || strings.Contains(out, "revoked-session") {
		t.Errorf("a session credential reached the logs:\n%s", out)
	}
}

func TestAuthorizationAndRateLimitingOverRealHTTP(t *testing.T) {
	srv, _, _, runs, _ := accessServer(t, 4)
	cookie := sessionCookieValue(acSession)

	resp, body := getWith(t, srv.URL+"/api/v1/rooms/9", nil, cookie)
	if resp.StatusCode != 200 || !strings.Contains(body, `"room_id":"9"`) {
		t.Fatalf("allowed room: %d %s", resp.StatusCode, body)
	}
	resp, body = getWith(t, srv.URL+"/api/v1/rooms/7", nil, cookie)
	if resp.StatusCode != 403 || !strings.Contains(body, `"code":"NOT_ROOM_MEMBER"`) {
		t.Errorf("forbidden room: %d %s", resp.StatusCode, body)
	}

	// Budget for user 42 is 4: two requests above plus two more, then 429.
	for i := 0; i < 2; i++ {
		if resp, _ := getWith(t, srv.URL+"/api/v1/me", nil, cookie); resp.StatusCode != 200 {
			t.Fatalf("request %d within budget: %d", i+3, resp.StatusCode)
		}
	}
	runsBefore := runs.Load()
	resp, body = getWith(t, srv.URL+"/api/v1/me", nil, cookie)
	if resp.StatusCode != 429 || !strings.Contains(body, `"code":"RATE_LIMITED"`) || runs.Load() != runsBefore {
		t.Errorf("over budget: %d %s (handler runs %d -> %d)", resp.StatusCode, body, runsBefore, runs.Load())
	}
	if v := httpresponse.Violations(429, []byte(body)); len(v) != 0 {
		t.Errorf("%v", v)
	}
	// An unauthenticated request is rejected before it can spend anyone's budget.
	if resp, _ := getWith(t, srv.URL+"/api/v1/me", nil); resp.StatusCode != 401 {
		t.Errorf("unauthenticated: %d", resp.StatusCode)
	}
}

func TestGuardRejectionsReachTheLogsAndMetricsWithTheirCodes(t *testing.T) {
	srv, logs, service, _, _ := accessServer(t, 100)
	getWith(t, srv.URL+"/internal/v1/ping", nil)
	getWith(t, srv.URL+"/internal/v1/other", map[string]string{"Authorization": "Bearer " + acGatewayCredential})
	getWith(t, srv.URL+"/api/v1/me", nil)
	getWith(t, srv.URL+"/api/v1/me", nil, sessionCookieValue("auth-is-down"))

	seen := map[string]bool{}
	for _, rec := range logRecords(t, logs) {
		if rec["msg"] == "http_request" {
			if code, ok := rec["error_code"].(string); ok {
				seen[code] = true
			}
		}
	}
	for _, code := range []string{"SERVICE_UNAUTHENTICATED", "SERVICE_FORBIDDEN", "UNAUTHORIZED", "SERVICE_UNAVAILABLE"} {
		if !seen[code] {
			t.Errorf("no access-log record with error_code %s", code)
		}
	}

	_, metricsBody := getWith(t, srv.URL+"/metrics", nil)
	for _, code := range []string{"SERVICE_UNAUTHENTICATED", "SERVICE_FORBIDDEN", "UNAUTHORIZED", "SERVICE_UNAVAILABLE"} {
		if !strings.Contains(metricsBody, `http_errors_total{code="`+code+`",service="`+service+`"}`) {
			t.Errorf("metrics lack the %s error counter", code)
		}
	}
}
