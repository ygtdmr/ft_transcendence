package service

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
)

var (
	// ErrOperationConflict reports an operation id already used for another user.
	ErrOperationConflict = errors.New("cleanup operation belongs to another user")
	// ErrDeletionConflict reports a user already cleaned up by another operation.
	ErrDeletionConflict = errors.New("user cleaned up by another operation")
	// ErrInvalidOperationID reports an operation id that is not a UUID.
	ErrInvalidOperationID = errors.New("invalid operation id")
)

var operationIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// CleanupReceipt describes the stored cleanup operation.
type CleanupReceipt struct {
	OperationID string
	UserID      int64
	CompletedAt time.Time
}

// CleanupStore removes one user's identity from Stats records.
type CleanupStore interface {
	// CleanUp applies the cleanup once in one transaction and returns its
	// receipt. created is false for a replay of the same operation. It returns
	// ErrOperationConflict or ErrDeletionConflict when the pair does not match
	// the stored one.
	CleanUp(ctx context.Context, operationID string, userID int64) (receipt CleanupReceipt, created bool, err error)
}

// AccountCleanup answers account deletion cleanup requests.
type AccountCleanup struct {
	store CleanupStore
}

// NewAccountCleanup returns the cleanup service.
func NewAccountCleanup(store CleanupStore) *AccountCleanup {
	return &AccountCleanup{store: store}
}

// CleanUp clears userID's counters and anonymizes retained records.
//
// Errors:
//
//   - ErrInvalidOperationID: operationID is not a UUID.
//   - ErrOperationConflict, ErrDeletionConflict: see CleanupStore.
//   - ErrUnavailable or other database errors: nothing changed; retry.
func (s *AccountCleanup) CleanUp(ctx context.Context, operationID string, userID int64) (CleanupReceipt, bool, error) {
	normalized := strings.ToLower(operationID)
	if !operationIDPattern.MatchString(normalized) {
		return CleanupReceipt{}, false, ErrInvalidOperationID
	}

	return s.store.CleanUp(ctx, normalized, userID)
}
