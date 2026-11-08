package integration_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/melmut-42/ft_transcendence/backend/shared/ginhttp"
	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/shared/identity"
	"github.com/melmut-42/ft_transcendence/backend/shared/logging"
	"github.com/melmut-42/ft_transcendence/backend/shared/requestid"
	"github.com/melmut-42/ft_transcendence/backend/shared/sessionclient"
	"github.com/melmut-42/ft_transcendence/backend/shared/tracecontext"
)

func TestSessionClientMapsEveryVerdictTheContractDefines(t *testing.T) {
	rec := newReceiver(t)
	validator := sessionclient.New(clientFor(t, rec.srv.URL, pcGatewayCredential, 3*time.Second))

	id, err := validator.Validate(context.Background(), "good")
	if err != nil || id.UserID != 42 || id.SessionID != "8f14e45f-ceea-4e6b-9c3a-2d1b5f7a9e01" ||
		!id.ExpiresAt.Equal(time.Date(2026, 11, 5, 9, 45, 0, 0, time.UTC)) {
		t.Fatalf("valid session: %+v, %v", id, err)
	}

	for _, tc := range []struct {
		credential string
		want       error
	}{
		{"revoked", identity.ErrInvalidSession},
		{"unknown-value", identity.ErrInvalidSession},
		{"db-down", identity.ErrUnavailable},
		{"boom", identity.ErrUnavailable},
		{"malformed", identity.ErrUnavailable},
		{"zero-user", identity.ErrUnavailable},
		{"bad-expiry", identity.ErrUnavailable},
	} {
		if _, err := validator.Validate(context.Background(), tc.credential); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.credential, err, tc.want)
		}
	}

	// A refusal the contract does not define is neither a logout nor an outage.
	_, err = validator.Validate(context.Background(), "contract-mismatch")
	if err == nil || errors.Is(err, identity.ErrInvalidSession) || errors.Is(err, identity.ErrUnavailable) || errors.Is(err, identity.ErrServiceRejected) {
		t.Errorf("contract-mismatch: err = %v; it must stay an unclassified failure so it is logged", err)
	}
}

func TestSessionClientDistinguishesAServiceFaultFromALogout(t *testing.T) {
	rec := newReceiver(t)
	for name, credential := range map[string]string{
		"unknown service credential": "not-the-credential-0123456789abcdef-0123456",
		"known caller, wrong scope":  pcUserCredential, // user-service may not call session.validate
	} {
		validator := sessionclient.New(clientFor(t, rec.srv.URL, credential, 3*time.Second))
		_, err := validator.Validate(context.Background(), "good")
		if !errors.Is(err, identity.ErrServiceRejected) || errors.Is(err, identity.ErrInvalidSession) {
			t.Errorf("%s: err = %v, want ErrServiceRejected and never a logout", name, err)
		}
	}
}

func TestSessionClientNeverSendsAnImpossibleCredential(t *testing.T) {
	rec := newReceiver(t)
	validator := sessionclient.New(clientFor(t, rec.srv.URL, pcGatewayCredential, 3*time.Second))
	for _, credential := range []string{"", "a;b", "a b", "a\r\nX-Evil: 1", `a"b`} {
		if _, err := validator.Validate(context.Background(), credential); !errors.Is(err, identity.ErrInvalidSession) {
			t.Errorf("credential %q: err = %v, want ErrInvalidSession", credential, err)
		}
	}
	if n := len(rec.requests()); n != 0 {
		t.Errorf("%d requests reached the receiver for values that cannot be a session", n)
	}
}

func TestSessionClientReportsAnUnreachableAuthAsUnavailable(t *testing.T) {
	rec := newReceiver(t)
	validator := sessionclient.New(clientFor(t, rec.srv.URL, pcGatewayCredential, time.Second))
	rec.srv.Close()
	if _, err := validator.Validate(context.Background(), "good"); !errors.Is(err, identity.ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable (an outage is not a logout)", err)
	}
}

// gatewayChain builds a gateway the way #89/#90/#88 will: the shared guard in
// front of a handler, validating through the private session client.
func gatewayChain(t *testing.T, authURL, serviceCredential string) (*httptest.Server, *lockedBuffer) {
	t.Helper()
	logs := newLockedBuffer()
	logger, err := logging.New(logging.Config{Format: "json", Level: "debug", Service: "gateway-itest"}, logs)
	if err != nil {
		t.Fatal(err)
	}
	validator := sessionclient.New(clientFor(t, authURL, serviceCredential, 2*time.Second))

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(ginhttp.Trace(), ginhttp.AccessLog(logger))
	router.GET("/api/v1/me", ginhttp.Wrap(identity.Authenticate(validator, identity.Options{Logger: logger})), func(c *gin.Context) {
		id, _ := identity.FromContext(c.Request.Context())
		httpresponse.WriteData(c.Writer, 200, map[string]any{"user_id": id.UserID})
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv, logs
}

func TestBrowserToGatewayToAuthChain(t *testing.T) {
	rec := newReceiver(t)
	gateway, logs := gatewayChain(t, rec.srv.URL, pcGatewayCredential)

	// Everything a real browser sends: both cookies, tracing, a spoofed identity.
	resp, body := getWith(t, gateway.URL+"/api/v1/me", map[string]string{
		"X-Request-ID":        "browser-req-9",
		"Traceparent":         "00-" + pcTraceID + "-" + pcParentSpan + "-01",
		"X-User-ID":           "1",
		"X-Forwarded-For":     "198.51.100.7",
		"X-Internal-Override": "yes",
	}, &http.Cookie{Name: "ft_session", Value: "good"}, &http.Cookie{Name: "ft_refresh", Value: "refresh-secret-must-stay-home"})
	if resp.StatusCode != 200 || !strings.Contains(body, `"user_id":42`) {
		t.Fatalf("got %d %s", resp.StatusCode, body)
	}

	call := rec.requests()[0]
	if call.Cookie != "ft_session=good" {
		t.Errorf("Auth received Cookie %q; only ft_session may be forwarded, never ft_refresh", call.Cookie)
	}
	if call.Authorization != "Bearer "+pcGatewayCredential {
		t.Errorf("the gateway presented %q", call.Authorization)
	}
	if call.RequestID != "browser-req-9" {
		t.Errorf("Auth saw request id %q, want the browser's, one request one id across services", call.RequestID)
	}
	if sent, ok := tracecontext.Parse(call.Traceparent); !ok || sent.TraceID != pcTraceID {
		t.Errorf("Auth saw traceparent %q, want the same trace", call.Traceparent)
	}
	for _, h := range []string{"X-User-Id", "X-Forwarded-For", "X-Internal-Override"} {
		if call.Header.Get(h) != "" {
			t.Errorf("the browser's %s header was forwarded to the private listener", h)
		}
	}
	if resp.Header.Get(requestid.Header) != "browser-req-9" {
		t.Errorf("response request id = %q", resp.Header.Get(requestid.Header))
	}
	if out := logs.String(); strings.Contains(out, "refresh-secret-must-stay-home") || strings.Contains(out, pcGatewayCredential) || strings.Contains(out, "ft_session=good") {
		t.Errorf("a credential reached the gateway logs:\n%s", out)
	}
}

func TestGatewayVerdictsFollowTheContractOverTheWholeChain(t *testing.T) {
	rec := newReceiver(t)
	gateway, _ := gatewayChain(t, rec.srv.URL, pcGatewayCredential)

	for _, tc := range []struct {
		cookie string
		status int
		code   string
	}{
		{"revoked", 401, "UNAUTHORIZED"},
		{"db-down", 503, "SERVICE_UNAVAILABLE"}, // Auth's database is down: not a logout
		{"boom", 503, "SERVICE_UNAVAILABLE"},
		{"malformed", 503, "SERVICE_UNAVAILABLE"},
	} {
		resp, body := getWith(t, gateway.URL+"/api/v1/me", nil, &http.Cookie{Name: "ft_session", Value: tc.cookie})
		if resp.StatusCode != tc.status || !strings.Contains(body, `"code":"`+tc.code+`"`) {
			t.Errorf("%s: %d %s, want %d %s", tc.cookie, resp.StatusCode, body, tc.status, tc.code)
		}
		if len(resp.Header["Set-Cookie"]) != 0 {
			t.Errorf("%s: the browser's cookie must be left alone", tc.cookie)
		}
		if v := httpresponse.Violations(resp.StatusCode, []byte(body)); len(v) != 0 {
			t.Errorf("%s: %v", tc.cookie, v)
		}
	}
}

func TestAMisconfiguredGatewayFailsClosedAndSaysSoInTheLogsOnly(t *testing.T) {
	rec := newReceiver(t)
	gateway, logs := gatewayChain(t, rec.srv.URL, "wrong-gateway-credential-0123456789abcdef-0123")

	resp, body := getWith(t, gateway.URL+"/api/v1/me", nil, &http.Cookie{Name: "ft_session", Value: "good"})
	if resp.StatusCode != 500 || !strings.Contains(body, `"code":"INTERNAL_ERROR"`) || strings.Contains(body, "credential") {
		t.Errorf("got %d %s; a service-credential fault must be a generic 500, never a logout", resp.StatusCode, body)
	}
	out := logs.String()
	if !strings.Contains(out, "session_verifier_rejected_service_credential") {
		t.Errorf("the fault must be logged for operators:\n%s", out)
	}
	for _, leak := range []string{"wrong-gateway-credential", pcGatewayCredential, "ft_session=good"} {
		if strings.Contains(out, leak) {
			t.Errorf("%q reached the logs", leak)
		}
	}
}

func TestGatewayAnswers503WhenAuthIsDown(t *testing.T) {
	rec := newReceiver(t)
	gateway, _ := gatewayChain(t, rec.srv.URL, pcGatewayCredential)
	rec.srv.Close()
	resp, body := getWith(t, gateway.URL+"/api/v1/me", nil, &http.Cookie{Name: "ft_session", Value: "good"})
	if resp.StatusCode != 503 || !strings.Contains(body, "SERVICE_UNAVAILABLE") {
		t.Errorf("got %d %s", resp.StatusCode, body)
	}
}
