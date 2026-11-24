package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

var _ service.UsernameStore = (*UsernameRepository)(nil)

// UsernameRepository renames profiles and records their outbox events.
type UsernameRepository struct {
	pool *pgxpool.Pool
}

// NewUsernames returns a username store; pool ownership stays with the caller.
func NewUsernames(pool *pgxpool.Pool) *UsernameRepository {
	return &UsernameRepository{pool: pool}
}

// Rename implements service.UsernameStore.
//
// Under the account lock that provisioning, avatar changes and deletion take,
// it updates the username and records user.profile.updated in the same
// transaction. An identical username changes nothing and records no event.
// The case-insensitive unique index decides concurrent renames to one name:
// the loser gets service.ErrUsernameTaken.
func (r *UsernameRepository) Rename(ctx context.Context, userID int64, username string,
	event service.ProfileEventBuilder) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return classify(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if err := rename(ctx, tx, userID, username, event); err != nil {
		if isUniqueViolation(err, usernameKey) {
			return service.ErrUsernameTaken
		}
		return classify(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("%w: %w", service.ErrOutcomeUnknown, classify(err))
	}

	return nil
}

func rename(ctx context.Context, tx pgx.Tx, userID int64, username string, event service.ProfileEventBuilder) error {
	if _, err := tx.Exec(ctx, lockAccount, accountLockKey(userID)); err != nil {
		return err
	}
	if err := ensureNotDeleted(ctx, tx, userID); err != nil {
		return err
	}
	profile := service.StoredProfile{UserID: userID}
	err := tx.QueryRow(ctx, `SELECT username, avatar_kind, COALESCE(avatar_ref, '')
		FROM profiles WHERE user_id = $1 FOR NO KEY UPDATE`, userID).
		Scan(&profile.Username, &profile.AvatarKind, &profile.AvatarRef)
	if errors.Is(err, pgx.ErrNoRows) {
		return service.ErrUserNotFound
	}
	if err != nil || profile.Username == username {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE profiles SET username = $2 WHERE user_id = $1`, userID, username); err != nil {
		return err
	}
	recipients, err := profileRecipients(ctx, tx, userID)
	if err != nil {
		return err
	}
	profile.Username = username
	_, err = AppendEvent(ctx, tx, event(profile, recipients))

	return err
}
