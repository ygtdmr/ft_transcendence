package unit_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/client/gameclient"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/client/gatewayclient"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
)

const departureSession = "3F9B2C71-5D4E-4A86-9C1B-7E2A6D0F8B53"

func departureReceipt(sessionID string, userID int64, state string) string {
	raw, _ := json.Marshal(map[string]any{"data": map[string]any{
		"session_id": sessionID, "user_id": userID, "state": state, "completed_at": "2026-11-24T14:05:00.123456Z",
	}})
	return string(raw)
}

// TestGameClientSessionDeparture checks the departure request, its retries
// and every answer mapping.
//
// Expected output:
//
//   - PUT carries the lowercase session id in the path, {"user_id"} as the
//     body and the Bearer credential.
//   - 201 and a replayed 200 with a matching COMPLETED receipt succeed.
//   - 500, 503 and timeouts are retried with the identical request; three
//     failures, or a receipt for another session or user, are
//     ErrServiceUnavailable.
//   - 400, 401, 403, 409 and 422 are plain errors sent once.
//   - No error message contains the credential.
func TestGameClientSessionDeparture(t *testing.T) {
	lower := strings.ToLower(departureSession)
	ok := departureReceipt(lower, 42, "COMPLETED")
	for _, tt := range []struct {
		name     string
		answers  []int
		body     string
		delay    time.Duration
		want     error
		attempts int32
	}{
		{"applied", []int{http.StatusCreated}, ok, 0, nil, 1},
		{"replayed", []int{http.StatusOK}, ok, 0, nil, 1},
		{"retried then applied", []int{http.StatusServiceUnavailable, http.StatusInternalServerError, http.StatusCreated}, ok, 0, nil, 3},
		{"unavailable", []int{http.StatusServiceUnavailable}, `{"error":{"code":"SERVICE_UNAVAILABLE"}}`, 0, service.ErrServiceUnavailable, 3},
		{"timeout", []int{http.StatusCreated}, ok, 300 * time.Millisecond, service.ErrServiceUnavailable, 3},
		{"other session", []int{http.StatusCreated}, departureReceipt("00000000-0000-4000-8000-000000000001", 42, "COMPLETED"), 0, service.ErrServiceUnavailable, 1},
		{"other user", []int{http.StatusCreated}, departureReceipt(lower, 43, "COMPLETED"), 0, service.ErrServiceUnavailable, 1},
		{"not completed", []int{http.StatusCreated}, departureReceipt(lower, 42, "PENDING"), 0, service.ErrServiceUnavailable, 1},
		{"not json", []int{http.StatusCreated}, `ok`, 0, service.ErrServiceUnavailable, 1},
		{"conflict", []int{http.StatusConflict}, `{"error":{"code":"OPERATION_CONFLICT"}}`, 0, errors.New("plain"), 1},
		{"unauthenticated", []int{http.StatusUnauthorized}, `{"error":{"code":"SERVICE_UNAUTHENTICATED"}}`, 0, errors.New("plain"), 1},
		{"forbidden", []int{http.StatusForbidden}, `{"error":{"code":"SERVICE_FORBIDDEN"}}`, 0, errors.New("plain"), 1},
		{"invalid", []int{http.StatusUnprocessableEntity}, `{"error":{"code":"VALIDATION_ERROR"}}`, 0, errors.New("plain"), 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := attempts.Add(1)
				body, _ := io.ReadAll(r.Body)
				if r.Method != http.MethodPut || r.URL.Path != "/internal/v1/session-departures/"+lower ||
					r.Header.Get("Authorization") != "Bearer "+authToGameCredential ||
					r.Header.Get("Content-Type") != "application/json" || string(body) != `{"user_id":42}` {
					t.Errorf("unexpected request %s %s %q", r.Method, r.URL.Path, body)
				}
				time.Sleep(tt.delay)
				status := tt.answers[min(int(n), len(tt.answers))-1]
				w.WriteHeader(status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			client, err := gameclient.New(server.URL, authToGameCredential, 100*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}

			err = client.EndSession(context.Background(), 42, departureSession)
			switch {
			case tt.want == nil && err != nil:
				t.Fatalf("EndSession: %v", err)
			case tt.want == nil:
			case errors.Is(tt.want, service.ErrServiceUnavailable) && !errors.Is(err, service.ErrServiceUnavailable):
				t.Fatalf("error = %v, want ErrServiceUnavailable", err)
			case !errors.Is(tt.want, service.ErrServiceUnavailable) && (err == nil || errors.Is(err, service.ErrServiceUnavailable)):
				t.Fatalf("error = %v, want a plain error", err)
			}
			if err != nil && strings.Contains(err.Error(), authToGameCredential) {
				t.Fatal("error exposes the credential")
			}
			if got := attempts.Load(); got != tt.attempts {
				t.Fatalf("attempts = %d, want %d", got, tt.attempts)
			}
		})
	}
}

// TestGameClientSessionDepartureStopsOnCancel checks that a cancelled logout
// stops retrying and reports Game Session as unavailable.
func TestGameClientSessionDepartureStopsOnCancel(t *testing.T) {
	var attempts atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		cancel()
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client, err := gameclient.New(server.URL, authToGameCredential, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.EndSession(ctx, 42, departureSession); !errors.Is(err, service.ErrServiceUnavailable) {
		t.Fatalf("cancelled departure = %v", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts after cancel = %d", got)
	}
}

const (
	authToGameGatewayCredential         = "auth-to-game-gateway-credential-0123456789"
	authToNotificationGatewayCredential = "auth-to-notification-gateway-credential-01"
)

type revocationRequest struct {
	path, authorization, body string
}

// TestGatewayClientSessionRevoked checks that every instance receives the
// revocation and that each failure is reported.
//
// Expected output:
//
//   - Every instance gets PUT /internal/v1/session-revocations/{lowercase id}
//     with {"user_id","cause"} and its own kind's Bearer credential.
//   - 204 from every instance succeeds.
//   - Any other answer, a timeout or an unreachable instance fails the call,
//     naming the instance but never a credential, while the other instances
//     are still notified.
func TestGatewayClientSessionRevoked(t *testing.T) {
	var mu sync.Mutex
	received := map[string][]revocationRequest{}
	instance := func(name string, status int, delay time.Duration) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			received[name] = append(received[name], revocationRequest{r.URL.Path, r.Header.Get("Authorization"), string(body)})
			mu.Unlock()
			if r.Method != http.MethodPut || r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("unexpected request %s", r.Method)
			}
			time.Sleep(delay)
			w.WriteHeader(status)
		}))
	}
	game1 := instance("game-1", http.StatusNoContent, 0)
	defer game1.Close()
	game2 := instance("game-2", http.StatusNoContent, 0)
	defer game2.Close()
	notification := instance("notification", http.StatusNoContent, 0)
	defer notification.Close()
	failing := instance("failing", http.StatusInternalServerError, 0)
	defer failing.Close()
	slow := instance("slow", http.StatusNoContent, 300*time.Millisecond)
	defer slow.Close()
	unreachable := httptest.NewServer(http.NotFoundHandler())
	unreachableURL := unreachable.URL
	unreachable.Close()

	newClient := func(extra ...gatewayclient.Instance) *gatewayclient.Client {
		instances := append([]gatewayclient.Instance{
			{URL: game1.URL, Credential: authToGameGatewayCredential},
			{URL: game2.URL + "/", Credential: authToGameGatewayCredential},
			{URL: notification.URL, Credential: authToNotificationGatewayCredential},
		}, extra...)
		client, err := gatewayclient.New(instances, 100*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		return client
	}

	if err := newClient().SessionRevoked(context.Background(), 42, departureSession, service.RevocationAccountDeleted); err != nil {
		t.Fatalf("SessionRevoked: %v", err)
	}
	wantPath := "/internal/v1/session-revocations/" + strings.ToLower(departureSession)
	wantBody := `{"user_id":42,"cause":"ACCOUNT_DELETED"}`
	for name, credential := range map[string]string{
		"game-1": authToGameGatewayCredential, "game-2": authToGameGatewayCredential, "notification": authToNotificationGatewayCredential,
	} {
		got := received[name]
		if len(got) != 1 || got[0].path != wantPath || got[0].authorization != "Bearer "+credential || got[0].body != wantBody {
			t.Fatalf("%s received %+v", name, got)
		}
	}

	for name, extra := range map[string]gatewayclient.Instance{
		"failing":     {URL: failing.URL, Credential: authToNotificationGatewayCredential},
		"slow":        {URL: slow.URL, Credential: authToNotificationGatewayCredential},
		"unreachable": {URL: unreachableURL, Credential: authToNotificationGatewayCredential},
	} {
		t.Run(name, func(t *testing.T) {
			mu.Lock()
			before := len(received["game-1"])
			mu.Unlock()
			err := newClient(extra).SessionRevoked(context.Background(), 42, departureSession, service.RevocationSessionRevoked)
			if err == nil || !strings.Contains(err.Error(), extra.URL) {
				t.Fatalf("error = %v, want one naming %s", err, extra.URL)
			}
			for _, credential := range []string{authToGameGatewayCredential, authToNotificationGatewayCredential} {
				if strings.Contains(err.Error(), credential) {
					t.Fatal("error exposes a credential")
				}
			}
			mu.Lock()
			after := len(received["game-1"])
			mu.Unlock()
			if after != before+1 {
				t.Fatal("a failing instance stopped the other notifications")
			}
		})
	}
}

// TestGatewayClientRefusesInvalidSettings checks construction errors.
func TestGatewayClientRefusesInvalidSettings(t *testing.T) {
	valid := gatewayclient.Instance{URL: "http://game-gateway:8091", Credential: authToGameGatewayCredential}
	for name, tt := range map[string]struct {
		instances []gatewayclient.Instance
		timeout   time.Duration
	}{
		"no instance":   {nil, time.Second},
		"no timeout":    {[]gatewayclient.Instance{valid}, 0},
		"path":          {[]gatewayclient.Instance{{URL: "http://game-gateway:8091/internal", Credential: valid.Credential}}, time.Second},
		"scheme":        {[]gatewayclient.Instance{{URL: "ftp://game-gateway:8091", Credential: valid.Credential}}, time.Second},
		"user info":     {[]gatewayclient.Instance{{URL: "http://user:pass@game-gateway:8091", Credential: valid.Credential}}, time.Second},
		"no credential": {[]gatewayclient.Instance{{URL: valid.URL}}, time.Second},
	} {
		if _, err := gatewayclient.New(tt.instances, tt.timeout); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
