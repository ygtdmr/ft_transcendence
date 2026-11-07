package unit_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/client/userclient"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
)

const authToUserCredential = "auth-to-user-credential-0123456789abcdef"

var provisionRequest = service.ProvisionRequest{
	OperationID: "6b1f0c2e-9a4d-4c7e-8f3b-2d5e7a9c1b40", UserID: 42, Username: "player_one",
	CreatedAt: time.Date(2026, 11, 5, 9, 30, 0, 123456000, time.UTC),
}

// TestUserClientProvision checks the provisioning request and every answer mapping.
//
// Expected output:
//
//   - PUT carries the operation in the path, the canonical body and the Bearer
//     credential; 201 and 200 decode the receipt.
//   - Contract error codes map to their service errors; 5xx, timeouts and
//     malformed receipts are ErrProvisionUnavailable; other answers are rejected.
func TestUserClientProvision(t *testing.T) {
	receipt := `{"data":{"operation_id":"6B1F0C2E-9A4D-4C7E-8F3B-2D5E7A9C1B40","user_id":42,"outcome":"PROVISIONED","completed_at":"2026-11-05T09:30:00.245789Z"}}`
	for _, tt := range []struct {
		name   string
		status int
		body   string
		delay  time.Duration
		want   error
	}{
		{"created", http.StatusCreated, receipt, 0, nil},
		{"replayed", http.StatusOK, receipt, 0, nil},
		{"username taken", http.StatusConflict, `{"error":{"code":"USERNAME_TAKEN"}}`, 0, service.ErrProvisionUsernameTaken},
		{"invalid username", http.StatusUnprocessableEntity, `{"error":{"code":"INVALID_USERNAME"}}`, 0, service.ErrProvisionUsernameInvalid},
		{"already provisioned", http.StatusConflict, `{"error":{"code":"USER_ALREADY_PROVISIONED"}}`, 0, service.ErrProvisionAlreadyProvisioned},
		{"deleted", http.StatusConflict, `{"error":{"code":"USER_DELETED"}}`, 0, service.ErrProvisionUserDeleted},
		{"operation conflict", http.StatusConflict, `{"error":{"code":"OPERATION_CONFLICT"}}`, 0, service.ErrProvisionRejected},
		{"service forbidden", http.StatusForbidden, `{"error":{"code":"SERVICE_FORBIDDEN"}}`, 0, service.ErrProvisionRejected},
		{"validation", http.StatusBadRequest, `{"error":{"code":"VALIDATION_ERROR"}}`, 0, service.ErrProvisionRejected},
		{"unavailable", http.StatusServiceUnavailable, `{}`, 0, service.ErrProvisionUnavailable},
		{"internal error", http.StatusInternalServerError, `{}`, 0, service.ErrProvisionUnavailable},
		{"malformed receipt", http.StatusCreated, `{"data":{}}`, 0, service.ErrProvisionUnavailable},
		{"timeout", http.StatusCreated, receipt, 300 * time.Millisecond, service.ErrProvisionUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if r.Method != http.MethodPut || r.URL.Path != "/internal/v1/provisioning-operations/"+provisionRequest.OperationID ||
					r.Header.Get("Authorization") != "Bearer "+authToUserCredential || len(body) != 3 ||
					body["user_id"] != float64(42) || body["username"] != "player_one" || body["created_at"] != "2026-11-05T09:30:00.123456Z" {
					t.Errorf("unexpected request %s %s body=%v", r.Method, r.URL.Path, body)
				}
				time.Sleep(tt.delay)
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			client, err := userclient.New(server.URL, authToUserCredential, 100*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.Provision(context.Background(), provisionRequest)
			if tt.want == nil {
				if err != nil || got.OperationID != provisionRequest.OperationID || got.UserID != 42 {
					t.Fatalf("Provision() = %+v, %v", got, err)
				}
				return
			}
			if !errors.Is(err, tt.want) || strings.Contains(err.Error(), authToUserCredential) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

// TestUserClientReceiptByUser checks the receipt lookup and an unreachable User Service.
func TestUserClientReceiptByUser(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/internal/v1/provisioning-operations" || r.URL.RawQuery != "user_id=42" {
			t.Errorf("unexpected request %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"data":{"operation_id":"6b1f0c2e-9a4d-4c7e-8f3b-2d5e7a9c1b40","user_id":42,"outcome":"PROVISIONED","completed_at":"2026-11-05T09:30:00.245789Z"}}`))
	}))
	client, _ := userclient.New(server.URL, authToUserCredential, time.Second)
	if got, err := client.ReceiptByUser(context.Background(), 42); err != nil || got.OperationID != provisionRequest.OperationID {
		t.Fatalf("ReceiptByUser() = %+v, %v", got, err)
	}
	server.Close()
	if _, err := client.ReceiptByUser(context.Background(), 42); !errors.Is(err, service.ErrProvisionUnavailable) {
		t.Fatalf("closed server error = %v, want ErrProvisionUnavailable", err)
	}
}
