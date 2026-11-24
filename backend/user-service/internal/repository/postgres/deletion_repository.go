package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

var _ service.DeletionStore = (*DeletionRepository)(nil)

// maxDeletionError bounds last_error to the column constraint.
const maxDeletionError = 2000

const deletionColumns = `operation_id::text, user_id, state, attempts,
	auth_begun_at IS NOT NULL, game_cleaned_at IS NOT NULL,
	stats_cleaned_at IS NOT NULL, user_cleaned_at IS NOT NULL`

// stepColumns maps each coordinator step to its progress column.
var stepColumns = map[string]string{
	service.StepAuthBegin:    "auth_begun_at",
	service.StepGameCleanup:  "game_cleaned_at",
	service.StepStatsCleanup: "stats_cleaned_at",
	service.StepUserCleanup:  "user_cleaned_at",
}

// DeletionRepository stores deletion operations and applies User's cleanup.
type DeletionRepository struct {
	pool *pgxpool.Pool
}

// NewDeletions returns a deletion store; pool ownership stays with the caller.
func NewDeletions(pool *pgxpool.Pool) *DeletionRepository {
	return &DeletionRepository{pool: pool}
}

func scanDeletion(row pgx.Row) (service.DeletionOperation, error) {
	var op service.DeletionOperation
	err := row.Scan(&op.OperationID, &op.UserID, &op.State, &op.Attempts,
		&op.AuthBegun, &op.GameCleaned, &op.StatsCleaned, &op.UserCleaned)

	return op, err
}

// Start implements service.DeletionStore. The account lock is the one
// provisioning and avatar changes take, so a deletion never interleaves with
// them; the operation_id comes from gen_random_uuid().
func (r *DeletionRepository) Start(ctx context.Context, userID int64) (service.DeletionOperation, bool, error) {
	var op service.DeletionOperation
	var created bool
	err := inTx(ctx, r.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, lockAccount, accountLockKey(userID)); err != nil {
			return err
		}
		existing, err := scanDeletion(tx.QueryRow(ctx,
			`SELECT `+deletionColumns+` FROM deletion_operations WHERE user_id = $1`, userID))
		if err == nil {
			op = existing
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		op, err = scanDeletion(tx.QueryRow(ctx, `
			INSERT INTO deletion_operations (operation_id, user_id) VALUES (gen_random_uuid(), $1)
			RETURNING `+deletionColumns, userID))
		created = err == nil
		return err
	})
	if err != nil {
		return service.DeletionOperation{}, false, classify(err)
	}

	return op, created, nil
}

// Operation implements service.DeletionStore.
func (r *DeletionRepository) Operation(ctx context.Context, userID int64) (service.DeletionOperation, error) {
	op, err := scanDeletion(r.pool.QueryRow(ctx,
		`SELECT `+deletionColumns+` FROM deletion_operations WHERE user_id = $1`, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return service.DeletionOperation{}, service.ErrUserNotFound
	}
	if err != nil {
		return service.DeletionOperation{}, classify(err)
	}

	return op, nil
}

// MarkStep implements service.DeletionStore. The first confirmation time is kept.
func (r *DeletionRepository) MarkStep(ctx context.Context, operationID, step string) error {
	column, ok := stepColumns[step]
	if !ok {
		return fmt.Errorf("unknown deletion step %q", step)
	}
	_, err := r.pool.Exec(ctx, `UPDATE deletion_operations
		SET `+column+` = COALESCE(`+column+`, now()), state = 'in_progress'
		WHERE operation_id = $1 AND state <> 'completed'`, operationID)

	return classify(err)
}

// CleanUpUser implements service.DeletionStore.
//
// Under the operation row lock and the account lock it reads the active
// friends and pending requests for the events, then deletes the user's
// friendships, blocks, friend requests, profile and provisioning record. The
// user stays refused by the recorded deletion. Reports are kept.
func (r *DeletionRepository) CleanUpUser(ctx context.Context, operationID string,
	events service.DeletionEventBuilder) (service.UserCleanup, error) {
	var cleanup service.UserCleanup
	err := inTx(ctx, r.pool, func(tx pgx.Tx) error {
		var userID int64
		var done bool
		err := tx.QueryRow(ctx, `SELECT user_id, user_cleaned_at IS NOT NULL FROM deletion_operations
			WHERE operation_id = $1 FOR UPDATE`, operationID).Scan(&userID, &done)
		if err != nil || done {
			return err
		}
		if _, err := tx.Exec(ctx, lockAccount, accountLockKey(userID)); err != nil {
			return err
		}
		var kind, ref string
		err = tx.QueryRow(ctx, `SELECT avatar_kind, COALESCE(avatar_ref, '') FROM profiles WHERE user_id = $1`,
			userID).Scan(&kind, &ref)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if kind == service.AvatarUpload {
			cleanup.UploadKey = ref
		}
		friendIDs, err := activeFriendIDs(ctx, tx, userID)
		if err != nil {
			return err
		}
		requests, err := pendingRequests(ctx, tx, userID)
		if err != nil {
			return err
		}
		if err := appendEvents(ctx, tx, events(userID, friendIDs, requests)); err != nil {
			return err
		}
		// Friendships go before blocks, so the blocks_release trigger finds no
		// friendship of this user to reactivate.
		for _, statement := range []string{
			`DELETE FROM friendships WHERE user_low = $1 OR user_high = $1`,
			`DELETE FROM blocks WHERE blocker_id = $1 OR blocked_id = $1`,
			`DELETE FROM friend_requests WHERE requester_id = $1 OR addressee_id = $1`,
			`DELETE FROM profiles WHERE user_id = $1`,
			`DELETE FROM provisioning_operations WHERE user_id = $1`,
		} {
			if _, err := tx.Exec(ctx, statement, userID); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE deletion_operations SET user_cleaned_at = now(), state = 'in_progress'
			WHERE operation_id = $1`, operationID)
		return err
	})
	if err != nil {
		return service.UserCleanup{}, classify(err)
	}

	return cleanup, nil
}

func activeFriendIDs(ctx context.Context, tx pgx.Tx, userID int64) ([]int64, error) {
	rows, err := tx.Query(ctx, `SELECT CASE WHEN user_low = $1 THEN user_high ELSE user_low END
		FROM friendships WHERE (user_low = $1 OR user_high = $1) AND active ORDER BY 1`, userID)
	if err != nil {
		return nil, err
	}

	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

func pendingRequests(ctx context.Context, tx pgx.Tx, userID int64) ([]service.StoredFriendRequest, error) {
	rows, err := tx.Query(ctx, `SELECT id, requester_id, addressee_id, created_at FROM friend_requests
		WHERE (requester_id = $1 OR addressee_id = $1) AND state = 'pending' ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}

	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (service.StoredFriendRequest, error) {
		var request service.StoredFriendRequest
		err := row.Scan(&request.ID, &request.From.UserID, &request.To.UserID, &request.CreatedAt)
		request.State = service.FriendRequestPending
		return request, err
	})
}

// Complete implements service.DeletionStore.
func (r *DeletionRepository) Complete(ctx context.Context, operationID string) error {
	_, err := r.pool.Exec(ctx, `UPDATE deletion_operations
		SET state = 'completed', completed_at = COALESCE(completed_at, now()), last_error = NULL
		WHERE operation_id = $1`, operationID)

	return classify(err)
}

// RecordFailure implements service.DeletionStore.
func (r *DeletionRepository) RecordFailure(ctx context.Context, operationID, message string, next time.Time,
	permanent bool) error {
	if len(message) > maxDeletionError {
		message = message[:maxDeletionError]
	}
	state := "in_progress"
	if permanent {
		state = service.DeletionFailed
	}
	_, err := r.pool.Exec(ctx, `UPDATE deletion_operations
		SET attempts = attempts + 1, last_error = $2, next_attempt_at = $3, state = $4
		WHERE operation_id = $1 AND state <> 'completed'`, operationID, message, next, state)

	return classify(err)
}

// Due implements service.DeletionStore.
func (r *DeletionRepository) Due(ctx context.Context, limit int) ([]service.DeletionOperation, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+deletionColumns+` FROM deletion_operations
		WHERE state IN ('pending', 'in_progress') AND next_attempt_at <= now()
		ORDER BY next_attempt_at LIMIT $1`, limit)
	if err != nil {
		return nil, classify(err)
	}
	ops, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (service.DeletionOperation, error) {
		return scanDeletion(row)
	})
	if err != nil {
		return nil, classify(err)
	}

	return ops, nil
}
