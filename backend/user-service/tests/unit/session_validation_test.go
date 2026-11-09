package unit_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/client/authclient"
)

// TestValidateSession checks the Auth call and how each answer is classified.
//
// Expected output:
//
//   - The session travels only as a Cookie, next to the service credential.
//   - 200 returns the user id; 401 UNAUTHORIZED is an invalid session.
//   - 401 SERVICE_UNAUTHENTICATED and 403 are configuration faults.
//   - 5xx, malformed bodies and network failures are unavailable, never invalid.
func TestValidateSession(t *testing.T) {
	credential := strings.Repeat("c", 40)
	for _, tt := range []struct {
		name   string
		status int
		body   string
		user   int64
		want   error
	}{
		{"valid", http.StatusOK, `{"data":{"user_id":42,"session_id":"s","session_expires_at":"2026-11-09T00:00:00Z"}}`, 42, nil},
		{"invalid session", http.StatusUnauthorized, `{"error":{"code":"UNAUTHORIZED"}}`, 0, authclient.ErrSessionInvalid},
		{"bad service credential", http.StatusUnauthorized, `{"error":{"code":"SERVICE_UNAUTHENTICATED"}}`, 0, authclient.ErrMisconfigured},
		{"missing scope", http.StatusForbidden, `{"error":{"code":"SERVICE_FORBIDDEN"}}`, 0, authclient.ErrMisconfigured},
		{"outage", http.StatusServiceUnavailable, `{}`, 0, authclient.ErrUnavailable},
		{"internal error", http.StatusInternalServerError, `{}`, 0, authclient.ErrUnavailable},
		{"malformed body", http.StatusOK, `{"data":`, 0, authclient.ErrUnavailable},
		{"no user", http.StatusOK, `{"data":{"user_id":0}}`, 0, authclient.ErrUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				cookie, err := r.Cookie("ft_session")
				if r.URL.Path != "/internal/v1/sessions/current" || r.Method != http.MethodGet || err != nil || cookie.Value != "browser-token" ||
					r.Header.Get("Authorization") != "Bearer "+credential || r.URL.RawQuery != "" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			client, err := authclient.New(server.URL, credential, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			user, err := client.ValidateSession(context.Background(), "browser-token")
			if user != tt.user || !errors.Is(err, tt.want) {
				t.Fatalf("ValidateSession = %d, %v; want %d, %v", user, err, tt.user, tt.want)
			}
		})
	}

	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()
	client, err := authclient.New(url, credential, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ValidateSession(context.Background(), "browser-token"); !errors.Is(err, authclient.ErrUnavailable) {
		t.Fatalf("network failure = %v, want ErrUnavailable", err)
	}
}
