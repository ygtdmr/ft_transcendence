package unit_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/shared/originguard"
)

const publicOrigin = "https://play.example.com"

func newGuard(t *testing.T, origin string) *originguard.Guard {
	t.Helper()
	g, err := originguard.New(origin)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestOriginGuardParsesOnlyRealOrigins(t *testing.T) {
	for _, ok := range []string{"https://play.example.com", "https://play.example.com:8443", "http://localhost:5173", "HTTPS://PLAY.EXAMPLE.COM", "https://[::1]:8443"} {
		if _, err := originguard.New(ok); err != nil {
			t.Errorf("New(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{
		"", "null", "play.example.com", "ftp://play.example.com", "https://", "https://play.example.com/",
		"https://play.example.com/path", "https://play.example.com?x=1", "https://play.example.com#f",
		"https://user:pw@play.example.com", "https://play.example.com:notaport",
	} {
		if _, err := originguard.New(bad); err == nil {
			t.Errorf("New(%q) accepted a value that is not an origin", bad)
		}
	}
}

func runGuard(mw func(http.Handler) http.Handler, method string, headers map[string][]string) (*httptest.ResponseRecorder, int32) {
	var reached atomic.Int32
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1); w.WriteHeader(204) }))
	req := httptest.NewRequest(method, "/api/v1/rooms", nil)
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, reached.Load()
}

func TestCSRFAllowsTheDeploymentsOwnOriginForStateChangingRequests(t *testing.T) {
	g := newGuard(t, publicOrigin)
	for _, origin := range []string{publicOrigin, "https://play.example.com:443", "HTTPS://Play.Example.Com"} {
		for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
			if rec, reached := runGuard(g.CSRF(), method, map[string][]string{"Origin": {origin}}); rec.Code != 204 || reached != 1 {
				t.Errorf("%s from %q: %d reached=%d", method, origin, rec.Code, reached)
			}
		}
	}
}

func TestCSRFRejectsForeignOriginsBeforeTheHandlerRuns(t *testing.T) {
	g := newGuard(t, publicOrigin)
	for name, origin := range map[string]string{
		"another site":         "https://evil.example",
		"downgraded scheme":    "http://play.example.com",
		"different port":       "https://play.example.com:8443",
		"suffix trick":         "https://play.example.com.evil.example",
		"prefix trick":         "https://evilplay.example.com",
		"subdomain":            "https://sub.play.example.com",
		"opaque origin":        "null",
		"empty":                "",
		"origin with a path":   "https://play.example.com/",
		"origin with userinfo": "https://play.example.com@evil.example",
	} {
		rec, reached := runGuard(g.CSRF(), "POST", map[string][]string{"Origin": {origin}})
		if rec.Code != 403 || reached != 0 {
			t.Errorf("%s (%q): %d reached=%d, want 403 and no handler", name, origin, rec.Code, reached)
			continue
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"code":"FORBIDDEN_ORIGIN"`) || !strings.Contains(body, `"details":{}`) || strings.Contains(body, "evil") {
			t.Errorf("%s: body = %s", name, body)
		}
		if v := httpresponse.Violations(rec.Code, rec.Body.Bytes()); len(v) != 0 {
			t.Errorf("%s: %v", name, v)
		}
	}
}

func TestCSRFFallsBackToFetchMetadataThenToNonBrowserClients(t *testing.T) {
	g := newGuard(t, publicOrigin)
	cases := []struct {
		name    string
		headers map[string][]string
		allowed bool
	}{
		{"cross-site fetch", map[string][]string{"Sec-Fetch-Site": {"cross-site"}}, false},
		{"same-site (sibling subdomain) fetch", map[string][]string{"Sec-Fetch-Site": {"same-site"}}, false},
		{"same-origin fetch", map[string][]string{"Sec-Fetch-Site": {"same-origin"}}, true},
		{"user-initiated navigation", map[string][]string{"Sec-Fetch-Site": {"none"}}, true},
		{"non-browser client (no headers)", nil, true},
		{"two Origin headers, both ours", map[string][]string{"Origin": {publicOrigin, publicOrigin}}, false},
		{"our Origin plus a foreign one", map[string][]string{"Origin": {publicOrigin, "https://evil.example"}}, false},
		{"our Origin wins over lying metadata", map[string][]string{"Origin": {publicOrigin}, "Sec-Fetch-Site": {"cross-site"}}, true},
		{"foreign Origin is not saved by same-origin metadata", map[string][]string{"Origin": {"https://evil.example"}, "Sec-Fetch-Site": {"same-origin"}}, false},
	}
	for _, tc := range cases {
		rec, reached := runGuard(g.CSRF(), "DELETE", tc.headers)
		if tc.allowed != (rec.Code == 204 && reached == 1) {
			t.Errorf("%s: %d reached=%d, allowed should be %v", tc.name, rec.Code, reached, tc.allowed)
		}
	}
}

func TestCSRFLeavesSafeMethodsAlone(t *testing.T) {
	g := newGuard(t, publicOrigin)
	for _, method := range []string{"GET", "HEAD", "OPTIONS"} {
		rec, reached := runGuard(g.CSRF(), method, map[string][]string{"Origin": {"https://evil.example"}, "Sec-Fetch-Site": {"cross-site"}})
		if rec.Code != 204 || reached != 1 {
			t.Errorf("%s: %d reached=%d; safe methods must not be blocked by the CSRF check", method, rec.Code, reached)
		}
	}
}

func TestTheGuardNeverEmitsCORSHeaders(t *testing.T) {
	g := newGuard(t, publicOrigin)
	for _, headers := range []map[string][]string{
		{"Origin": {publicOrigin}},
		{"Origin": {"https://evil.example"}, "Access-Control-Request-Method": {"POST"}},
	} {
		for _, method := range []string{"POST", "OPTIONS"} {
			rec, _ := runGuard(g.CSRF(), method, headers)
			for name := range rec.Header() {
				if strings.HasPrefix(strings.ToLower(name), "access-control-") {
					t.Errorf("%s emitted %s; with a single public origin no CORS header may ever be sent", method, name)
				}
			}
		}
	}
}

func upgradeHeaders(origin ...string) map[string][]string {
	h := map[string][]string{"Upgrade": {"websocket"}, "Connection": {"Upgrade"}, "Sec-Websocket-Version": {"13"}}
	if len(origin) > 0 {
		h["Origin"] = origin
	}
	return h
}

func TestWebSocketHandshakeNeedsExactlyTheDeploymentsOrigin(t *testing.T) {
	g := newGuard(t, publicOrigin)
	if rec, reached := runGuard(g.WebSocket(), "GET", upgradeHeaders(publicOrigin)); rec.Code != 204 || reached != 1 {
		t.Fatalf("our own origin: %d reached=%d", rec.Code, reached)
	}
	for name, origins := range map[string][]string{
		"no Origin":    nil,
		"foreign":      {"https://evil.example"},
		"opaque":       {"null"},
		"wrong scheme": {"http://play.example.com"},
		"two headers":  {publicOrigin, publicOrigin},
		"empty":        {""},
	} {
		rec, reached := runGuard(g.WebSocket(), "GET", upgradeHeaders(origins...))
		if rec.Code != 403 || reached != 0 {
			t.Errorf("%s: %d reached=%d, want 403 and no handler", name, rec.Code, reached)
			continue
		}
		if rec.Body.Len() != 0 {
			t.Errorf("%s: the handshake rejection must have no body/code (contract), got %q", name, rec.Body.String())
		}
	}
}

func TestWebSocketGuardRecognisesUpgradeRequestsCaseInsensitively(t *testing.T) {
	g := newGuard(t, publicOrigin)
	for name, headers := range map[string]map[string][]string{
		"mixed case":        {"Upgrade": {"WebSocket"}, "Connection": {"upgrade"}, "Origin": {"https://evil.example"}},
		"connection list":   {"Upgrade": {"websocket"}, "Connection": {"keep-alive, Upgrade"}, "Origin": {"https://evil.example"}},
		"second Connection": {"Upgrade": {"websocket"}, "Connection": {"keep-alive", "Upgrade"}, "Origin": {"https://evil.example"}},
	} {
		if rec, reached := runGuard(g.WebSocket(), "GET", headers); rec.Code != 403 || reached != 0 {
			t.Errorf("%s: %d reached=%d, a foreign upgrade slipped through", name, rec.Code, reached)
		}
	}
	// A plain request to a socket route is not this guard's concern.
	if rec, reached := runGuard(g.WebSocket(), "GET", map[string][]string{"Origin": {"https://evil.example"}}); rec.Code != 204 || reached != 1 {
		t.Errorf("a non-upgrade request: %d reached=%d", rec.Code, reached)
	}
	if rec, reached := runGuard(g.WebSocket(), "GET", map[string][]string{"Upgrade": {"h2c"}, "Connection": {"Upgrade"}, "Origin": {"https://evil.example"}}); rec.Code != 204 || reached != 1 {
		t.Errorf("a non-WebSocket upgrade: %d reached=%d", rec.Code, reached)
	}
}
