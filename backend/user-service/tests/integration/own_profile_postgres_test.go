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

var ownAccountCreated = time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

// accountStub answers Auth account metadata, or fails.
type accountStub struct {
	status string
	err    error
}

func (a accountStub) Account(_ context.Context, userID int64) (service.Account, error) {
	if a.err != nil {
		return service.Account{}, a.err
	}
	status := a.status
	if status == "" {
		status = service.AccountActive
	}
	return service.Account{Email: fmt.Sprintf("player.%d@example.com", userID), Status: status, CreatedAt: ownAccountCreated}, nil
}

// ownStatsStub returns counters derived from the user ID, or err.
type ownStatsStub struct{ err error }

func (s ownStatsStub) OwnStats(ctx context.Context, userID int64) (service.OwnStats, error) {
	public, err := statsStub{err: s.err}.PublicStats(ctx, userID)
	if err != nil {
		return service.OwnStats{}, err
	}
	return service.OwnStats{PublicStats: public, PenaltyPoints: 3}, nil
}

// membershipStub returns a fixed membership, or fails.
type membershipStub struct {
	membership service.Membership
	err        error
}

func (m membershipStub) CurrentMembership(context.Context, int64) (service.Membership, error) {
	return m.membership, m.err
}

// ownProfileHandler serves the own and public profile routes together, so the
// test also covers /me beside /{user_id}.
func ownProfileHandler(t *testing.T, pool *pgxpool.Pool, accounts service.AccountReader, stats service.OwnStatsReader,
	memberships service.MembershipReader) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	avatars := service.AvatarURLs{}
	reads := service.NewProfileRead(userpostgres.NewProfile(pool), avatars)
	public := service.NewPublicProfiles(userpostgres.NewPublicProfiles(pool), statsStub{}, onlineStub{}, avatars)
	own := service.NewOwnProfiles(reads, accounts, stats, memberships)
	health := controller.NewHealth(service.NewHealth(userpostgres.NewHealth(pool)))
	return server.New("0", health, logger,
		controller.NewPublicProfile(public, fakeSessions{}),
		controller.NewOwnProfile(own, fakeSessions{})).Handler
}

func readOwnProfile(handler http.Handler, owner int64) (friendResponse, map[string]any) {
	response := friendCall(handler, http.MethodGet, "/api/v1/users/me", "", session(owner))
	var data map[string]any
	_ = json.Unmarshal(response.Data, &data)
	return response, data
}

// TestOwnProfilePostgres checks the composed response against the Bruno fields.
//
// Expected output: the twelve contract fields with Auth email and creation
// time, Stats counters with penalty points and the Game room pair, or both
// room fields null; /api/v1/users/{user_id} still serves the public profile.
func TestOwnProfilePostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 12)
	roomID, version := int64(1001), "v2"
	handler := ownProfileHandler(t, pool, accountStub{}, ownStatsStub{},
		membershipStub{membership: service.Membership{RoomID: &roomID, APIVersion: &version}})

	response, data := readOwnProfile(handler, 12)
	want := map[string]any{"user_id": float64(12), "username": "player_12", "email": "player.12@example.com",
		"avatar_url": "/static/avatars/default/12.svg", "level": float64(4), "wins": float64(12), "losses": float64(2),
		"matches_played": float64(14), "penalty_points": float64(3), "active_room_id": float64(1001),
		"active_room_api_version": "v2", "created_at": "2026-08-01T10:00:00.000000Z"}
	if response.status != http.StatusOK || len(data) != len(want) {
		t.Fatalf("own profile = %d %s", response.status, response.raw)
	}
	for key, value := range want {
		if data[key] != value {
			t.Fatalf("%s = %v, want %v (%s)", key, data[key], value, response.raw)
		}
	}

	noRoom := ownProfileHandler(t, pool, accountStub{}, ownStatsStub{}, service.NoMembership{})
	response, data = readOwnProfile(noRoom, 12)
	roomField, hasRoom := data["active_room_id"]
	versionField, hasVersion := data["active_room_api_version"]
	if response.status != http.StatusOK || !hasRoom || !hasVersion || roomField != nil || versionField != nil {
		t.Fatalf("no room = %d %s", response.status, response.raw)
	}

	if public := friendCall(handler, http.MethodGet, "/api/v1/users/12", "", session(12)); public.status != http.StatusOK {
		t.Fatalf("public profile beside /me = %d %s", public.status, public.raw)
	}
}

// TestOwnProfileRefusalsPostgres checks inactive owners, sessions and outages.
//
// Expected output: 401 UNAUTHORIZED without a session, for a recorded
// deletion, a missing profile or account and an account that is not ACTIVE;
// 503 SERVICE_UNAVAILABLE when Auth, Stats or Game Session cannot answer;
// 500 for an inconsistent room pair. No refusal carries profile data.
func TestOwnProfileRefusalsPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 3, 6)
	mustExec(t, pool, `INSERT INTO deletion_operations (operation_id, user_id) VALUES ('3f9b2c71-5d4e-4a86-9c1b-7e2a6d0f8b53', 6)`)
	roomID := int64(1001)
	for _, tt := range []struct {
		name        string
		owner       int64
		session     string
		accounts    service.AccountReader
		stats       service.OwnStatsReader
		memberships service.MembershipReader
		status      int
		code        string
	}{
		{"no session", 3, "", accountStub{}, ownStatsStub{}, service.NoMembership{}, http.StatusUnauthorized, "UNAUTHORIZED"},
		{"deletion recorded", 6, session(6), accountStub{}, ownStatsStub{}, service.NoMembership{}, http.StatusUnauthorized, "UNAUTHORIZED"},
		{"no profile", 99, session(99), accountStub{}, ownStatsStub{}, service.NoMembership{}, http.StatusUnauthorized, "UNAUTHORIZED"},
		{"no account", 3, session(3), accountStub{err: service.ErrAccountNotFound}, ownStatsStub{}, service.NoMembership{}, http.StatusUnauthorized, "UNAUTHORIZED"},
		{"account deleting", 3, session(3), accountStub{status: "DELETING"}, ownStatsStub{}, service.NoMembership{}, http.StatusUnauthorized, "UNAUTHORIZED"},
		{"auth outage", 3, session(3), accountStub{err: fmt.Errorf("%w: test", service.ErrUnavailable)}, ownStatsStub{}, service.NoMembership{}, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE"},
		{"stats outage", 3, session(3), accountStub{}, ownStatsStub{err: errors.New("stats down")}, service.NoMembership{}, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE"},
		{"game outage", 3, session(3), accountStub{}, ownStatsStub{}, membershipStub{err: fmt.Errorf("%w: test", service.ErrMembershipUnavailable)}, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE"},
		{"inconsistent room", 3, session(3), accountStub{}, ownStatsStub{}, membershipStub{membership: service.Membership{RoomID: &roomID}}, http.StatusInternalServerError, "INTERNAL_ERROR"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			handler := ownProfileHandler(t, pool, tt.accounts, tt.stats, tt.memberships)
			response := friendCall(handler, http.MethodGet, "/api/v1/users/me", "", tt.session)
			if response.status != tt.status || response.Error.Code != tt.code || len(response.Data) != 0 {
				t.Fatalf("status = %d %s, want %d %s", response.status, response.raw, tt.status, tt.code)
			}
		})
	}
}
