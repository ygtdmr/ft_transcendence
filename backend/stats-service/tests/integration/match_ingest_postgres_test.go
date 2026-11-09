package integration_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	statspostgres "github.com/melmut-42/ft_transcendence/backend/stats-service/internal/repository/postgres"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/service"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/migrations"
)

// freshDatabase creates an empty database, dropped when the test ends. The test
// is skipped when STATS_TEST_DATABASE_URL is unset.
func freshDatabase(t *testing.T) string {
	t.Helper()
	adminURL := os.Getenv("STATS_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("set STATS_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatal("connect to STATS_TEST_DATABASE_URL failed")
	}
	t.Cleanup(func() { _ = admin.Close(ctx) })

	name := "stats_service_test_" + strings.ToLower(rand.Text())
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+quoted+" WITH (FORCE)")
	})
	parsed, err := url.Parse(adminURL)
	if err != nil {
		t.Fatal("parse STATS_TEST_DATABASE_URL failed")
	}
	parsed.Path = "/" + name

	return parsed.String()
}

func statsPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := freshDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := statspostgres.Migrate(ctx, dsn); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	pool, err := statspostgres.OpenPool(ctx, dsn)
	if err != nil {
		t.Fatalf("OpenPool() error = %v", err)
	}
	t.Cleanup(pool.Close)

	return pool
}

func count(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var value int
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func wantCounters(t *testing.T, pool *pgxpool.Pool, user int64, wins, losses int) {
	t.Helper()
	var gotWins, gotLosses int
	err := pool.QueryRow(context.Background(), `SELECT wins, losses FROM user_stats WHERE user_id = $1`, user).Scan(&gotWins, &gotLosses)
	if err != nil || gotWins != wins || gotLosses != losses {
		t.Fatalf("user %d = %d wins %d losses, %v; want %d, %d", user, gotWins, gotLosses, err, wins, losses)
	}
}

func matchFor(game int64, winner string, red, blue []int64) service.MatchFinalized {
	loser := "BLUE"
	if winner == "BLUE" {
		loser = "RED"
	}
	match := service.MatchFinalized{
		GameID: game, RoomID: 1001, Winner: winner, Loser: loser, EndReason: "ALL_TEAM_CARDS_REVEALED",
		Score:      &service.MatchScore{Red: 9, Blue: 5},
		StartedAt:  time.Date(2026, 9, 10, 8, 30, 0, 0, time.UTC),
		FinishedAt: time.Date(2026, 9, 10, 8, 50, 0, 0, time.UTC),
	}
	for _, id := range red {
		match.Players = append(match.Players, service.FinalizedPlayer{UserID: id, Username: fmt.Sprintf("player_%d", id), Team: "RED", Role: "OPERATIVE"})
	}
	for _, id := range blue {
		match.Players = append(match.Players, service.FinalizedPlayer{UserID: id, Username: fmt.Sprintf("player_%d", id), Team: "BLUE", Role: "OPERATIVE"})
	}
	return match
}

func payloadOf(t *testing.T, match service.MatchFinalized) []byte {
	t.Helper()
	raw, err := json.Marshal(match)
	if err != nil {
		t.Error(err)
	}
	return raw
}

func mustIngest(t *testing.T, ingest *service.MatchIngest, match service.MatchFinalized) bool {
	t.Helper()
	recorded, err := ingest.Ingest(context.Background(), payloadOf(t, match))
	if err != nil {
		t.Fatalf("Ingest(game %d) = %v", match.GameID, err)
	}
	return recorded
}

func newIngest(pool *pgxpool.Pool) *service.MatchIngest {
	return service.NewMatchIngest(statspostgres.NewMatches(pool))
}

// TestIngestRecordsMatchPostgres checks one match on real records.
//
// Expected output:
//
//   - The match, its participants and usernames are stored.
//   - Winners gain a win and losers a loss.
func TestIngestRecordsMatchPostgres(t *testing.T) {
	pool := statsPool(t)
	if !mustIngest(t, newIngest(pool), matchFor(5001, "RED", []int64{1, 2}, []int64{3, 4})) {
		t.Fatal("first delivery was not recorded")
	}

	var winner, reason string
	var red, blue int
	err := pool.QueryRow(context.Background(), `SELECT winner, end_reason, score_red, score_blue FROM matches WHERE game_id = 5001`).
		Scan(&winner, &reason, &red, &blue)
	if err != nil || winner != "RED" || reason != "ALL_TEAM_CARDS_REVEALED" || red != 9 || blue != 5 {
		t.Fatalf("match = %s %s %d %d, %v", winner, reason, red, blue, err)
	}
	if got := count(t, pool, `SELECT count(*) FROM match_participants WHERE game_id = 5001`); got != 4 {
		t.Fatalf("participants = %d", got)
	}
	if got := count(t, pool, `SELECT count(*) FROM match_participants WHERE user_id = 3 AND username = 'player_3' AND team = 'BLUE'`); got != 1 {
		t.Fatal("participant row is wrong")
	}
	for _, user := range []int64{1, 2} {
		wantCounters(t, pool, user, 1, 0)
	}
	for _, user := range []int64{3, 4} {
		wantCounters(t, pool, user, 0, 1)
	}
}

// TestIngestDuplicatesPostgres checks repeated delivery.
//
// Expected output:
//
//   - A repeat returns false and changes nothing, even with different content.
//   - The first recorded username stays, and new matches keep adding.
func TestIngestDuplicatesPostgres(t *testing.T) {
	pool := statsPool(t)
	ingest := newIngest(pool)
	first := matchFor(5001, "RED", []int64{1}, []int64{2})
	mustIngest(t, ingest, first)

	if mustIngest(t, ingest, first) {
		t.Fatal("repeat was recorded")
	}
	changed := matchFor(5001, "BLUE", []int64{1}, []int64{2})
	changed.Players[0].Username = "renamed_one"
	if mustIngest(t, ingest, changed) {
		t.Fatal("changed repeat was recorded")
	}
	wantCounters(t, pool, 1, 1, 0)
	wantCounters(t, pool, 2, 0, 1)
	if got := count(t, pool, `SELECT count(*) FROM match_participants WHERE username = 'renamed_one'`); got != 0 {
		t.Fatal("a repeat changed a recorded username")
	}

	later := matchFor(5002, "BLUE", []int64{1}, []int64{2})
	later.Players[0].Username = "renamed_one"
	mustIngest(t, ingest, later)
	wantCounters(t, pool, 1, 1, 1)
	if got := count(t, pool, `SELECT count(*) FROM match_participants WHERE game_id = 5001 AND username = 'player_1'`); got != 1 {
		t.Fatal("a later match changed the earlier username")
	}
}

// TestIngestOutOfOrderPostgres checks games arriving in any order.
//
// Expected output: totals match the set of games, however they arrive.
func TestIngestOutOfOrderPostgres(t *testing.T) {
	pool := statsPool(t)
	ingest := newIngest(pool)
	for _, game := range []int64{7, 3, 9, 1} {
		winner := "RED"
		if game == 3 {
			winner = "BLUE"
		}
		mustIngest(t, ingest, matchFor(game, winner, []int64{1}, []int64{2}))
	}
	wantCounters(t, pool, 1, 3, 1)
	wantCounters(t, pool, 2, 1, 3)
}

// TestIngestRefusedStoresNothingPostgres checks refused events on real records.
//
// Expected output: a cancelled game and a match without usernames store nothing.
func TestIngestRefusedStoresNothingPostgres(t *testing.T) {
	pool := statsPool(t)
	ingest := newIngest(pool)

	cancelled := matchFor(5001, "RED", []int64{1}, []int64{2})
	cancelled.EndReason = "INSUFFICIENT_PLAYERS"
	unnamed := matchFor(5002, "RED", []int64{1}, []int64{2})
	unnamed.Players[0].Username = ""
	for _, match := range []service.MatchFinalized{cancelled, unnamed} {
		if _, err := ingest.Ingest(context.Background(), payloadOf(t, match)); !errors.Is(err, service.ErrInvalidMatch) {
			t.Fatalf("Ingest = %v", err)
		}
	}
	for _, table := range []string{"matches", "match_participants", "user_stats"} {
		if got := count(t, pool, "SELECT count(*) FROM "+table); got != 0 {
			t.Fatalf("%s rows = %d", table, got)
		}
	}
}

// TestRecordRollsBackPostgres checks that a failed write leaves nothing.
//
// Expected output: a database failure on the last participant removes the
// match, its participants and every counter.
func TestRecordRollsBackPostgres(t *testing.T) {
	pool := statsPool(t)
	match := matchFor(5001, "RED", []int64{1, 2}, []int64{3})
	match.Players[2].Team = "INVALID"

	if _, err := statspostgres.NewMatches(pool).RecordFinalizedMatch(context.Background(), match); err == nil {
		t.Fatal("the invalid row was stored")
	}
	for _, table := range []string{"matches", "match_participants", "user_stats"} {
		if got := count(t, pool, "SELECT count(*) FROM "+table); got != 0 {
			t.Fatalf("%s rows after rollback = %d", table, got)
		}
	}
}

func runConcurrently(count int, work func(int) error) []error {
	errs := make([]error, count)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for index := range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[index] = work(index)
		}()
	}
	close(start)
	wg.Wait()
	return errs
}

// TestIngestConcurrentPostgres checks races on real records.
//
// Expected output:
//
//   - Eight deliveries of one game record once and count once.
//   - Games sharing players in opposite orders do not deadlock, and totals are exact.
func TestIngestConcurrentPostgres(t *testing.T) {
	pool := statsPool(t)
	ingest := newIngest(pool)

	match := matchFor(5001, "RED", []int64{1, 2}, []int64{3, 4})
	raw := payloadOf(t, match)
	var mutex sync.Mutex
	recorded := 0
	for _, err := range runConcurrently(8, func(int) error {
		done, err := ingest.Ingest(context.Background(), raw)
		if done {
			mutex.Lock()
			recorded++
			mutex.Unlock()
		}
		return err
	}) {
		if err != nil {
			t.Fatalf("Ingest = %v", err)
		}
	}
	if recorded != 1 {
		t.Fatalf("recorded = %d, want 1", recorded)
	}
	wantCounters(t, pool, 1, 1, 0)
	wantCounters(t, pool, 3, 0, 1)

	const games = 12
	payloads := make([][]byte, games)
	for index := range games {
		red, blue := []int64{10, 11}, []int64{12, 13}
		if index%2 == 1 {
			red, blue = []int64{13, 12}, []int64{11, 10}
		}
		payloads[index] = payloadOf(t, matchFor(int64(6000+index), "RED", red, blue))
	}
	for _, err := range runConcurrently(games, func(index int) error {
		_, err := ingest.Ingest(context.Background(), payloads[index])
		return err
	}) {
		if err != nil {
			t.Fatalf("Ingest = %v", err)
		}
	}
	for _, user := range []int64{10, 11} {
		wantCounters(t, pool, user, games/2, games/2)
	}
	for _, user := range []int64{12, 13} {
		wantCounters(t, pool, user, games/2, games/2)
	}
}

// TestMigrationsPostgres checks that migrations repeat and reverse cleanly.
//
// Expected output: a second run is a no-op, and the down file removes every table.
func TestMigrationsPostgres(t *testing.T) {
	dsn := freshDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for range 2 {
		if err := statspostgres.Migrate(ctx, dsn); err != nil {
			t.Fatalf("Migrate() error = %v", err)
		}
	}
	pool, err := statspostgres.OpenPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	const tables = `SELECT count(*) FROM information_schema.tables WHERE table_schema = 'stats' AND table_name <> 'schema_migrations'`
	if got := count(t, pool, tables); got != 3 {
		t.Fatalf("tables = %d, want 3", got)
	}
	down, err := migrations.Files.ReadFile("000001_match_results.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(down)); err != nil {
		t.Fatalf("down migration: %v", err)
	}
	if got := count(t, pool, tables); got != 0 {
		t.Fatalf("tables after down = %d", got)
	}
}
