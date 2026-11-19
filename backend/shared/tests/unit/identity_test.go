package unit_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/shared/identity"
	"github.com/melmut-42/ft_transcendence/backend/shared/logging"
)

// fakeValidator answers by credential value and counts how often it is asked.
type fakeValidator struct {
	calls atomic.Int32
	last  atomic.Value
	reply func(credential string) (identity.Identity, error)
}

func (f *fakeValidator) Validate(_ context.Context, credential string) (identity.Identity, error) {
	f.calls.Add(1)
	f.last.Store(credential)
	return f.reply(credential)
}

var okIdentity = identity.Identity{UserID: 42, SessionID: "8f14e45f-ceea-4e6b-9c3a-2d1b5f7a9e01", ExpiresAt: time.Date(2026, 11, 5, 9, 45, 0, 0, time.UTC)}

func validatorFor(replies map[string]error) *fakeValidator {
	return &fakeValidator{reply: func(c string) (identity.Identity, error) {
		if err, ok := replies[c]; ok {
			return identity.Identity{}, err
		}
		return okIdentity, nil
	}}
}

func authed(v identity.SessionValidator, opts identity.Options, req *http.Request) (*httptest.ResponseRecorder, *identity.Identity) {
	var seen *identity.Identity
	h := identity.Authenticate(v, opts)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := identity.FromContext(r.Context()); ok {
			seen = &id
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, seen
}

func withCookies(values ...string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	for _, v := range values {
		req.AddCookie(&http.Cookie{Name: identity.SessionCookie, Value: v})
	}
	return req
}

func TestAuthenticatePassesTheIdentityToHandlers(t *testing.T) {
	v := validatorFor(nil)
	rec, id := authed(v, identity.Options{}, withCookies("good-session"))
	if rec.Code != 204 || id == nil || *id != okIdentity {
		t.Fatalf("got %d identity=%v", rec.Code, id)
	}
	if v.last.Load() != "good-session" {
		t.Errorf("the validator must receive exactly the cookie value, got %v", v.last.Load())
	}
}

func TestAuthenticateReadsTheSessionOnlyFromTheCookie(t *testing.T) {
	v := validatorFor(nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me?ft_session=good&access_token=good&token=good", nil)
	req.Header.Set("Authorization", "Bearer good")
	req.Header.Set("X-Session", "good")
	req.Header.Set("X-Access-Token", "good")
	req.Header.Set("X-User-ID", "1")
	req.AddCookie(&http.Cookie{Name: "ft_refresh", Value: "good"}) // wrong cookie
	req.AddCookie(&http.Cookie{Name: "session", Value: "good"})    // wrong name
	rec, id := authed(v, identity.Options{}, req)

	if rec.Code != 401 || id != nil || v.calls.Load() != 0 {
		t.Fatalf("a credential outside the ft_session cookie authenticated: %d identity=%v validator calls=%d", rec.Code, id, v.calls.Load())
	}
	if !strings.Contains(rec.Body.String(), `"code":"UNAUTHORIZED"`) {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestAuthenticateRefusesAmbiguousOrImplausibleCredentials(t *testing.T) {
	for name, req := range map[string]*http.Request{
		"no cookie":   httptest.NewRequest(http.MethodGet, "/", nil),
		"empty value": withCookies(""),
		"two cookies": withCookies("first", "second"),
		"oversized":   withCookies(strings.Repeat("a", 5000)),
	} {
		v := validatorFor(nil)
		rec, id := authed(v, identity.Options{}, req)
		if rec.Code != 401 || id != nil || v.calls.Load() != 0 {
			t.Errorf("%s: %d identity=%v validator calls=%d", name, rec.Code, id, v.calls.Load())
		}
	}
}

func TestAClientCannotChooseItsOwnIdentity(t *testing.T) {
	v := validatorFor(nil)
	req := withCookies("good-session")
	for _, h := range []string{"X-User-ID", "X-User", "X-Forwarded-User", "X-Authenticated-User", "X-Session-ID"} {
		req.Header.Set(h, "1")
	}
	_, id := authed(v, identity.Options{}, req)
	if id == nil || id.UserID != 42 || id.SessionID != okIdentity.SessionID {
		t.Errorf("identity must come from the validator only, got %+v", id)
	}
}

func TestAuthenticateMapsVerdictsToTheContractResponses(t *testing.T) {
	var logs bytes.Buffer
	logger, _ := logging.New(logging.Config{Format: "json", Service: "gw"}, &logs)
	opts := identity.Options{Logger: logger}
	v := validatorFor(map[string]error{
		"revoked":   identity.ErrInvalidSession,
		"wrapped":   fmt.Errorf("lookup: %w", identity.ErrInvalidSession),
		"auth-down": identity.ErrUnavailable,
		"misconfig": identity.ErrServiceRejected,
		"weird":     errors.New("pq: password authentication failed for user svc at db.internal"),
	})
	cases := []struct {
		cookie string
		status int
		code   string
	}{
		{"revoked", 401, "UNAUTHORIZED"},
		{"wrapped", 401, "UNAUTHORIZED"},
		{"auth-down", 503, "SERVICE_UNAVAILABLE"},
		{"misconfig", 500, "INTERNAL_ERROR"},
		{"weird", 500, "INTERNAL_ERROR"},
	}
	for _, tc := range cases {
		rec, id := authed(v, opts, withCookies(tc.cookie))
		if rec.Code != tc.status || id != nil || !strings.Contains(rec.Body.String(), `"code":"`+tc.code+`"`) {
			t.Errorf("%s: got %d %s", tc.cookie, rec.Code, rec.Body.String())
		}
		if v := httpresponse.Violations(rec.Code, rec.Body.Bytes()); len(v) != 0 {
			t.Errorf("%s: %v", tc.cookie, v)
		}
		if strings.Contains(rec.Body.String(), "db.internal") || strings.Contains(rec.Body.String(), tc.cookie) && tc.status == 500 {
			t.Errorf("%s: internals or the credential leaked: %s", tc.cookie, rec.Body.String())
		}
	}
	// An unavailable verdict must never read as a logout: no 401 and no cookie clearing.
	rec, _ := authed(v, opts, withCookies("auth-down"))
	if rec.Code == 401 || len(rec.Header()["Set-Cookie"]) != 0 {
		t.Error("an availability failure must not look like a logout")
	}
	out := logs.String()
	for _, leak := range []string{"db.internal", "hunter2", "revoked", "auth-down"} {
		if strings.Contains(out, leak) {
			t.Errorf("the log leaked %q: %s", leak, out)
		}
	}
	if !strings.Contains(out, "session_verifier_rejected_service_credential") || !strings.Contains(out, "session_validation_failed_unexpectedly") {
		t.Errorf("configuration faults and unexpected failures must be logged: %s", out)
	}
}

func TestAuthenticateRejectsAValidatorThatReturnsNoIdentity(t *testing.T) {
	for name, bad := range map[string]identity.Identity{
		"zero user":     {UserID: 0, SessionID: "s"},
		"negative user": {UserID: -1, SessionID: "s"},
		"no session id": {UserID: 1},
	} {
		v := &fakeValidator{reply: func(string) (identity.Identity, error) { return bad, nil }}
		rec, id := authed(v, identity.Options{}, withCookies("c"))
		if rec.Code != 500 || id != nil {
			t.Errorf("%s: %d identity=%v; a nil error with no identity must not authenticate", name, rec.Code, id)
		}
	}
}

func TestAuthorizeRunsTheServicesRuleAndMapsItsRefusal(t *testing.T) {
	notMember := errors.New("not a room member")
	mapper := httpresponse.NewMapper().Map(notMember, httpresponse.Error{Status: 403, Code: "NOT_ROOM_MEMBER", Message: "You are not a member of this room."})
	var reached atomic.Int32
	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1); w.WriteHeader(204) })

	run := func(ctx context.Context, check func(*http.Request, identity.Identity) error) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		identity.Authorize(check, mapper, identity.Options{})(final).ServeHTTP(rec, httptest.NewRequest("GET", "/rooms/7", nil).WithContext(ctx))
		return rec
	}
	signedIn := identity.WithIdentity(context.Background(), okIdentity)

	if rec := run(signedIn, func(_ *http.Request, id identity.Identity) error {
		if id.UserID != 42 {
			t.Errorf("the rule must see the authenticated identity, got %+v", id)
		}
		return nil
	}); rec.Code != 204 || reached.Load() != 1 {
		t.Errorf("allowed: %d reached=%d", rec.Code, reached.Load())
	}
	if rec := run(signedIn, func(*http.Request, identity.Identity) error { return fmt.Errorf("check: %w", notMember) }); rec.Code != 403 ||
		!strings.Contains(rec.Body.String(), `"code":"NOT_ROOM_MEMBER"`) || reached.Load() != 1 {
		t.Errorf("refused: %d %s reached=%d", rec.Code, rec.Body.String(), reached.Load())
	}
	if rec := run(signedIn, func(*http.Request, identity.Identity) error { return errors.New("db exploded: dsn=x") }); rec.Code != 500 || strings.Contains(rec.Body.String(), "dsn") {
		t.Errorf("an unexpected rule failure: %d %s", rec.Code, rec.Body.String())
	}
	if rec := run(context.Background(), func(*http.Request, identity.Identity) error { t.Error("the rule ran without an identity"); return nil }); rec.Code != 401 || reached.Load() != 1 {
		t.Errorf("no identity must fail closed with 401, got %d", rec.Code)
	}
}

var _ = slog.LevelInfo
