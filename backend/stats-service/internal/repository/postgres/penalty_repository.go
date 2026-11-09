package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/service"
)

var _ service.PenaltyStore = (*PenaltyRepository)(nil)

// PenaltyRepository stores leave penalties and their running total.
type PenaltyRepository struct {
	pool *pgxpool.Pool
}

// NewPenalties returns a penalty store; pool ownership stays with the caller.
func NewPenalties(pool *pgxpool.Pool) *PenaltyRepository {
	return &PenaltyRepository{pool: pool}
}

// RecordPenalty implements service.PenaltyStore. The primary key on event_id
// decides a race: a second delivery waits for the first and inserts nothing.
func (r *PenaltyRepository) RecordPenalty(ctx context.Context, eventID string, penalty service.PlayerPenalized) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, classify(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if err := lockUser(ctx, tx, penalty.UserID); err != nil {
		return false, classify(err)
	}
	deleted, err := isDeleted(ctx, tx, penalty.UserID)
	if err != nil {
		return false, classify(err)
	}
	var owner *int64
	if !deleted {
		owner = &penalty.UserID
	}

	inserted, err := tx.Exec(ctx, `
		INSERT INTO penalties (event_id, user_id, room_id, game_id, role, reason, points, penalized_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (event_id) DO NOTHING`,
		eventID, owner, penalty.RoomID, penalty.GameID, penalty.Role, penalty.Reason, *penalty.Points, penalty.PenalizedAt)
	if err != nil {
		return false, classify(err)
	}
	if inserted.RowsAffected() == 0 {
		return false, nil
	}
	if deleted {
		return true, classify(tx.Commit(ctx))
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO user_stats (user_id, penalty_points) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE
		SET penalty_points = user_stats.penalty_points + EXCLUDED.penalty_points, updated_at = now()`,
		penalty.UserID, *penalty.Points); err != nil {
		return false, classify(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, classify(err)
	}

	return true, nil
}
