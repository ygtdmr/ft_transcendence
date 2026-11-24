package unit_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/client/authclient"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/client/gameclient"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/client/statsclient"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

const deletionOperation = "3f9b2c71-5d4e-4a86-9c1b-7e2a6d0f8b53"

// participantServer answers one request after checking method, path, credential and body.
func participantServer(t *testing.T, path, credential, wantBody string, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPut || r.URL.Path != path || r.Header.Get("Authorization") != "Bearer "+credential ||
			strings.TrimSpace(string(raw)) != wantBody {
			t.Errorf("request = %s %s body=%q", r.Method, r.URL.Path, raw)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func receipt(state string, userID int64) string {
	raw, _ := json.Marshal(map[string]any{"data": map[string]any{
		"operation_id": strings.ToUpper(deletionOperation), "user_id": userID, "state": state,
		"begun_at": "2026-11-19T10:00:00Z", "completed_at": nil,
	}})
	return string(raw)
}

// TestAuthDeletionClient checks Begin and Complete requests and answer mapping.
//
// Expected output: 201/200 receipts for the operation succeed; 5xx and
// malformed receipts are ErrUnavailable; 401/403 are ErrMisconfigured;
// conflicts, missing records and foreign receipts are ErrRejected; Complete
// requires COMPLETED and sends no body.
func TestAuthDeletionClient(t *testing.T) {
	credential := strings.Repeat("a", 40)
	beginPath := "/internal/v1/account-deletions/" + deletionOperation
	for _, tt := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"created", http.StatusCreated, receipt("BEGUN", 42), nil},
		{"replayed completed", http.StatusOK, receipt("COMPLETED", 42), nil},
		{"other user", http.StatusOK, receipt("BEGUN", 43), authclient.ErrRejected},
		{"malformed", http.StatusCreated, `{"data":null}`, authclient.ErrUnavailable},
		{"unavailable", http.StatusServiceUnavailable, `{}`, authclient.ErrUnavailable},
		{"forbidden", http.StatusForbidden, `{}`, authclient.ErrMisconfigured},
		{"user not found", http.StatusNotFound, `{"error":{"code":"USER_NOT_FOUND"}}`, authclient.ErrRejected},
		{"deletion conflict", http.StatusConflict, `{"error":{"code":"DELETION_CONFLICT"}}`, authclient.ErrRejected},
	} {
		t.Run("begin "+tt.name, func(t *testing.T) {
			server := participantServer(t, beginPath, credential, `{"user_id":42}`, tt.status, tt.body)
			defer server.Close()
			client, _ := authclient.New(server.URL, credential, time.Second)
			err := client.BeginDeletion(context.Background(), deletionOperation, 42)
			if (tt.want == nil && err != nil) || (tt.want != nil && !errors.Is(err, tt.want)) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
	for _, tt := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"completed", http.StatusOK, receipt("COMPLETED", 42), nil},
		{"still begun", http.StatusOK, receipt("BEGUN", 42), authclient.ErrRejected},
		{"not found", http.StatusNotFound, `{"error":{"code":"DELETION_NOT_FOUND"}}`, authclient.ErrRejected},
		{"internal error", http.StatusInternalServerError, `{}`, authclient.ErrUnavailable},
	} {
		t.Run("complete "+tt.name, func(t *testing.T) {
			server := participantServer(t, beginPath+"/completion", credential, "", tt.status, tt.body)
			defer server.Close()
			client, _ := authclient.New(server.URL, credential, time.Second)
			err := client.CompleteDeletion(context.Background(), deletionOperation)
			if (tt.want == nil && err != nil) || (tt.want != nil && !errors.Is(err, tt.want)) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

// TestCleanupClients checks the Stats and Game Session cleanup requests.
//
// Expected output: a COMPLETED receipt for the operation and user succeeds;
// outages and malformed receipts are retryable; conflicts, refused
// credentials and foreign receipts are permanent.
func TestCleanupClients(t *testing.T) {
	credential := strings.Repeat("c", 40)
	path := "/internal/v1/account-cleanups/" + deletionOperation
	type cleaner func(url string) func(context.Context, string, int64) error
	clients := map[string]struct {
		build                 cleaner
		unavailable, rejected error
		misconfigured         error
	}{
		"stats": {func(url string) func(context.Context, string, int64) error {
			client, _ := statsclient.New(url, credential, time.Second)
			return client.CleanUpAccount
		}, statsclient.ErrUnavailable, statsclient.ErrRejected, statsclient.ErrMisconfigured},
		"game": {func(url string) func(context.Context, string, int64) error {
			client, _ := gameclient.New(url, credential, time.Second)
			return client.CleanUpAccount
		}, service.ErrUnavailable, service.ErrParticipantRejected, service.ErrParticipantRejected},
	}
	for name, c := range clients {
		for _, tt := range []struct {
			name   string
			status int
			body   string
			want   error
		}{
			{"created", http.StatusCreated, receipt("COMPLETED", 42), nil},
			{"replayed", http.StatusOK, receipt("COMPLETED", 42), nil},
			{"other user", http.StatusOK, receipt("COMPLETED", 43), c.rejected},
			{"malformed", http.StatusOK, `nope`, c.unavailable},
			{"unavailable", http.StatusServiceUnavailable, `{}`, c.unavailable},
			{"conflict", http.StatusConflict, `{"error":{"code":"DELETION_CONFLICT"}}`, c.rejected},
			{"unauthenticated", http.StatusUnauthorized, `{}`, c.misconfigured},
		} {
			t.Run(name+" "+tt.name, func(t *testing.T) {
				server := participantServer(t, path, credential, `{"user_id":42}`, tt.status, tt.body)
				defer server.Close()
				err := c.build(server.URL)(context.Background(), deletionOperation, 42)
				if (tt.want == nil && err != nil) || (tt.want != nil && !errors.Is(err, tt.want)) {
					t.Fatalf("err = %v, want %v", err, tt.want)
				}
				if err != nil && strings.Contains(err.Error(), credential) {
					t.Fatal("error leaks the credential")
				}
			})
		}
	}
}
