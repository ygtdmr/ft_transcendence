package integration_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/controller"
	userpostgres "github.com/melmut-42/ft_transcendence/backend/user-service/internal/repository/postgres"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/server"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

func profileUpdateHandler(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	avatars := service.AvatarURLs{}
	own := service.NewOwnProfiles(service.NewProfileRead(userpostgres.NewProfile(pool), avatars),
		accountStub{}, ownStatsStub{}, service.NoMembership{})
	usernames := service.NewUsernames(userpostgres.NewUsernames(pool), avatars)
	health := controller.NewHealth(service.NewHealth(userpostgres.NewHealth(pool)))
	return server.New("0", health, logger,
		controller.NewOwnProfile(own, fakeSessions{}),
		controller.NewProfileUpdate(usernames, own, fakeSessions{})).Handler
}

func patchProfile(handler http.Handler, owner int64, body string) (friendResponse, map[string]any) {
	response := friendCall(handler, http.MethodPatch, "/api/v1/users/me", body, session(owner))
	var data map[string]any
	_ = json.Unmarshal(response.Data, &data)
	return response, data
}

// TestProfileUpdatePostgres checks a username change and its event.
//
// Expected output: 200 with the own profile and the trimmed new name; one
// user.profile.updated with the user, active friends and pending-request
// peers as recipients; a letter-case change is a change; the same name or an
// omitted username changes nothing and records no event.
func TestProfileUpdatePostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 3, 7, 9, 12)
	mustExec(t, pool, `INSERT INTO friendships (user_low, user_high, initiated_by) VALUES (3, 7, 3)`)
	mustExec(t, pool, `INSERT INTO friend_requests (requester_id, addressee_id) VALUES (9, 3)`)
	mustExec(t, pool, `INSERT INTO blocks (blocker_id, blocked_id) VALUES (3, 12)`)
	handler := profileUpdateHandler(t, pool)

	response, data := patchProfile(handler, 3, `{"username":"  new_name "}`)
	if response.status != http.StatusOK || data["username"] != "new_name" || data["email"] != "player.3@example.com" ||
		len(data) != 12 {
		t.Fatalf("rename = %d %s", response.status, response.raw)
	}
	var payload struct {
		UserID     int64   `json:"user_id"`
		Username   string  `json:"username"`
		AvatarURL  string  `json:"avatar_url"`
		Recipients []int64 `json:"recipient_user_ids"`
	}
	raw := queryString(t, pool, `SELECT payload::text FROM outbox WHERE event_type = 'user.profile.updated'`)
	if err := json.Unmarshal([]byte(raw), &payload); err != nil || payload.UserID != 3 || payload.Username != "new_name" ||
		payload.AvatarURL != "/static/avatars/default/3.svg" || len(payload.Recipients) != 3 ||
		payload.Recipients[0] != 3 || payload.Recipients[1] != 7 || payload.Recipients[2] != 9 {
		t.Fatalf("event = %s", raw)
	}

	if response, data := patchProfile(handler, 3, `{"username":"New_Name"}`); response.status != http.StatusOK || data["username"] != "New_Name" {
		t.Fatalf("case change = %d %s", response.status, response.raw)
	}
	for _, body := range []string{`{"username":"New_Name"}`, `{}`} {
		if response, data := patchProfile(handler, 3, body); response.status != http.StatusOK || data["username"] != "New_Name" {
			t.Fatalf("%s = %d %s", body, response.status, response.raw)
		}
	}
	if n := count(t, pool, `SELECT count(*) FROM outbox WHERE event_type = 'user.profile.updated'`); n != 2 {
		t.Fatalf("events = %d, want 2", n)
	}
	if got := queryString(t, pool, "SELECT username FROM profiles WHERE user_id = 3"); got != "New_Name" {
		t.Fatalf("stored username = %q", got)
	}
}

// TestProfileUpdateRefusalsPostgres checks refused changes.
//
// Expected output: a name another user holds in any letter case is 409
// USERNAME_TAKEN; a malformed or null name is 422 INVALID_USERNAME; another
// field is 400; a recorded deletion is 401. None writes a row or an event.
func TestProfileUpdateRefusalsPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 3, 4, 6)
	mustExec(t, pool, `INSERT INTO deletion_operations (operation_id, user_id) VALUES ('3f9b2c71-5d4e-4a86-9c1b-7e2a6d0f8b53', 6)`)
	handler := profileUpdateHandler(t, pool)

	for _, tt := range []struct {
		name   string
		owner  int64
		body   string
		status int
		code   string
	}{
		{"taken", 3, `{"username":"PLAYER_4"}`, http.StatusConflict, "USERNAME_TAKEN"},
		{"too short", 3, `{"username":"ab"}`, http.StatusUnprocessableEntity, "INVALID_USERNAME"},
		{"bad characters", 3, `{"username":"bad name"}`, http.StatusUnprocessableEntity, "INVALID_USERNAME"},
		{"null", 3, `{"username":null}`, http.StatusUnprocessableEntity, "INVALID_USERNAME"},
		{"number", 3, `{"username":42}`, http.StatusUnprocessableEntity, "INVALID_USERNAME"},
		{"email not editable", 3, `{"email":"x@example.com"}`, http.StatusBadRequest, "VALIDATION_ERROR"},
		{"deletion recorded", 6, `{"username":"fresh_name"}`, http.StatusUnauthorized, "UNAUTHORIZED"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response, _ := patchProfile(handler, tt.owner, tt.body)
			if response.status != tt.status || response.Error.Code != tt.code {
				t.Fatalf("status = %d %s, want %d %s", response.status, response.raw, tt.status, tt.code)
			}
		})
	}
	if n := count(t, pool, "SELECT count(*) FROM outbox"); n != 0 {
		t.Fatalf("events = %d, want none", n)
	}
	if got := queryString(t, pool, "SELECT username FROM profiles WHERE user_id = 3"); got != "player_3" {
		t.Fatalf("username changed to %q", got)
	}
}

// TestProfileUpdateRacePostgres checks concurrent renames to one name.
//
// Expected output: exactly one rename succeeds, the others are 409, and one
// event is recorded.
func TestProfileUpdateRacePostgres(t *testing.T) {
	pool := schemaPool(t)
	ids := []int64{3, 4, 5, 6, 7, 8}
	seedProfiles(t, pool, ids...)
	handler := profileUpdateHandler(t, pool)

	statuses := make([]int, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Go(func() {
			response, _ := patchProfile(handler, id, `{"username":"Contested"}`)
			statuses[i] = response.status
		})
	}
	wg.Wait()
	won, lost := 0, 0
	for _, status := range statuses {
		switch status {
		case http.StatusOK:
			won++
		case http.StatusConflict:
			lost++
		}
	}
	if won != 1 || lost != len(ids)-1 {
		t.Fatalf("statuses = %v", statuses)
	}
	if n := count(t, pool, "SELECT count(*) FROM outbox WHERE event_type = 'user.profile.updated'"); n != 1 {
		t.Fatalf("events = %d, want 1", n)
	}
}
