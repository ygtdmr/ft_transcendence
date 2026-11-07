package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/model"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
)

var (
	_ service.GoogleAccountStore = (*AuthRepository)(nil)
	_ service.OAuthStateConsumer = (*AuthRepository)(nil)
	_ service.GoogleLinkTx       = (*authTx)(nil)
)

// ConsumeOAuthState marks an unexpired, unused state as consumed exactly once.
func (r *AuthRepository) ConsumeOAuthState(ctx context.Context, stateHash string) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE oauth_states SET consumed_at = now()
		WHERE state_hash = $1 AND consumed_at IS NULL AND expires_at > now()`, stateHash)
	if err != nil {
		return false, classifyUnavailable(err)
	}

	return tag.RowsAffected() == 1, nil
}

// FindGoogleUser returns the account linked to a Google subject.
//
// Errors:
//
//   - service.ErrNotFound: no account is linked to subject.
func (r *AuthRepository) FindGoogleUser(ctx context.Context, subject string) (model.User, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT u.id, u.email, u.username, COALESCE(u.password_hash, ''), u.status, u.created_at, u.updated_at
		FROM oauth_links l JOIN users u ON u.id = l.user_id
		WHERE l.provider = 'google' AND l.subject = $1`, subject)

	return scanUser(row)
}

// LinkGoogle links subject to an existing account.
//
// Errors:
//
//   - service.ErrGoogleSubjectTaken: subject is linked to another account.
//   - service.ErrGoogleAccountLinked: the account is linked to another subject.
func (r *AuthRepository) LinkGoogle(ctx context.Context, subject string, userID int64) error {
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO oauth_links (provider, subject, user_id) VALUES ('google', $1, $2)
		ON CONFLICT DO NOTHING`, subject, userID)
	if err != nil {
		return classifyUnavailable(err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var linked int64
	err = r.pool.QueryRow(ctx, `SELECT user_id FROM oauth_links WHERE provider = 'google' AND subject = $1`, subject).Scan(&linked)
	switch {
	case err == nil && linked == userID:
		return nil
	case err == nil:
		return service.ErrGoogleSubjectTaken
	case errors.Is(err, pgx.ErrNoRows):
		return service.ErrGoogleAccountLinked
	default:
		return classifyUnavailable(err)
	}
}

// CreateGoogleLink links subject to an account created in the same transaction.
func (t *authTx) CreateGoogleLink(ctx context.Context, subject string, userID int64) error {
	_, err := t.tx.Exec(ctx, `INSERT INTO oauth_links (provider, subject, user_id) VALUES ('google', $1, $2)`, subject, userID)
	if isUniqueViolation(err) {
		return service.ErrGoogleSubjectTaken
	}

	return err
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError

	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
