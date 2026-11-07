package postgres

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

var _ service.ProfileStore = (*ProfileRepository)(nil)

const (
	codeUniqueViolation = "23505"
	usernameKey         = "profiles_username_key"
)

// lockAccount serializes every lifecycle change of one user_id for the rest of
// the transaction. Account deletion must take the same lock, so a provisioning
// request and a deletion for the same user never interleave.
const lockAccount = "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))"

// accountLockKey names the advisory lock for one user_id.
func accountLockKey(userID int64) string {
	return "users.account:" + strconv.FormatInt(userID, 10)
}

const deletionRecorded = "SELECT EXISTS (SELECT 1 FROM deletion_operations WHERE user_id = $1)"

type ProfileRepository struct {
	pool *pgxpool.Pool
}

// NewProfile returns a profile store; pool ownership stays with the caller.
func NewProfile(pool *pgxpool.Pool) *ProfileRepository {
	return &ProfileRepository{pool: pool}
}

// Provision applies the contract's decision order in one transaction.
//
// Parameters:
//
//   - ctx: Controls the transaction.
//   - record: Canonical request with its fingerprint.
//
// Flow, under the account lock for record.UserID:
//
//  1. Account deletion is recorded for the user: USER_DELETED.
//  2. The operation exists: identical fingerprint replays the stored receipt;
//     a different one is OPERATION_CONFLICT.
//  3. Another operation or a profile already owns the user: USER_ALREADY_PROVISIONED.
//  4. Insert the operation record and the profile with the default avatar. A
//     username clash is USERNAME_TAKEN and rolls the operation back with it, so
//     a failed attempt never leaves a username reserved.
//
// Concurrent identical requests produce exactly one profile: the account lock
// and the operation primary key make the later one see the committed record.
// Concurrent requests for the same normalized username let at most one user
// succeed through the unique username index. Nothing is ever overwritten.
//
// Errors:
//
//   - *service.UserDeletedError, service.ErrOperationConflict,
//     service.ErrUserAlreadyProvisioned, service.ErrUsernameTaken: see the flow.
//   - service.ErrUnavailable: the database could not be reached.
//   - Other database errors: the transaction was rolled back.
func (r *ProfileRepository) Provision(ctx context.Context, record service.ProvisionRecord) (service.ProvisionResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return service.ProvisionResult{}, classify(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	result, err := provision(ctx, tx, record)
	if err != nil {
		return service.ProvisionResult{}, classify(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return service.ProvisionResult{}, classify(err)
	}

	return result, nil
}

func provision(ctx context.Context, tx pgx.Tx, record service.ProvisionRecord) (service.ProvisionResult, error) {
	if _, err := tx.Exec(ctx, lockAccount, accountLockKey(record.UserID)); err != nil {
		return service.ProvisionResult{}, err
	}
	if err := ensureNotDeleted(ctx, tx, record.UserID); err != nil {
		return service.ProvisionResult{}, err
	}

	var stored string
	var receipt service.Receipt
	err := tx.QueryRow(ctx, `
		SELECT payload_fingerprint, operation_id::text, user_id, completed_at
		FROM provisioning_operations WHERE operation_id = $1 AND state = 'completed'`,
		record.OperationID).Scan(&stored, &receipt.OperationID, &receipt.UserID, &receipt.CompletedAt)
	switch {
	case err == nil && stored == record.Fingerprint:
		receipt.Outcome = service.OutcomeProvisioned
		return service.ProvisionResult{Receipt: receipt, Created: false}, nil
	case err == nil:
		return service.ProvisionResult{}, service.ErrOperationConflict
	case !errors.Is(err, pgx.ErrNoRows):
		return service.ProvisionResult{}, err
	}

	var owned bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM provisioning_operations WHERE user_id = $1)
		    OR EXISTS (SELECT 1 FROM profiles WHERE user_id = $1)`, record.UserID).Scan(&owned)
	if err != nil {
		return service.ProvisionResult{}, err
	}
	if owned {
		return service.ProvisionResult{}, service.ErrUserAlreadyProvisioned
	}

	err = tx.QueryRow(ctx, `
		INSERT INTO provisioning_operations (operation_id, user_id, state, payload_fingerprint, completed_at)
		VALUES ($1, $2, 'completed', $3, now())
		RETURNING operation_id::text, user_id, completed_at`,
		record.OperationID, record.UserID, record.Fingerprint).
		Scan(&receipt.OperationID, &receipt.UserID, &receipt.CompletedAt)
	if err != nil {
		// Another transaction committed the same operation id for a different
		// user between the lookup above and this insert.
		if isUniqueViolation(err, "") {
			return service.ProvisionResult{}, service.ErrOperationConflict
		}
		return service.ProvisionResult{}, err
	}

	_, err = tx.Exec(ctx, `INSERT INTO profiles (user_id, username, created_at) VALUES ($1, $2, $3)`,
		record.UserID, record.Username, record.CreatedAt)
	if err != nil {
		if isUniqueViolation(err, usernameKey) {
			return service.ProvisionResult{}, service.ErrUsernameTaken
		}
		return service.ProvisionResult{}, err
	}
	receipt.Outcome = service.OutcomeProvisioned

	return service.ProvisionResult{Receipt: receipt, Created: true}, nil
}

// ReceiptByOperation reads the receipt of one completed operation.
//
// Errors:
//
//   - service.ErrProvisioningNotFound: no completed operation has this id.
//   - *service.UserDeletedError: account deletion is recorded for its user.
//   - service.ErrUnavailable or other database errors.
func (r *ProfileRepository) ReceiptByOperation(ctx context.Context, operationID string) (service.Receipt, error) {
	receipt, err := r.readReceipt(ctx, "operation_id = $1", operationID)
	if err != nil {
		return service.Receipt{}, err
	}
	if err := ensureNotDeleted(ctx, r.pool, receipt.UserID); err != nil {
		return service.Receipt{}, classify(err)
	}

	return receipt, nil
}

// ReceiptByUser reads the receipt of the operation of record for userID.
//
// Errors:
//
//   - *service.UserDeletedError: account deletion is recorded for userID.
//   - service.ErrProvisioningNotFound: no completed operation exists for userID.
//   - service.ErrUnavailable or other database errors.
func (r *ProfileRepository) ReceiptByUser(ctx context.Context, userID int64) (service.Receipt, error) {
	if err := ensureNotDeleted(ctx, r.pool, userID); err != nil {
		return service.Receipt{}, classify(err)
	}

	return r.readReceipt(ctx, "user_id = $1", userID)
}

func (r *ProfileRepository) readReceipt(ctx context.Context, condition string, value any) (service.Receipt, error) {
	var receipt service.Receipt
	err := r.pool.QueryRow(ctx, `
		SELECT operation_id::text, user_id, completed_at
		FROM provisioning_operations WHERE state = 'completed' AND `+condition, value).
		Scan(&receipt.OperationID, &receipt.UserID, &receipt.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return service.Receipt{}, service.ErrProvisioningNotFound
	}
	if err != nil {
		return service.Receipt{}, classify(err)
	}
	receipt.Outcome = service.OutcomeProvisioned

	return receipt, nil
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ensureNotDeleted returns *service.UserDeletedError when deletion is recorded for userID.
func ensureNotDeleted(ctx context.Context, q querier, userID int64) error {
	var deleted bool
	if err := q.QueryRow(ctx, deletionRecorded, userID).Scan(&deleted); err != nil {
		return err
	}
	if deleted {
		return &service.UserDeletedError{UserID: userID}
	}

	return nil
}

// isUniqueViolation reports a unique violation, on constraint when it is not empty.
func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError

	return errors.As(err, &pgErr) && pgErr.Code == codeUniqueViolation &&
		(constraint == "" || pgErr.ConstraintName == constraint)
}

// classify marks connectivity failures as service.ErrUnavailable so callers
// answer 503 and may retry. Domain errors and other failures pass through.
func classify(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case len(pgErr.Code) == 5 && pgErr.Code[:2] == "08", // connection exception
			pgErr.Code == "53300",                                               // too_many_connections
			pgErr.Code == "57P01", pgErr.Code == "57P02", pgErr.Code == "57P03": // shutdown, cannot connect now
			return fmt.Errorf("%w: %w", service.ErrUnavailable, err)
		}
		return err
	}
	var connectErr *pgconn.ConnectError
	var netErr net.Error
	if errors.As(err, &connectErr) || errors.As(err, &netErr) || pgconn.SafeToRetry(err) ||
		errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", service.ErrUnavailable, err)
	}

	return err
}

var _ service.ProfileReader = (*ProfileRepository)(nil)

// StoredProfile reads the current profile row and the user's deletion state together.
//
// Errors:
//
//   - *service.UserDeletedError: account deletion is recorded for userID.
//   - service.ErrUserNotFound: no profile exists for userID.
//   - service.ErrUnavailable or other database errors.
func (r *ProfileRepository) StoredProfile(ctx context.Context, userID int64) (service.StoredProfile, error) {
	var deleted bool
	var id *int64
	var username, kind, ref *string
	var createdAt *time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM deletion_operations WHERE user_id = $1),
		       p.user_id, p.username, p.avatar_kind, p.avatar_ref, p.created_at
		FROM (SELECT 1) AS one LEFT JOIN profiles p ON p.user_id = $1`, userID).
		Scan(&deleted, &id, &username, &kind, &ref, &createdAt)
	if err != nil {
		return service.StoredProfile{}, classify(err)
	}
	if deleted {
		return service.StoredProfile{}, &service.UserDeletedError{UserID: userID}
	}
	if id == nil {
		return service.StoredProfile{}, service.ErrUserNotFound
	}
	profile := service.StoredProfile{UserID: *id, Username: *username, AvatarKind: *kind, CreatedAt: createdAt.UTC()}
	if ref != nil {
		profile.AvatarRef = *ref
	}

	return profile, nil
}
