package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	sharedpostgres "github.com/melmut-42/ft_transcendence/backend/shared/postgres"
)

// OpenPool opens a connection pool whose every connection is limited to Schema.
//
// Parameters:
//
//   - ctx: Controls startup; its deadline bounds the connectivity check.
//   - dsn: PostgreSQL URL without a search_path; one is added.
//
// It returns a ready pool owned by the caller, who must close it.
//
// Errors:
//
//   - ErrInvalidDSN: dsn is not a PostgreSQL URL.
//   - Pool errors: see shared postgres.Open; none of them contain the DSN.
func OpenPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	scoped, err := DSNWithSchema(dsn)
	if err != nil {
		return nil, err
	}

	return sharedpostgres.Open(ctx, sharedpostgres.Config{DSN: scoped})
}
