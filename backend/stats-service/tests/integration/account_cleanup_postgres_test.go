package integration_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/controller"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/middleware/serviceauth"
	statspostgres "github.com/melmut-42/ft_transcendence/backend/stats-service/internal/repository/postgres"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/server"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/service"
)

const (
	operationOne = "6b1f0c2e-9a4d-4c7e-8f3b-2d5e7a9c1b40"
	operationTwo = "7c2a1d3f-8b5e-4d6f-9a4c-3e6f8b0d2c51"
)

func cleanupHandler(pool *pgxpool.Pool) http.Handler {
	gin.SetMode(gin.TestMode)
	auth := serviceauth.New(map[string][]string{"user-service": {userCredential}, "other-service": {"other-credential-0123456789"}},
		serviceauth.Matrix{"user-service": {serviceauth.ScopeAccountCleanup}, "other-service": {"unrelated.scope"}})
	cleanup := service.NewAccountCleanup(statspostgres.NewAccountCleanup(pool))

	return server.NewInternal("0", slog.New(slog.NewTextHandler(io.Discard, nil)), controller.NewInternalCleanup(cleanup, auth)).Handler
}

func putCleanup(handler http.Handler, operation, body, credential string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPut, "/internal/v1/account-cleanups/"+operation, strings.NewReader(body))
	if credential != "" {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	return recorder
}

func cleanUpUser(t *testing.T, handler http.Handler, operation string, user int64) *httptest.ResponseRecorder {
	t.Helper()

	return putCleanup(handler, operation, fmt.Sprintf(`{"user_id":%d}`, user), userCredential)
}

func wantStatus(t *testing.T, got *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if got.Code != status || !strings.Contains(got.Body.String(), code) {
		t.Fatalf("response = %d %s, want %d %s", got.Code, got.Body, status, code)
	}
}

// TestAccountCleanupPostgres checks removal, replay and what other users keep.
//
// Expected output:
//
//   - Counters and penalty points disappear; retained rows hold no user id or name.
//   - A replay answers 200 with the same time and changes nothing.
//   - Opponents keep their counters and see deleted_user with a null id.
func TestAccountCleanupPostgres(t *testing.T) {
	pool := statsPool(t)
	mustIngest(t, newIngest(pool), matchFor(5001, "RED", []int64{1, 2}, []int64{3, 4}))
	if _, err := service.NewPenaltyIngest(statspostgres.NewPenalties(pool)).Ingest(context.Background(), "event-1", penaltyPayload(3, 5)); err != nil {
		t.Fatal(err)
	}
	handler := cleanupHandler(pool)

	first := cleanUpUser(t, handler, strings.ToUpper(operationOne), 3)
	wantStatus(t, first, http.StatusCreated, `"state":"COMPLETED"`)
	if !strings.Contains(first.Body.String(), operationOne) {
		t.Fatalf("receipt = %s", first.Body)
	}
	replay := cleanUpUser(t, handler, operationOne, 3)
	wantStatus(t, replay, http.StatusOK, `"state":"COMPLETED"`)
	if replay.Body.String() != first.Body.String() {
		t.Fatalf("replay = %s, first = %s", replay.Body, first.Body)
	}

	for query, want := range map[string]int{
		`SELECT count(*) FROM user_stats WHERE user_id = 3`:                                           0,
		`SELECT count(*) FROM match_participants WHERE user_id = 3`:                                   0,
		`SELECT count(*) FROM match_participants WHERE username = 'player_3'`:                         0,
		`SELECT count(*) FROM match_participants WHERE user_id IS NULL AND username = 'deleted_user'`: 1,
		`SELECT count(*) FROM penalties WHERE user_id = 3`:                                            0,
		`SELECT count(*) FROM penalties WHERE event_id = 'event-1'`:                                   1,
	} {
		if got := count(t, pool, query); got != want {
			t.Fatalf("%s = %d, want %d", query, got, want)
		}
	}
	wantCounters(t, pool, 1, 1, 0)
	wantCounters(t, pool, 4, 0, 1)

	history := getMatches(publicHandler(pool, sessionStub{userID: 1}), "").Body.String()
	if !strings.Contains(history, `{"user_id":null,"username":"deleted_user"}`) || strings.Contains(history, "player_3") {
		t.Fatalf("history = %s", history)
	}
	own := getMatches(publicHandler(pool, sessionStub{userID: 3}), "").Body.String()
	if !strings.Contains(own, `"matches":[]`) {
		t.Fatalf("deleted user's history = %s", own)
	}
}

// TestAccountCleanupRefusals checks conflicts, input and service authorization.
func TestAccountCleanupRefusals(t *testing.T) {
	pool := statsPool(t)
	handler := cleanupHandler(pool)
	wantStatus(t, cleanUpUser(t, handler, operationOne, 3), http.StatusCreated, "COMPLETED")

	wantStatus(t, cleanUpUser(t, handler, operationOne, 4), http.StatusConflict, "OPERATION_CONFLICT")
	wantStatus(t, cleanUpUser(t, handler, operationTwo, 3), http.StatusConflict, "DELETION_CONFLICT")
	wantStatus(t, cleanUpUser(t, handler, "not-a-uuid", 5), http.StatusUnprocessableEntity, "operation_id")
	wantStatus(t, putCleanup(handler, operationTwo, `{"user_id":0}`, userCredential), http.StatusUnprocessableEntity, "user_id")
	wantStatus(t, putCleanup(handler, operationTwo, `{"user_id":"x"}`, userCredential), http.StatusUnprocessableEntity, "user_id")
	for _, body := range []string{``, `{`, `{}`, `{"user_id":5,"extra":1}`, `{"user_id":5} {}`} {
		wantStatus(t, putCleanup(handler, operationTwo, body, userCredential), http.StatusBadRequest, "VALIDATION_ERROR")
	}
	wantStatus(t, putCleanup(handler, operationTwo, `{"user_id":5}`, ""), http.StatusUnauthorized, "SERVICE_UNAUTHENTICATED")
	wantStatus(t, putCleanup(handler, operationTwo, `{"user_id":5}`, "other-credential-0123456789"), http.StatusForbidden, "SERVICE_FORBIDDEN")
	if got := count(t, pool, `SELECT count(*) FROM deleted_users`); got != 1 {
		t.Fatalf("deleted_users = %d, want 1", got)
	}
}

// TestLateEventsCannotRecreateIdentityPostgres delivers events after cleanup.
//
// Expected output:
//
//   - A late match is stored with the player anonymized and no counters for them.
//   - A late penalty adds no points and holds no user id; its replay is a duplicate.
func TestLateEventsCannotRecreateIdentityPostgres(t *testing.T) {
	pool := statsPool(t)
	wantStatus(t, cleanUpUser(t, cleanupHandler(pool), operationOne, 3), http.StatusCreated, "COMPLETED")
	if !mustIngest(t, newIngest(pool), matchFor(5001, "RED", []int64{1}, []int64{3})) {
		t.Fatal("late match was not recorded")
	}
	penalties := service.NewPenaltyIngest(statspostgres.NewPenalties(pool))
	ctx := context.Background()
	if recorded, err := penalties.Ingest(ctx, "event-9", penaltyPayload(3, 5)); err != nil || !recorded {
		t.Fatalf("late penalty = %v, %v", recorded, err)
	}
	if recorded, err := penalties.Ingest(ctx, "event-9", penaltyPayload(3, 5)); err != nil || recorded {
		t.Fatalf("replayed penalty = %v, %v", recorded, err)
	}
	for query, want := range map[string]int{
		`SELECT count(*) FROM user_stats WHERE user_id = 3`:                   0,
		`SELECT count(*) FROM match_participants WHERE user_id = 3`:           0,
		`SELECT count(*) FROM match_participants WHERE username = 'player_3'`: 0,
		`SELECT count(*) FROM penalties WHERE user_id = 3`:                    0,
	} {
		if got := count(t, pool, query); got != want {
			t.Fatalf("%s = %d, want %d", query, got, want)
		}
	}
	wantCounters(t, pool, 1, 1, 0)
}

// TestCleanupRacesIngestionPostgres runs cleanup against concurrent events.
//
// Expected output: whatever the order, no row for the user survives and the
// other player's counters count every match once.
func TestCleanupRacesIngestionPostgres(t *testing.T) {
	pool := statsPool(t)
	ingest := newIngest(pool)
	penalties := service.NewPenaltyIngest(statspostgres.NewPenalties(pool))
	handler := cleanupHandler(pool)
	ctx := context.Background()
	var group sync.WaitGroup
	failures := make(chan error, 32)
	start := make(chan struct{})
	for i := range 12 {
		group.Go(func() {
			<-start
			if _, err := ingest.Ingest(ctx, payloadOf(t, matchFor(int64(5001+i), "RED", []int64{1}, []int64{3}))); err != nil {
				failures <- err
			}
		})
		group.Go(func() {
			<-start
			if _, err := penalties.Ingest(ctx, fmt.Sprintf("event-%d", i), penaltyPayload(3, 2)); err != nil {
				failures <- err
			}
		})
	}
	group.Go(func() {
		<-start
		if got := cleanUpUser(t, handler, operationOne, 3); got.Code != http.StatusCreated {
			failures <- fmt.Errorf("cleanup = %d %s", got.Code, got.Body)
		}
	})
	close(start)
	group.Wait()
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	for _, query := range []string{
		`SELECT count(*) FROM user_stats WHERE user_id = 3`,
		`SELECT count(*) FROM match_participants WHERE user_id = 3`,
		`SELECT count(*) FROM penalties WHERE user_id = 3`,
	} {
		if got := count(t, pool, query); got != 0 {
			t.Fatalf("%s = %d, want 0", query, got)
		}
	}
	wantCounters(t, pool, 1, 12, 0)
}

// TestAccountCleanupDuringDatabaseOutage checks that an outage is retryable.
func TestAccountCleanupDuringDatabaseOutage(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://stats@127.0.0.1:1/stats?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	wantStatus(t, cleanUpUser(t, cleanupHandler(pool), operationOne, 3), http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE")
}
