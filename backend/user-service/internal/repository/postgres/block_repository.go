package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

var _ service.BlockStore = (*BlockRepository)(nil)

// BlockRepository stores blocks and their outbox events.
type BlockRepository struct {
	pool *pgxpool.Pool
}

// NewBlocks returns a block store; pool ownership stays with the caller.
func NewBlocks(pool *pgxpool.Pool) *BlockRepository {
	return &BlockRepository{pool: pool}
}

// PlaceBlock implements service.BlockStore.
//
// The blocks_apply trigger cancels the pending request and suspends the
// friendship, so both are read first. Every writer of those rows takes the
// pair lock, which keeps them unchanged until the insert.
func (r *BlockRepository) PlaceBlock(ctx context.Context, blockerID, blockedID int64,
	events service.BlockEventBuilder) (time.Time, error) {
	var blockedAt time.Time
	err := inTx(ctx, r.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, lockPair, blockerID, blockedID); err != nil {
			return err
		}
		blocker, blocked, err := loadPair(ctx, tx, blockerID, blockedID)
		if err != nil {
			return err
		}
		placement := service.BlockPlacement{Blocker: blocker, Blocked: blocked}
		if err := readBlockEffects(ctx, tx, &placement); err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `INSERT INTO blocks (blocker_id, blocked_id) VALUES ($1, $2)
			ON CONFLICT DO NOTHING RETURNING created_at`, blockerID, blockedID).Scan(&blockedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return tx.QueryRow(ctx, `SELECT created_at FROM blocks WHERE blocker_id = $1 AND blocked_id = $2`,
				blockerID, blockedID).Scan(&blockedAt)
		}
		if err != nil {
			return err
		}

		return appendEvents(ctx, tx, events(placement))
	})
	if err != nil {
		return time.Time{}, err
	}

	return blockedAt.UTC(), nil
}

// readBlockEffects fills in what a new block between the pair is about to change.
func readBlockEffects(ctx context.Context, tx pgx.Tx, placement *service.BlockPlacement) error {
	blockerID, blockedID := placement.Blocker.UserID, placement.Blocked.UserID
	if err := tx.QueryRow(ctx, activeFriendship, blockerID, blockedID).Scan(&placement.FriendshipSuspended); err != nil {
		return err
	}

	request := service.StoredFriendRequest{State: service.FriendRequestCancelled}
	var requesterID int64
	err := tx.QueryRow(ctx, `SELECT id, requester_id, created_at FROM friend_requests
		WHERE user_low = LEAST($1::bigint, $2::bigint) AND user_high = GREATEST($1::bigint, $2::bigint) AND state = 'pending'`,
		blockerID, blockedID).Scan(&request.ID, &requesterID, &request.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	request.From, request.To = placement.Blocker, placement.Blocked
	if requesterID == blockedID {
		request.From, request.To = placement.Blocked, placement.Blocker
	}
	request.CreatedAt = request.CreatedAt.UTC()
	placement.CancelledRequest = &request

	return nil
}

// LiftBlock implements service.BlockStore.
//
// The blocks_release trigger reactivates the friendship only when no block
// remains, and an active friendship cannot exist beside a block. So a friendship
// that is active after this delete was restored by it.
func (r *BlockRepository) LiftBlock(ctx context.Context, blockerID, blockedID int64,
	events service.RestoreEventBuilder) error {
	return inTx(ctx, r.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, lockPair, blockerID, blockedID); err != nil {
			return err
		}
		blocker, blocked, err := loadPair(ctx, tx, blockerID, blockedID)
		if err != nil {
			return err
		}
		removed, err := tx.Exec(ctx, `DELETE FROM blocks WHERE blocker_id = $1 AND blocked_id = $2`, blockerID, blockedID)
		if err != nil || removed.RowsAffected() == 0 {
			return err
		}
		var restored bool
		if err := tx.QueryRow(ctx, activeFriendship, blockerID, blockedID).Scan(&restored); err != nil {
			return err
		}
		if !restored {
			return nil
		}

		return appendEvents(ctx, tx, events(blocker, blocked))
	})
}

func appendEvents(ctx context.Context, tx pgx.Tx, events []service.OutboxEvent) error {
	for _, event := range events {
		if _, err := AppendEvent(ctx, tx, event); err != nil {
			return err
		}
	}

	return nil
}
