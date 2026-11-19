package integration_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/config"
	"github.com/melmut-42/ft_transcendence/backend/shared/privateclient"
	"github.com/melmut-42/ft_transcendence/backend/shared/requestid"
	"github.com/melmut-42/ft_transcendence/backend/shared/serviceauth"
	"github.com/melmut-42/ft_transcendence/backend/shared/tracecontext"
)

const (
	pcGatewayCredential = "gateway-to-auth-credential-0123456789abcdef-01"
	pcUserCredential    = "user-to-auth-credential-0123456789abcdef-0123"
	pcTraceID           = "4bf92f3577b34da6a3ce929d0e0e4736"
	pcParentSpan        = "00f067aa0ba902b7"
)

// seen is what the receiver observed for one request.
type seen struct {
	Path, Authorization, Cookie, RequestID, Traceparent, Body string
	Header                                                    http.Header
}

// receiver is a real private listener: serviceauth in front of contract-shaped
// handlers whose behaviour is chosen by the request.
type receiver struct {
	srv *httptest.Server
	mu  sync.Mutex
	got []seen
}

func (r *receiver) requests() []seen {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]seen(nil), r.got...)
}

func envelope(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func apiError(w http.ResponseWriter, status int, code string) {
	envelope(w, status, `{"error":{"code":"`+code+`","message":"x","details":{}}}`)
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()
	rec := &receiver{}
	auth := serviceauth.New(
		map[string][]string{"game-gateway": {pcGatewayCredential}, "user-service": {pcUserCredential}},
		serviceauth.Matrix{"game-gateway": {"session.validate"}, "user-service": {"account.metadata.read", "account.deletion.begin"}},
	)

	mux := http.NewServeMux()
	mux.Handle("/internal/v1/sessions/current", auth.Require("session.validate")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("ft_session")
		if err != nil {
			apiError(w, 401, "UNAUTHORIZED")
			return
		}
		switch cookie.Value {
		case "good":
			envelope(w, 200, `{"data":{"user_id":42,"session_id":"8f14e45f-ceea-4e6b-9c3a-2d1b5f7a9e01","session_expires_at":"2026-11-05T09:45:00Z"}}`)
		case "revoked":
			apiError(w, 401, "UNAUTHORIZED")
		case "db-down":
			apiError(w, 503, "SERVICE_UNAVAILABLE")
		case "boom":
			apiError(w, 500, "INTERNAL_ERROR")
		case "contract-mismatch":
			apiError(w, 400, "VALIDATION_ERROR")
		case "malformed":
			envelope(w, 200, `{"data":{"user_id":"forty-two"}}`)
		case "zero-user":
			envelope(w, 200, `{"data":{"user_id":0,"session_id":"s","session_expires_at":"2026-11-05T09:45:00Z"}}`)
		case "bad-expiry":
			envelope(w, 200, `{"data":{"user_id":1,"session_id":"s","session_expires_at":"tomorrow"}}`)
		default:
			apiError(w, 401, "UNAUTHORIZED")
		}
	})))
	mux.Handle("/internal/v1/accounts/", auth.Require("account.metadata.read")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/internal/v1/accounts/") {
		case "42":
			envelope(w, 200, `{"data":{"user_id":42,"status":"ACTIVE"}}`)
		case "404":
			envelope(w, 404, `{"error":{"code":"USER_NOT_FOUND","message":"User 404 was not found.","details":{"user_id":404}}}`)
		case "422":
			apiError(w, 422, "VALIDATION_ERROR")
		case "302":
			w.Header().Set("Location", "http://redirect-target.invalid/steal")
			w.WriteHeader(302)
		case "nocontent":
			w.WriteHeader(204)
		case "big":
			envelope(w, 200, `{"data":{"blob":"`+strings.Repeat("x", 200_000)+`"}}`)
		case "bigerror":
			envelope(w, 404, `{"error":{"code":"USER_NOT_FOUND","message":"`+strings.Repeat("x", 200_000)+`","details":{}}}`)
		case "slow":
			time.Sleep(2 * time.Second)
			envelope(w, 200, `{"data":{"user_id":1}}`)
		case "badjson":
			envelope(w, 200, `this is not json`)
		case "nulldata":
			envelope(w, 200, `{"data":null}`)
		case "nodata":
			envelope(w, 200, `{}`)
		case "noenvelope":
			envelope(w, 418, `not an error envelope`)
		}
	})))
	mux.Handle("/internal/v1/account-deletions/", auth.Require("account.deletion.begin")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		envelope(w, 200, `{"data":{"echo":`+string(body)+`,"content_type":"`+r.Header.Get("Content-Type")+`","method":"`+r.Method+`"}}`)
	})))

	record := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		rec.mu.Lock()
		rec.got = append(rec.got, seen{
			Path: r.URL.RequestURI(), Authorization: r.Header.Get("Authorization"), Cookie: r.Header.Get("Cookie"),
			RequestID: r.Header.Get(requestid.Header), Traceparent: r.Header.Get(tracecontext.Header), Body: string(body), Header: r.Header.Clone(),
		})
		rec.mu.Unlock()
		mux.ServeHTTP(w, r)
	})
	rec.srv = httptest.NewServer(record)
	t.Cleanup(rec.srv.Close)
	return rec
}

func clientFor(t *testing.T, baseURL, credential string, timeout time.Duration) *privateclient.Client {
	t.Helper()
	c, err := privateclient.New(privateclient.Config{BaseURL: baseURL, Credential: config.NewSecret(credential), Timeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPrivateClientSendsExactlyWhatTheContractAsks(t *testing.T) {
	rec := newReceiver(t)
	c := clientFor(t, rec.srv.URL, pcUserCredential, 5*time.Second)

	ctx := tracecontext.WithContext(requestid.WithID(context.Background(), "req-private-1"),
		tracecontext.Context{TraceID: pcTraceID, SpanID: pcParentSpan})
	var out struct {
		UserID int64  `json:"user_id"`
		Status string `json:"status"`
	}
	if err := c.Do(ctx, privateclientRequest("GET", "/internal/v1/accounts/42"), &out); err != nil {
		t.Fatal(err)
	}
	if out.UserID != 42 || out.Status != "ACTIVE" {
		t.Errorf("decoded %+v", out)
	}

	got := rec.requests()[0]
	if got.Authorization != "Bearer "+pcUserCredential {
		t.Errorf("Authorization = %q", got.Authorization)
	}
	if got.Header.Get("Accept") != "application/json" || got.RequestID != "req-private-1" {
		t.Errorf("Accept=%q request id=%q", got.Header.Get("Accept"), got.RequestID)
	}
	if sent, ok := tracecontext.Parse(got.Traceparent); !ok || sent.TraceID != pcTraceID || sent.SpanID == pcParentSpan {
		t.Errorf("traceparent %q must continue the trace in a new span", got.Traceparent)
	}
	if got.Cookie != "" {
		t.Errorf("no cookie was requested, but %q was sent", got.Cookie)
	}
	if len(got.Header.Values("Authorization")) != 1 {
		t.Error("exactly one Authorization header must be sent")
	}
}

func TestPrivateClientSendsJSONBodiesAndOnlyTheSessionCookie(t *testing.T) {
	rec := newReceiver(t)
	c := clientFor(t, rec.srv.URL, pcUserCredential, 5*time.Second)
	var out struct {
		Echo        map[string]any `json:"echo"`
		ContentType string         `json:"content_type"`
		Method      string         `json:"method"`
	}
	err := c.Do(context.Background(), privateclientRequest("PUT", "/internal/v1/account-deletions/abc", func(r *privateRequest) {
		r.Body = map[string]any{"user_id": 7, "reason": "requested"}
	}), &out)
	if err != nil || out.Method != "PUT" || out.ContentType != "application/json" || out.Echo["user_id"] != float64(7) {
		t.Fatalf("err=%v out=%+v", err, out)
	}

	gw := clientFor(t, rec.srv.URL, pcGatewayCredential, 5*time.Second)
	if err := gw.Do(context.Background(), privateclientRequest("GET", "/internal/v1/sessions/current", func(r *privateRequest) { r.SessionCookie = "good" }), &struct{}{}); err != nil {
		t.Fatal(err)
	}
	last := rec.requests()[len(rec.requests())-1]
	if last.Cookie != "ft_session=good" {
		t.Errorf("Cookie header = %q, want exactly ft_session=good", last.Cookie)
	}
}

func TestPrivateClientClassifiesEveryOutcome(t *testing.T) {
	rec := newReceiver(t)
	user := clientFor(t, rec.srv.URL, pcUserCredential, 5*time.Second)
	wrongCredential := clientFor(t, rec.srv.URL, "not-the-credential-0123456789abcdef-0123456", 5*time.Second)
	gatewayOnUserRoute := clientFor(t, rec.srv.URL, pcGatewayCredential, 5*time.Second) // known caller, wrong scope
	tiny, _ := privateclient.New(privateclient.Config{BaseURL: rec.srv.URL, Credential: config.NewSecret(pcUserCredential), Timeout: 5 * time.Second, MaxResponseBytes: 1024})

	type want struct {
		is     error
		status int
		code   string
	}
	cases := []struct {
		name   string
		client *privateclient.Client
		path   string
		out    any
		want   want
	}{
		{"success", user, "/internal/v1/accounts/42", &struct{}{}, want{}},
		{"204 no content", user, "/internal/v1/accounts/nocontent", &struct{}{}, want{}},
		{"discarded body", user, "/internal/v1/accounts/42", nil, want{}},
		{"unknown credential", wrongCredential, "/internal/v1/accounts/42", &struct{}{}, want{is: privateclient.ErrServiceRejected}},
		{"known caller, missing scope", gatewayOnUserRoute, "/internal/v1/accounts/42", &struct{}{}, want{is: privateclient.ErrServiceRejected}},
		{"domain 404", user, "/internal/v1/accounts/404", &struct{}{}, want{status: 404, code: "USER_NOT_FOUND"}},
		{"validation 422", user, "/internal/v1/accounts/422", &struct{}{}, want{status: 422, code: "VALIDATION_ERROR"}},
		{"redirect is not followed", user, "/internal/v1/accounts/302", &struct{}{}, want{status: 302, code: ""}},
		{"4xx without an envelope", user, "/internal/v1/accounts/noenvelope", &struct{}{}, want{status: 418, code: ""}},
		{"malformed success body", user, "/internal/v1/accounts/badjson", &struct{}{}, want{is: privateclient.ErrUnavailable}},
		{"success without data", user, "/internal/v1/accounts/nodata", &struct{}{}, want{is: privateclient.ErrUnavailable}},
		{"success with null data", user, "/internal/v1/accounts/nulldata", &struct{}{}, want{is: privateclient.ErrUnavailable}},
		{"oversized answer", tiny, "/internal/v1/accounts/big", &struct{}{}, want{is: privateclient.ErrUnavailable}},
		// An oversized error body is not trusted either: its code is not read.
		{"oversized error answer", tiny, "/internal/v1/accounts/bigerror", &struct{}{}, want{is: privateclient.ErrUnavailable}},
	}
	var errs []error
	for _, tc := range cases {
		err := tc.client.Do(context.Background(), privateclientRequest("GET", tc.path), tc.out)
		errs = append(errs, err)
		switch {
		case tc.want.is == nil && tc.want.status == 0:
			if err != nil {
				t.Errorf("%s: unexpected error %v", tc.name, err)
			}
		case tc.want.is != nil:
			if !errors.Is(err, tc.want.is) {
				t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want.is)
			}
		default:
			var remote *privateclient.RemoteError
			if !errors.As(err, &remote) || remote.Status != tc.want.status || remote.Code != tc.want.code || privateclient.Code(err) != tc.want.code {
				t.Errorf("%s: err = %v, want RemoteError %d %q", tc.name, err, tc.want.status, tc.want.code)
			}
			if errors.Is(err, privateclient.ErrUnavailable) || errors.Is(err, privateclient.ErrServiceRejected) {
				t.Errorf("%s: an explicit refusal must not read as unavailable or rejected", tc.name)
			}
		}
		if tc.want.is == privateclient.ErrUnavailable && !privateclient.Retryable(err) {
			t.Errorf("%s: an unavailable receiver must be retryable", tc.name)
		}
		if tc.want.is == privateclient.ErrServiceRejected && privateclient.Retryable(err) {
			t.Errorf("%s: a rejected credential must not be retried", tc.name)
		}
	}

	// The credential must not appear in any error, for any outcome.
	for i, err := range errs {
		if err != nil && (strings.Contains(err.Error(), pcUserCredential) || strings.Contains(err.Error(), pcGatewayCredential) || strings.Contains(err.Error(), "not-the-credential")) {
			t.Errorf("case %q leaked a credential: %v", cases[i].name, err)
		}
	}
}

func TestPrivateClientTreatsServerFailuresAsUnknownOutcomes(t *testing.T) {
	rec := newReceiver(t)
	gw := clientFor(t, rec.srv.URL, pcGatewayCredential, 5*time.Second)
	for _, cookie := range []string{"db-down", "boom"} {
		err := gw.Do(context.Background(), privateclientRequest("GET", "/internal/v1/sessions/current", func(r *privateRequest) { r.SessionCookie = cookie }), &struct{}{})
		if !errors.Is(err, privateclient.ErrUnavailable) || !privateclient.Retryable(err) {
			t.Errorf("%s: err = %v, want ErrUnavailable", cookie, err)
		}
	}
}

func TestPrivateClientBoundsTimeoutsAndHonoursCancellation(t *testing.T) {
	rec := newReceiver(t)
	c := clientFor(t, rec.srv.URL, pcUserCredential, 300*time.Millisecond)

	begin := time.Now()
	err := c.Do(context.Background(), privateclientRequest("GET", "/internal/v1/accounts/slow"), &struct{}{})
	if !errors.Is(err, privateclient.ErrUnavailable) || time.Since(begin) > 1500*time.Millisecond {
		t.Errorf("timeout: err=%v after %s; a 300ms timeout on a 2s answer must be an unknown outcome", err, time.Since(begin))
	}

	slow := clientFor(t, rec.srv.URL, pcUserCredential, 10*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	begin = time.Now()
	err = slow.Do(ctx, privateclientRequest("GET", "/internal/v1/accounts/slow"), &struct{}{})
	if !errors.Is(err, privateclient.ErrUnavailable) || !errors.Is(err, context.DeadlineExceeded) || time.Since(begin) > 1500*time.Millisecond {
		t.Errorf("caller deadline: err=%v after %s", err, time.Since(begin))
	}
}

func TestPrivateClientReportsAnUnreachableReceiverAsUnavailable(t *testing.T) {
	rec := newReceiver(t)
	base := rec.srv.URL
	rec.srv.Close()
	err := clientFor(t, base, pcUserCredential, time.Second).Do(context.Background(), privateclientRequest("GET", "/internal/v1/accounts/42"), &struct{}{})
	if !errors.Is(err, privateclient.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), pcUserCredential) || strings.Contains(err.Error(), base) {
		t.Errorf("the transport error leaked: %v", err)
	}
}

func TestPrivateClientNeverFollowsRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a redirect was followed: the credential went to a second host (Authorization=%q)", r.Header.Get("Authorization"))
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL+"/steal")
		w.WriteHeader(http.StatusFound)
	}))
	defer redirector.Close()

	err := clientFor(t, redirector.URL, pcUserCredential, 2*time.Second).Do(context.Background(), privateclientRequest("GET", "/internal/v1/accounts/42"), &struct{}{})
	var remote *privateclient.RemoteError
	if !errors.As(err, &remote) || remote.Status != 302 {
		t.Errorf("err = %v, want the 302 reported, not followed", err)
	}
}

func TestPrivateClientRefusesUnsafeRequestsWithoutSendingAnything(t *testing.T) {
	rec := newReceiver(t)
	c := clientFor(t, rec.srv.URL, pcUserCredential, 2*time.Second)

	for _, path := range []string{
		"/api/v1/accounts/42", "/internal", "internal/v1/x", "/internal/../api/v1/me", "/internal/v1/../../x",
		"/internal//v1/x", "/internal/v1/x?admin=1", "/internal/v1/x#frag", `/internal/v1\x`, "", "/internal/v1/./x",
	} {
		err := c.Do(context.Background(), privateclientRequest("GET", path), &struct{}{})
		if !errors.Is(err, privateclient.ErrInvalidRequest) {
			t.Errorf("path %q: err = %v, want ErrInvalidRequest", path, err)
		}
	}
	for _, cookie := range []string{"a;b", "a b", "a\r\nX-Evil: 1", `a"b`, "a,b", "ü", "a\\b"} {
		err := c.Do(context.Background(), privateclientRequest("GET", "/internal/v1/sessions/current", func(r *privateRequest) { r.SessionCookie = cookie }), &struct{}{})
		if !errors.Is(err, privateclient.ErrInvalidRequest) {
			t.Errorf("cookie %q: err = %v, want ErrInvalidRequest", cookie, err)
		}
	}
	if err := c.Do(context.Background(), privateclientRequest("PUT", "/internal/v1/account-deletions/x", func(r *privateRequest) { r.Body = make(chan int) }), &struct{}{}); !errors.Is(err, privateclient.ErrInvalidRequest) {
		t.Errorf("unencodable body: %v", err)
	}
	if n := len(rec.requests()); n != 0 {
		t.Errorf("%d requests reached the receiver; unsafe requests must be refused locally", n)
	}
}

func TestPrivateClientConfigurationIsValidated(t *testing.T) {
	good := config.NewSecret(pcUserCredential)
	for name, cfg := range map[string]privateclient.Config{
		"empty URL":        {BaseURL: "", Credential: good, Timeout: time.Second},
		"ftp scheme":       {BaseURL: "ftp://user-service:8091", Credential: good, Timeout: time.Second},
		"path in URL":      {BaseURL: "http://user-service:8091/internal", Credential: good, Timeout: time.Second},
		"query in URL":     {BaseURL: "http://user-service:8091?x=1", Credential: good, Timeout: time.Second},
		"userinfo":         {BaseURL: "http://u:p@user-service:8091", Credential: good, Timeout: time.Second},
		"no credential":    {BaseURL: "http://user-service:8091", Timeout: time.Second},
		"no timeout":       {BaseURL: "http://user-service:8091", Credential: good},
		"negative timeout": {BaseURL: "http://user-service:8091", Credential: good, Timeout: -time.Second},
	} {
		if _, err := privateclient.New(cfg); !errors.Is(err, privateclient.ErrInvalidConfig) {
			t.Errorf("%s: err = %v, want ErrInvalidConfig", name, err)
		} else if strings.Contains(err.Error(), pcUserCredential) {
			t.Errorf("%s: the error leaked the credential", name)
		}
	}
	if _, err := privateclient.New(privateclient.Config{BaseURL: "http://user-service:8091/", Credential: good, Timeout: time.Second}); err != nil {
		t.Errorf("a trailing slash must be accepted: %v", err)
	}
}

func TestPrivateClientConfigFromEnv(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "cred")
	if err := os.WriteFile(file, []byte(pcUserCredential+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lookup := func(values map[string]string) *config.Reader {
		return config.NewReader(func(n string) (string, bool) { v, ok := values[n]; return v, ok })
	}

	r := lookup(map[string]string{"USER_INTERNAL_URL": "http://user-service:8091", "USER_CRED_FILE": file, "USER_TIMEOUT": "5s"})
	cfg := privateclient.ConfigFromEnv(r, "USER_INTERNAL_URL", "USER_CRED_FILE", "USER_TIMEOUT")
	if err := r.Err(); err != nil || cfg.Timeout != 5*time.Second || cfg.Credential.Reveal() != pcUserCredential || cfg.BaseURL != "http://user-service:8091" {
		t.Fatalf("cfg=%+v err=%v", cfg, r.Err())
	}
	if _, err := privateclient.New(cfg); err != nil {
		t.Errorf("a config read from the environment must be usable: %v", err)
	}

	r = lookup(map[string]string{})
	privateclient.ConfigFromEnv(r, "USER_INTERNAL_URL", "USER_CRED_FILE", "USER_TIMEOUT")
	err := r.Err()
	for _, name := range []string{"USER_INTERNAL_URL", "USER_CRED_FILE", "USER_TIMEOUT"} {
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("a missing %s must be named: %v", name, err)
		}
	}
}

// privateRequest and privateclientRequest keep the tests short.
type privateRequest = privateclient.Request

func privateclientRequest(method, path string, mods ...func(*privateRequest)) privateRequest {
	r := privateRequest{Method: method, Path: path}
	for _, m := range mods {
		m(&r)
	}
	return r
}
