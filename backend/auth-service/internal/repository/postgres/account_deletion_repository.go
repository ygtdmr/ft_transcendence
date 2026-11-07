package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/model"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
)

var _ service.AccountDeletionStore = (*AuthRepository)(nil)

const deletionColumns = `operation_id::text, user_id, state, begun_at, completed_at`

// BeginAccountDeletion records the operation, sets the account to DELETING and
// revokes every session and refresh-token family in one transaction.
//
// The account row is locked first, so concurrent Begins for one user and
// session creation (which share-locks the same row) are serialized.
//
// Errors:
//
//   - service.ErrDeletionOperationConflict, service.ErrDeletionConflict,
//     service.ErrNotFound: nothing was written.
//   - service.ErrUnavailable or other database errors.
func (r *AuthRepository) BeginAccountDeletion(ctx context.Context, operationID string, userID int64) (service.DeletionBegin, error) {
	var result service.DeletionBegin
	err := pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var exists bool
		err := tx.QueryRow(ctx, `SELECT true FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&exists)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		stored, err := scanDeletion(tx.QueryRow(ctx,
			`SELECT `+deletionColumns+` FROM account_deletions WHERE operation_id = $1`, operationID))
		switch {
		case err == nil && stored.UserID == userID:
			result = service.DeletionBegin{Deletion: stored}
			return nil
		case err == nil:
			return service.ErrDeletionOperationConflict
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}

		var other bool
		err = tx.QueryRow(ctx, `SELECT true FROM account_deletions WHERE user_id = $1`, userID).Scan(&other)
		if err == nil {
			return service.ErrDeletionConflict
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if !exists {
			return service.ErrNotFound
		}

		stored, err = scanDeletion(tx.QueryRow(ctx, `
			INSERT INTO account_deletions (user_id, operation_id, state) VALUES ($1, $2, 'BEGUN')
			RETURNING `+deletionColumns, userID, operationID))
		if err != nil {
			if isUniqueViolation(err) {
				return service.ErrDeletionConflict
			}
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE users SET status = 'DELETING', updated_at = now() WHERE id = $1`, userID); err != nil {
			return err
		}
		revoked, err := revokeAccountSessions(ctx, tx, userID)
		if err != nil {
			return err
		}
		result = service.DeletionBegin{Deletion: stored, Created: true, RevokedSessions: revoked}
		return nil
	})
	if err != nil {
		return service.DeletionBegin{}, deletionError(err)
	}

	return result, nil
}

// revokeAccountSessions revokes every open session and refresh token of the
// account and queues one ACCOUNT_DELETED notification per newly revoked session.
// Sessions revoked earlier keep their original notification.
func revokeAccountSessions(ctx context.Context, tx pgx.Tx, userID int64) ([]string, error) {
	rows, err := tx.Query(ctx, `
		UPDATE sessions SET revoked_at = now()
		WHERE user_id = $1 AND revoked_at IS NULL
		RETURNING id::text`, userID)
	if err != nil {
		return nil, err
	}
	revoked, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE refresh_tokens SET revoked_at = COALESCE(revoked_at, now())
		WHERE session_id IN (SELECT id FROM sessions WHERE user_id = $1)`, userID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO session_revocations (session_id, user_id, revoked_at, cause)
		SELECT id, user_id, revoked_at, 'ACCOUNT_DELETED' FROM sessions WHERE id = ANY($1::uuid[])
		ON CONFLICT (session_id) DO NOTHING`, revoked); err != nil {
		return nil, err
	}

	return revoked, nil
}

// CompleteAccountDeletion removes the account of a BEGUN operation. Deleting
// the users row cascades to sessions, refresh tokens, the Google link and the
// provisioning operation. Queued revocation notifications are kept.
//
// Errors:
//
//   - service.ErrDeletionNotFound: Begin has not recorded operationID.
//   - service.ErrUnavailable or other database errors.
func (r *AuthRepository) CompleteAccountDeletion(ctx context.Context, operationID string) (model.AccountDeletion, error) {
	var stored model.AccountDeletion
	err := pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var err error
		stored, err = scanDeletion(tx.QueryRow(ctx,
			`SELECT `+deletionColumns+` FROM account_deletions WHERE operation_id = $1 FOR UPDATE`, operationID))
		if errors.Is(err, pgx.ErrNoRows) {
			return service.ErrDeletionNotFound
		}
		if err != nil || stored.State == model.DeletionCompleted {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1 AND status = 'DELETING'`, stored.UserID); err != nil {
			return err
		}
		stored, err = scanDeletion(tx.QueryRow(ctx, `
			UPDATE account_deletions SET state = 'COMPLETED', completed_at = now()
			WHERE operation_id = $1 RETURNING `+deletionColumns, operationID))
		return err
	})
	if err != nil {
		return model.AccountDeletion{}, deletionError(err)
	}

	return stored, nil
}

func scanDeletion(row pgx.Row) (model.AccountDeletion, error) {
	var deletion model.AccountDeletion
	err := row.Scan(&deletion.OperationID, &deletion.UserID, &deletion.State, &deletion.BegunAt, &deletion.CompletedAt)
	if err != nil {
		return model.AccountDeletion{}, err
	}
	deletion.BegunAt = deletion.BegunAt.UTC()
	if deletion.CompletedAt != nil {
		value := deletion.CompletedAt.UTC()
		deletion.CompletedAt = &value
	}

	return deletion, nil
}

func deletionError(err error) error {
	switch {
	case errors.Is(err, service.ErrDeletionOperationConflict),
		errors.Is(err, service.ErrDeletionConflict),
		errors.Is(err, service.ErrDeletionNotFound),
		errors.Is(err, service.ErrNotFound):
		return err
	}

	return classifyUnavailable(err)
}
