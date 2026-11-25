package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/melmut-42/ft_transcendence/backend/notification-gateway/internal/app"
	"github.com/melmut-42/ft_transcendence/backend/notification-gateway/internal/config"
)

const (
	invalidEvent = `{"type":"error","payload":{"code":"INVALID_EVENT"}}`
	unauthorized = `{"type":"error","payload":{"code":"UNAUTHORIZED"}}`
)

func handshakeError(t *testing.T, resp *http.Response) (int, string) {
	t.Helper()
	if resp == nil {
		t.Fatal("no handshake response")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)

	return resp.StatusCode, errorCode(body)
}

// --- Admission ---------------------------------------------------------------

func TestHandshakeRefusesAForeignOriginBeforeLookingAtTheSession(t *testing.T) {
	h := newHarness(t, nil)
	for name, origin := range map[string]string{"foreign": "https://evil.example", "null": "null",
		"other port": publicOrigin + ":8443", "missing": ""} {
		_, resp, err := dial(t, h.public.URL, "valid-a", origin, nil)
		if err == nil {
			t.Fatalf("%s origin: upgraded", name)
		}
		if status, code := handshakeError(t, resp); status != http.StatusForbidden || code != "ORIGIN_NOT_ALLOWED" {
			t.Errorf("%s origin: %d %q, want 403 ORIGIN_NOT_ALLOWED", name, status, code)
		}
	}
	if n := h.auth.calls.Load(); n != 0 {
		t.Fatalf("Auth was asked %d times for foreign pages", n)
	}
}

func TestHandshakeAcceptsTheFtSessionCookieOnly(t *testing.T) {
	h := newHarness(t, nil)
	cases := map[string]struct {
		cookie string
		path   string
		header http.Header
	}{
		"no credential":  {},
		"query token":    {path: "?access_token=valid-a&ft_session=valid-a"},
		"bearer header":  {header: http.Header{"Authorization": {"Bearer valid-a"}}},
		"refresh cookie": {header: http.Header{"Cookie": {"ft_refresh=valid-a"}}},
	}
	for name, tc := range cases {
		header := http.Header{"Origin": {publicOrigin}}
		for k, v := range tc.header {
			header[k] = v
		}
		_, resp, err := websocket.DefaultDialer.Dial(wsURL(h.public.URL)+tc.path, header)
		if err == nil {
			t.Fatalf("%s: upgraded", name)
		}
		if status, code := handshakeError(t, resp); status != http.StatusUnauthorized || code != "UNAUTHORIZED" {
			t.Errorf("%s: %d %q, want 401 UNAUTHORIZED", name, status, code)
		}
	}
	if n := h.auth.calls.Load(); n != 0 {
		t.Fatalf("Auth was asked %d times without an ft_session cookie", n)
	}
}

func TestHandshakeMapsAuthVerdictsWithoutTurningOutagesIntoLogouts(t *testing.T) {
	h := newHarness(t, nil)

	_, resp, _ := dial(t, h.public.URL, "expired", publicOrigin, nil)
	if status, code := handshakeError(t, resp); status != http.StatusUnauthorized || code != "UNAUTHORIZED" {
		t.Errorf("invalid session: %d %q, want 401 UNAUTHORIZED", status, code)
	}

	h.auth.setMode("outage")
	_, resp, _ = dial(t, h.public.URL, "valid-a", publicOrigin, nil)
	if status, code := handshakeError(t, resp); status != http.StatusServiceUnavailable || code != "SERVICE_UNAVAILABLE" {
		t.Errorf("Auth 503: %d %q, want 503 SERVICE_UNAVAILABLE", status, code)
	}

	h.auth.setMode("reject-credential")
	_, resp, _ = dial(t, h.public.URL, "valid-a", publicOrigin, nil)
	if status, code := handshakeError(t, resp); status != http.StatusInternalServerError || code != "INTERNAL_ERROR" {
		t.Errorf("gateway credential refused: %d %q, want 500 INTERNAL_ERROR (never a logout)", status, code)
	}

	h.auth.server.Close()
	_, resp, _ = dial(t, h.public.URL, "valid-a", publicOrigin, nil)
	if status, code := handshakeError(t, resp); status != http.StatusServiceUnavailable || code != "SERVICE_UNAVAILABLE" {
		t.Errorf("Auth unreachable: %d %q, want 503 SERVICE_UNAVAILABLE", status, code)
	}
}

func TestHandshakeForwardsOnlyFtSessionWithTheGatewayCredential(t *testing.T) {
	h := newHarness(t, nil)
	conn, resp, err := dial(t, h.public.URL, "", publicOrigin,
		http.Header{"Cookie": {"ft_session=valid-a; ft_refresh=refresh-secret; theme=dark"}})
	if err != nil {
		t.Fatalf("dial: %v (%v)", err, resp)
	}
	readFrame(t, conn)
	cookies, bearer := h.auth.seen()
	if strings.Join(cookies, ";") != "ft_session=valid-a" {
		t.Errorf("Auth received cookies %q, want only ft_session", cookies)
	}
	if bearer != "Bearer "+gatewayToAuth {
		t.Error("Auth did not receive the gateway's own service credential")
	}
}

func TestNonUpgradeRequestsToTheSocketRouteAreRefused(t *testing.T) {
	h := newHarness(t, nil)
	req, _ := http.NewRequest(http.MethodGet, h.public.URL+"/ws/v2/notifications", nil)
	req.Header.Set("Origin", publicOrigin)
	req.AddCookie(&http.Cookie{Name: "ft_session", Value: "valid-a"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if status, code := handshakeError(t, resp); status != http.StatusBadRequest || code != "VALIDATION_ERROR" {
		t.Errorf("plain GET: %d %q, want 400 VALIDATION_ERROR", status, code)
	}
	if n := h.auth.calls.Load(); n != 0 {
		t.Errorf("Auth was asked %d times for a request that is not an upgrade", n)
	}
}

// --- Socket behaviour ----------------------------------------------------------

func TestReadyEventFirstThenClientFramesGetInvalidEventAndTheSocketStaysOpen(t *testing.T) {
	h := newHarness(t, nil)
	conn := mustDial(t, h.public.URL, "valid-a")

	ready := readFrame(t, conn)
	var payload struct {
		UserID      int64  `json:"user_id"`
		ConnectedAt string `json:"connected_at"`
	}
	_ = json.Unmarshal(ready.Payload, &payload)
	if ready.Type != "notifications.ready" || ready.SentAt != "2026-09-10T08:40:00Z" ||
		payload.UserID != 42 || payload.ConnectedAt != "2026-09-10T08:40:00Z" {
		t.Fatalf("ready event %+v %+v", ready, payload)
	}
	for i := 0; i < 3; i++ {
		stillOpen(t, conn)
	}
	if got := connectedIDs(t, h, 42); len(got) != 1 || got[0] != 42 {
		t.Fatalf("connections read %v, want [42]", got)
	}
}

func TestRepeatedClientFramesCloseTheSocketWithPolicyViolation(t *testing.T) {
	h := newHarness(t, nil)
	conn := mustDial(t, h.public.URL, "valid-a")
	readFrame(t, conn)
	for i := 0; i < 10; i++ {
		stillOpen(t, conn)
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte("again")); err != nil {
		t.Fatal(err)
	}
	if got := readRaw(t, conn); got != invalidEvent {
		t.Fatalf("got %s before the close, want INVALID_EVENT", got)
	}
	if code, reason := expectClose(t, conn); code != websocket.ClosePolicyViolation || reason != "POLICY_VIOLATION" {
		t.Fatalf("close %d %q, want 1008 POLICY_VIOLATION", code, reason)
	}
}

func TestAnOversizedClientFrameClosesTheSocket(t *testing.T) {
	h := newHarness(t, nil)
	conn := mustDial(t, h.public.URL, "valid-a")
	readFrame(t, conn)
	if err := conn.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("x", 5<<10))); err != nil {
		t.Fatal(err)
	}
	if code, _ := expectClose(t, conn); code != websocket.CloseMessageTooBig {
		t.Fatalf("close %d, want 1009", code)
	}
	waitFor(t, func() bool { return len(connectedIDs(t, h, 42)) == 0 }, "the closed socket to leave the registry")
}

func TestEventsReachEveryOpenTabOfTheUserOnly(t *testing.T) {
	h := newHarness(t, nil)
	tab1 := mustDial(t, h.public.URL, "valid-a")
	tab2 := mustDial(t, h.public.URL, "valid-a")
	otherSession := mustDial(t, h.public.URL, "valid-b")
	stranger := mustDial(t, h.public.URL, "valid-c")
	for _, c := range []*websocket.Conn{tab1, tab2, otherSession, stranger} {
		readFrame(t, c)
	}

	event := `{"type":"user.profile.updated","event_id":"evt_1","sent_at":"2026-09-10T08:45:00Z","payload":{"user_id":46}}`
	if n := h.gateway.Registry.SendToUser(42, []byte(event)); n != 3 {
		t.Fatalf("queued on %d sockets, want 3", n)
	}
	for name, c := range map[string]*websocket.Conn{"tab1": tab1, "tab2": tab2, "other session": otherSession} {
		if got := readRaw(t, c); got != event {
			t.Errorf("%s got %s", name, got)
		}
	}
	// The stranger's next frame is the answer to its own probe, not the event.
	stillOpen(t, stranger)
}

// --- Revocation ----------------------------------------------------------------

func TestRevocationClosesEveryTabOfThatSessionOnly(t *testing.T) {
	h := newHarness(t, nil)
	tab1 := mustDial(t, h.public.URL, "valid-a")
	tab2 := mustDial(t, h.public.URL, "valid-a")
	otherSession := mustDial(t, h.public.URL, "valid-b")
	stranger := mustDial(t, h.public.URL, "valid-c")
	for _, c := range []*websocket.Conn{tab1, tab2, otherSession, stranger} {
		readFrame(t, c)
	}

	url := h.internal.URL + "/internal/v1/session-revocations/" + sessionA
	if status, _, body := call(t, http.MethodPut, url, authToGateway, `{"user_id":42,"cause":"SESSION_REVOKED"}`); status != http.StatusNoContent {
		t.Fatalf("revocation: %d %v", status, body)
	}
	for name, c := range map[string]*websocket.Conn{"tab1": tab1, "tab2": tab2} {
		if got := readRaw(t, c); got != unauthorized {
			t.Errorf("%s got %s before the close, want UNAUTHORIZED", name, got)
		}
		if code, reason := expectClose(t, c); code != 4401 || reason != "SESSION_REVOKED" {
			t.Errorf("%s close %d %q, want 4401 SESSION_REVOKED", name, code, reason)
		}
	}
	stillOpen(t, otherSession)
	stillOpen(t, stranger)
	if got := connectedIDs(t, h, 43, 42); fmt.Sprint(got) != "[42 43]" {
		t.Errorf("connections read %v, want [42 43] (user 42 still has another session)", got)
	}

	// A duplicate notification is harmless.
	if status, _, _ := call(t, http.MethodPut, url, authToGateway, `{"user_id":42,"cause":"SESSION_REVOKED"}`); status != http.StatusNoContent {
		t.Fatalf("duplicate revocation: %d", status)
	}
	// A handshake Auth validated before it recorded the revocation is not
	// registered afterwards.
	late := mustDial(t, h.public.URL, "valid-a")
	if got := readRaw(t, late); got != unauthorized {
		t.Fatalf("late handshake got %s, want UNAUTHORIZED", got)
	}
	if code, _ := expectClose(t, late); code != 4401 {
		t.Fatalf("late handshake close %d, want 4401", code)
	}
}

func TestAccountDeletionRevocationCarriesItsCause(t *testing.T) {
	h := newHarness(t, nil)
	conn := mustDial(t, h.public.URL, "valid-c")
	readFrame(t, conn)
	url := h.internal.URL + "/internal/v1/session-revocations/" + strings.ToUpper(sessionC)
	if status, _, body := call(t, http.MethodPut, url, authToGateway, `{"user_id":43,"cause":"ACCOUNT_DELETED"}`); status != http.StatusNoContent {
		t.Fatalf("revocation: %d %v", status, body)
	}
	readRaw(t, conn)
	if code, reason := expectClose(t, conn); code != 4401 || reason != "ACCOUNT_DELETED" {
		t.Fatalf("close %d %q, want 4401 ACCOUNT_DELETED", code, reason)
	}
}

func TestRevocationRequestValidation(t *testing.T) {
	h := newHarness(t, nil)
	base := h.internal.URL + "/internal/v1/session-revocations/"
	valid := `{"user_id":42,"cause":"SESSION_REVOKED"}`
	cases := []struct {
		name       string
		path       string
		credential string
		body       string
		status     int
		code       string
		field      string
	}{
		{"no credential", sessionA, "", valid, 401, "SERVICE_UNAUTHENTICATED", ""},
		{"user-service lacks the scope", sessionA, userToGateway, valid, 403, "SERVICE_FORBIDDEN", ""},
		{"session id not a UUID", "not-a-uuid", authToGateway, valid, 422, "VALIDATION_ERROR", "session_id"},
		{"empty body", sessionA, authToGateway, "", 400, "VALIDATION_ERROR", ""},
		{"unknown field", sessionA, authToGateway, `{"user_id":42,"cause":"SESSION_REVOKED","reason":"x"}`, 400, "VALIDATION_ERROR", ""},
		{"malformed", sessionA, authToGateway, `{"user_id":`, 400, "VALIDATION_ERROR", ""},
		{"user id not positive", sessionA, authToGateway, `{"user_id":0,"cause":"SESSION_REVOKED"}`, 422, "VALIDATION_ERROR", "user_id"},
		{"unknown cause", sessionA, authToGateway, `{"user_id":42,"cause":"LOGOUT"}`, 422, "VALIDATION_ERROR", "cause"},
	}
	for _, tc := range cases {
		status, header, body := call(t, http.MethodPut, base+tc.path, tc.credential, tc.body)
		if status != tc.status || errorCode(body) != tc.code || (tc.field != "" && errorField(body) != tc.field) {
			t.Errorf("%s: %d %s field=%q, want %d %s field=%q", tc.name, status, errorCode(body), errorField(body),
				tc.status, tc.code, tc.field)
		}
		if tc.status == 401 && header.Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("%s: missing WWW-Authenticate: Bearer", tc.name)
		}
	}
}

// --- Connections read ------------------------------------------------------------

func TestConnectionsReadAnswersFromThisInstanceOnly(t *testing.T) {
	h := newHarness(t, nil)
	if got := connectedIDs(t, h, 42, 43); len(got) != 0 {
		t.Fatalf("empty registry reported %v", got)
	}
	a := mustDial(t, h.public.URL, "valid-c")
	b := mustDial(t, h.public.URL, "valid-a")
	readFrame(t, a)
	readFrame(t, b)
	if got := connectedIDs(t, h, 99, 43, 42, 43); fmt.Sprint(got) != "[42 43]" {
		t.Fatalf("connections read %v, want ascending [42 43]", got)
	}

	// The raw answer is an array even when nobody is connected.
	status, _, body := call(t, http.MethodGet, h.internal.URL+"/internal/v1/connections?user_id=7", userToGateway, "")
	data, _ := body["data"].(map[string]any)
	if ids, ok := data["connected_user_ids"].([]any); status != 200 || !ok || len(ids) != 0 {
		t.Fatalf("no match: %d %v, want 200 with []", status, body)
	}

	// Closing the last socket of a user removes them; restart is covered below.
	_ = a.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	waitFor(t, func() bool { return fmt.Sprint(connectedIDs(t, h, 42, 43)) == "[42]" }, "user 43 to go offline")
}

func TestConnectionsReadValidation(t *testing.T) {
	h := newHarness(t, nil)
	base := h.internal.URL + "/internal/v1/connections"
	many := make([]string, 0, 101)
	for i := 1; i <= 101; i++ {
		many = append(many, "user_id="+strconv.Itoa(i))
	}
	hundredWithRepeats := append(append([]string{}, many[:100]...), "user_id=1", "user_id=2")
	cases := []struct {
		name       string
		query      string
		credential string
		status     int
		code       string
	}{
		{"no credential", "?user_id=1", "", 401, "SERVICE_UNAUTHENTICATED"},
		{"unknown credential", "?user_id=1", strings.Repeat("z", 40), 401, "SERVICE_UNAUTHENTICATED"},
		{"auth-service lacks the scope", "?user_id=1", authToGateway, 403, "SERVICE_FORBIDDEN"},
		{"missing user_id", "", userToGateway, 422, "VALIDATION_ERROR"},
		{"zero", "?user_id=0", userToGateway, 422, "VALIDATION_ERROR"},
		{"negative", "?user_id=-4", userToGateway, 422, "VALIDATION_ERROR"},
		{"not a number", "?user_id=abc", userToGateway, 422, "VALIDATION_ERROR"},
		{"101 distinct ids", "?" + strings.Join(many, "&"), userToGateway, 422, "VALIDATION_ERROR"},
		{"unknown parameter", "?user_id=1&cursor=2", userToGateway, 422, "VALIDATION_ERROR"},
		{"100 distinct ids with repeats", "?" + strings.Join(hundredWithRepeats, "&"), userToGateway, 200, ""},
	}
	for _, tc := range cases {
		status, _, body := call(t, http.MethodGet, base+tc.query, tc.credential, "")
		if status != tc.status || errorCode(body) != tc.code {
			t.Errorf("%s: %d %s, want %d %s", tc.name, status, errorCode(body), tc.status, tc.code)
		}
		if tc.status == 422 && tc.name != "unknown parameter" && errorField(body) != "user_id" {
			t.Errorf("%s: field %q, want user_id", tc.name, errorField(body))
		}
	}
}

// --- Listeners, shutdown, restart --------------------------------------------------

func TestPublicAndPrivateRoutesLiveOnSeparateListeners(t *testing.T) {
	h := newHarness(t, nil)
	if status, _, body := call(t, http.MethodGet, h.public.URL+"/internal/v1/connections?user_id=1", userToGateway, ""); status != 404 || errorCode(body) != "NOT_FOUND" {
		t.Errorf("public listener served /internal: %d %v", status, body)
	}
	if status, _, _ := call(t, http.MethodGet, h.internal.URL+"/ws/v2/notifications", "", ""); status != 404 {
		t.Errorf("private listener served the socket route: %d", status)
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		if status, _, _ := call(t, http.MethodGet, h.public.URL+path, "", ""); status != 200 {
			t.Errorf("%s: %d", path, status)
		}
	}
}

func TestShutdownClosesEverySocketAndARestartForgetsThem(t *testing.T) {
	auth := newAuthStub(t)
	start := func() (*app.Gateway, string, string, context.CancelFunc, chan error) {
		publicPort, internalPort := freePort(t), freePort(t)
		cfg, err := config.Load(lookup(env(t, auth.server.URL, map[string]string{
			"PORT": strconv.Itoa(publicPort), "INTERNAL_HOST": "127.0.0.1", "INTERNAL_PORT": strconv.Itoa(internalPort),
			"SHUTDOWN_TIMEOUT": "2s",
		})))
		if err != nil {
			t.Fatal(err)
		}
		g, err := app.New(cfg, nil, app.Dependencies{})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- g.Run(ctx) }()
		publicURL := fmt.Sprintf("http://127.0.0.1:%d", publicPort)
		internalURL := fmt.Sprintf("http://127.0.0.1:%d", internalPort)
		waitFor(t, func() bool {
			resp, err := http.Get(publicURL + "/healthz")
			if err == nil {
				resp.Body.Close()
			}
			return err == nil
		}, "the gateway to listen")

		return g, publicURL, internalURL, cancel, done
	}

	g, publicURL, _, cancel, done := start()
	conn := mustDial(t, publicURL, "valid-a")
	readFrame(t, conn)
	cancel()
	if code, reason := expectClose(t, conn); code != websocket.CloseGoingAway || reason != "GOING_AWAY" {
		t.Fatalf("close %d %q, want 1001 GOING_AWAY", code, reason)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after shutdown")
	}
	if !g.Health.Draining() {
		t.Error("readiness was not marked draining")
	}

	// A new instance starts with an empty registry: nobody stays reported.
	_, _, internalURL, cancel2, done2 := start()
	defer func() {
		cancel2()
		<-done2
	}()
	status, _, body := call(t, http.MethodGet, internalURL+"/internal/v1/connections?user_id=42", userToGateway, "")
	data, _ := body["data"].(map[string]any)
	if ids, _ := data["connected_user_ids"].([]any); status != 200 || len(ids) != 0 {
		t.Fatalf("restarted instance reported %v (status %d)", body, status)
	}
}

// --- Configuration -------------------------------------------------------------------

func TestConfigReportsEveryProblemByNameWithoutValues(t *testing.T) {
	values := env(t, "http://auth.internal:8081", nil)
	shortSecret := values["USER_SERVICE_CREDENTIAL_FILE"] + ".short"
	values["USER_SERVICE_CREDENTIAL_FILE"] = shortSecret
	delete(values, "PUBLIC_BASE_URL")
	values["AUTH_INTERNAL_URL"] = "ftp://secret-host.example/path"
	values["INTERNAL_HOST"] = "host:9999"
	values["SOCKET_PING_INTERVAL"] = "30s"
	values["SOCKET_PONG_WAIT"] = "20s"

	_, err := config.Load(lookup(values))
	if err == nil {
		t.Fatal("config.Load accepted an invalid configuration")
	}
	msg := err.Error()
	for _, name := range []string{"PUBLIC_BASE_URL", "AUTH_INTERNAL_URL", "USER_SERVICE_CREDENTIAL_FILE", "INTERNAL_HOST", "SOCKET_PONG_WAIT"} {
		if !strings.Contains(msg, name) {
			t.Errorf("error does not name %s: %s", name, msg)
		}
	}
	for _, leaked := range []string{"secret-host", shortSecret, "host:9999", gatewayToAuth} {
		if strings.Contains(msg, leaked) {
			t.Errorf("error leaks %q: %s", leaked, msg)
		}
	}
}

func TestConfigDefaultsAndMissingCallers(t *testing.T) {
	values := env(t, "http://auth.internal:8081", map[string]string{"AUTH_SERVICE_CREDENTIAL_FILE": ""})
	cfg, err := config.Load(lookup(values))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 8080 || cfg.InternalAddr != ":8081" || cfg.Socket.SendQueue != 64 ||
		cfg.Socket.PingInterval != 25*time.Second || cfg.Socket.PongWait != 60*time.Second {
		t.Errorf("defaults %+v %q %+v", cfg.Port, cfg.InternalAddr, cfg.Socket)
	}
	if got := cfg.MissingCallers(); fmt.Sprint(got) != "[auth-service]" {
		t.Errorf("MissingCallers = %v, want [auth-service]", got)
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
