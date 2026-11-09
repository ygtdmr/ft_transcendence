package unit_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/controller"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

type reportStoreStub struct {
	last  service.NewReport
	err   error
	calls int
}

func (s *reportStoreStub) FileReport(_ context.Context, report service.NewReport) (service.StoredReport, error) {
	s.calls++
	s.last = report
	return service.StoredReport{ID: 9001, CreatedAt: time.Date(2026, 9, 10, 8, 50, 0, 0, time.UTC)}, s.err
}

func reportRequest(store *reportStoreStub, target, body, cookie string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	controller.NewReports(service.NewReports(store, 24*time.Hour), blockSessions{}).RegisterRoutes(engine)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/users/"+target+"/reports", strings.NewReader(body))
	if cookie != "" {
		request.AddCookie(&http.Cookie{Name: "ft_session", Value: cookie})
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

// TestReportRefusals checks the result and order of every refusal.
//
// Expected output: each case returns its status, code and field, and none
// reaches the store.
func TestReportRefusals(t *testing.T) {
	tooLong := strings.Repeat("é", 501)
	for _, tt := range []struct {
		name, target, body, cookie, code, field string
		status                                  int
	}{
		{"no session", "44", `{"reason":"SPAM"}`, "", "UNAUTHORIZED", "", http.StatusUnauthorized},
		{"text id", "abc", `{"reason":"SPAM"}`, "valid", "VALIDATION_ERROR", "user_id", http.StatusUnprocessableEntity},
		{"zero id", "0", `{"reason":"SPAM"}`, "valid", "VALIDATION_ERROR", "user_id", http.StatusUnprocessableEntity},
		{"self", "42", `{"reason":"SPAM"}`, "valid", "CANNOT_REPORT_SELF", "", http.StatusUnprocessableEntity},
		{"self wins over bad body", "42", `{`, "valid", "CANNOT_REPORT_SELF", "", http.StatusUnprocessableEntity},
		{"malformed", "44", `{`, "valid", "VALIDATION_ERROR", "body", http.StatusBadRequest},
		{"empty body", "44", ``, "valid", "VALIDATION_ERROR", "body", http.StatusBadRequest},
		{"two objects", "44", `{}{}`, "valid", "VALIDATION_ERROR", "body", http.StatusBadRequest},
		{"too large", "44", `{"reason":"` + strings.Repeat("a", 9000) + `"}`, "valid", "VALIDATION_ERROR", "body", http.StatusBadRequest},
		{"missing reason", "44", `{}`, "valid", "VALIDATION_ERROR", "reason", http.StatusUnprocessableEntity},
		{"lowercase reason", "44", `{"reason":"spam"}`, "valid", "VALIDATION_ERROR", "reason", http.StatusUnprocessableEntity},
		{"unknown reason", "44", `{"reason":"BORED"}`, "valid", "VALIDATION_ERROR", "reason", http.StatusUnprocessableEntity},
		{"numeric reason", "44", `{"reason":3}`, "valid", "VALIDATION_ERROR", "reason", http.StatusUnprocessableEntity},
		{"details too long", "44", `{"reason":"SPAM","details":"` + tooLong + `"}`, "valid", "VALIDATION_ERROR", "details", http.StatusUnprocessableEntity},
		{"details null", "44", `{"reason":"SPAM","details":null}`, "valid", "VALIDATION_ERROR", "details", http.StatusUnprocessableEntity},
		{"details number", "44", `{"reason":"SPAM","details":7}`, "valid", "VALIDATION_ERROR", "details", http.StatusUnprocessableEntity},
		{"details nul byte", "44", `{"reason":"SPAM","details":"a\u0000b"}`, "valid", "VALIDATION_ERROR", "details", http.StatusUnprocessableEntity},
		{"room zero", "44", `{"reason":"SPAM","room_id":0}`, "valid", "VALIDATION_ERROR", "room_id", http.StatusUnprocessableEntity},
		{"room negative", "44", `{"reason":"SPAM","room_id":-1}`, "valid", "VALIDATION_ERROR", "room_id", http.StatusUnprocessableEntity},
		{"room quoted", "44", `{"reason":"SPAM","room_id":"1001"}`, "valid", "VALIDATION_ERROR", "room_id", http.StatusUnprocessableEntity},
		{"room fraction", "44", `{"reason":"SPAM","room_id":1.5}`, "valid", "VALIDATION_ERROR", "room_id", http.StatusUnprocessableEntity},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &reportStoreStub{}
			recorder := reportRequest(store, tt.target, tt.body, tt.cookie)
			var body struct {
				Error struct {
					Code    string         `json:"code"`
					Details map[string]any `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("body %q: %v", recorder.Body.String(), err)
			}
			if recorder.Code != tt.status || body.Error.Code != tt.code {
				t.Fatalf("response = %d %s, want %d %s", recorder.Code, recorder.Body.String(), tt.status, tt.code)
			}
			if tt.field != "" && body.Error.Details["field"] != tt.field {
				t.Fatalf("details = %v, want field %s", body.Error.Details, tt.field)
			}
			if store.calls != 0 {
				t.Fatal("a refused report reached the store")
			}
		})
	}
}

// TestReportAccepted checks accepted input and the response.
//
// Expected output:
//
//   - The response has the contract's fields and the public reason.
//   - The store gets the session's reporter, the stored reason and trimmed details.
//   - 500 multibyte characters are accepted, and room_id may be null or absent.
//   - A reporter_id in the body is ignored.
func TestReportAccepted(t *testing.T) {
	store := &reportStoreStub{}
	body := `{"reason":"OFFENSIVE_USERNAME","details":"  rude  ","room_id":1001,"reporter_id":7}`
	recorder := reportRequest(store, "44", body, "valid")
	want := `{"data":{"created_at":"2026-09-10T08:50:00.000000Z","reason":"OFFENSIVE_USERNAME","report_id":9001,"reported_user_id":44}}`
	if recorder.Code != http.StatusCreated || recorder.Body.String() != want {
		t.Fatalf("report = %d %s", recorder.Code, recorder.Body.String())
	}
	got := store.last
	if got.ReporterID != 42 || got.TargetID != 44 || got.Reason != "offensive_username" || got.Details != "rude" ||
		got.RoomID == nil || *got.RoomID != 1001 || got.Window != 24*time.Hour {
		t.Fatalf("stored = %+v", got)
	}

	for name, requestBody := range map[string]string{
		"500 characters": `{"reason":"OTHER","details":"` + strings.Repeat("é", 500) + `"}`,
		"padded":         `{"reason":"OTHER","details":"  ` + strings.Repeat("a", 500) + `  "}`,
		"empty details":  `{"reason":"OTHER","details":""}`,
		"null room":      `{"reason":"OTHER","room_id":null}`,
		"reason only":    `{"reason":"CHEATING"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if recorder := reportRequest(&reportStoreStub{}, "44", requestBody, "valid"); recorder.Code != http.StatusCreated {
				t.Fatalf("report = %d %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

// TestReportStoreRefusals checks how store results are answered.
//
// Expected output: each store refusal has its documented status and details.
func TestReportStoreRefusals(t *testing.T) {
	for _, tt := range []struct {
		err    error
		status int
		want   string
	}{
		{service.ErrUserNotFound, http.StatusNotFound, `"user_id":44`},
		{&service.AlreadyReportedError{RetryAfterSeconds: 82800}, http.StatusConflict, `"details":{"retry_after_seconds":82800,"user_id":44}`},
		{fmt.Errorf("%w: test", service.ErrUnavailable), http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE"},
	} {
		recorder := reportRequest(&reportStoreStub{err: tt.err}, "44", `{"reason":"SPAM"}`, "valid")
		if recorder.Code != tt.status || !strings.Contains(recorder.Body.String(), tt.want) {
			t.Fatalf("store error %v = %d %s", tt.err, recorder.Code, recorder.Body.String())
		}
	}
}
