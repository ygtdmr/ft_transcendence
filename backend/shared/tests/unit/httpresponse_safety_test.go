package unit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/shared/logging"
	"github.com/melmut-42/ft_transcendence/backend/shared/requestid"
	"github.com/melmut-42/ft_transcendence/backend/shared/validate"
)

func TestEveryAPIResponseCarriesTheSafetyHeaders(t *testing.T) {
	for name, write := range map[string]func(http.ResponseWriter){
		"data":       func(w http.ResponseWriter) { httpresponse.WriteData(w, 200, map[string]any{"a": 1}) },
		"created":    func(w http.ResponseWriter) { httpresponse.WriteData(w, 201, nil) },
		"error":      func(w http.ResponseWriter) { httpresponse.WriteError(w, httpresponse.InternalError()) },
		"no content": func(w http.ResponseWriter) { httpresponse.WriteNoContent(w) },
	} {
		rec := httptest.NewRecorder()
		write(rec)
		if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: headers = %v", name, rec.Header())
		}
	}
	rec := httptest.NewRecorder()
	httpresponse.WriteNoContent(rec)
	if rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "" {
		t.Error("204 must still have no body and no Content-Type")
	}
}

func TestErrorDetailsNeverCarryCredentials(t *testing.T) {
	details := map[string]any{
		"field":    "email",
		"password": "pw-secret-1",
		"nested": map[string]any{
			"refresh_token": "tok-secret-2",
			"ok":            "fine",
			"list":          []any{map[string]any{"Authorization": "Bearer auth-secret-3", "n": 1}},
		},
	}
	rec := httptest.NewRecorder()
	httpresponse.WriteError(rec, &httpresponse.Error{Status: 422, Code: "VALIDATION_ERROR", Message: "bad", Details: details})

	body := rec.Body.String()
	for _, secret := range []string{"pw-secret-1", "tok-secret-2", "auth-secret-3"} {
		if strings.Contains(body, secret) {
			t.Errorf("a credential reached the response: %s", body)
		}
	}
	for _, kept := range []string{`"field":"email"`, `"ok":"fine"`, `"n":1`} {
		if !strings.Contains(body, kept) {
			t.Errorf("response lost %q: %s", kept, body)
		}
	}
	for _, key := range []string{"password", "refresh_token", "Authorization"} {
		if strings.Contains(body, key) {
			t.Errorf("the key %q must be omitted entirely, not just its value: %s", key, body)
		}
	}
	if details["password"] != "pw-secret-1" || details["nested"].(map[string]any)["refresh_token"] != "tok-secret-2" {
		t.Error("the caller's details map must not be modified")
	}
	if v := httpresponse.Violations(rec.Code, rec.Body.Bytes()); len(v) != 0 {
		t.Errorf("sanitized response violates the conventions: %v", v)
	}
}

func TestViolationsFlagCredentialKeysInErrorDetails(t *testing.T) {
	body := `{"error":{"code":"BAD","message":"m","details":{"ok":1,"inner":{"access_token":"x"}}}}`
	got := strings.Join(httpresponse.Violations(400, []byte(body)), "|")
	if !strings.Contains(got, "error.details.inner.access_token") {
		t.Errorf("violations = %q, want the sensitive key reported with its path", got)
	}
}

func TestMapperResolutionOrder(t *testing.T) {
	errNotFound := errors.New("user missing")
	errConflict := errors.New("name taken")
	m := httpresponse.NewMapper().
		Map(errNotFound, httpresponse.Error{Status: 404, Code: "USER_NOT_FOUND", Message: "User was not found."}).
		Map(errConflict, httpresponse.Error{Status: 409, Code: "USERNAME_TAKEN", Message: "That username is taken."}).
		MapFunc(func(err error) bool { return strings.Contains(err.Error(), "dup") }, httpresponse.Error{Status: 409, Code: "CONFLICT", Message: "Conflict."})

	cases := []struct {
		name     string
		err      error
		status   int
		code     string
		expected bool
	}{
		{"registered", errNotFound, 404, "USER_NOT_FOUND", true},
		{"registered and wrapped", fmt.Errorf("lookup 7: %w", errConflict), 409, "USERNAME_TAKEN", true},
		{"match func", errors.New("dup key"), 409, "CONFLICT", true},
		{"already public", &httpresponse.Error{Status: 403, Code: "NOT_PERMITTED", Message: "no"}, 403, "NOT_PERMITTED", true},
		{"public in chain", fmt.Errorf("ctx: %w", &httpresponse.Error{Status: 403, Code: "NOT_PERMITTED", Message: "no"}), 403, "NOT_PERMITTED", true},
		{"validation field", &validate.FieldError{Field: "f", Message: "f is bad."}, 422, "VALIDATION_ERROR", true},
		{"validation body", &validate.BodyError{}, 400, "VALIDATION_ERROR", true},
		{"deadline", context.DeadlineExceeded, 503, "SERVICE_UNAVAILABLE", true},
		{"wrapped deadline", fmt.Errorf("query: %w", context.DeadlineExceeded), 503, "SERVICE_UNAVAILABLE", true},
		{"canceled", context.Canceled, 503, "SERVICE_UNAVAILABLE", true},
		{"unknown", errors.New("pq: password authentication failed for user svc at db.internal"), 500, "INTERNAL_ERROR", false},
	}
	for _, tc := range cases {
		resp, expected := m.Response(tc.err)
		if resp.Status != tc.status || resp.Code != tc.code || expected != tc.expected {
			t.Errorf("%s: got %d %s expected=%v, want %d %s expected=%v", tc.name, resp.Status, resp.Code, expected, tc.status, tc.code, tc.expected)
		}
	}
}

func TestMapperNeverCopiesAnUnknownErrorIntoTheResponse(t *testing.T) {
	const leak = "postgres://svc:hunter2@db.internal:5432/app"
	resp, expected := httpresponse.NewMapper().Response(fmt.Errorf("connect %s: refused", leak))
	rec := httptest.NewRecorder()
	httpresponse.WriteError(rec, resp)
	if expected || strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "db.internal") || strings.Contains(rec.Body.String(), "refused") {
		t.Errorf("an unknown error's text reached the response: %s", rec.Body.String())
	}
}

func TestMapperZeroAndNilValuesWork(t *testing.T) {
	var nilMapper *httpresponse.Mapper
	for _, m := range []*httpresponse.Mapper{nilMapper, {}, httpresponse.NewMapper()} {
		if resp, expected := m.Response(&validate.BodyError{}); resp.Status != 400 || !expected {
			t.Errorf("built-in rules must apply without registered rules: %+v", resp)
		}
		if resp, expected := m.Response(errors.New("x")); resp.Status != 500 || expected {
			t.Errorf("fallback: %+v", resp)
		}
	}
}

func TestMapperRulesAreCopiedAndFirstMatchWins(t *testing.T) {
	target := errors.New("t")
	original := httpresponse.Error{Status: 409, Code: "A", Message: "first", Details: map[string]any{"k": "v"}}
	m := httpresponse.NewMapper().Map(target, original).Map(target, httpresponse.Error{Status: 400, Code: "B", Message: "second"})
	original.Code = "CHANGED"
	original.Details["k"] = "changed"

	resp, _ := m.Response(target)
	if resp.Code != "A" || resp.Details["k"] != "v" {
		t.Errorf("the registered response must be a copy and the first rule must win: %+v", resp)
	}
	resp.Code = "MUTATED"
	resp.Details["k"] = "poisoned"
	resp.Details["injected"] = true
	again, _ := m.Response(target)
	if again.Code != "A" || again.Details["k"] != "v" || again.Details["injected"] != nil {
		t.Errorf("a returned response must not alias the rule (one request's changes must not reach the next): %+v", again)
	}
}

func TestAdaptWritesMappedErrorsAndLogsUnexpectedOnesByTypeOnly(t *testing.T) {
	var logs bytes.Buffer
	logger, err := logging.New(logging.Config{Format: "json", Service: "svc"}, &logs)
	if err != nil {
		t.Fatal(err)
	}
	const leak = "dsn=postgres://svc:hunter2@db/app"
	m := httpresponse.NewMapper()

	run := func(fn httpresponse.HandlerFunc) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(requestid.WithID(context.Background(), "req-5"))
		httpresponse.Adapt(fn, m, logger)(rec, req)
		return rec
	}

	if rec := run(func(w http.ResponseWriter, r *http.Request) error {
		httpresponse.WriteData(w, 200, map[string]any{"ok": true})
		return nil
	}); rec.Code != 200 || logs.Len() != 0 {
		t.Errorf("success: %d, logs=%q", rec.Code, logs.String())
	}

	if rec := run(func(http.ResponseWriter, *http.Request) error {
		return &validate.FieldError{Field: "user_id", Message: "user_id must be a positive integer."}
	}); rec.Code != 422 || !strings.Contains(rec.Body.String(), `"field":"user_id"`) || logs.Len() != 0 {
		t.Errorf("validation: %d %s logs=%q", rec.Code, rec.Body.String(), logs.String())
	}

	rec := run(func(http.ResponseWriter, *http.Request) error { return errors.New(leak) })
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "hunter2") {
		t.Errorf("unexpected error: %d %s", rec.Code, rec.Body.String())
	}
	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &entry); err != nil {
		t.Fatalf("log: %v: %s", err, logs.String())
	}
	if entry["msg"] != "unhandled_error" || entry["error_type"] != "*errors.errorString" || entry["request_id"] != "req-5" || entry["level"] != "ERROR" {
		t.Errorf("log entry = %v", entry)
	}
	if strings.Contains(logs.String(), "hunter2") {
		t.Errorf("the error text reached the log: %s", logs.String())
	}
}

func TestAdaptToleratesANilLoggerAndMapper(t *testing.T) {
	rec := httptest.NewRecorder()
	httpresponse.Adapt(func(http.ResponseWriter, *http.Request) error { return errors.New("boom") }, nil, nil)(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 500 {
		t.Errorf("status %d", rec.Code)
	}
}

type userRecord struct { // a service/database struct that must not be serialised directly
	ID           int64
	Username     string
	PasswordHash string
	CreatedAt    time.Time
}

type userDTO struct {
	UserID    int64             `json:"user_id"`
	Username  string            `json:"username"`
	CreatedAt httpresponse.Time `json:"created_at"`
}

func TestRespondSerialisesOnlyTheDTO(t *testing.T) {
	rec := httptest.NewRecorder()
	record := userRecord{ID: 7, Username: "ann", PasswordHash: "$2a$hash-must-not-leak", CreatedAt: time.Date(2026, 11, 5, 19, 0, 0, 0, time.FixedZone("EET", 7200))}
	httpresponse.Respond(rec, http.StatusOK, record, func(r userRecord) userDTO {
		return userDTO{UserID: r.ID, Username: r.Username, CreatedAt: httpresponse.Time(r.CreatedAt)}
	})
	body := strings.TrimSpace(rec.Body.String())
	if body != `{"data":{"user_id":7,"username":"ann","created_at":"2026-11-05T17:00:00Z"}}` {
		t.Errorf("body = %s", body)
	}
	if strings.Contains(body, "hash") {
		t.Error("a field outside the DTO leaked")
	}
}

func TestSliceNeverEncodesAsNull(t *testing.T) {
	var none []userDTO
	rec := httptest.NewRecorder()
	httpresponse.WriteData(rec, 200, httpresponse.Slice(none))
	if strings.TrimSpace(rec.Body.String()) != `{"data":[]}` {
		t.Errorf("empty list = %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	httpresponse.WriteData(rec, 200, none)
	if !strings.Contains(rec.Body.String(), "null") {
		t.Skip("encoding/json no longer encodes a nil slice as null; Slice is then merely defensive")
	}
	if got := httpresponse.Slice([]int{1, 2}); len(got) != 2 {
		t.Error("a non-empty slice must pass through")
	}
}

var (
	sessionCookie = httpresponse.Cookie{Name: "ft_session", Path: "/", MaxAge: 15 * time.Minute}
	refreshCookie = httpresponse.Cookie{Name: "ft_refresh", Path: "/api/v1/auth/refresh", MaxAge: 30 * 24 * time.Hour}
)

func cookiesOf(rec *httptest.ResponseRecorder) map[string]*http.Cookie {
	out := map[string]*http.Cookie{}
	for _, c := range rec.Result().Cookies() {
		out[c.Name] = c
	}
	return out
}

func TestCookiesFollowTheContractAttributes(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := httpresponse.SetCookie(rec, sessionCookie, "jwt.value.here"); err != nil {
		t.Fatal(err)
	}
	if err := httpresponse.SetCookie(rec, refreshCookie, "rtok_abc123"); err != nil {
		t.Fatal(err)
	}
	httpresponse.WriteData(rec, 200, map[string]any{"user_id": 1})

	cookies := cookiesOf(rec)
	s, r := cookies["ft_session"], cookies["ft_refresh"]
	if s == nil || r == nil {
		t.Fatalf("cookies = %v", rec.Header()["Set-Cookie"])
	}
	for _, c := range []*http.Cookie{s, r} {
		if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
			t.Errorf("%s: HttpOnly=%v Secure=%v SameSite=%v, want HttpOnly, Secure, Lax", c.Name, c.HttpOnly, c.Secure, c.SameSite)
		}
	}
	if s.Path != "/" || s.MaxAge != 900 || r.Path != "/api/v1/auth/refresh" || r.MaxAge != 2592000 {
		t.Errorf("session=%+v refresh=%+v", s, r)
	}
	if strings.Contains(rec.Body.String(), "jwt.value.here") || strings.Contains(rec.Body.String(), "rtok_abc123") {
		t.Error("a credential must never be in the response body")
	}
}

func TestLogoutClearsBothCookiesWithA204(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := httpresponse.ClearCookie(rec, sessionCookie); err != nil {
		t.Fatal(err)
	}
	if err := httpresponse.ClearCookie(rec, refreshCookie); err != nil {
		t.Fatal(err)
	}
	httpresponse.WriteNoContent(rec)

	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("got %d with body %q", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Value != "" || c.MaxAge >= 0 || !c.HttpOnly || !c.Secure {
			t.Errorf("%s must be cleared (empty, Max-Age=0) with the same attributes: %+v", c.Name, c)
		}
	}
	if raw := strings.Join(rec.Header()["Set-Cookie"], "\n"); !strings.Contains(raw, "Max-Age=0") || !strings.Contains(raw, "Path=/api/v1/auth/refresh") {
		t.Errorf("Set-Cookie = %s", raw)
	}
}

func TestInsecureOnlyDropsTheSecureAttribute(t *testing.T) {
	rec := httptest.NewRecorder()
	dev := sessionCookie
	dev.Insecure = true
	if err := httpresponse.SetCookie(rec, dev, "v"); err != nil {
		t.Fatal(err)
	}
	c := cookiesOf(rec)["ft_session"]
	if c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("Insecure must only drop Secure: %+v", c)
	}
}

func TestInvalidCookiesAreRejectedNotSilentlyDropped(t *testing.T) {
	bad := []struct {
		cookie httpresponse.Cookie
		value  string
	}{
		{sessionCookie, "has;semicolon"},
		{sessionCookie, "has space"},
		{sessionCookie, `back\slash`},
		{sessionCookie, `quo"te`},
		{sessionCookie, "com,ma"},
		{sessionCookie, "non-ascii-ü"},
		{sessionCookie, "tab\tvalue"},
		{sessionCookie, "line\r\nbreak: Set-Cookie: evil=1"},
		{sessionCookie, ""},
		{httpresponse.Cookie{Name: "bad name", Path: "/"}, "v"},
		{httpresponse.Cookie{Name: "ft_session", Path: ""}, "v"},
		{httpresponse.Cookie{Name: "ft_session", Path: "api"}, "v"},
		{httpresponse.Cookie{Name: "", Path: "/"}, "v"},
	}
	for _, tc := range bad {
		rec := httptest.NewRecorder()
		if err := httpresponse.SetCookie(rec, tc.cookie, tc.value); err == nil {
			t.Errorf("SetCookie(%+v, %q) accepted", tc.cookie, tc.value)
		}
		if len(rec.Header()["Set-Cookie"]) != 0 {
			t.Errorf("a rejected cookie still wrote a header: %v", rec.Header()["Set-Cookie"])
		}
	}
	if err := httpresponse.ClearCookie(httptest.NewRecorder(), httpresponse.Cookie{Name: "x", Path: ""}); err == nil {
		t.Error("ClearCookie accepted a missing path")
	}
}

var _ = slog.LevelInfo
