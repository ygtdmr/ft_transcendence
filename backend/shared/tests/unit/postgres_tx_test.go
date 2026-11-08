package unit_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/melmut-42/ft_transcendence/backend/shared/postgres"
)

func pgErr(code, constraint string) error {
	return fmt.Errorf("exec: %w", &pgconn.PgError{Code: code, ConstraintName: constraint, Message: "secret-looking detail: user svc"})
}

func TestPostgresErrorClassification(t *testing.T) {
	if !postgres.IsSerializationFailure(pgErr("40001", "")) || postgres.IsSerializationFailure(pgErr("40P01", "")) {
		t.Error("IsSerializationFailure must match 40001 only")
	}
	if !postgres.IsDeadlock(pgErr("40P01", "")) || postgres.IsDeadlock(pgErr("40001", "")) {
		t.Error("IsDeadlock must match 40P01 only")
	}
	if !postgres.IsRetryable(pgErr("40001", "")) || !postgres.IsRetryable(pgErr("40P01", "")) {
		t.Error("serialization failures and deadlocks are retryable")
	}
	for _, code := range []string{"23505", "23503", "23514", "42P01", "22001", "XX000", "57014"} {
		if postgres.IsRetryable(pgErr(code, "")) {
			t.Errorf("%s must not be retried blindly", code)
		}
	}
	if postgres.IsRetryable(errors.New("plain")) || postgres.IsRetryable(nil) || postgres.IsRetryable(context.Canceled) {
		t.Error("non-database errors are not retryable")
	}
}

func TestPostgresConstraintViolationHelpers(t *testing.T) {
	if name, ok := postgres.UniqueViolation(pgErr("23505", "username_key")); !ok || name != "username_key" {
		t.Errorf("UniqueViolation = %q, %v", name, ok)
	}
	if name, ok := postgres.ForeignKeyViolation(pgErr("23503", "rooms_host_fkey")); !ok || name != "rooms_host_fkey" {
		t.Errorf("ForeignKeyViolation = %q, %v", name, ok)
	}
	if name, ok := postgres.CheckViolation(pgErr("23514", "chk_team")); !ok || name != "chk_team" {
		t.Errorf("CheckViolation = %q, %v", name, ok)
	}
	if _, ok := postgres.UniqueViolation(pgErr("23503", "x")); ok {
		t.Error("UniqueViolation matched a foreign-key error")
	}
	if _, ok := postgres.UniqueViolation(errors.New("plain")); ok {
		t.Error("UniqueViolation matched a plain error")
	}
}

func TestIsConnectionFailure(t *testing.T) {
	for _, code := range []string{"08000", "08003", "08006", "08001", "57P01", "57P02", "57P03", "53300"} {
		if !postgres.IsConnectionFailure(pgErr(code, "")) {
			t.Errorf("%s must count as a connection failure (503, not a domain answer)", code)
		}
	}
	for _, code := range []string{"23505", "40001", "42601", "22P02"} {
		if postgres.IsConnectionFailure(pgErr(code, "")) {
			t.Errorf("%s is not a connection failure", code)
		}
	}
	if !postgres.IsConnectionFailure(fmt.Errorf("open: %w", postgres.ErrUnavailable)) ||
		!postgres.IsConnectionFailure(fmt.Errorf("open: %w", postgres.ErrConnect)) ||
		!postgres.IsConnectionFailure(&pgconn.ConnectError{}) {
		t.Error("the package's own unavailability errors and connect errors must count")
	}
	if postgres.IsConnectionFailure(errors.New("plain")) || postgres.IsConnectionFailure(pgx.ErrNoRows) {
		t.Error("unrelated errors must not count")
	}
}

type failingBeginner struct{ err error }

func (f failingBeginner) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) { return nil, f.err }

func TestInTxReportsABeginFailureAsUnavailableWithoutLeakingTheCause(t *testing.T) {
	ran := false
	err := postgres.InTx(context.Background(), failingBeginner{errors.New("dial tcp db.internal:5432: password=hunter2")}, postgres.TxOptions{},
		func(context.Context, pgx.Tx) error { ran = true; return nil })
	if !errors.Is(err, postgres.ErrUnavailable) || ran {
		t.Fatalf("err = %v, fn ran = %v", err, ran)
	}
	if got := err.Error(); containsAny(got, "hunter2", "db.internal") {
		t.Errorf("the driver error leaked: %s", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = postgres.InTx(ctx, failingBeginner{errors.New("x")}, postgres.TxOptions{}, func(context.Context, pgx.Tx) error { return nil })
	if !errors.Is(err, postgres.ErrUnavailable) || !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled context must stay visible to the caller: %v", err)
	}
}

func containsAny(s string, parts ...string) bool {
	for _, p := range parts {
		if len(p) > 0 && indexOf(s, p) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
