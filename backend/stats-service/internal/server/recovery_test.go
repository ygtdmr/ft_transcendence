package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/controller"
)

// TestPanicRecovery checks that a panicking handler becomes a clean 500.
//
// Parameters:
//
//   - t: Go test runner used for assertions and failure reporting.
//
// Expected output:
//
//   - HTTP 500 with code INTERNAL_ERROR in the common envelope; the panic value
//     is neither in the response nor lost: the log has an http_panic record, and
//     the server keeps answering the next request.
func TestPanicRecovery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var logs bytes.Buffer
	engine := newRouter(controller.NewHealth(nil), slog.New(slog.NewTextHandler(&logs, nil)))
	engine.GET("/boom", func(*gin.Context) { panic("secret internal detail") })

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "secret internal detail") {
		t.Fatalf("panic value leaked into the response: %s", recorder.Body.String())
	}
	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || body.Error.Code != "INTERNAL_ERROR" {
		t.Fatalf("body = %q, want INTERNAL_ERROR envelope", recorder.Body.String())
	}
	if !strings.Contains(logs.String(), "http_panic") {
		t.Fatalf("log = %q, want an http_panic record", logs.String())
	}

	again := httptest.NewRecorder()
	engine.ServeHTTP(again, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if again.Code != http.StatusNotFound {
		t.Fatalf("after panic status = %d, want 404 (server must keep serving)", again.Code)
	}
}
