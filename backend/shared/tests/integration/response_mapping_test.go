package integration_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/melmut-42/ft_transcendence/backend/shared/ginhttp"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/shared/logging"
	"github.com/melmut-42/ft_transcendence/backend/shared/metrics"
	"github.com/melmut-42/ft_transcendence/backend/shared/validate"
)

var (
	errUserMissing  = errors.New("user missing")
	errNameTaken    = errors.New("name taken")
	sessionCookie   = httpresponse.Cookie{Name: "ft_session", Path: "/", MaxAge: 15 * time.Minute}
	refreshCookie   = httpresponse.Cookie{Name: "ft_refresh", Path: "/api/v1/auth/refresh", MaxAge: 30 * 24 * time.Hour}
	sessionJWT      = "header.payload.signature-session-9f31"
	refreshOpaque   = "rtok_refresh-opaque-77c2"
	internalDSNLeak = "pq: password authentication failed for user svc at postgres://svc:hunter2@db.internal:5432/app"
)

// accountRecord is what a repository returns; accountDTO is what may go on the wire.
type accountRecord struct {
	ID           int64
	Email        string
	PasswordHash string
	CreatedAt    time.Time
}

type accountDTO struct {
	UserID    int64             `json:"user_id"`
	CreatedAt httpresponse.Time `json:"created_at"`
}

func responseServer(t *testing.T) (*httptest.Server, *lockedBuffer, string) {
	t.Helper()
	service := "itest-" + strings.ToLower(strings.NewReplacer("/", "-", "_", "-").Replace(t.Name()))
	logs := newLockedBuffer()
	logger, err := logging.New(logging.Config{Format: "json", Level: "debug", Service: service}, logs)
	if err != nil {
		t.Fatal(err)
	}
	mapper := httpresponse.NewMapper().
		Map(errUserMissing, httpresponse.Error{Status: 404, Code: "USER_NOT_FOUND", Message: "The user was not found.", Details: map[string]any{"user_id": 9999}}).
		Map(errNameTaken, httpresponse.Error{Status: 409, Code: "USERNAME_TAKEN", Message: "That username is taken."})

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(ginhttp.Trace(), ginhttp.AccessLog(logger), metrics.Middleware(service))
	handle := func(fn func(*gin.Context) error) gin.HandlerFunc { return ginhttp.Handle(fn, mapper, logger) }

	router.POST("/api/v1/auth/login", handle(func(c *gin.Context) error {
		record := accountRecord{ID: 7, Email: "ann@example.com", PasswordHash: "$2a$secret-hash", CreatedAt: time.Date(2026, 11, 5, 19, 0, 0, 0, time.FixedZone("EET", 7200))}
		if err := httpresponse.SetCookie(c.Writer, sessionCookie, sessionJWT); err != nil {
			return err
		}
		if err := httpresponse.SetCookie(c.Writer, refreshCookie, refreshOpaque); err != nil {
			return err
		}
		httpresponse.Respond(c.Writer, http.StatusOK, record, func(r accountRecord) accountDTO {
			return accountDTO{UserID: r.ID, CreatedAt: httpresponse.Time(r.CreatedAt)}
		})
		return nil
	}))
	router.DELETE("/api/v1/auth/session", handle(func(c *gin.Context) error {
		if err := httpresponse.ClearCookie(c.Writer, sessionCookie); err != nil {
			return err
		}
		if err := httpresponse.ClearCookie(c.Writer, refreshCookie); err != nil {
			return err
		}
		httpresponse.WriteNoContent(c.Writer)
		return nil
	}))
	router.GET("/api/v1/friends", handle(func(c *gin.Context) error {
		var none []accountDTO // a repository found nothing
		httpresponse.WriteData(c.Writer, http.StatusOK, httpresponse.Slice(none))
		return nil
	}))
	router.GET("/api/v1/users/:user_id", handle(func(c *gin.Context) error {
		id, err := validate.ParsePositiveInt("user_id", c.Param("user_id"))
		if err != nil {
			return err
		}
		switch id {
		case 9999:
			return fmt.Errorf("get user %d: %w", id, errUserMissing)
		case 500:
			return errors.New(internalDSNLeak)
		}
		httpresponse.WriteData(c.Writer, http.StatusOK, map[string]any{"user_id": id})
		return nil
	}))
	router.POST("/api/v1/users", handle(func(c *gin.Context) error { return fmt.Errorf("insert: %w", errNameTaken) }))

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv, logs, service
}

func TestLoginResponseSetsTheContractCookiesAndOnlyTheDTO(t *testing.T) {
	srv, logs, _ := responseServer(t)
	resp, body := doS(t, "POST", srv.URL+"/api/v1/auth/login", nil)

	if resp.StatusCode != 200 || strings.TrimSpace(body) != `{"data":{"user_id":7,"created_at":"2026-11-05T17:00:00Z"}}` {
		t.Fatalf("got %d %s", resp.StatusCode, body)
	}
	if v := httpresponse.Violations(resp.StatusCode, []byte(body)); len(v) != 0 {
		t.Errorf("%v", v)
	}
	if strings.Contains(body, "secret-hash") || strings.Contains(body, "ann@example.com") {
		t.Error("a field outside the DTO reached the wire")
	}
	if resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Errorf("headers = %v", resp.Header)
	}

	cookies := map[string]*http.Cookie{}
	for _, c := range resp.Cookies() {
		cookies[c.Name] = c
	}
	s, r := cookies["ft_session"], cookies["ft_refresh"]
	if s == nil || r == nil || s.Value != sessionJWT || r.Value != refreshOpaque {
		t.Fatalf("cookies = %v", resp.Header["Set-Cookie"])
	}
	for _, c := range []*http.Cookie{s, r} {
		if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
			t.Errorf("%s lacks HttpOnly/Secure/SameSite=Lax: %+v", c.Name, c)
		}
	}
	if s.Path != "/" || r.Path != "/api/v1/auth/refresh" {
		t.Errorf("paths: session=%q refresh=%q", s.Path, r.Path)
	}
	if strings.Contains(body, sessionJWT) || strings.Contains(body, refreshOpaque) {
		t.Error("a credential was returned in the body; it must only travel in a cookie")
	}
	if out := logs.String(); strings.Contains(out, sessionJWT) || strings.Contains(out, refreshOpaque) {
		t.Errorf("a cookie value reached the logs:\n%s", out)
	}
}

func TestLogoutIs204AndClearsBothCookies(t *testing.T) {
	srv, _, _ := responseServer(t)
	resp, body := doS(t, "DELETE", srv.URL+"/api/v1/auth/session", nil)
	if resp.StatusCode != 204 || body != "" || resp.Header.Get("Content-Type") != "" {
		t.Fatalf("got %d body=%q content-type=%q", resp.StatusCode, body, resp.Header.Get("Content-Type"))
	}
	cleared := map[string]string{}
	for _, c := range resp.Cookies() {
		if c.Value != "" || c.MaxAge >= 0 || !c.HttpOnly || !c.Secure {
			t.Errorf("%s not properly cleared: %+v", c.Name, c)
		}
		cleared[c.Name] = c.Path
	}
	if cleared["ft_session"] != "/" || cleared["ft_refresh"] != "/api/v1/auth/refresh" {
		t.Errorf("cleared cookies/paths = %v", cleared)
	}
}

func TestAnEmptyListIsAnEmptyArray(t *testing.T) {
	srv, _, _ := responseServer(t)
	resp, body := doS(t, "GET", srv.URL+"/api/v1/friends", nil)
	if resp.StatusCode != 200 || strings.TrimSpace(body) != `{"data":[]}` {
		t.Errorf("got %d %s", resp.StatusCode, body)
	}
}

func TestServiceErrorsBecomeTheirContractResponses(t *testing.T) {
	srv, _, _ := responseServer(t)
	for _, tc := range []struct {
		method, path string
		status       int
		want         string
	}{
		{"GET", "/api/v1/users/9999", 404, `{"error":{"code":"USER_NOT_FOUND","message":"The user was not found.","details":{"user_id":9999}}}`},
		{"POST", "/api/v1/users", 409, `{"error":{"code":"USERNAME_TAKEN","message":"That username is taken.","details":{}}}`},
		{"GET", "/api/v1/users/zero", 422, `{"error":{"code":"VALIDATION_ERROR","message":"user_id must be a positive integer.","details":{"field":"user_id"}}}`},
	} {
		resp, body := doS(t, tc.method, srv.URL+tc.path, nil)
		if resp.StatusCode != tc.status || strings.TrimSpace(body) != tc.want {
			t.Errorf("%s %s: got %d %s\nwant %d %s", tc.method, tc.path, resp.StatusCode, body, tc.status, tc.want)
		}
		if v := httpresponse.Violations(resp.StatusCode, []byte(body)); len(v) != 0 {
			t.Errorf("%s %s: %v", tc.method, tc.path, v)
		}
	}
}

func TestAnUnexpectedErrorIsGenericInTheResponseAndLoggedByTypeOnly(t *testing.T) {
	srv, logs, _ := responseServer(t)
	resp, body := doS(t, "GET", srv.URL+"/api/v1/users/500", map[string]string{"X-Request-ID": "req-leak-check"})

	if resp.StatusCode != 500 || strings.TrimSpace(body) != `{"error":{"code":"INTERNAL_ERROR","message":"An internal error occurred.","details":{}}}` {
		t.Fatalf("got %d %s", resp.StatusCode, body)
	}
	for _, leak := range []string{"hunter2", "db.internal", "password authentication"} {
		if strings.Contains(body, leak) || strings.Contains(logs.String(), leak) {
			t.Errorf("%q leaked into the response or the logs:\n%s", leak, logs.String())
		}
	}

	var unhandled, access bool
	for _, rec := range logRecords(t, logs) {
		switch rec["msg"] {
		case "unhandled_error":
			unhandled = rec["level"] == "ERROR" && rec["request_id"] == "req-leak-check" && rec["error_type"] == "*errors.errorString"
		case "http_request":
			access = rec["status"] == float64(500) && rec["error_code"] == "INTERNAL_ERROR" && rec["level"] == "ERROR"
		}
	}
	if !unhandled || !access {
		t.Errorf("expected an unhandled_error record (type only, with the request id) and a 500 access record:\n%s", logs.String())
	}
}

func TestErrorCodesReachTheMetricsThroughHandle(t *testing.T) {
	// Build a second server that also exposes /metrics, because Handle must feed
	// the error-code counter without any handler-side code.
	service := "itest-handle-metrics"
	logger, _ := logging.New(logging.Config{Format: "json", Service: service}, newLockedBuffer())
	mapper := httpresponse.NewMapper().Map(errNameTaken, httpresponse.Error{Status: 409, Code: "USERNAME_TAKEN", Message: "That username is taken."})
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(ginhttp.Trace(), metrics.Middleware(service))
	router.GET("/metrics", metrics.Handler())
	router.POST("/taken", ginhttp.Handle(func(*gin.Context) error { return errNameTaken }, mapper, logger))
	router.GET("/boom", ginhttp.Handle(func(*gin.Context) error { return errors.New(internalDSNLeak) }, mapper, logger))
	srv := httptest.NewServer(router)
	defer srv.Close()

	doS(t, "POST", srv.URL+"/taken", nil)
	doS(t, "GET", srv.URL+"/boom", nil)
	_, text := doS(t, "GET", srv.URL+"/metrics", nil)
	for _, want := range []string{
		`http_errors_total{code="USERNAME_TAKEN",service="` + service + `"} 1`,
		`http_errors_total{code="INTERNAL_ERROR",service="` + service + `"} 1`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}

// doS is do with the body as a string.
func doS(t *testing.T, method, url string, header map[string]string) (*http.Response, string) {
	t.Helper()
	resp, body := do(t, method, url, header)
	return resp, string(body)
}
