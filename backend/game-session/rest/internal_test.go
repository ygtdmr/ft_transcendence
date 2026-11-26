package rest_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"game-session/postgres"
	"game-session/rest"
	"game-session/roomaction"
	"game-session/service"
)

func newInternalServers(t *testing.T) (public *httptest.Server, internal *httptest.Server) {
	t.Helper()
	dsn := os.Getenv("GS_DB")
	if dsn == "" {
		t.Skip("GS_DB not set")
	}
	store, err := postgres.Open(dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.ResetForTests(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	svc := service.NewRoomService(placeholderProfiles{}, nil)
	handler := rest.NewHandler(svc, roomaction.New(svc, placeholderProfiles{}), store)
	access := rest.NewServiceAccessCheck(
		map[string]string{"user-service": "secret", "auth-service": "auth-secret"},
		map[string][]string{"user-service": {"account.cleanup"}, "auth-service": {"session.departure"}},
	)
	public = httptest.NewServer(handler.Routes())
	internal = httptest.NewServer(handler.InternalRoutes(access))
	t.Cleanup(public.Close)
	t.Cleanup(internal.Close)
	return public, internal
}

func cleanupUser(t *testing.T, internal *httptest.Server, userID string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, internal.URL+"/internal/v1/account-cleanups/6b1f0c2e-9a4d-4c7e-8f3b-2d5e7a9c1b40", strings.NewReader(`{"user_id": `+userID+`}`))
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestAccountCleanupRemovesMember(t *testing.T) {
	public, internal := newInternalServers(t)

	status, payload := call(t, public, "POST", "/api/v2/rooms", "42", `{"max_players": 4}`)
	if status != http.StatusCreated {
		t.Fatalf("create: want 201, got %d %v", status, payload)
	}
	roomID := itoa(int64(data(payload)["room_id"].(float64)))
	if status, payload = call(t, public, "POST", "/api/v2/rooms/"+roomID+"/members", "43", ""); status != http.StatusCreated {
		t.Fatalf("join: want 201, got %d %v", status, payload)
	}

	if status := cleanupUser(t, internal, "43"); status != http.StatusCreated {
		t.Fatalf("cleanup: want 201, got %d", status)
	}
	if status, payload = call(t, public, "GET", "/api/v2/rooms/"+roomID, "43", ""); status != http.StatusForbidden {
		t.Fatalf("snapshot after cleanup: want 403, got %d %v", status, payload)
	}
}

func TestJoinAfterAccountCleanup(t *testing.T) {
	public, internal := newInternalServers(t)

	status, payload := call(t, public, "POST", "/api/v2/rooms", "42", `{"max_players": 4}`)
	if status != http.StatusCreated {
		t.Fatalf("create: want 201, got %d %v", status, payload)
	}
	roomID := itoa(int64(data(payload)["room_id"].(float64)))

	if status := cleanupUser(t, internal, "43"); status != http.StatusCreated {
		t.Fatalf("cleanup: want 201, got %d", status)
	}
	if status, payload = call(t, public, "POST", "/api/v2/rooms/"+roomID+"/members", "43", ""); status != http.StatusUnauthorized {
		t.Fatalf("join after cleanup: want 401, got %d %v", status, payload)
	}
}

func TestSessionDepartureRemovesMember(t *testing.T) {
	public, internal := newInternalServers(t)

	status, payload := call(t, public, "POST", "/api/v2/rooms", "42", `{"max_players": 4}`)
	if status != http.StatusCreated {
		t.Fatalf("create: want 201, got %d %v", status, payload)
	}
	roomID := itoa(int64(data(payload)["room_id"].(float64)))
	if status, payload = call(t, public, "POST", "/api/v2/rooms/"+roomID+"/members", "43", ""); status != http.StatusCreated {
		t.Fatalf("join: want 201, got %d %v", status, payload)
	}

	req, _ := http.NewRequest(http.MethodPut, internal.URL+"/internal/v1/session-departures/3f9b2c71-5d4e-4a86-9c1b-7e2a6d0f8b53", strings.NewReader(`{"user_id": 43}`))
	req.Header.Set("Authorization", "Bearer auth-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("departure: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("departure: want 201, got %d", resp.StatusCode)
	}
	if status, payload = call(t, public, "GET", "/api/v2/rooms/"+roomID, "43", ""); status != http.StatusForbidden {
		t.Fatalf("snapshot after departure: want 403, got %d %v", status, payload)
	}
	if status, payload = call(t, public, "POST", "/api/v2/rooms/"+roomID+"/members", "43", ""); status != http.StatusCreated {
		t.Fatalf("join again after logout: want 201, got %d %v", status, payload)
	}
}
