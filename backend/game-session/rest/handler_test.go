package rest_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"game-session/postgres"
	"game-session/rest"
	"game-session/room"
	"game-session/service"
)

func newServer(t *testing.T) *httptest.Server {
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
	svc := service.NewRoomService(store, placeholderProfiles{}, nil)
	handler := rest.NewHandler(svc, store)
	server := httptest.NewServer(handler.Routes())
	t.Cleanup(server.Close)
	return server
}

func call(t *testing.T, server *httptest.Server, method, path, userID, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if userID != "" {
		req.Header.Set("X-User-Id", userID)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	var payload map[string]any
	if resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			t.Fatalf("decode %s %s: %v", method, path, err)
		}
	}
	return resp.StatusCode, payload
}

func errorCode(payload map[string]any) string {
	errObj, _ := payload["error"].(map[string]any)
	code, _ := errObj["code"].(string)
	return code
}

func data(payload map[string]any) map[string]any {
	d, _ := payload["data"].(map[string]any)
	return d
}

func TestRoomLifecycleOverHTTP(t *testing.T) {
	server := newServer(t)

	status, payload := call(t, server, "POST", "/api/v2/rooms", "42", `{"max_players": 4}`)
	if status != http.StatusCreated {
		t.Fatalf("create: want 201, got %d %v", status, payload)
	}
	created := data(payload)
	roomID := int64(created["room_id"].(float64))
	code := created["room_code"].(string)
	if created["status"] != "WAITING" || created["player_count"].(float64) != 1 || created["host_user_id"].(float64) != 42 {
		t.Fatalf("unexpected create payload: %v", created)
	}
	players := created["players"].([]any)
	if len(players) != 1 || players[0].(map[string]any)["username"] != "user-42" {
		t.Fatalf("unexpected players: %v", players)
	}

	status, payload = call(t, server, "GET", "/api/v2/rooms/lookup/"+strings.ToLower(code), "43", "")
	if status != http.StatusOK || data(payload)["joinable"] != true || data(payload)["room_code"] != code {
		t.Fatalf("lookup: want 200 joinable, got %d %v", status, payload)
	}

	for _, id := range []string{"43", "44", "45"} {
		status, payload = call(t, server, "POST", "/api/v2/rooms/"+itoa(roomID)+"/members", id, "")
		if status != http.StatusCreated {
			t.Fatalf("join %s: want 201, got %d %v", id, status, payload)
		}
	}
	status, payload = call(t, server, "POST", "/api/v2/rooms/"+itoa(roomID)+"/members", "46", "")
	if status != http.StatusConflict || errorCode(payload) != "ROOM_FULL" {
		t.Fatalf("join full: want 409 ROOM_FULL, got %d %v", status, payload)
	}
	status, payload = call(t, server, "POST", "/api/v2/rooms/"+itoa(roomID)+"/members", "43", "")
	if status != http.StatusConflict || errorCode(payload) != "ALREADY_IN_ROOM" {
		t.Fatalf("join twice: want 409 ALREADY_IN_ROOM, got %d %v", status, payload)
	}

	status, payload = call(t, server, "GET", "/api/v2/rooms/lookup/"+code, "46", "")
	if status != http.StatusOK || data(payload)["joinable"] != false {
		t.Fatalf("lookup full room: want joinable=false, got %d %v", status, payload)
	}

	status, payload = call(t, server, "GET", "/api/v2/rooms/"+itoa(roomID), "44", "")
	if status != http.StatusOK || data(payload)["player_count"].(float64) != 4 {
		t.Fatalf("snapshot: want 200 with 4 players, got %d %v", status, payload)
	}
	status, payload = call(t, server, "GET", "/api/v2/rooms/"+itoa(roomID), "99", "")
	if status != http.StatusForbidden || errorCode(payload) != "NOT_ROOM_MEMBER" {
		t.Fatalf("snapshot stranger: want 403, got %d %v", status, payload)
	}

	status, _ = call(t, server, "DELETE", "/api/v2/rooms/"+itoa(roomID)+"/members/me", "44", "")
	if status != http.StatusNoContent {
		t.Fatalf("leave: want 204, got %d", status)
	}
	status, payload = call(t, server, "DELETE", "/api/v2/rooms/"+itoa(roomID)+"/members/me", "44", "")
	if status != http.StatusForbidden || errorCode(payload) != "NOT_ROOM_MEMBER" {
		t.Fatalf("leave twice: want 403, got %d %v", status, payload)
	}
}

func TestValidationAndAuthErrors(t *testing.T) {
	server := newServer(t)

	status, payload := call(t, server, "POST", "/api/v2/rooms", "", "")
	if status != http.StatusUnauthorized || errorCode(payload) != "UNAUTHORIZED" {
		t.Fatalf("no identity: want 401, got %d %v", status, payload)
	}
	status, payload = call(t, server, "POST", "/api/v2/rooms", "42", `{"max_players": 3}`)
	if status != http.StatusUnprocessableEntity || errorCode(payload) != "VALIDATION_ERROR" {
		t.Fatalf("max_players 3: want 422, got %d %v", status, payload)
	}
	status, payload = call(t, server, "POST", "/api/v2/rooms", "42", `not json`)
	if status != http.StatusBadRequest || errorCode(payload) != "VALIDATION_ERROR" {
		t.Fatalf("bad json: want 400 VALIDATION_ERROR, got %d %v", status, payload)
	}
	status, payload = call(t, server, "GET", "/api/v2/rooms/lookup/AB", "42", "")
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("short code: want 422, got %d %v", status, payload)
	}
	status, payload = call(t, server, "GET", "/api/v2/rooms/lookup/ZZZZZZ", "42", "")
	if status != http.StatusNotFound || errorCode(payload) != "ROOM_NOT_FOUND" {
		t.Fatalf("unknown code: want 404, got %d %v", status, payload)
	}
	status, payload = call(t, server, "GET", "/api/v2/rooms/abc", "42", "")
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("bad room id: want 422, got %d %v", status, payload)
	}
	status, payload = call(t, server, "GET", "/api/v2/rooms/999999", "42", "")
	if status != http.StatusNotFound {
		t.Fatalf("missing room: want 404, got %d %v", status, payload)
	}
	status, payload = call(t, server, "GET", "/readyz", "", "")
	if status != http.StatusOK {
		t.Fatalf("readyz: want 200, got %d %v", status, payload)
	}
}

func itoa(v int64) string {
	return strings.TrimSpace(strings.Replace(json.Number(itoaRaw(v)).String(), " ", "", -1))
}

func itoaRaw(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Testler için örnek profil kaynağı.
type placeholderProfiles struct{}

func (placeholderProfiles) ProfilesByUserIDs(userIDs []int64) (map[int64]room.Profile, error) {
	profiles := make(map[int64]room.Profile, len(userIDs))
	for _, id := range userIDs {
		profiles[id] = room.Profile{
			Username:  fmt.Sprintf("user-%d", id),
			AvatarURL: fmt.Sprintf("/static/avatars/default/%d.svg", id),
		}
	}
	return profiles, nil
}
