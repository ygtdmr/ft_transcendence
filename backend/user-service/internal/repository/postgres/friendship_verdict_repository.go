package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

var _ service.FriendshipVerdictReader = (*FriendshipVerdictRepository)(nil)

// FriendshipVerdictRepository reads the active-friend verdict.
type FriendshipVerdictRepository struct {
	pool *pgxpool.Pool
}

// NewFriendshipVerdicts returns a verdict reader; pool ownership stays with the caller.
func NewFriendshipVerdicts(pool *pgxpool.Pool) *FriendshipVerdictRepository {
	return &FriendshipVerdictRepository{pool: pool}
}

// ActiveFriendship implements service.FriendshipVerdictReader. One statement
// reads every condition from the same snapshot. The block check does not trust
// the friendship's active flag alone.
func (r *FriendshipVerdictRepository) ActiveFriendship(ctx context.Context, firstID, secondID int64) (bool, error) {
	var active bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM friendships f
			WHERE f.user_low = LEAST($1::bigint, $2::bigint)
			  AND f.user_high = GREATEST($1::bigint, $2::bigint)
			  AND f.active
			  AND NOT EXISTS (SELECT 1 FROM blocks b
			                  WHERE (b.blocker_id = $1 AND b.blocked_id = $2)
			                     OR (b.blocker_id = $2 AND b.blocked_id = $1))
			  AND NOT EXISTS (SELECT 1 FROM deletion_operations d WHERE d.user_id IN ($1, $2)))`,
		firstID, secondID).Scan(&active)
	if err != nil {
		return false, classify(err)
	}

	return active, nil
}
