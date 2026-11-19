package unit_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/client/gameclient"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

const userToGameCredential = "user-to-game-credential-0123456789abcdef"

// TestGameClientMembership checks the membership request and every answer mapping.
//
// Expected output:
//
//   - GET carries the user in the path and the Bearer credential.
//   - 200 decodes a room or an explicit null pair.
//   - 5xx, timeouts, a wrong user and missing or malformed fields are
//     ErrMembershipUnavailable; 401, 403 and 422 are plain errors.
//   - No error message contains the credential.
func TestGameClientMembership(t *testing.T) {
	for _, tt := range []struct {
		name     string
		status   int
		body     string
		delay    time.Duration
		want     error
		wantRoom int64
	}{
		{"in room", http.StatusOK, `{"data":{"user_id":42,"active_room_id":1001,"active_room_api_version":"v2"}}`, 0, nil, 1001},
		{"no room", http.StatusOK, `{"data":{"user_id":42,"active_room_id":null,"active_room_api_version":null}}`, 0, nil, 0},
		{"wrong user", http.StatusOK, `{"data":{"user_id":43,"active_room_id":null,"active_room_api_version":null}}`, 0, service.ErrMembershipUnavailable, 0},
		{"missing fields", http.StatusOK, `{"data":{"user_id":42}}`, 0, service.ErrMembershipUnavailable, 0},
		{"wrong field type", http.StatusOK, `{"data":{"user_id":42,"active_room_id":"1001","active_room_api_version":"v2"}}`, 0, service.ErrMembershipUnavailable, 0},
		{"not json", http.StatusOK, `ok`, 0, service.ErrMembershipUnavailable, 0},
		{"unavailable", http.StatusServiceUnavailable, `{"error":{"code":"SERVICE_UNAVAILABLE"}}`, 0, service.ErrMembershipUnavailable, 0},
		{"internal error", http.StatusInternalServerError, `{}`, 0, service.ErrMembershipUnavailable, 0},
		{"timeout", http.StatusOK, `{"data":{"user_id":42,"active_room_id":null,"active_room_api_version":null}}`, 300 * time.Millisecond, service.ErrMembershipUnavailable, 0},
		{"unauthenticated", http.StatusUnauthorized, `{"error":{"code":"SERVICE_UNAUTHENTICATED"}}`, 0, errors.New("plain"), 0},
		{"forbidden", http.StatusForbidden, `{"error":{"code":"SERVICE_FORBIDDEN"}}`, 0, errors.New("plain"), 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/internal/v1/memberships/42" ||
					r.Header.Get("Authorization") != "Bearer "+userToGameCredential {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				time.Sleep(tt.delay)
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			client, err := gameclient.New(server.URL, userToGameCredential, 100*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.CurrentMembership(context.Background(), 42)
			switch {
			case tt.want == nil && tt.wantRoom != 0:
				if err != nil || got.RoomID == nil || *got.RoomID != tt.wantRoom || got.APIVersion == nil || *got.APIVersion != "v2" {
					t.Fatalf("CurrentMembership() = %+v, %v", got, err)
				}
			case tt.want == nil:
				if err != nil || got.RoomID != nil || got.APIVersion != nil {
					t.Fatalf("CurrentMembership() = %+v, %v", got, err)
				}
			case errors.Is(tt.want, service.ErrMembershipUnavailable):
				if !errors.Is(err, service.ErrMembershipUnavailable) {
					t.Fatalf("error = %v, want ErrMembershipUnavailable", err)
				}
			default:
				if err == nil || errors.Is(err, service.ErrMembershipUnavailable) {
					t.Fatalf("error = %v, want a plain configuration error", err)
				}
			}
			if err != nil && strings.Contains(err.Error(), userToGameCredential) {
				t.Fatal("error message leaks the credential")
			}
		})
	}
}

// TestGameClientRefusesUnreachableAndInvalidSettings checks construction rules
// and that a closed listener is an outage, not an empty membership.
func TestGameClientRefusesUnreachableAndInvalidSettings(t *testing.T) {
	for _, base := range []string{"", "game-session:8093", "ftp://game-session", "http://game-session:8093/api"} {
		if _, err := gameclient.New(base, userToGameCredential, time.Second); err == nil {
			t.Fatalf("New(%q) accepted", base)
		}
	}
	if _, err := gameclient.New("http://game-session:8093", "", time.Second); err == nil {
		t.Fatal("empty credential accepted")
	}
	server := httptest.NewServer(http.NotFoundHandler())
	base := server.URL
	server.Close()
	client, err := gameclient.New(base, userToGameCredential, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CurrentMembership(context.Background(), 42); !errors.Is(err, service.ErrMembershipUnavailable) {
		t.Fatalf("closed listener error = %v", err)
	}
}
