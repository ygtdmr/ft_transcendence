package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrUnavailable reports that an open pool could not reach the database. A pool
// that was opened successfully is "connected"; Check tells whether the
// dependency is still available right now.
var ErrUnavailable = errors.New("postgres: dependency unavailable")

// Check pings the database through pool within timeout (default 2s when zero)
// and returns nil or an error wrapping ErrUnavailable. The driver error is
// dropped because it may contain host and user details; a context error from
// ctx is preserved so callers can tell shutdown from an outage.
//
// Check is meant for readiness endpoints: it never blocks longer than timeout.
func Check(ctx context.Context, pool *pgxpool.Pool, timeout time.Duration) error {
	if pool == nil {
		return fmt.Errorf("%w: pool is not open", ErrUnavailable)
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := pool.Ping(pingCtx); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%w: %w", ErrUnavailable, ctx.Err())
		}
		return ErrUnavailable
	}
	return nil
}
