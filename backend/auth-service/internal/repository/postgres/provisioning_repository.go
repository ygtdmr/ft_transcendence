package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/model"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
)

var (
	_ service.ProvisioningRepository = (*AuthRepository)(nil)
	_ service.ProvisioningTx         = (*authTx)(nil)
)

// CreateProvisioningOperation stores the operation in the account's creation transaction.
func (t *authTx) CreateProvisioningOperation(ctx context.Context, operation model.ProvisioningOperation) error {
	_, err := t.tx.Exec(ctx, `
		INSERT INTO provisioning_operations (user_id, operation_id, username, account_created_at)
		VALUES ($1, $2, $3, $4)`,
		operation.UserID, operation.OperationID, operation.Username, operation.AccountCreatedAt)

	return err
}

// ProvisioningOperation reads the stored operation for userID.
//
// Errors:
//
//   - service.ErrNotFound: no operation exists for userID.
//   - service.ErrUnavailable or other database errors.
func (r *AuthRepository) ProvisioningOperation(ctx context.Context, userID int64) (model.ProvisioningOperation, error) {
	var operation model.ProvisioningOperation
	err := r.pool.QueryRow(ctx, `
		SELECT user_id, operation_id::text, username, account_created_at, state, attempts
		FROM provisioning_operations WHERE user_id = $1`, userID).
		Scan(&operation.UserID, &operation.OperationID, &operation.Username,
			&operation.AccountCreatedAt, &operation.State, &operation.Attempts)
	if err != nil {
		return model.ProvisioningOperation{}, sessionError(err)
	}
	operation.AccountCreatedAt = operation.AccountCreatedAt.UTC()

	return operation, nil
}

// ActivateProvisionedAccount completes the operation and activates the account atomically.
//
// It locks the operation, then marks it COMPLETED and moves the account from
// PROVISIONING to ACTIVE. Repeating it for an ACTIVE account is a no-op.
//
// Errors:
//
//   - service.ErrNotFound: the operation does not match, is FAILED, or the
//     account is gone or DELETING.
//   - service.ErrUnavailable or other database errors.
func (r *AuthRepository) ActivateProvisionedAccount(ctx context.Context, userID int64, operationID string) (model.User, error) {
	var user model.User
	err := pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var state string
		err := tx.QueryRow(ctx, `
			SELECT state FROM provisioning_operations
			WHERE user_id = $1 AND operation_id = $2 FOR UPDATE`, userID, operationID).Scan(&state)
		if err != nil {
			return err
		}
		if state == model.ProvisioningFailed {
			return pgx.ErrNoRows
		}
		if _, err := tx.Exec(ctx, `
			UPDATE provisioning_operations
			SET state = 'COMPLETED', completed_at = COALESCE(completed_at, now()), updated_at = now(), last_error = NULL
			WHERE user_id = $1`, userID); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `
			UPDATE users SET status = 'ACTIVE', updated_at = CASE WHEN status = 'ACTIVE' THEN updated_at ELSE now() END
			WHERE id = $1 AND status IN ('PROVISIONING', 'ACTIVE')
			RETURNING id, email, username, COALESCE(password_hash, ''), status, created_at, updated_at`, userID)
		user, err = scanUser(row)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, service.ErrNotFound) {
		return model.User{}, service.ErrNotFound
	}
	if err != nil {
		return model.User{}, classifyUnavailable(err)
	}

	return user, nil
}

// RecordProvisioningAttempt counts an unsuccessful attempt for a PENDING operation.
func (r *AuthRepository) RecordProvisioningAttempt(ctx context.Context, userID int64, code string, terminal bool) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE provisioning_operations
		SET attempts = attempts + 1, last_error = $2, updated_at = now(),
		    state = CASE WHEN $3 THEN 'FAILED' ELSE state END
		WHERE user_id = $1 AND state = 'PENDING'`, userID, code, terminal)

	return classifyUnavailable(err)
}

// DiscardProvisionalAccount deletes a PROVISIONING account; its operation is
// removed with it. ACTIVE and DELETING accounts are never touched.
func (r *AuthRepository) DiscardProvisionalAccount(ctx context.Context, userID int64) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM users WHERE id = $1 AND status = 'PROVISIONING'`, userID)

	return classifyUnavailable(err)
}
