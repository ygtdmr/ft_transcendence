package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/service"
)

var _ service.ReadinessProbe = (*HealthRepository)(nil)

type HealthRepository struct {
	pool *pgxpool.Pool
}

// NewHealth returns a database probe; pool ownership stays with the caller.
func NewHealth(pool *pgxpool.Pool) *HealthRepository {
	return &HealthRepository{pool: pool}
}

// Ping checks that a connection to the database can be acquired and answers.
//
// Parameters:
//
//   - ctx: Controls the check, including its deadline.
//
// Errors:
//
//   - Database errors: The pool is closed or the server is unreachable; context
//     cancellation and deadlines are included.
func (r *HealthRepository) Ping(ctx context.Context) error {
	return r.pool.Ping(ctx)
}
