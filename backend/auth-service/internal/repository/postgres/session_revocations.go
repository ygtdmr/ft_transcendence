package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
)

var _ service.RevocationRepository = (*AuthRepository)(nil)

func (t *authTx) enqueueSessionRevocation(ctx context.Context, id string) error {
	_, err := t.tx.Exec(ctx, `
		INSERT INTO session_revocations (session_id, user_id, revoked_at)
		SELECT id, user_id, revoked_at FROM sessions WHERE id = $1
		ON CONFLICT (session_id) DO NOTHING`, id)
	return err
}

// PendingSessionRevocations reads a bounded batch for idempotent delivery.
func (r *AuthRepository) PendingSessionRevocations(ctx context.Context, limit int) ([]service.SessionRevocation, error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("revocation batch limit must be between 1 and 1000")
	}
	rows, err := r.pool.Query(ctx, `
		SELECT session_id::text, user_id, cause, revoked_at, next_attempt_at FROM session_revocations
		WHERE delivered_at IS NULL AND next_attempt_at <= now()
		ORDER BY next_attempt_at, revoked_at, session_id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pending []service.SessionRevocation
	for rows.Next() {
		var event service.SessionRevocation
		if err := rows.Scan(&event.SessionID, &event.UserID, &event.Cause, &event.RevokedAt, &event.NextAttemptAt); err != nil {
			return nil, err
		}
		event.RevokedAt = event.RevokedAt.UTC()
		event.NextAttemptAt = event.NextAttemptAt.UTC()
		pending = append(pending, event)
	}
	return pending, rows.Err()
}

// MarkSessionRevocationDelivered retains the first successful delivery timestamp.
func (r *AuthRepository) MarkSessionRevocationDelivered(ctx context.Context, id string) error {
	result, err := r.pool.Exec(ctx, `
		UPDATE session_revocations SET delivered_at = COALESCE(delivered_at, now())
		WHERE session_id = $1`, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return service.ErrNotFound
	}
	return nil
}

// ScheduleSessionRevocationRetry delays a pending event without changing delivered events.
func (r *AuthRepository) ScheduleSessionRevocationRetry(ctx context.Context, id string, next time.Time) error {
	result, err := r.pool.Exec(ctx, `
		UPDATE session_revocations SET next_attempt_at =
		CASE WHEN delivered_at IS NULL THEN $2 ELSE next_attempt_at END
		WHERE session_id = $1`, id, next)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return service.ErrNotFound
	}
	return nil
}
