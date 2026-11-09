package integration_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/controller"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/middleware/serviceauth"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/server"
)

var (
	gatewayCredential = strings.Repeat("g", 40)
	userCredential    = strings.Repeat("u", 40)
)

type internalFixture struct {
	postgresSessionFixture
	internal http.Handler
	public   http.Handler
}

// newInternalFixture registers one account on PostgreSQL and builds both listeners.
func newInternalFixture(t *testing.T) internalFixture {
	t.Helper()
	f := newPostgresSession(t)
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	auth := serviceauth.New(map[string][]string{
		"game-gateway": {gatewayCredential},
		"user-service": {userCredential},
	}, serviceauth.DefaultMatrix())
	internal := server.NewInternal("0", logger, controller.NewInternalReads(f.auth, auth)).Handler
	public := server.New("0", controller.NewAuth(f.auth), logger).Handler

	return internalFixture{postgresSessionFixture: f, internal: internal, public: public}
}

// internalCall sends one private request with an optional credential and ft_session cookie.
func internalCall(handler http.Handler, path, credential, session string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if credential != "" {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	if session != "" {
		request.AddCookie(&http.Cookie{Name: "ft_session", Value: session})
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	return recorder
}

func internalBody(t *testing.T, recorder *httptest.ResponseRecorder) (map[string]any, string, map[string]any) {
	t.Helper()
	var body struct {
		Data  map[string]any `json:"data"`
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", recorder.Body, err)
	}

	return body.Data, body.Error.Code, body.Error.Details
}

// TestInternalValidateSessionPostgres checks session validation against real records.
//
// Expected output:
//
//   - An active session on an ACTIVE account returns user_id, session_id and the
//     access expiry, with no profile or email data.
//   - Missing, malformed or revoked credentials and PROVISIONING or DELETING
//     accounts return 401 UNAUTHORIZED with empty details.
func TestInternalValidateSessionPostgres(t *testing.T) {
	f := newInternalFixture(t)
	access := f.original.Credentials.AccessToken
	const path = "/internal/v1/sessions/current"

	ok := internalCall(f.internal, path, gatewayCredential, access)
	data, _, _ := internalBody(t, ok)
	if ok.Code != http.StatusOK || data["user_id"] != float64(f.original.User.ID) ||
		data["session_id"] != f.original.Credentials.Session.ID || len(data) != 3 {
		t.Fatalf("valid session = %d %s", ok.Code, ok.Body)
	}
	expires, err := time.Parse(time.RFC3339Nano, data["session_expires_at"].(string))
	if err != nil || !expires.Equal(f.original.Credentials.AccessExpiresAt) || !strings.HasSuffix(data["session_expires_at"].(string), "Z") {
		t.Fatalf("session_expires_at = %v, want %v in UTC", data["session_expires_at"], f.original.Credentials.AccessExpiresAt)
	}

	for name, session := range map[string]string{"missing": "", "malformed": "not-a-jwt"} {
		if response := internalCall(f.internal, path, gatewayCredential, session); response.Code != http.StatusUnauthorized {
			t.Fatalf("%s session = %d %s", name, response.Code, response.Body)
		}
	}

	for _, status := range []string{"PROVISIONING", "DELETING"} {
		if _, err := f.pool.Exec(f.ctx, "UPDATE users SET status = $1", status); err != nil {
			t.Fatal(err)
		}
		response := internalCall(f.internal, path, gatewayCredential, access)
		_, code, details := internalBody(t, response)
		if response.Code != http.StatusUnauthorized || code != "UNAUTHORIZED" || len(details) != 0 {
			t.Fatalf("%s account = %d %s, want 401 UNAUTHORIZED", status, response.Code, response.Body)
		}
	}
	if _, err := f.pool.Exec(f.ctx, "UPDATE users SET status = 'ACTIVE'"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, "UPDATE sessions SET revoked_at = now()"); err != nil {
		t.Fatal(err)
	}
	if response := internalCall(f.internal, path, gatewayCredential, access); response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session = %d %s, want 401", response.Code, response.Body)
	}
}

// TestInternalAccountMetadataPostgres checks account metadata reads.
//
// Expected output:
//
//   - user_id, email, status and created_at for an existing account, never the
//     password hash; the status follows the stored value.
//   - 404 USER_NOT_FOUND with user_id for an unknown account; 422 VALIDATION_ERROR
//     naming user_id for a non-positive or non-numeric id.
func TestInternalAccountMetadataPostgres(t *testing.T) {
	f := newInternalFixture(t)
	user := f.original.User
	path := "/internal/v1/accounts/" + strconv.FormatInt(user.ID, 10)

	response := internalCall(f.internal, path, userCredential, "")
	data, _, _ := internalBody(t, response)
	if response.Code != http.StatusOK || data["user_id"] != float64(user.ID) || data["email"] != user.Email ||
		data["status"] != "ACTIVE" || len(data) != 4 || strings.Contains(response.Body.String(), "password") {
		t.Fatalf("metadata = %d %s", response.Code, response.Body)
	}
	created, err := time.Parse(time.RFC3339Nano, data["created_at"].(string))
	if err != nil || !created.Equal(user.CreatedAt) {
		t.Fatalf("created_at = %v, want %v", data["created_at"], user.CreatedAt)
	}

	if _, err := f.pool.Exec(f.ctx, "UPDATE users SET status = 'DELETING'"); err != nil {
		t.Fatal(err)
	}
	if data, _, _ := internalBody(t, internalCall(f.internal, path, userCredential, "")); data["status"] != "DELETING" {
		t.Fatalf("status = %v, want DELETING", data["status"])
	}

	missing := internalCall(f.internal, "/internal/v1/accounts/999999", userCredential, "")
	_, code, details := internalBody(t, missing)
	if missing.Code != http.StatusNotFound || code != "USER_NOT_FOUND" || details["user_id"] != float64(999999) {
		t.Fatalf("missing account = %d %s", missing.Code, missing.Body)
	}
	for _, raw := range []string{"0", "-4", "abc", "1.5"} {
		invalid := internalCall(f.internal, "/internal/v1/accounts/"+raw, userCredential, "")
		_, code, details := internalBody(t, invalid)
		if invalid.Code != http.StatusUnprocessableEntity || code != "VALIDATION_ERROR" || details["field"] != "user_id" {
			t.Fatalf("user_id %q = %d %s", raw, invalid.Code, invalid.Body)
		}
	}
}

// TestInternalReadsAuthorizationPostgres checks the authorization matrix and listener split.
//
// Expected output:
//
//   - Missing or unknown credentials get 401 SERVICE_UNAUTHENTICATED; a caller
//     using another caller's operation gets 403 SERVICE_FORBIDDEN.
//   - A service credential is never accepted as a user session.
//   - The public listener does not serve /internal routes.
func TestInternalReadsAuthorizationPostgres(t *testing.T) {
	f := newInternalFixture(t)
	access := f.original.Credentials.AccessToken
	session := "/internal/v1/sessions/current"
	account := "/internal/v1/accounts/" + strconv.FormatInt(f.original.User.ID, 10)

	for _, tt := range []struct {
		name, path, credential, session string
		status                          int
		code                            string
	}{
		{"no credential", session, "", access, http.StatusUnauthorized, "SERVICE_UNAUTHENTICATED"},
		{"unknown credential", account, strings.Repeat("x", 40), "", http.StatusUnauthorized, "SERVICE_UNAUTHENTICATED"},
		{"gateway reading accounts", account, gatewayCredential, "", http.StatusForbidden, "SERVICE_FORBIDDEN"},
		{"service credential as session", session, gatewayCredential, gatewayCredential, http.StatusUnauthorized, "UNAUTHORIZED"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response := internalCall(f.internal, tt.path, tt.credential, tt.session)
			_, code, _ := internalBody(t, response)
			if response.Code != tt.status || code != tt.code {
				t.Fatalf("got %d %s, want %d %s", response.Code, response.Body, tt.status, tt.code)
			}
		})
	}
	for _, path := range []string{session, account} {
		if response := internalCall(f.public, path, gatewayCredential, access); response.Code != http.StatusNotFound {
			t.Fatalf("public %s = %d, want 404", path, response.Code)
		}
	}
}
