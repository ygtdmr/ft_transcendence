package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/controller"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/middleware/requestid"
	repository "github.com/melmut-42/ft_transcendence/backend/auth-service/internal/repository/postgres"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/server"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
)

type readinessFunc func(context.Context) error

func (f readinessFunc) Ready(ctx context.Context) error { return f(ctx) }

func healthServer(t *testing.T, readiness controller.Readiness) http.Handler {
	t.Helper()
	spy := registrationServiceFunc(func(context.Context, service.RegisterInput) (service.AuthResult, error) {
		t.Error("a health request reached the registration service")
		return service.AuthResult{}, service.ErrInvalidEmail
	})

	return server.New("0", controller.NewAuth(spy), testLogger(t, "text", "info", io.Discard), controller.NewHealth(readiness)).Handler
}

func healthRequest(handler http.Handler, method, path string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	request.Header.Set(requestid.Header, "request-health")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	return recorder
}

func errorCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not the error envelope: %v", recorder.Body.String(), err)
	}

	return body.Error.Code
}

// TestHealthEndpoints checks liveness and readiness on the public listener.
//
// Parameters:
//
//   - t: Go test runner used for subtests, assertions and failure reporting.
//
// Inputs:
//
//   - A readiness check that succeeds, one that fails like an unreachable database (with
//     a driver message carrying a password) and one that fails for an unknown reason.
//
// Flow:
//
//  1. Build the real public server with a stand-in readiness check.
//  2. Request /healthz and /readyz, and the same names under /api/v1/auth.
//
// Expected output:
//
//   - /healthz is 200 {"data":{"status":"OK"}} whatever the readiness check says.
//   - /readyz is 200 when the dependency works, 503 DEPENDENCY_UNAVAILABLE naming only
//     "postgres" when it does not, and 500 INTERNAL_ERROR for an unknown failure.
//   - The driver message never reaches the response.
//   - Neither route exists under /api/v1/auth, and POST is 405.
//   - Every response carries the request ID.
func TestHealthEndpoints(t *testing.T) {
	driver := "failed to connect: password authentication failed for user auth_secret_login"
	cases := []struct {
		name       string
		readiness  error
		path       string
		status     int
		errorCode  string
		dependency string
	}{
		{"liveness while healthy", nil, "/healthz", http.StatusOK, "", ""},
		{"liveness while the database is down", fmt.Errorf("%w: %s", service.ErrUnavailable, driver), "/healthz", http.StatusOK, "", ""},
		{"readiness while healthy", nil, "/readyz", http.StatusOK, "", ""},
		{"readiness while the database is down", fmt.Errorf("%w: %s", service.ErrUnavailable, driver), "/readyz", http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "postgres"},
		{"readiness with an unknown failure", errors.New("boom " + driver), "/readyz", http.StatusInternalServerError, "INTERNAL_ERROR", ""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			handler := healthServer(t, readinessFunc(func(context.Context) error { return tt.readiness }))
			recorder := healthRequest(handler, http.MethodGet, tt.path)
			if recorder.Code != tt.status {
				t.Fatalf("GET %s = %d, want %d (%s)", tt.path, recorder.Code, tt.status, recorder.Body.String())
			}
			if recorder.Header().Get(requestid.Header) != "request-health" {
				t.Errorf("request ID header = %q", recorder.Header().Get(requestid.Header))
			}
			if strings.Contains(recorder.Body.String(), "auth_secret_login") || strings.Contains(recorder.Body.String(), "password") {
				t.Errorf("the response leaks the driver message: %s", recorder.Body.String())
			}
			if tt.errorCode == "" {
				if body := strings.TrimSpace(recorder.Body.String()); body != `{"data":{"status":"OK"}}` {
					t.Errorf("body = %s", body)
				}
				return
			}
			if got := errorCode(t, recorder); got != tt.errorCode {
				t.Errorf("error code = %q, want %q", got, tt.errorCode)
			}
			if tt.dependency != "" && !strings.Contains(recorder.Body.String(), `"dependency":"`+tt.dependency+`"`) {
				t.Errorf("details do not name %s: %s", tt.dependency, recorder.Body.String())
			}
		})
	}

	handler := healthServer(t, readinessFunc(func(context.Context) error { return nil }))
	for _, path := range []string{"/api/v1/auth/healthz", "/api/v1/auth/readyz"} {
		if recorder := healthRequest(handler, http.MethodGet, path); recorder.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404: the probes are not part of the public API", path, recorder.Code)
		}
	}
	if recorder := healthRequest(handler, http.MethodPost, "/readyz"); recorder.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /readyz = %d, want 405", recorder.Code)
	}
}

// TestReadinessFollowsTheDatabase checks readiness against real PostgreSQL.
//
// Parameters:
//
//   - t: Go test runner used for assertions and failure reporting.
//
// Inputs:
//
//   - AUTH_TEST_DATABASE_URL; the test is skipped without it.
//
// Flow:
//
//  1. Open the pool and build the real handler on the real repository.
//  2. Call /readyz and /healthz while the pool works.
//  3. Close the pool, which stands in for an unavailable database, and call both again.
//
// Expected output:
//
//   - 200 for both while the database works.
//   - Afterwards /healthz stays 200 and /readyz is 503 DEPENDENCY_UNAVAILABLE.
func TestReadinessFollowsTheDatabase(t *testing.T) {
	pool := registrationTestPool(t)
	handler := healthServer(t, service.NewHealth(repository.NewHealth(pool)))

	for _, path := range []string{"/readyz", "/healthz"} {
		if recorder := healthRequest(handler, http.MethodGet, path); recorder.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200 while the database works", path, recorder.Code)
		}
	}

	pool.Close()

	if recorder := healthRequest(handler, http.MethodGet, "/healthz"); recorder.Code != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want 200 even without a database", recorder.Code)
	}
	recorder := healthRequest(handler, http.MethodGet, "/readyz")
	if recorder.Code != http.StatusServiceUnavailable || errorCode(t, recorder) != "DEPENDENCY_UNAVAILABLE" {
		t.Fatalf("GET /readyz = %d %s, want 503 DEPENDENCY_UNAVAILABLE without a database", recorder.Code, recorder.Body.String())
	}
}
