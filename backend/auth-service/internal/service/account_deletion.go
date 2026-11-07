package service

import (
	"context"
	"errors"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/model"
)

// Session revocation causes carried to Gateways.
const (
	RevocationSessionRevoked = "SESSION_REVOKED"
	RevocationAccountDeleted = "ACCOUNT_DELETED"
)

var (
	// ErrDeletionOperationConflict reports an operation_id recorded for another user.
	ErrDeletionOperationConflict = errors.New("deletion operation belongs to another user")
	// ErrDeletionConflict reports a user who already has another deletion operation.
	ErrDeletionConflict = errors.New("another deletion operation exists for this user")
	// ErrDeletionNotFound reports a Complete call for an operation Begin never recorded.
	ErrDeletionNotFound = errors.New("deletion operation was not found")
)

// DeletionBegin is the committed result of BeginAccountDeletion.
type DeletionBegin struct {
	Deletion model.AccountDeletion
	// Created is true only for the call that recorded the operation.
	Created bool
	// RevokedSessions lists sessions revoked by this call.
	RevokedSessions []string
}

// AccountDeletionStore commits each deletion step in one transaction.
type AccountDeletionStore interface {
	BeginAccountDeletion(ctx context.Context, operationID string, userID int64) (DeletionBegin, error)
	CompleteAccountDeletion(ctx context.Context, operationID string) (model.AccountDeletion, error)
}

// BeginAccountDeletion blocks new credentials for userID and revokes every
// session and refresh-token family with cause ACCOUNT_DELETED.
//
// A retry with the same operationID and userID returns the stored record,
// which is COMPLETED when Complete already ran. Revocation notifications are
// sent after commit; undelivered ones stay queued for the retry worker, so a
// Gateway outage never fails the call.
//
// Errors:
//
//   - ErrDeletionOperationConflict: operationID is recorded for another user.
//   - ErrDeletionConflict: userID already has another deletion operation.
//   - ErrNotFound: no account and no deletion record exist for userID.
//   - ErrUnavailable or other database errors; nothing was written.
func (s *AuthService) BeginAccountDeletion(ctx context.Context, operationID string, userID int64) (DeletionBegin, error) {
	store, ok := s.repository.(AccountDeletionStore)
	if !ok {
		return DeletionBegin{}, ErrSessionUnavailable
	}
	result, err := store.BeginAccountDeletion(ctx, operationID, userID)
	if err != nil {
		return DeletionBegin{}, err
	}
	for _, sessionID := range result.RevokedSessions {
		// Failures are already deferred for DeliverPendingRevocations.
		_ = s.notifySessionRevoked(ctx, userID, sessionID, RevocationAccountDeleted)
	}

	return result, nil
}

// CompleteAccountDeletion removes the account, its password hash, refresh
// records and Google link for a begun operation. The coordinator calls it only
// after every other participant has confirmed cleanup. Retries return the
// stored COMPLETED record.
//
// Errors:
//
//   - ErrDeletionNotFound: Begin has not recorded operationID.
//   - ErrUnavailable or other database errors; nothing was written.
func (s *AuthService) CompleteAccountDeletion(ctx context.Context, operationID string) (model.AccountDeletion, error) {
	store, ok := s.repository.(AccountDeletionStore)
	if !ok {
		return model.AccountDeletion{}, ErrSessionUnavailable
	}

	return store.CompleteAccountDeletion(ctx, operationID)
}
