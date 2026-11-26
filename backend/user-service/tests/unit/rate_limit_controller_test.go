package unit_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/shared/ratelimit"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/client/authclient"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/controller"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

// twoUserSessions accepts "alice" as user 42 and "bob" as user 43.
type twoUserSessions struct{}

func (twoUserSessions) ValidateSession(_ context.Context, session string) (int64, error) {
	switch session {
	case "alice":
		return 42, nil
	case "bob":
		return 43, nil
	}
	return 0, authclient.ErrSessionInvalid
}

type deletionStub struct{ calls int }

func (d *deletionStub) Request(context.Context, int64, string) error {
	d.calls++
	return service.ErrConfirmationMismatch
}

func hourLimiter(t *testing.T, count int) *ratelimit.Limiter {
	t.Helper()
	l, err := ratelimit.New(ratelimit.Config{Limit: count, Window: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func serve(engine *gin.Engine, method, path, body, session string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if session != "" {
		request.AddCookie(&http.Cookie{Name: "ft_session", Value: session})
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

func assertLimited(t *testing.T, recorder *httptest.ResponseRecorder, message string) {
	t.Helper()
	var body struct {
		Error struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if recorder.Code != http.StatusTooManyRequests || json.Unmarshal(recorder.Body.Bytes(), &body) != nil ||
		body.Error.Code != "RATE_LIMITED" || body.Error.Message != message || body.Error.Details == nil {
		t.Fatalf("response = %d %s, want 429 RATE_LIMITED %q", recorder.Code, recorder.Body.String(), message)
	}
}

// TestReportRateLimit checks the per-user report limit.
//
// Expected output:
//
//   - The third report of one user within the window is 429 RATE_LIMITED and
//     never reaches the store, even with a malformed body.
//   - Another user keeps their own allowance; a missing session is still 401.
func TestReportRateLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &reportStoreStub{}
	engine := gin.New()
	controller.NewReports(service.NewReports(store, time.Nanosecond), twoUserSessions{}).
		WithRateLimit(hourLimiter(t, 2)).RegisterRoutes(engine)
	const message = "Too many reports; try again later."

	for i, target := range []string{"44", "45"} {
		if recorder := serve(engine, http.MethodPost, "/api/v1/users/"+target+"/reports", `{"reason":"SPAM"}`, "alice"); recorder.Code != http.StatusCreated {
			t.Fatalf("report %d = %d %s", i, recorder.Code, recorder.Body.String())
		}
	}
	assertLimited(t, serve(engine, http.MethodPost, "/api/v1/users/46/reports", `{"reason":"SPAM"}`, "alice"), message)
	assertLimited(t, serve(engine, http.MethodPost, "/api/v1/users/46/reports", `{`, "alice"), message)
	if store.calls != 2 {
		t.Fatalf("store saw %d reports, want 2", store.calls)
	}
	if recorder := serve(engine, http.MethodPost, "/api/v1/users/46/reports", `{"reason":"SPAM"}`, "bob"); recorder.Code != http.StatusCreated {
		t.Fatalf("another user = %d", recorder.Code)
	}
	if recorder := serve(engine, http.MethodPost, "/api/v1/users/46/reports", `{"reason":"SPAM"}`, ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("no session = %d", recorder.Code)
	}
}

// TestAccountDeletionRateLimit checks the per-user deletion limit.
//
// Expected output:
//
//   - The third deletion request of one user within the window is 429
//     RATE_LIMITED before the body is read, so the coordinator is not called
//     and nothing is deleted.
//   - Another user keeps their own allowance.
func TestAccountDeletionRateLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	deletions := &deletionStub{}
	engine := gin.New()
	controller.NewAccountDeletion(deletions, twoUserSessions{}).WithRateLimit(hourLimiter(t, 2)).RegisterRoutes(engine)
	const message = "Too many account deletion attempts; try again later."
	body := `{"confirm_username":"wrong"}`

	for i := 0; i < 2; i++ {
		if recorder := serve(engine, http.MethodDelete, "/api/v1/users/me", body, "alice"); recorder.Code != http.StatusUnprocessableEntity {
			t.Fatalf("attempt %d = %d %s", i, recorder.Code, recorder.Body.String())
		}
	}
	assertLimited(t, serve(engine, http.MethodDelete, "/api/v1/users/me", body, "alice"), message)
	if deletions.calls != 2 {
		t.Fatalf("coordinator saw %d requests, want 2", deletions.calls)
	}
	if recorder := serve(engine, http.MethodDelete, "/api/v1/users/me", body, "bob"); recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("another user = %d", recorder.Code)
	}
}
