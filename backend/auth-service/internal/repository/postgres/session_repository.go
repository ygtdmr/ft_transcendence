package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/model"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
)

var (
	_ service.SessionRepository = (*AuthRepository)(nil)
	_ service.SessionTx         = (*authTx)(nil)
	_ service.AccountReader     = (*AuthRepository)(nil)
)

// FindUserByEmail matches the unique case-insensitive email index.
func (r *AuthRepository) FindUserByEmail(ctx context.Context, email string) (model.User, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, email, username, COALESCE(password_hash, ''), status, created_at, updated_at
		FROM users WHERE lower(email) = lower($1)`, email)
	return scanUser(row)
}

// FindAccount reads one account by id.
//
// Errors:
//
//   - service.ErrNotFound: no account has this id.
//   - service.ErrUnavailable: the database could not be reached.
//   - Other database errors.
func (r *AuthRepository) FindAccount(ctx context.Context, id int64) (model.User, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, email, username, COALESCE(password_hash, ''), status, created_at, updated_at
		FROM users WHERE id = $1`, id)
	return scanUser(row)
}

func scanUser(row pgx.Row) (model.User, error) {
	var user model.User
	err := row.Scan(&user.ID, &user.Email, &user.Username, &user.PasswordHash,
		&user.Status, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return model.User{}, sessionError(err)
	}
	user.CreatedAt, user.UpdatedAt = user.CreatedAt.UTC(), user.UpdatedAt.UTC()
	return user, nil
}

// FindSessionUser reads session revocation state together with its account.
func (r *AuthRepository) FindSessionUser(ctx context.Context, id string) (model.User, model.Session, error) {
	var user model.User
	var session model.Session
	err := r.pool.QueryRow(ctx, `
		SELECT u.id, u.email, u.username, COALESCE(u.password_hash, ''), u.status, u.created_at, u.updated_at,
		       s.id::text, s.user_id, s.created_at, s.revoked_at
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.id = $1`, id).Scan(
		&user.ID, &user.Email, &user.Username, &user.PasswordHash, &user.Status, &user.CreatedAt,
		&user.UpdatedAt, &session.ID, &session.UserID, &session.CreatedAt, &session.RevokedAt)
	if err != nil {
		return model.User{}, model.Session{}, sessionError(err)
	}
	user.CreatedAt, user.UpdatedAt = user.CreatedAt.UTC(), user.UpdatedAt.UTC()
	normalizeSession(&session)
	return user, session, nil
}

// WithSessionTransaction uses the same transaction writes as registration.
func (r *AuthRepository) WithSessionTransaction(ctx context.Context, fn func(service.SessionTx) error) error {
	return pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		return fn(&authTx{tx: tx})
	})
}

func sessionError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return service.ErrNotFound
	}
	return classifyUnavailable(err)
}

func normalizeSession(session *model.Session) {
	session.CreatedAt = session.CreatedAt.UTC()
	if session.RevokedAt != nil {
		value := session.RevokedAt.UTC()
		session.RevokedAt = &value
	}
}
