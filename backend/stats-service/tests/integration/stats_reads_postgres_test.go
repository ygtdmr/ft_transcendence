package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/client/authclient"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/controller"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/middleware/serviceauth"
	statspostgres "github.com/melmut-42/ft_transcendence/backend/stats-service/internal/repository/postgres"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/server"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/service"
)

const userCredential = "user-to-stats-credential-0123456789"

type sessionStub struct {
	userID int64
	err    error
}

func (s sessionStub) ValidateSession(context.Context, string) (int64, error) { return s.userID, s.err }

func publicHandler(pool *pgxpool.Pool, sessions sessionStub) http.Handler {
	gin.SetMode(gin.TestMode)
	reads := service.NewStatsReads(statspostgres.NewStatsReads(pool))

	return server.New("0", controller.NewHealth(nil), slog.New(slog.NewTextHandler(io.Discard, nil)),
		controller.NewMatches(reads, sessions)).Handler
}

func internalHandler(pool *pgxpool.Pool) http.Handler {
	gin.SetMode(gin.TestMode)
	reads := service.NewStatsReads(statspostgres.NewStatsReads(pool))
	auth := serviceauth.New(map[string][]string{"user-service": {userCredential}, "other-service": {"other-credential-0123456789"}},
		serviceauth.Matrix{"user-service": {serviceauth.ScopeStatsRead}, "other-service": {"unrelated.scope"}})

	return server.NewInternal("0", slog.New(slog.NewTextHandler(io.Discard, nil)), controller.NewInternalStats(reads, auth)).Handler
}

func getMatches(handler http.Handler, query string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/users/me/matches"+query, nil)
	request.AddCookie(&http.Cookie{Name: "ft_session", Value: "token"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	return recorder
}

func getStats(handler http.Handler, user, credential string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/user-stats/"+user, nil)
	if credential != "" {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	return recorder
}

type historyBody struct {
	Data struct {
		Matches []struct {
			GameID    int64  `json:"game_id"`
			Team      string `json:"team"`
			Result    string `json:"result"`
			Opponents []struct {
				UserID   int64  `json:"user_id"`
				Username string `json:"username"`
			} `json:"opponents"`
			Score      struct{ Red, Blue int } `json:"score"`
			FinishedAt string                  `json:"finished_at"`
		} `json:"matches"`
	} `json:"data"`
}

func decodeHistory(t *testing.T, recorder *httptest.ResponseRecorder) historyBody {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d %s", recorder.Code, recorder.Body)
	}
	var body historyBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}

	return body
}

func penaltyPayload(user int64, points int) []byte {
	return fmt.Appendf(nil, `{"user_id":%d,"room_id":1001,"game_id":5001,"role":"OPERATIVE","reason":"EXITED","points":%d,"penalized_at":"2026-09-10T08:50:00Z"}`, user, points)
}

// TestMatchHistoryHTTPPostgres checks shape, order, paging and input limits.
//
// Expected output:
//
//   - Matches are newest first, with the other team as opponents and the result per user.
//   - limit and before page through them; invalid values answer 422.
func TestMatchHistoryHTTPPostgres(t *testing.T) {
	pool := statsPool(t)
	ingest := newIngest(pool)
	start := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	for i := range int64(3) {
		match := matchFor(5001+i, "RED", []int64{1, 2}, []int64{3, 4})
		match.FinishedAt = start.Add(time.Duration(i) * time.Hour)
		match.StartedAt = match.FinishedAt.Add(-time.Minute)
		mustIngest(t, ingest, match)
	}
	handler := publicHandler(pool, sessionStub{userID: 3})

	all := decodeHistory(t, getMatches(handler, "")).Data.Matches
	if len(all) != 3 || all[0].GameID != 5003 || all[2].GameID != 5001 {
		t.Fatalf("matches = %+v", all)
	}
	first := all[0]
	if first.Team != "BLUE" || first.Result != "LOSS" || first.Score.Red != 9 || len(first.Opponents) != 2 ||
		first.Opponents[0].UserID != 1 || first.Opponents[0].Username != "player_1" {
		t.Fatalf("first = %+v", first)
	}

	page := decodeHistory(t, getMatches(handler, "?limit=1")).Data.Matches
	next := decodeHistory(t, getMatches(handler, "?limit=1&before="+page[0].FinishedAt)).Data.Matches
	if len(page) != 1 || len(next) != 1 || next[0].GameID != 5002 {
		t.Fatalf("page = %+v, next = %+v", page, next)
	}
	for _, query := range []string{"?limit=0", "?limit=51", "?limit=x", "?limit=1&limit=2", "?before=yesterday"} {
		if got := getMatches(handler, query); got.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s = %d", query, got.Code)
		}
	}
	if empty := getMatches(publicHandler(pool, sessionStub{userID: 99}), ""); !strings.Contains(empty.Body.String(), `"matches":[]`) {
		t.Fatalf("no matches = %s", empty.Body)
	}
}

// TestMatchHistorySessionFailures checks that an outage is not a signed-out user.
func TestMatchHistorySessionFailures(t *testing.T) {
	pool := statsPool(t)
	if got := getMatches(publicHandler(pool, sessionStub{err: authclient.ErrSessionInvalid}), ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("invalid session = %d", got.Code)
	}
	if got := getMatches(publicHandler(pool, sessionStub{err: authclient.ErrUnavailable}), ""); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("auth outage = %d", got.Code)
	}
}

// TestUserStatsPrivateRoutePostgres checks authorization, counters and an unknown user.
func TestUserStatsPrivateRoutePostgres(t *testing.T) {
	pool := statsPool(t)
	ingest := newIngest(pool)
	for i := range int64(4) {
		mustIngest(t, ingest, matchFor(5001+i, "RED", []int64{1}, []int64{2}))
	}
	if _, err := service.NewPenaltyIngest(statspostgres.NewPenalties(pool)).Ingest(context.Background(), "event-1", penaltyPayload(1, 3)); err != nil {
		t.Fatal(err)
	}
	handler := internalHandler(pool)

	if got := getStats(handler, "1", ""); got.Code != http.StatusUnauthorized || !strings.Contains(got.Body.String(), "SERVICE_UNAUTHENTICATED") {
		t.Fatalf("no credential = %d %s", got.Code, got.Body)
	}
	if got := getStats(handler, "1", "other-credential-0123456789"); got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), "SERVICE_FORBIDDEN") {
		t.Fatalf("wrong caller = %d %s", got.Code, got.Body)
	}
	if got := getStats(handler, "abc", userCredential); got.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad id = %d", got.Code)
	}
	want := `{"data":{"level":2,"losses":0,"matches_played":4,"penalty_points":3,"user_id":1,"wins":4}}`
	if got := getStats(handler, "1", userCredential); got.Code != http.StatusOK || strings.TrimSpace(got.Body.String()) != want {
		t.Fatalf("stats = %d %s", got.Code, got.Body)
	}
	want = `{"data":{"level":1,"losses":0,"matches_played":0,"penalty_points":0,"user_id":77,"wins":0}}`
	if got := getStats(handler, "77", userCredential); strings.TrimSpace(got.Body.String()) != want {
		t.Fatalf("unknown user = %s", got.Body)
	}
}

// TestReadsDuringDatabaseOutage checks that a failed read never answers zeros.
func TestReadsDuringDatabaseOutage(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://stats@127.0.0.1:1/stats?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	got := getStats(internalHandler(pool), "1", userCredential)
	if got.Code != http.StatusServiceUnavailable || strings.Contains(got.Body.String(), "wins") {
		t.Fatalf("stats during outage = %d %s", got.Code, got.Body)
	}
	if got := getMatches(publicHandler(pool, sessionStub{userID: 1}), ""); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("history during outage = %d %s", got.Code, got.Body)
	}
}

// TestPenaltyIdempotencyPostgres checks duplicates, a restart and untouched results.
//
// Expected output:
//
//   - A repeated event adds nothing, also from a new service instance.
//   - Points are taken from the event; wins and losses do not change.
func TestPenaltyIdempotencyPostgres(t *testing.T) {
	pool := statsPool(t)
	mustIngest(t, newIngest(pool), matchFor(5001, "RED", []int64{1}, []int64{2}))
	ctx := context.Background()
	first := service.NewPenaltyIngest(statspostgres.NewPenalties(pool))
	if recorded, err := first.Ingest(ctx, "event-1", penaltyPayload(1, 7)); err != nil || !recorded {
		t.Fatalf("first = %v, %v", recorded, err)
	}
	restarted := service.NewPenaltyIngest(statspostgres.NewPenalties(pool))
	if recorded, err := restarted.Ingest(ctx, "event-1", penaltyPayload(1, 100)); err != nil || recorded {
		t.Fatalf("duplicate = %v, %v", recorded, err)
	}
	if got := count(t, pool, `SELECT penalty_points FROM user_stats WHERE user_id = 1`); got != 7 {
		t.Fatalf("penalty_points = %d, want 7", got)
	}
	wantCounters(t, pool, 1, 1, 0)
}

// TestConcurrentPenaltiesAndMatchesPostgres races deliveries for one user.
//
// Expected output: each event and match counts exactly once.
func TestConcurrentPenaltiesAndMatchesPostgres(t *testing.T) {
	pool := statsPool(t)
	ctx := context.Background()
	penalties := service.NewPenaltyIngest(statspostgres.NewPenalties(pool))
	matches := newIngest(pool)
	var group sync.WaitGroup
	failures := make(chan error, 64)
	start := make(chan struct{})
	for i := range 8 {
		for range 2 {
			group.Go(func() {
				<-start
				if _, err := penalties.Ingest(ctx, fmt.Sprintf("event-%d", i), penaltyPayload(1, 2)); err != nil {
					failures <- err
				}
			})
			group.Go(func() {
				<-start
				if _, err := matches.Ingest(ctx, payloadOf(t, matchFor(int64(5001+i), "RED", []int64{1}, []int64{2}))); err != nil {
					failures <- err
				}
			})
		}
	}
	close(start)
	group.Wait()
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	if got := count(t, pool, `SELECT penalty_points FROM user_stats WHERE user_id = 1`); got != 16 {
		t.Fatalf("penalty_points = %d, want 16", got)
	}
	wantCounters(t, pool, 1, 8, 0)
}
