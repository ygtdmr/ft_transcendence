package integration_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/controller"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/middleware/serviceauth"
	userpostgres "github.com/melmut-42/ft_transcendence/backend/user-service/internal/repository/postgres"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/server"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

var (
	channelCredential = strings.Repeat("h", 40)
	gameCredential    = strings.Repeat("g", 40)
)

func verdictHandler(reader service.FriendshipVerdictReader) http.Handler {
	gin.SetMode(gin.TestMode)
	auth := serviceauth.New(map[string][]string{
		"auth-service":    {authCredential},
		"channel-service": {channelCredential},
		"game-session":    {gameCredential},
	}, serviceauth.DefaultMatrix())
	return server.NewInternal("0", slog.New(slog.NewTextHandler(io.Discard, nil)),
		controller.NewInternalFriendshipVerdict(service.NewFriendshipVerdicts(reader), auth)).Handler
}

func verdictPath(actor, target int64) string {
	return fmt.Sprintf("/internal/v1/friendship-verdicts?actor_user_id=%d&target_user_id=%d", actor, target)
}

func verdictBody(t *testing.T, handler http.Handler, actor, target int64) string {
	t.Helper()
	recorder := call(handler, http.MethodGet, verdictPath(actor, target), channelCredential, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("verdict %d/%d = %d %s", actor, target, recorder.Code, recorder.Body)
	}
	return recorder.Body.String()
}

const (
	verdictTrue  = `{"data":{"active_friendship":true}}`
	verdictFalse = `{"data":{"active_friendship":false}}`
)

func wantVerdict(t *testing.T, handler http.Handler, first, second int64, want string) {
	t.Helper()
	for _, pair := range [][2]int64{{first, second}, {second, first}} {
		if got := verdictBody(t, handler, pair[0], pair[1]); got != want {
			t.Fatalf("verdict %d/%d = %s, want %s", pair[0], pair[1], got, want)
		}
	}
}

// TestFriendshipVerdictPostgres checks every condition against real records.
//
// Expected output:
//
//   - Active friends are true, in both directions.
//   - No friendship, a block in either direction, a deleting user and an
//     unknown user all give the identical false body.
//   - The next verdict follows a block and an unblock at once.
//   - Both blocks must be lifted before the verdict returns to true.
func TestFriendshipVerdictPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2, 3, 4, 5)
	social := blockHandler(t, pool)
	handler := verdictHandler(userpostgres.NewFriendshipVerdicts(pool))

	befriend(t, social, 1, 2)
	wantVerdict(t, handler, 1, 2, verdictTrue)

	wantVerdict(t, handler, 1, 3, verdictFalse)
	wantVerdict(t, handler, 1, 404, verdictFalse)

	mustBlock(t, social, 1, 2)
	wantVerdict(t, handler, 1, 2, verdictFalse)
	mustUnblock(t, social, 1, 2)
	wantVerdict(t, handler, 1, 2, verdictTrue)

	mustBlock(t, social, 2, 1)
	wantVerdict(t, handler, 1, 2, verdictFalse)
	mustBlock(t, social, 1, 2)
	mustUnblock(t, social, 2, 1)
	wantVerdict(t, handler, 1, 2, verdictFalse)
	mustUnblock(t, social, 1, 2)
	wantVerdict(t, handler, 1, 2, verdictTrue)

	befriend(t, social, 4, 5)
	wantVerdict(t, handler, 4, 5, verdictTrue)
	mustExec(t, pool, `INSERT INTO deletion_operations (operation_id, user_id) VALUES ('3f9b2c71-5d4e-4a86-9c1b-7e2a6d0f8b53', 5)`)
	wantVerdict(t, handler, 4, 5, verdictFalse)
}

// TestFriendshipVerdictChecksBlocksDirectly checks that the stored flag is not
// trusted alone.
//
// Expected output: an active row beside a block row is still false.
func TestFriendshipVerdictChecksBlocksDirectly(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2)
	social := blockHandler(t, pool)
	handler := verdictHandler(userpostgres.NewFriendshipVerdicts(pool))

	befriend(t, social, 1, 2)
	mustBlock(t, social, 2, 1)
	mustExec(t, pool, `UPDATE friendships SET active = true`)
	wantVerdict(t, handler, 1, 2, verdictFalse)
}

// TestFriendshipVerdictAccessPostgres checks who can read the verdict.
//
// Expected output:
//
//   - Channel Service and Game Session are allowed.
//   - Auth Service is 403; a missing or unknown credential is 401.
//   - The public listener does not serve the route.
func TestFriendshipVerdictAccessPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2)
	handler := verdictHandler(userpostgres.NewFriendshipVerdicts(pool))
	path := verdictPath(1, 2)

	for _, tt := range []struct {
		name, credential string
		status           int
		code             string
	}{
		{"channel service", channelCredential, http.StatusOK, ""},
		{"game session", gameCredential, http.StatusOK, ""},
		{"caller without scope", authCredential, http.StatusForbidden, "SERVICE_FORBIDDEN"},
		{"unknown credential", strings.Repeat("z", 40), http.StatusUnauthorized, "SERVICE_UNAUTHENTICATED"},
		{"missing credential", "", http.StatusUnauthorized, "SERVICE_UNAUTHENTICATED"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			recorder := call(handler, http.MethodGet, path, tt.credential, "")
			if recorder.Code != tt.status || !strings.Contains(recorder.Body.String(), tt.code) {
				t.Fatalf("response = %d %s, want %d %s", recorder.Code, recorder.Body, tt.status, tt.code)
			}
		})
	}

	public := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer "+channelCredential)
	blockHandler(t, pool).ServeHTTP(public, request)
	if public.Code != http.StatusNotFound {
		t.Fatalf("public listener = %d %s, want 404", public.Code, public.Body)
	}
}

// TestFriendshipVerdictDatabaseOutage checks a real connection failure.
//
// Expected output: 503 SERVICE_UNAVAILABLE with no verdict in the body.
func TestFriendshipVerdictDatabaseOutage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig("postgres://user:unused@127.0.0.1:1/user?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	recorder := call(verdictHandler(userpostgres.NewFriendshipVerdicts(pool)), http.MethodGet, verdictPath(1, 2), channelCredential, "")
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "SERVICE_UNAVAILABLE") ||
		strings.Contains(recorder.Body.String(), "active_friendship") {
		t.Fatalf("outage = %d %s", recorder.Code, recorder.Body)
	}
}
