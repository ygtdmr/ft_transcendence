package unit_test

import (
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

func presenceRequest(presence service.PresenceReader, caller, query string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	credentials := map[string][]string{"web-bff": {strings.Repeat("b", 40)}, "game-session": {strings.Repeat("s", 40)}}
	auth := serviceauth.New(credentials, serviceauth.DefaultMatrix())
	engine := gin.New()
	controller.NewInternalPresence(presence, auth).RegisterRoutes(engine)
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/presence"+query, nil)
	if values := credentials[caller]; len(values) > 0 {
		request.Header.Set("Authorization", "Bearer "+values[0])
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

// TestInternalPresenceRead checks the private presence read.
//
// Expected output: only web-bff is admitted; IDs are distinct and ascending;
// invalid IDs are 422, an unknown parameter is 400, and unknown presence is 503.
func TestInternalPresenceRead(t *testing.T) {
	presence := presenceStub{online: map[int64]bool{42: true}}

	ok := presenceRequest(presence, "web-bff", "?user_id=43&user_id=42&user_id=43")
	want := `{"data":{"presence":[{"is_online":true,"user_id":42},{"is_online":false,"user_id":43}]}}`
	if ok.Code != http.StatusOK || ok.Body.String() != want {
		t.Fatalf("read = %d %s", ok.Code, ok.Body.String())
	}

	tooMany := "?user_id=1"
	for i := 2; i <= 101; i++ {
		tooMany += fmt.Sprintf("&user_id=%d", i)
	}
	for _, tt := range []struct {
		name, caller, query, code string
		status                    int
	}{
		{"no credential", "", "?user_id=1", "SERVICE_UNAUTHENTICATED", http.StatusUnauthorized},
		{"other caller", "game-session", "?user_id=1", "SERVICE_FORBIDDEN", http.StatusForbidden},
		{"missing user_id", "web-bff", "", "VALIDATION_ERROR", http.StatusUnprocessableEntity},
		{"zero", "web-bff", "?user_id=0", "VALIDATION_ERROR", http.StatusUnprocessableEntity},
		{"text", "web-bff", "?user_id=x", "VALIDATION_ERROR", http.StatusUnprocessableEntity},
		{"over 100", "web-bff", tooMany, "VALIDATION_ERROR", http.StatusUnprocessableEntity},
		{"unknown parameter", "web-bff", "?user_id=1&room_id=2", "VALIDATION_ERROR", http.StatusBadRequest},
	} {
		recorder := presenceRequest(presence, tt.caller, tt.query)
		if recorder.Code != tt.status || !strings.Contains(recorder.Body.String(), `"code":"`+tt.code+`"`) {
			t.Fatalf("%s = %d %s", tt.name, recorder.Code, recorder.Body.String())
		}
	}

	unknown := presenceRequest(presenceStub{err: fmt.Errorf("%w: registry", service.ErrUnavailable)}, "web-bff", "?user_id=1")
	if unknown.Code != http.StatusServiceUnavailable || strings.Contains(unknown.Body.String(), "is_online") {
		t.Fatalf("unknown = %d %s", unknown.Code, unknown.Body.String())
	}
}
