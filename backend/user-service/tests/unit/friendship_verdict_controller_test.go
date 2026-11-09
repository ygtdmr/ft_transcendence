package unit_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/controller"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/middleware/serviceauth"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

type verdictReaderStub struct {
	active bool
	err    error
	calls  int
}

func (s *verdictReaderStub) ActiveFriendship(context.Context, int64, int64) (bool, error) {
	s.calls++
	return s.active, s.err
}

func verdictRequest(reader *verdictReaderStub, query string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	credential := strings.Repeat("v", 40)
	auth := serviceauth.New(map[string][]string{"channel-service": {credential}}, serviceauth.DefaultMatrix())
	engine := gin.New()
	controller.NewInternalFriendshipVerdict(service.NewFriendshipVerdicts(reader), auth).RegisterRoutes(engine)
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/friendship-verdicts"+query, nil)
	request.Header.Set("Authorization", "Bearer "+credential)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

// TestFriendshipVerdictValidation checks the order and result of each refusal.
//
// Expected output: a missing, unknown or repeated parameter is 400, a bad value
// or equal ids is 422, each names its field, and no refusal reaches the records.
func TestFriendshipVerdictValidation(t *testing.T) {
	for _, tt := range []struct {
		name, query, field string
		status             int
	}{
		{"no parameters", "", "actor_user_id", http.StatusBadRequest},
		{"missing target", "?actor_user_id=1", "target_user_id", http.StatusBadRequest},
		{"empty actor", "?actor_user_id=&target_user_id=2", "actor_user_id", http.StatusBadRequest},
		{"unknown parameter", "?actor_user_id=1&target_user_id=2&extra=1", "extra", http.StatusBadRequest},
		{"repeated actor", "?actor_user_id=1&actor_user_id=3&target_user_id=2", "actor_user_id", http.StatusBadRequest},
		{"unknown wins over bad value", "?actor_user_id=x&target_user_id=2&extra=1", "extra", http.StatusBadRequest},
		{"text actor", "?actor_user_id=x&target_user_id=2", "actor_user_id", http.StatusUnprocessableEntity},
		{"text target", "?actor_user_id=1&target_user_id=x", "target_user_id", http.StatusUnprocessableEntity},
		{"zero actor", "?actor_user_id=0&target_user_id=2", "actor_user_id", http.StatusUnprocessableEntity},
		{"negative target", "?actor_user_id=1&target_user_id=-2", "target_user_id", http.StatusUnprocessableEntity},
		{"overflow", "?actor_user_id=99999999999999999999&target_user_id=2", "actor_user_id", http.StatusUnprocessableEntity},
		{"same user", "?actor_user_id=42&target_user_id=42", "target_user_id", http.StatusUnprocessableEntity},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reader := &verdictReaderStub{}
			recorder := verdictRequest(reader, tt.query)
			var body struct {
				Error struct {
					Code    string         `json:"code"`
					Message string         `json:"message"`
					Details map[string]any `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("body %q: %v", recorder.Body.String(), err)
			}
			if recorder.Code != tt.status || body.Error.Code != "VALIDATION_ERROR" || body.Error.Details["field"] != tt.field {
				t.Fatalf("response = %d %s, want %d field %s", recorder.Code, recorder.Body.String(), tt.status, tt.field)
			}
			if tt.name == "same user" && body.Error.Message != "target_user_id must differ from actor_user_id." {
				t.Fatalf("message = %q", body.Error.Message)
			}
			if reader.calls != 0 {
				t.Fatal("a refused request reached the records")
			}
		})
	}
}

// TestFriendshipVerdictAnswers checks the two verdicts and failure handling.
//
// Expected output: true and false are 200; an outage is 503 and an unexpected
// failure is 500, and neither body holds a verdict.
func TestFriendshipVerdictAnswers(t *testing.T) {
	const query = "?actor_user_id=1&target_user_id=2"
	for _, active := range []bool{true, false} {
		recorder := verdictRequest(&verdictReaderStub{active: active}, query)
		want := fmt.Sprintf(`{"data":{"active_friendship":%t}}`, active)
		if recorder.Code != http.StatusOK || recorder.Body.String() != want {
			t.Fatalf("verdict = %d %s, want %s", recorder.Code, recorder.Body.String(), want)
		}
	}
	for _, tt := range []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("%w: test", service.ErrUnavailable), http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE"},
		{fmt.Errorf("unexpected"), http.StatusInternalServerError, "INTERNAL_ERROR"},
	} {
		recorder := verdictRequest(&verdictReaderStub{err: tt.err}, query)
		if recorder.Code != tt.status || !strings.Contains(recorder.Body.String(), tt.code) ||
			strings.Contains(recorder.Body.String(), "active_friendship") {
			t.Fatalf("failure = %d %s, want %d %s", recorder.Code, recorder.Body.String(), tt.status, tt.code)
		}
	}
}
