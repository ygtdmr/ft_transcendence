package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/controller"
	userpostgres "github.com/melmut-42/ft_transcendence/backend/user-service/internal/repository/postgres"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/server"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

// statsStub returns counters derived from the user ID, or err.
type statsStub struct{ err error }

func (s statsStub) PublicStats(_ context.Context, userID int64) (service.PublicStats, error) {
	if s.err != nil {
		return service.PublicStats{}, s.err
	}
	wins := int(userID)
	return service.PublicStats{Level: wins/4 + 1, Wins: wins, Losses: 2, MatchesPlayed: wins + 2}, nil
}

// onlineStub reports the listed users online, or fails.
type onlineStub struct {
	online map[int64]bool
	err    error
}

func (p onlineStub) Online(_ context.Context, ids []int64) (map[int64]bool, error) {
	if p.err != nil {
		return nil, p.err
	}
	online := map[int64]bool{}
	for _, id := range ids {
		online[id] = p.online[id]
	}
	return online, nil
}

// profileHandler serves the public profile next to the other /api/v1/users
// routes, so the test also covers route registration.
func publicProfileHandler(t *testing.T, pool *pgxpool.Pool, stats service.StatsReader, presence service.PresenceReader) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	avatars := service.AvatarURLs{}
	requests := service.NewFriendRequests(userpostgres.NewFriendRequests(pool), avatars, service.NoPresence{})
	profiles := service.NewPublicProfiles(userpostgres.NewPublicProfiles(pool), stats, presence, avatars)
	health := controller.NewHealth(service.NewHealth(userpostgres.NewHealth(pool)))
	return server.New("0", health, logger,
		controller.NewFriendRequests(requests, fakeSessions{}),
		controller.NewBlocks(service.NewBlocks(userpostgres.NewBlocks(pool), avatars), fakeSessions{}),
		controller.NewUserSearch(service.NewUserSearch(userpostgres.NewUserSearch(pool), avatars), fakeSessions{}),
		controller.NewReports(service.NewReports(userpostgres.NewReports(pool), time.Hour), fakeSessions{}),
		controller.NewPublicProfile(profiles, fakeSessions{})).Handler
}

func readPublicProfile(handler http.Handler, viewer, target int64) (friendResponse, map[string]any) {
	response := friendCall(handler, http.MethodGet, fmt.Sprintf("/api/v1/users/%d", target), "", session(viewer))
	var data map[string]any
	_ = json.Unmarshal(response.Data, &data)
	return response, data
}

// TestPublicProfilePostgres checks the composed response and relationship states.
//
// Expected output: canonical fields, Stats counters, presence and the viewer's
// relationship, with friend_request_id only for a pending request and no
// owner-only field; search still routes beside /api/v1/users/{user_id}.
func TestPublicProfilePostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 3, 7, 9, 12)
	handler := publicProfileHandler(t, pool, statsStub{}, onlineStub{online: map[int64]bool{12: true}})

	response, data := readPublicProfile(handler, 3, 12)
	want := map[string]any{"user_id": float64(12), "username": "player_12", "avatar_url": "/static/avatars/default/12.svg",
		"level": float64(4), "wins": float64(12), "losses": float64(2), "matches_played": float64(14),
		"is_online": true, "relationship": "NONE", "friend_request_id": nil}
	if response.status != http.StatusOK || len(data) != len(want) {
		t.Fatalf("profile = %d %s", response.status, response.raw)
	}
	for key, value := range want {
		if data[key] != value {
			t.Fatalf("%s = %v, want %v (%s)", key, data[key], value, response.raw)
		}
	}

	sent := sendTo(handler, 3, 7)
	var request struct {
		RequestID int64 `json:"request_id"`
	}
	if err := json.Unmarshal(sent.Data, &request); err != nil || sent.status != http.StatusCreated {
		t.Fatalf("send = %d %s", sent.status, sent.raw)
	}
	if _, data := readPublicProfile(handler, 3, 7); data["relationship"] != "REQUEST_SENT" || data["friend_request_id"] != float64(request.RequestID) {
		t.Fatalf("sender view = %v", data)
	}
	if _, data := readPublicProfile(handler, 7, 3); data["relationship"] != "REQUEST_RECEIVED" || data["friend_request_id"] != float64(request.RequestID) {
		t.Fatalf("recipient view = %v", data)
	}
	if accepted := friendCall(handler, http.MethodPost, resolvePath(request.RequestID, "accept"), "", session(7)); accepted.status != http.StatusOK {
		t.Fatalf("accept = %d %s", accepted.status, accepted.raw)
	}
	if _, data := readPublicProfile(handler, 3, 7); data["relationship"] != "FRIENDS" || data["friend_request_id"] != nil {
		t.Fatalf("friend view = %v", data)
	}

	if blocked := putBlock(handler, 9, 3); blocked.status != http.StatusOK && blocked.status != http.StatusNoContent {
		t.Fatalf("block = %d %s", blocked.status, blocked.raw)
	}
	if _, data := readPublicProfile(handler, 9, 3); data["relationship"] != "BLOCKED" {
		t.Fatalf("blocker view = %v", data)
	}
	if _, data := readPublicProfile(handler, 3, 9); data["relationship"] != "NONE" {
		t.Fatalf("blocked user's view = %v", data)
	}

	if search := friendCall(handler, http.MethodGet, "/api/v1/users/search?q=player", "", session(3)); search.status != http.StatusOK {
		t.Fatalf("search beside profile route = %d %s", search.status, search.raw)
	}
}

// TestPublicProfileRefusalsPostgres checks missing, deleting and invalid users,
// sessions and dependency outages.
//
// Expected output: 404 USER_NOT_FOUND for a missing or deleting user, 422 for
// a bad ID, 401 for no session, and 503 without counters or is_online when
// Stats or presence cannot answer.
func TestPublicProfileRefusalsPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 3, 7)
	mustExec(t, pool, `INSERT INTO deletion_operations (operation_id, user_id) VALUES ('3f9b2c71-5d4e-4a86-9c1b-7e2a6d0f8b53', 7)`)
	handler := publicProfileHandler(t, pool, statsStub{}, onlineStub{})

	for _, target := range []int64{7, 9999} {
		response, _ := readPublicProfile(handler, 3, target)
		if response.status != http.StatusNotFound || response.Error.Code != "USER_NOT_FOUND" || response.Error.Details["user_id"] != float64(target) {
			t.Fatalf("target %d = %d %s", target, response.status, response.raw)
		}
	}
	for _, path := range []string{"/api/v1/users/0", "/api/v1/users/zero"} {
		if response := friendCall(handler, http.MethodGet, path, "", session(3)); response.status != http.StatusUnprocessableEntity ||
			response.Error.Details["field"] != "user_id" {
			t.Fatalf("%s = %d %s", path, response.status, response.raw)
		}
	}
	if response := friendCall(handler, http.MethodGet, "/api/v1/users/3", "", ""); response.status != http.StatusUnauthorized {
		t.Fatalf("no session = %d %s", response.status, response.raw)
	}

	outage := fmt.Errorf("%w: test outage", service.ErrUnavailable)
	for name, h := range map[string]http.Handler{
		"stats":    publicProfileHandler(t, pool, statsStub{err: errors.New("stats down")}, onlineStub{}),
		"presence": publicProfileHandler(t, pool, statsStub{}, onlineStub{err: outage}),
	} {
		response, _ := readPublicProfile(h, 3, 3)
		if response.status != http.StatusServiceUnavailable || response.Error.Code != "SERVICE_UNAVAILABLE" || response.Data != nil {
			t.Fatalf("%s outage = %d %s", name, response.status, response.raw)
		}
	}
}
