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

const userToAuthCredential = "user-to-auth-credential-0123456789abcdef"

const accountJSON = `{"data":{"user_id":42,"email":"player@example.com","status":"ACTIVE","created_at":"2026-11-05T09:30:00.123456Z"}}`

// authStub answers every request with status and body after checking the request shape.
func authStub(t *testing.T, status int, body string, delay time.Duration) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/internal/v1/accounts/42" ||
			r.Header.Get("Authorization") != "Bearer "+userToAuthCredential {
			t.Errorf("unexpected request %s %s auth=%t", r.Method, r.URL.Path, r.Header.Get("Authorization") != "")
		}
		time.Sleep(delay)
		if status == http.StatusFound {
			w.Header().Set("Location", "https://elsewhere.example/steal")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	return server
}

// TestAuthClientAccountMetadata checks how Auth responses map to client results.
//
// Expected output:
//
//   - 200 decodes the account; 404 is ErrAccountNotFound.
//   - 5xx, timeouts, refused connections and malformed or mismatched bodies are
//     ErrUnavailable, never ErrAccountNotFound.
//   - 401 and 403 are ErrMisconfigured; redirects are not followed.
func TestAuthClientAccountMetadata(t *testing.T) {
	ok := authStub(t, http.StatusOK, accountJSON, 0)
	client, err := authclient.New(ok.URL, userToAuthCredential, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	account, err := client.AccountMetadata(context.Background(), 42)
	if err != nil || account.UserID != 42 || account.Email != "player@example.com" || account.Status != authclient.StatusActive ||
		!account.CreatedAt.Equal(time.Date(2026, 11, 5, 9, 30, 0, 123456000, time.UTC)) {
		t.Fatalf("AccountMetadata() = %+v, %v", account, err)
	}

	for _, tt := range []struct {
		name   string
		status int
		body   string
		delay  time.Duration
		want   error
	}{
		{"not found", http.StatusNotFound, `{"error":{"code":"USER_NOT_FOUND","message":"x","details":{"user_id":42}}}`, 0, authclient.ErrAccountNotFound},
		{"service unauthenticated", http.StatusUnauthorized, `{}`, 0, authclient.ErrMisconfigured},
		{"service forbidden", http.StatusForbidden, `{}`, 0, authclient.ErrMisconfigured},
		{"internal error", http.StatusInternalServerError, `{}`, 0, authclient.ErrUnavailable},
		{"unavailable", http.StatusServiceUnavailable, `{}`, 0, authclient.ErrUnavailable},
		{"malformed body", http.StatusOK, `{"data":`, 0, authclient.ErrUnavailable},
		{"other user", http.StatusOK, strings.Replace(accountJSON, `"user_id":42`, `"user_id":43`, 1), 0, authclient.ErrUnavailable},
		{"unknown status", http.StatusOK, strings.Replace(accountJSON, "ACTIVE", "BANNED", 1), 0, authclient.ErrUnavailable},
		{"timeout", http.StatusOK, accountJSON, 300 * time.Millisecond, authclient.ErrUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stub := authStub(t, tt.status, tt.body, tt.delay)
			client, err := authclient.New(stub.URL, userToAuthCredential, 100*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.AccountMetadata(context.Background(), 42); !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}

	redirect := authStub(t, http.StatusFound, ``, 0)
	client, _ = authclient.New(redirect.URL, userToAuthCredential, time.Second)
	if _, err := client.AccountMetadata(context.Background(), 42); err == nil ||
		errors.Is(err, authclient.ErrAccountNotFound) || errors.Is(err, authclient.ErrUnavailable) {
		t.Fatalf("redirect error = %v, want an unexpected-response error", err)
	}

	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	client, _ = authclient.New(closed.URL, userToAuthCredential, time.Second)
	_, err = client.AccountMetadata(context.Background(), 42)
	if !errors.Is(err, authclient.ErrUnavailable) || strings.Contains(err.Error(), userToAuthCredential) {
		t.Fatalf("refused connection error = %v, want ErrUnavailable without the credential", err)
	}
}

// TestAuthClientRejectsBadSettings checks constructor validation.
func TestAuthClientRejectsBadSettings(t *testing.T) {
	for _, tt := range []struct {
		base, credential string
		timeout          time.Duration
	}{
		{"auth-service:8090", userToAuthCredential, time.Second},
		{"http://auth-service:8090/internal", userToAuthCredential, time.Second},
		{"http://auth-service:8090", "", time.Second},
		{"http://auth-service:8090", userToAuthCredential, 0},
	} {
		if _, err := authclient.New(tt.base, tt.credential, tt.timeout); err == nil {
			t.Errorf("New(%q, credential=%t, %v) accepted", tt.base, tt.credential != "", tt.timeout)
		}
	}
}
