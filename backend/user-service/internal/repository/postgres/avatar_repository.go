package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

var _ service.AvatarStore = (*AvatarRepository)(nil)

// AvatarRepository updates avatars and records their outbox events.
type AvatarRepository struct {
	pool *pgxpool.Pool
}

// NewAvatars returns an avatar store; pool ownership stays with the caller.
func NewAvatars(pool *pgxpool.Pool) *AvatarRepository {
	return &AvatarRepository{pool: pool}
}

// SetAvatar implements service.AvatarStore. It takes the account lock that
// deletion takes. Concurrent changes for one user therefore apply one at a
// time, and each sees the file the previous one installed.
func (r *AvatarRepository) SetAvatar(ctx context.Context, userID int64, kind, ref string,
	event service.AvatarEventBuilder) (service.StoredProfile, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return service.StoredProfile{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	previous, err := applyAvatar(ctx, tx, userID, kind, ref, event)
	if err != nil {
		return service.StoredProfile{}, classify(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return service.StoredProfile{}, fmt.Errorf("%w: %w", service.ErrOutcomeUnknown, classify(err))
	}

	return previous, nil
}

func applyAvatar(ctx context.Context, tx pgx.Tx, userID int64, kind, ref string,
	event service.AvatarEventBuilder) (service.StoredProfile, error) {
	if _, err := tx.Exec(ctx, lockAccount, accountLockKey(userID)); err != nil {
		return service.StoredProfile{}, err
	}
	if err := ensureNotDeleted(ctx, tx, userID); err != nil {
		return service.StoredProfile{}, err
	}
	previous := service.StoredProfile{UserID: userID}
	err := tx.QueryRow(ctx, `SELECT username, avatar_kind, COALESCE(avatar_ref, '')
		FROM profiles WHERE user_id = $1 FOR NO KEY UPDATE`, userID).
		Scan(&previous.Username, &previous.AvatarKind, &previous.AvatarRef)
	if errors.Is(err, pgx.ErrNoRows) {
		return service.StoredProfile{}, service.ErrUserNotFound
	}
	if err != nil {
		return service.StoredProfile{}, err
	}
	if previous.AvatarKind == kind && previous.AvatarRef == ref {
		return previous, nil
	}

	if _, err := tx.Exec(ctx, `UPDATE profiles SET avatar_kind = $2, avatar_ref = $3 WHERE user_id = $1`,
		userID, kind, ref); err != nil {
		return service.StoredProfile{}, err
	}
	recipients, err := profileRecipients(ctx, tx, userID)
	if err != nil {
		return service.StoredProfile{}, err
	}
	updated := previous
	updated.AvatarKind, updated.AvatarRef = kind, ref
	if _, err := AppendEvent(ctx, tx, event(updated, recipients)); err != nil {
		return service.StoredProfile{}, err
	}

	return previous, nil
}

// profileRecipients returns the user, their active friends and the other side
// of their pending requests. A block suspends the friendship and cancels the
// request, so blocked users are not included.
func profileRecipients(ctx context.Context, tx pgx.Tx, userID int64) ([]int64, error) {
	rows, err := tx.Query(ctx, `
		SELECT $1::bigint
		UNION
		SELECT CASE WHEN user_low = $1 THEN user_high ELSE user_low END
		FROM friendships WHERE (user_low = $1 OR user_high = $1) AND active
		UNION
		SELECT CASE WHEN requester_id = $1 THEN addressee_id ELSE requester_id END
		FROM friend_requests WHERE (requester_id = $1 OR addressee_id = $1) AND state = 'pending'
		ORDER BY 1`, userID)
	if err != nil {
		return nil, err
	}

	return pgx.CollectRows(rows, pgx.RowTo[int64])
}
