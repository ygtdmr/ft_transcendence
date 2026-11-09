package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/service"
)

const deletedUsername = "deleted_user"

var _ service.CleanupStore = (*AccountCleanupRepository)(nil)

// AccountCleanupRepository removes deleted users from Stats records.
type AccountCleanupRepository struct {
	pool *pgxpool.Pool
}

// NewAccountCleanup returns a cleanup store; pool ownership stays with the caller.
func NewAccountCleanup(pool *pgxpool.Pool) *AccountCleanupRepository {
	return &AccountCleanupRepository{pool: pool}
}

// CleanUp implements service.CleanupStore.
func (r *AccountCleanupRepository) CleanUp(ctx context.Context, operationID string, userID int64) (service.CleanupReceipt, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return service.CleanupReceipt{}, false, classify(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	receipt, created, err := applyCleanup(ctx, tx, operationID, userID)
	if err != nil {
		return service.CleanupReceipt{}, false, cleanupError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return service.CleanupReceipt{}, false, cleanupError(err)
	}

	return receipt, created, nil
}

func applyCleanup(ctx context.Context, tx pgx.Tx, operationID string, userID int64) (service.CleanupReceipt, bool, error) {
	if err := lockUser(ctx, tx, userID); err != nil {
		return service.CleanupReceipt{}, false, err
	}
	receipt := service.CleanupReceipt{OperationID: operationID, UserID: userID}
	var storedUser int64
	err := tx.QueryRow(ctx, `SELECT user_id, completed_at FROM deleted_users WHERE operation_id = $1`, operationID).
		Scan(&storedUser, &receipt.CompletedAt)
	if err == nil {
		if storedUser != userID {
			return service.CleanupReceipt{}, false, service.ErrOperationConflict
		}
		return receipt, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return service.CleanupReceipt{}, false, err
	}
	deleted, err := isDeleted(ctx, tx, userID)
	if err != nil {
		return service.CleanupReceipt{}, false, err
	}
	if deleted {
		return service.CleanupReceipt{}, false, service.ErrDeletionConflict
	}

	if err := tx.QueryRow(ctx, `INSERT INTO deleted_users (user_id, operation_id) VALUES ($1, $2) RETURNING completed_at`,
		userID, operationID).Scan(&receipt.CompletedAt); err != nil {
		return service.CleanupReceipt{}, false, err
	}
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM user_stats WHERE user_id = $1`, []any{userID}},
		{`UPDATE match_participants SET user_id = NULL, username = $2 WHERE user_id = $1`, []any{userID, deletedUsername}},
		{`UPDATE penalties SET user_id = NULL WHERE user_id = $1`, []any{userID}},
	} {
		if _, err := tx.Exec(ctx, statement.sql, statement.args...); err != nil {
			return service.CleanupReceipt{}, false, err
		}
	}

	return receipt, true, nil
}

func cleanupError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "deleted_users_operation_unique" {
		return service.ErrOperationConflict
	}

	return classify(err)
}

// lockUser serializes cleanup and event ingestion for one user until the
// transaction ends. Callers holding several users lock them in user_id order.
func lockUser(ctx context.Context, tx pgx.Tx, userID int64) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('stats-user:' || $1::bigint::text, 0))`, userID)

	return err
}

func isDeleted(ctx context.Context, tx pgx.Tx, userID int64) (bool, error) {
	var deleted bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM deleted_users WHERE user_id = $1)`, userID).Scan(&deleted)

	return deleted, err
}
