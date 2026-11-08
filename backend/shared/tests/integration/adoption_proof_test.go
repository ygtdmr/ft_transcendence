package integration_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/melmut-42/ft_transcendence/backend/shared/postgres"
)

// This is the adoption proof the issue asks for: the pattern a service uses for
// a command (state change + idempotency ledger + outbox event, all in ONE
// transaction), written against postgres.InTx exactly as an owning service would.
// The shape follows the contract's request ledger keyed by (sender, request_id)
// and the transactional outbox.

type ledgerFixture struct {
	pool                  *pgxpool.Pool
	state, ledger, outbox string
}

func newLedgerFixture(t *testing.T) ledgerFixture {
	t.Helper()
	pool, base := txFixture(t, 4)
	f := ledgerFixture{pool: pool, state: base, ledger: base + "_ledger", outbox: base + "_outbox"}
	ctx := context.Background()
	for _, ddl := range []string{
		"CREATE TABLE " + f.ledger + " (sender int NOT NULL, request_id text NOT NULL, result int NOT NULL, PRIMARY KEY (sender, request_id))",
		"CREATE TABLE " + f.outbox + " (id serial PRIMARY KEY, event text NOT NULL)",
	} {
		if _, err := pool.Exec(ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+f.ledger+", "+f.outbox)
	})
	return f
}

func (f ledgerFixture) counts(t *testing.T) (state, ledger, outbox int) {
	t.Helper()
	for table, dst := range map[string]*int{f.state: &state, f.ledger: &ledger, f.outbox: &outbox} {
		if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	return
}

// command applies one request exactly once: it records the request in the
// ledger first (a replay hits the primary key and returns the stored result),
// then changes state, then queues the event. failAfter injects a failure at the
// named step so the test can prove the whole unit rolls back.
func (f ledgerFixture) command(ctx context.Context, sender int, requestID string, id int, failAfter string) (result int, replayed bool, err error) {
	err = postgres.InTx(ctx, f.pool, postgres.TxOptions{MaxAttempts: 3}, func(ctx context.Context, tx pgx.Tx) error {
		if _, insertErr := tx.Exec(ctx, "INSERT INTO "+f.ledger+" (sender, request_id, result) VALUES ($1, $2, $3)", sender, requestID, id); insertErr != nil {
			if _, unique := postgres.UniqueViolation(insertErr); unique {
				return errReplay
			}
			return insertErr
		}
		if failAfter == "ledger" {
			return errInjected
		}
		if _, execErr := tx.Exec(ctx, "INSERT INTO "+f.state+" (id, n) VALUES ($1, 1)", id); execErr != nil {
			return execErr
		}
		if failAfter == "state" {
			return errInjected
		}
		if _, execErr := tx.Exec(ctx, "INSERT INTO "+f.outbox+" (event) VALUES ($1)", fmt.Sprintf("room_created:%d", id)); execErr != nil {
			return execErr
		}
		if failAfter == "outbox" {
			return errInjected
		}
		return nil
	})
	if errors.Is(err, errReplay) {
		// The request was already applied: answer from the ledger, change nothing.
		replyErr := f.pool.QueryRow(ctx, "SELECT result FROM "+f.ledger+" WHERE sender = $1 AND request_id = $2", sender, requestID).Scan(&result)
		return result, true, replyErr
	}
	return id, false, err
}

var (
	errInjected = errors.New("injected failure")
	errReplay   = errors.New("request already applied")
)

func TestStateLedgerAndOutboxCommitOrRollBackTogether(t *testing.T) {
	f := newLedgerFixture(t)
	ctx := context.Background()

	if _, replayed, err := f.command(ctx, 42, "req-1", 7, ""); err != nil || replayed {
		t.Fatalf("first command: replayed=%v err=%v", replayed, err)
	}
	if s, l, o := f.counts(t); s != 1 || l != 1 || o != 1 {
		t.Fatalf("after success: state=%d ledger=%d outbox=%d, want 1 1 1", s, l, o)
	}

	// A failure at ANY step leaves no trace in ANY of the three tables.
	for _, step := range []string{"ledger", "state", "outbox"} {
		_, _, err := f.command(ctx, 42, "req-fail-"+step, 100+len(step), step)
		if !errors.Is(err, errInjected) {
			t.Fatalf("failure after %s: err = %v", step, err)
		}
		if s, l, o := f.counts(t); s != 1 || l != 1 || o != 1 {
			t.Errorf("failure after %s left a partial result: state=%d ledger=%d outbox=%d, want 1 1 1 (a recorded request without its state, or an event without its state, is the bug this prevents)", step, s, l, o)
		}
	}
}

func TestAnIdenticalRetryReplaysTheStoredResultWithoutDuplicatingAnything(t *testing.T) {
	f := newLedgerFixture(t)
	ctx := context.Background()

	if _, _, err := f.command(ctx, 42, "req-1", 7, ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		result, replayed, err := f.command(ctx, 42, "req-1", 999, "") // same request, even if the retry's payload differs
		if err != nil || !replayed || result != 7 {
			t.Fatalf("retry %d: result=%d replayed=%v err=%v; want the original result 7 replayed", i+1, result, replayed, err)
		}
	}
	if s, l, o := f.counts(t); s != 1 || l != 1 || o != 1 {
		t.Errorf("after 3 retries: state=%d ledger=%d outbox=%d; retries must change nothing", s, l, o)
	}
	// A different sender may reuse the same request id: the ledger key is per sender.
	if _, replayed, err := f.command(ctx, 43, "req-1", 8, ""); err != nil || replayed {
		t.Errorf("another sender with the same request id: replayed=%v err=%v", replayed, err)
	}
}

func TestAnUnknownCommitOutcomeIsResolvedByRetryingTheSameRequest(t *testing.T) {
	f := newLedgerFixture(t)

	// The first attempt's context ends right at the commit: the outcome is unknown.
	ctx, cancel := context.WithCancel(context.Background())
	err := postgres.InTx(ctx, f.pool, postgres.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, "INSERT INTO "+f.ledger+" (sender, request_id, result) VALUES (42, 'req-u', 5)"); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, "INSERT INTO "+f.state+" (id, n) VALUES (5, 1)"); e != nil {
			return e
		}
		cancel()
		return nil
	})
	if !errors.Is(err, postgres.ErrCommitOutcomeUnknown) {
		t.Fatalf("err = %v, want ErrCommitOutcomeUnknown", err)
	}

	// The caller does not guess: it retries the identical request. Whether or not
	// the first commit landed, the end state is exactly one application.
	if _, _, err := f.command(context.Background(), 42, "req-u", 5, ""); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if s, l, o := f.counts(t); s != 1 || l != 1 || o > 1 {
		t.Errorf("after the retry: state=%d ledger=%d outbox=%d, want exactly one application", s, l, o)
	}
}
