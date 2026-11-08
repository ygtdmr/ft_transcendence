package postgres

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrCommitOutcomeUnknown reports that the commit was interrupted (the context
// ended, or the connection dropped) after it may already have reached the
// server, so the changes may or may not be durable. The caller must not assume
// failure: retry through an idempotency key (a command ledger entry, a request
// ID) or re-read before acting. It also wraps the context error when there was
// one, so errors.Is(err, context.DeadlineExceeded) still works.
var ErrCommitOutcomeUnknown = errors.New("postgres: commit outcome unknown")

// Beginner starts a transaction. *pgxpool.Pool and *pgx.Conn both satisfy it.
type Beginner interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// TxOptions configures InTx.
type TxOptions struct {
	// Isolation is the transaction isolation level; empty uses the server default.
	Isolation pgx.TxIsoLevel
	// ReadOnly makes any write fail.
	ReadOnly bool
	// MaxAttempts is how many times the whole function may run when the server
	// aborts the transaction with a serialization failure or a deadlock (the only
	// failures that are safe to simply run again). Zero or one means no retry;
	// the maximum is 10. fn must therefore be safe to re-run: no side effect
	// outside the transaction (no HTTP call, no publish) inside it.
	MaxAttempts int
}

const (
	maxAttemptsLimit = 10
	rollbackTimeout  = 5 * time.Second
	backoffBase      = 10 * time.Millisecond
	backoffCap       = 200 * time.Millisecond
)

// InTx runs fn inside one transaction, the way every service should hold its
// transaction boundary:
//
//   - fn returns nil: the transaction commits.
//   - fn returns an error: it rolls back and the same error is returned
//     unchanged, so errors.Is/As on the service's own errors keep working.
//   - fn panics: it rolls back and the panic continues.
//   - ctx ends mid-transaction: the server-side work is cancelled, the
//     transaction is rolled back, the connection goes back to the pool, and the
//     context error is returned. Rollback uses a context detached from the
//     caller's cancellation (bounded to 5s) so a cancelled request cannot leave
//     a transaction open.
//   - the commit itself is interrupted: ErrCommitOutcomeUnknown.
//
// Only serialization failures and deadlocks are retried, up to MaxAttempts, with
// a short jittered backoff that respects ctx.
//
// fn receives ctx and the pgx.Tx. Pass that ctx to every statement so
// cancellation reaches the database. A begin failure returns ErrUnavailable.
func InTx(ctx context.Context, db Beginner, opts TxOptions, fn func(ctx context.Context, tx pgx.Tx) error) error {
	attempts := opts.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	if attempts > maxAttemptsLimit {
		attempts = maxAttemptsLimit
	}

	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		err = runTx(ctx, db, opts, fn)
		if err == nil || !IsRetryable(err) || attempt == attempts {
			return err
		}
		if waitErr := sleep(ctx, backoff(attempt)); waitErr != nil {
			return err // the caller gave up; report the database's verdict
		}
	}
	return err
}

func runTx(ctx context.Context, db Beginner, opts TxOptions, fn func(ctx context.Context, tx pgx.Tx) error) (err error) {
	accessMode := pgx.ReadWrite
	if opts.ReadOnly {
		accessMode = pgx.ReadOnly
	}
	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: opts.Isolation, AccessMode: accessMode})
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%w: %w", ErrUnavailable, ctx.Err())
		}
		return ErrUnavailable
	}

	committed := false
	defer func() {
		// Runs on normal return, error return and panic alike.
		if !committed {
			rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()

	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			cause := ctx.Err()
			if cause == nil {
				cause = err
			}
			return fmt.Errorf("%w: %w", ErrCommitOutcomeUnknown, cause)
		}
		return err
	}
	committed = true
	return nil
}

func backoff(attempt int) time.Duration {
	d := backoffBase << (attempt - 1)
	if d > backoffCap || d <= 0 {
		d = backoffCap
	}
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SQLSTATE codes the helpers classify.
const (
	codeSerializationFailure = "40001"
	codeDeadlockDetected     = "40P01"
	codeUniqueViolation      = "23505"
	codeForeignKeyViolation  = "23503"
	codeCheckViolation       = "23514"
)

func pgError(err error) *pgconn.PgError {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr
	}
	return nil
}

// IsSerializationFailure reports SQLSTATE 40001.
func IsSerializationFailure(err error) bool {
	pgErr := pgError(err)
	return pgErr != nil && pgErr.Code == codeSerializationFailure
}

// IsDeadlock reports SQLSTATE 40P01.
func IsDeadlock(err error) bool {
	pgErr := pgError(err)
	return pgErr != nil && pgErr.Code == codeDeadlockDetected
}

// IsRetryable reports whether running the whole transaction again can succeed:
// a serialization failure or a deadlock. Nothing else is safe to retry blindly.
func IsRetryable(err error) bool { return IsSerializationFailure(err) || IsDeadlock(err) }

// UniqueViolation reports a unique-constraint violation (23505) and the
// constraint's name, so a service can map "username_key" to USERNAME_TAKEN. The
// name is schema detail: use it to choose a response, never to build one.
func UniqueViolation(err error) (constraint string, ok bool) {
	if pgErr := pgError(err); pgErr != nil && pgErr.Code == codeUniqueViolation {
		return pgErr.ConstraintName, true
	}
	return "", false
}

// ForeignKeyViolation reports a foreign-key violation (23503) and the constraint.
func ForeignKeyViolation(err error) (constraint string, ok bool) {
	if pgErr := pgError(err); pgErr != nil && pgErr.Code == codeForeignKeyViolation {
		return pgErr.ConstraintName, true
	}
	return "", false
}

// CheckViolation reports a check-constraint violation (23514) and the constraint.
func CheckViolation(err error) (constraint string, ok bool) {
	if pgErr := pgError(err); pgErr != nil && pgErr.Code == codeCheckViolation {
		return pgErr.ConstraintName, true
	}
	return "", false
}

// IsConnectionFailure reports errors that mean the database cannot be reached or
// is going away (connection class 08, shutdown 57P01-57P03, too many
// connections 53300, or a failure to connect). Services should answer these
// with an availability error (503), never a not-found, invalid or offline
// verdict.
func IsConnectionFailure(err error) bool {
	if pgErr := pgError(err); pgErr != nil {
		code := pgErr.Code
		return len(code) >= 2 && code[:2] == "08" || code == "57P01" || code == "57P02" || code == "57P03" || code == "53300"
	}
	var connectErr *pgconn.ConnectError
	return errors.As(err, &connectErr) || errors.Is(err, ErrUnavailable) || errors.Is(err, ErrConnect)
}
