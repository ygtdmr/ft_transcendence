package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/model"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
)

var (
	_ service.AuthRepository = (*AuthRepository)(nil)
	_ service.AuthTx         = (*authTx)(nil)
)

type AuthRepository struct {
	pool *pgxpool.Pool
}

// NewAuth returns a PostgreSQL repository; pool ownership stays with the caller.
func NewAuth(pool *pgxpool.Pool) *AuthRepository {
	return &AuthRepository{pool: pool}
}

// WithTransaction executes account writes in one database transaction.
//
// Parameters:
//
//   - ctx: Controls transaction begin, commit and rollback operations.
//   - fn: Callback receiving the transaction; it must not retain or use it after returning.
//
// It returns nil after a successful commit.
//
// Errors:
//
//   - Begin errors: A transaction could not be started.
//   - Callback errors: fn failed; rollback is attempted and the callback error is returned.
//   - Commit errors: The transaction could not be committed.
func (r *AuthRepository) WithTransaction(ctx context.Context, fn func(service.AuthTx) error) error {
	return pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		return fn(&authTx{tx: tx})
	})
}

type authTx struct {
	tx pgx.Tx
}

// CreateUser inserts an account within the current transaction.
//
// Parameters:
//
//   - ctx: Controls the insert query.
//   - user: Account email, username and already hashed password.
//
// It returns the stored user with its generated ID and UTC timestamps,
// or a zero User on failure.
//
// Errors:
//
//   - service.ErrEmailTaken: The unique email constraint was violated.
//   - service.ErrUsernameTaken: The unique username constraint was violated.
//   - Database errors: The insert or result scan failed, including context cancellation and deadlines.
func (t *authTx) CreateUser(ctx context.Context, user model.User) (model.User, error) {
	err := t.tx.QueryRow(ctx, `
		INSERT INTO users (email, username, password_hash, status) VALUES ($1, $2, NULLIF($3, ''), COALESCE(NULLIF($4, ''), 'ACTIVE'))
		RETURNING id, email, username, COALESCE(password_hash, ''), status, created_at, updated_at`,
		user.Email, user.Username, user.PasswordHash, user.Status,
	).Scan(&user.ID, &user.Email, &user.Username, &user.PasswordHash, &user.Status, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return model.User{}, accountError(err)
	}

	user.CreatedAt = user.CreatedAt.UTC()
	user.UpdatedAt = user.UpdatedAt.UTC()

	return user, nil
}

// CreateSession inserts a session only for an ACTIVE account. The account row
// is share-locked until commit, so a concurrent deletion Begin either waits and
// then revokes this session, or commits first and this insert is refused.
//
// Errors:
//
//   - service.ErrAccountUnavailable: the account is missing or not ACTIVE.
//   - Database errors.
func (t *authTx) CreateSession(ctx context.Context, session model.Session) error {
	var active bool
	err := t.tx.QueryRow(ctx, `
		SELECT status = 'ACTIVE' FROM users WHERE id = $1 FOR SHARE`, session.UserID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !active) {
		return service.ErrAccountUnavailable
	}
	if err != nil {
		return err
	}
	_, err = t.tx.Exec(ctx, `
		INSERT INTO sessions (id, user_id, created_at) VALUES ($1, $2, $3)`,
		session.ID, session.UserID, session.CreatedAt)

	return err
}

func (t *authTx) CreateRefreshToken(ctx context.Context, token model.RefreshToken) error {
	_, err := t.tx.Exec(ctx, `
		INSERT INTO refresh_tokens (id, session_id, token_hash, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5)`,
		token.ID, token.SessionID, token.TokenHash, token.CreatedAt, token.ExpiresAt)

	return err
}

func accountError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		switch pgErr.ConstraintName {
		case "users_email_unique":
			return service.ErrEmailTaken

		case "users_username_unique":
			return service.ErrUsernameTaken
		}
	}

	return err
}
