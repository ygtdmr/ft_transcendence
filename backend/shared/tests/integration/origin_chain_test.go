package integration_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/melmut-42/ft_transcendence/backend/shared/ginhttp"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/shared/identity"
	"github.com/melmut-42/ft_transcendence/backend/shared/originguard"
)

const chainOrigin = "https://play.example.com"

// originServer mounts the guards the way a gateway/BFF will: the origin check in
// front of the cookie check, both in front of the handler.
func originServer(t *testing.T) (*httptest.Server, *stubValidator, *atomic.Int32) {
	t.Helper()
	guard, err := originguard.New(chainOrigin)
	if err != nil {
		t.Fatal(err)
	}
	validator := &stubValidator{}
	var handlerRuns atomic.Int32

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(ginhttp.Trace())
	router.GET("/ws/v2/rooms/:id",
		ginhttp.Wrap(guard.WebSocket()),
		ginhttp.Wrap(identity.Authenticate(validator, identity.Options{})),
		func(c *gin.Context) {
			handlerRuns.Add(1)
			httpresponse.WriteData(c.Writer, 200, map[string]any{"socket": "would upgrade here"})
		})
	api := router.Group("/api/v1", ginhttp.Wrap(guard.CSRF()), ginhttp.Wrap(identity.Authenticate(validator, identity.Options{})))
	api.POST("/rooms", func(c *gin.Context) {
		handlerRuns.Add(1)
		httpresponse.WriteData(c.Writer, 201, map[string]any{"created": true})
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv, validator, &handlerRuns
}

func request(t *testing.T, method, url string, headers map[string]string, cookie string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "ft_session", Value: cookie})
	}
	resp, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 2048)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp, sb.String()
}

func upgrade(origin string) map[string]string {
	h := map[string]string{"Upgrade": "websocket", "Connection": "Upgrade", "Sec-WebSocket-Version": "13", "Sec-WebSocket-Key": "dGhlIHNhbXBsZSBub25jZQ=="}
	if origin != "" {
		h["Origin"] = origin
	}
	return h
}

func TestForeignWebSocketOriginIsRefusedBeforeTheCookieIsLookedAt(t *testing.T) {
	srv, validator, runs := originServer(t)

	// A foreign page with a victim's perfectly valid session cookie: the classic
	// cross-site WebSocket hijacking. The handshake dies before auth is consulted.
	resp, body := request(t, "GET", srv.URL+"/ws/v2/rooms/7", upgrade("https://evil.example"), acSession)
	if resp.StatusCode != 403 || body != "" {
		t.Fatalf("got %d %q, want a bare 403 (the contract: handshake rejected, no code)", resp.StatusCode, body)
	}
	resp, _ = request(t, "GET", srv.URL+"/ws/v2/rooms/7", upgrade(""), acSession)
	if resp.StatusCode != 403 {
		t.Errorf("a handshake without Origin: %d, want 403", resp.StatusCode)
	}
	if validator.calls.Load() != 0 || runs.Load() != 0 {
		t.Fatalf("validator asked %d times and handler ran %d times; a foreign origin must be refused before any cookie check", validator.calls.Load(), runs.Load())
	}

	// Our own origin proceeds to the cookie check, which then decides.
	resp, body = request(t, "GET", srv.URL+"/ws/v2/rooms/7", upgrade(chainOrigin), "")
	if resp.StatusCode != 401 || !strings.Contains(body, "UNAUTHORIZED") {
		t.Errorf("own origin, no cookie: %d %s", resp.StatusCode, body)
	}
	resp, _ = request(t, "GET", srv.URL+"/ws/v2/rooms/7", upgrade(chainOrigin), acSession)
	if resp.StatusCode != 200 || runs.Load() != 1 {
		t.Errorf("own origin with a valid session: %d runs=%d", resp.StatusCode, runs.Load())
	}
}

func TestCrossSiteStateChangingRequestsAreRefusedBeforeAuthentication(t *testing.T) {
	srv, validator, runs := originServer(t)

	// A foreign page's form post or fetch, carrying the victim's cookie.
	for name, headers := range map[string]map[string]string{
		"foreign Origin":    {"Origin": "https://evil.example"},
		"opaque Origin":     {"Origin": "null"},
		"cross-site fetch":  {"Sec-Fetch-Site": "cross-site"},
		"sibling subdomain": {"Sec-Fetch-Site": "same-site"},
	} {
		resp, body := request(t, "POST", srv.URL+"/api/v1/rooms", headers, acSession)
		if resp.StatusCode != 403 || !strings.Contains(body, `"code":"FORBIDDEN_ORIGIN"`) {
			t.Errorf("%s: %d %s", name, resp.StatusCode, body)
		}
		if v := httpresponse.Violations(resp.StatusCode, []byte(body)); len(v) != 0 {
			t.Errorf("%s: %v", name, v)
		}
		for h := range resp.Header {
			if strings.HasPrefix(strings.ToLower(h), "access-control-") {
				t.Errorf("%s: the response carries %s; no CORS header may be sent", name, h)
			}
		}
	}
	if validator.calls.Load() != 0 || runs.Load() != 0 {
		t.Fatalf("validator asked %d times, handler ran %d times for cross-site requests", validator.calls.Load(), runs.Load())
	}

	// The SPA on our own origin, and a script that is not a browser, both work.
	for name, headers := range map[string]map[string]string{
		"SPA same-origin": {"Origin": chainOrigin, "Sec-Fetch-Site": "same-origin"},
		"non-browser":     nil,
	} {
		if resp, body := request(t, "POST", srv.URL+"/api/v1/rooms", headers, acSession); resp.StatusCode != 201 {
			t.Errorf("%s: %d %s", name, resp.StatusCode, body)
		}
	}
}

func TestACrossOriginPreflightGetsNoPermission(t *testing.T) {
	srv, _, _ := originServer(t)
	resp, _ := request(t, "OPTIONS", srv.URL+"/api/v1/rooms", map[string]string{
		"Origin": "https://evil.example", "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "content-type",
	}, "")
	for h := range resp.Header {
		if strings.HasPrefix(strings.ToLower(h), "access-control-") {
			t.Errorf("the preflight answer carries %s; a foreign page must get no CORS permission", h)
		}
	}
	if resp.StatusCode == 200 || resp.StatusCode == 204 {
		t.Errorf("the preflight must not succeed, got %d", resp.StatusCode)
	}
}
