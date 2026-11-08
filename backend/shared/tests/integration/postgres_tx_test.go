package integration_test

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/melmut-42/ft_transcendence/backend/shared/postgres"
)

var tableSeq atomic.Int32

// txFixture opens a pool and creates a uniquely named table, dropped afterwards.
func txFixture(t *testing.T, maxConns int32) (*pgxpool.Pool, string) {
	t.Helper()
	dsn := requireEnv(t, "POSTGRES_TEST_DATABASE_URL")
	pool, err := postgres.Open(context.Background(), postgres.Config{DSN: dsn, MaxConns: maxConns})
	if err != nil {
		t.Fatal(err)
	}
	table := fmt.Sprintf("tx_itest_%d_%d", time.Now().UnixNano()%1_000_000, tableSeq.Add(1))
	if _, err := pool.Exec(context.Background(), "CREATE TABLE "+table+" (id int PRIMARY KEY, n int NOT NULL, name text UNIQUE)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+table)
		pool.Close()
	})
	return pool, table
}

func rowCount(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestInTxCommitsOnSuccessAndRollsBackOnError(t *testing.T) {
	pool, table := txFixture(t, 4)
	ctx := context.Background()

	if err := postgres.InTx(ctx, pool, postgres.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO "+table+" (id, n) VALUES (1, 10)")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if rowCount(t, pool, table) != 1 {
		t.Fatal("the committed row is missing")
	}

	errBusiness := errors.New("room is full")
	err := postgres.InTx(ctx, pool, postgres.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO "+table+" (id, n) VALUES (2, 20)"); err != nil {
			return err
		}
		return fmt.Errorf("join: %w", errBusiness)
	})
	if !errors.Is(err, errBusiness) {
		t.Fatalf("the service's own error must come back intact, got %v", err)
	}
	if rowCount(t, pool, table) != 1 {
		t.Error("a failed transaction left its insert behind")
	}
}

func TestInTxRollsBackAndRepanicsWhenTheFunctionPanics(t *testing.T) {
	pool, table := txFixture(t, 2)
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = postgres.InTx(context.Background(), pool, postgres.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			_, _ = tx.Exec(ctx, "INSERT INTO "+table+" (id, n) VALUES (1, 1)")
			panic("boom")
		})
	}()
	if recovered != "boom" {
		t.Fatalf("the panic must continue, recovered %v", recovered)
	}
	if rowCount(t, pool, table) != 0 {
		t.Error("a panicking transaction left its insert behind")
	}
	if err := eventually(t, 5*time.Second, func() error {
		if pool.Stat().AcquiredConns() != 0 {
			return errors.New("connection still acquired")
		}
		return nil
	}); err != nil {
		t.Errorf("the panic leaked a connection: %v", err)
	}
}

func TestInTxStopsOnCancellationRollsBackAndFreesTheConnection(t *testing.T) {
	pool, table := txFixture(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	begin := time.Now()
	err := postgres.InTx(ctx, pool, postgres.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO "+table+" (id, n) VALUES (1, 1)"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "SELECT pg_sleep(10)")
		return err
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline", err)
	}
	if elapsed := time.Since(begin); elapsed > 3*time.Second {
		t.Errorf("a 10s query under a 300ms deadline took %s", elapsed)
	}
	if rowCount(t, pool, table) != 0 {
		t.Error("the cancelled transaction's insert survived")
	}
	if err := eventually(t, 5*time.Second, func() error {
		if pool.Stat().AcquiredConns() != 0 {
			return errors.New("connection still acquired")
		}
		return nil
	}); err != nil {
		t.Errorf("a cancelled transaction left a connection checked out: %v", err)
	}
	if err := postgres.Check(context.Background(), pool, 2*time.Second); err != nil {
		t.Errorf("the pool is unusable afterwards: %v", err)
	}
}

func TestInTxReportsAnInterruptedCommitAsOutcomeUnknown(t *testing.T) {
	pool, _ := txFixture(t, 2)
	ctx, cancel := context.WithCancel(context.Background())
	err := postgres.InTx(ctx, pool, postgres.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		cancel() // the request goes away just before the commit
		return nil
	})
	if !errors.Is(err, postgres.ErrCommitOutcomeUnknown) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want ErrCommitOutcomeUnknown wrapping context.Canceled", err)
	}
}

func TestInTxReadOnlyRejectsWrites(t *testing.T) {
	pool, table := txFixture(t, 2)
	err := postgres.InTx(context.Background(), pool, postgres.TxOptions{ReadOnly: true}, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO "+table+" (id, n) VALUES (1, 1)")
		return err
	})
	if err == nil || rowCount(t, pool, table) != 0 {
		t.Errorf("a read-only transaction accepted a write: %v", err)
	}
}

// serializableIncrement reads the counter, waits for the other transaction to
// have read it too (first attempt only), then writes n+1: a classic write skew
// that SERIALIZABLE must break by aborting one of them.
func serializableIncrement(t *testing.T, pool *pgxpool.Pool, table string, maxAttempts int, barrier *sync.WaitGroup, attempts *atomic.Int32) error {
	first := true
	return postgres.InTx(context.Background(), pool, postgres.TxOptions{Isolation: pgx.Serializable, MaxAttempts: maxAttempts},
		func(ctx context.Context, tx pgx.Tx) error {
			attempts.Add(1)
			var n int
			if err := tx.QueryRow(ctx, "SELECT n FROM "+table+" WHERE id = 1").Scan(&n); err != nil {
				return err
			}
			if first {
				first = false
				barrier.Done()
				barrier.Wait()
			}
			_, err := tx.Exec(ctx, "UPDATE "+table+" SET n = $1 WHERE id = 1", n+1)
			return err
		})
}

func TestInTxRetriesSerializationFailuresUpToMaxAttempts(t *testing.T) {
	pool, table := txFixture(t, 4)
	if _, err := pool.Exec(context.Background(), "INSERT INTO "+table+" (id, n) VALUES (1, 0)"); err != nil {
		t.Fatal(err)
	}

	var barrier sync.WaitGroup
	barrier.Add(2)
	var attempts atomic.Int32
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() { defer wg.Done(); errs[i] = serializableIncrement(t, pool, table, 5, &barrier, &attempts) }()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("transaction %d failed despite retries: %v", i, err)
		}
	}
	var n int
	_ = pool.QueryRow(context.Background(), "SELECT n FROM "+table+" WHERE id = 1").Scan(&n)
	if n != 2 {
		t.Errorf("counter = %d, want 2: an increment was lost", n)
	}
	if attempts.Load() <= 2 {
		t.Errorf("only %d attempts ran; a conflicting pair must need a retry", attempts.Load())
	}
}

func TestInTxDoesNotRetryWithoutMaxAttempts(t *testing.T) {
	pool, table := txFixture(t, 4)
	if _, err := pool.Exec(context.Background(), "INSERT INTO "+table+" (id, n) VALUES (1, 0)"); err != nil {
		t.Fatal(err)
	}
	var barrier sync.WaitGroup
	barrier.Add(2)
	var attempts atomic.Int32
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() { defer wg.Done(); errs[i] = serializableIncrement(t, pool, table, 1, &barrier, &attempts) }()
	}
	wg.Wait()

	failures := 0
	for _, err := range errs {
		if err != nil {
			if !postgres.IsSerializationFailure(err) {
				t.Errorf("unexpected failure kind: %v", err)
			}
			failures++
		}
	}
	if failures != 1 || attempts.Load() != 2 {
		t.Errorf("failures=%d attempts=%d, want exactly one serialization failure and no retry", failures, attempts.Load())
	}
}

func TestInTxDoesNotRetryNonRetryableErrors(t *testing.T) {
	pool, table := txFixture(t, 2)
	var runs atomic.Int32
	err := postgres.InTx(context.Background(), pool, postgres.TxOptions{MaxAttempts: 5}, func(ctx context.Context, tx pgx.Tx) error {
		runs.Add(1)
		_, err := tx.Exec(ctx, "INSERT INTO "+table+" (id, n, name) VALUES (1, 1, 'ann'), (2, 2, 'ann')")
		return err
	})
	constraint, unique := postgres.UniqueViolation(err)
	if !unique || !strings.HasSuffix(constraint, "_name_key") || runs.Load() != 1 {
		t.Errorf("err=%v constraint=%q runs=%d; a unique violation must be classified and run once", err, constraint, runs.Load())
	}
	if postgres.IsRetryable(err) {
		t.Error("a unique violation is not retryable")
	}
}

func TestInTxReportsAnOutageAsUnavailableAndRecovers(t *testing.T) {
	pool, _ := txFixture(t, 2)
	container := requireEnv(t, "POSTGRES_TEST_CONTAINER")

	docker(t, "stop", container)
	t.Cleanup(func() { _ = exec.Command("docker", "start", container).Run() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := postgres.InTx(ctx, pool, postgres.TxOptions{}, func(context.Context, pgx.Tx) error { return nil })
	if err == nil || !postgres.IsConnectionFailure(err) {
		t.Fatalf("an outage must be a connection failure (503), got %v", err)
	}

	docker(t, "start", container)
	if err := eventually(t, 60*time.Second, func() error {
		return postgres.InTx(context.Background(), pool, postgres.TxOptions{}, func(context.Context, pgx.Tx) error { return nil })
	}); err != nil {
		t.Fatalf("transactions did not recover after the restart: %v", err)
	}
}
