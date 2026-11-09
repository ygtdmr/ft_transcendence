package integration_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	userpostgres "github.com/melmut-42/ft_transcendence/backend/user-service/internal/repository/postgres"
)

// latestMigration is the highest migration number in migrations/.
const latestMigration = 7

// Postgres error codes the schema tests expect.
const (
	codeUnique     = "23505"
	codeCheck      = "23514"
	codeForeignKey = "23503"
)

// schemaPool returns a pool on a freshly migrated, empty database.
//
// Parameters:
//
//   - t: Go test runner; the pool is closed and the database dropped at the end.
//
// The pool allows 16 connections so concurrent tests can hold one each.
func schemaPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := freshDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := userpostgres.Migrate(ctx, dsn); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	scoped, err := userpostgres.DSNWithSchema(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(scoped)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 16
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	return pool
}

// seedProfiles inserts one profile per id, named player_<id>.
func seedProfiles(t *testing.T, pool *pgxpool.Pool, ids ...int64) {
	t.Helper()
	for _, id := range ids {
		_, err := pool.Exec(context.Background(),
			"INSERT INTO profiles (user_id, username) VALUES ($1, $2)", id, fmt.Sprintf("player_%d", id))
		if err != nil {
			t.Fatalf("seed profile %d: %v", id, err)
		}
	}
}

// exec runs a statement and returns its error, so tests can inspect the failure.
func exec(pool *pgxpool.Pool, sql string, args ...any) error {
	_, err := pool.Exec(context.Background(), sql, args...)

	return err
}

// mustExec runs a statement that is expected to succeed.
func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if err := exec(pool, sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// count runs a SELECT count(*) query.
func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}

	return n
}

// wantViolation checks that err is a PostgreSQL error with the given code and
// constraint (or index) name.
func wantViolation(t *testing.T, err error, code, constraint string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("error = %v, want PostgreSQL error %s on %s", err, code, constraint)
	}
	if pgErr.Code != code || pgErr.ConstraintName != constraint {
		t.Fatalf("error = %s on %q (%s), want %s on %q", pgErr.Code, pgErr.ConstraintName, pgErr.Message, code, constraint)
	}
}

// raceResult is the outcome of one goroutine in a race.
type raceResult struct {
	index int
	err   error
}

// race runs every function at the same moment, each on its own connection.
//
// Parameters:
//
//   - t: Go test runner.
//   - pool: Source of connections. Each goroutine acquires one before the start signal.
//   - fns: Work to run. Each receives the pool-backed connection executor.
//
// Flow:
//
//  1. Acquire one connection per function, so no goroutine waits for a connection later.
//  2. Release all goroutines together by closing a barrier channel.
//  3. Collect every result.
//
// It returns the results in completion order.
func race(t *testing.T, pool *pgxpool.Pool, fns ...func(ctx context.Context, conn *pgxpool.Conn) error) []raceResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	conns := make([]*pgxpool.Conn, len(fns))
	for i := range fns {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("acquire connection %d: %v", i, err)
		}
		conns[i] = conn
	}

	start := make(chan struct{})
	results := make(chan raceResult, len(fns))
	var wg sync.WaitGroup
	for i, fn := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- raceResult{index: i, err: fn(ctx, conns[i])}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for _, conn := range conns {
		conn.Release()
	}

	var out []raceResult
	for r := range results {
		out = append(out, r)
	}

	return out
}

// countSuccesses returns how many race results had no error.
func countSuccesses(results []raceResult) int {
	n := 0
	for _, r := range results {
		if r.err == nil {
			n++
		}
	}

	return n
}
