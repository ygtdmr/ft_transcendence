package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/model"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/security"
)

var (
	// ErrServiceUnavailable reports that profile provisioning could not be
	// confirmed now; the account stays PROVISIONING and the same operation is
	// retried later.
	ErrServiceUnavailable = errors.New("profile provisioning is temporarily unavailable")
	// ErrProvisioningFailed reports a terminal provisioning answer that needs
	// investigation; no session is issued.
	ErrProvisioningFailed = errors.New("profile provisioning failed")
)

// Errors a ProfileProvisioner returns for User Service answers.
var (
	ErrProvisionUnavailable        = errors.New("user service unavailable")
	ErrProvisionUsernameTaken      = errors.New("user service: username taken")
	ErrProvisionUsernameInvalid    = errors.New("user service: invalid username")
	ErrProvisionAlreadyProvisioned = errors.New("user service: user already provisioned")
	ErrProvisionUserDeleted        = errors.New("user service: user deleted")
	ErrProvisionNotFound           = errors.New("user service: provisioning not found")
	ErrProvisionRejected           = errors.New("user service rejected the provisioning request")
)

// ProvisionRequest is the canonical request sent to User Service.
type ProvisionRequest struct {
	OperationID string
	UserID      int64
	Username    string
	CreatedAt   time.Time
}

// ProvisionReceipt is User Service's immutable provisioning receipt.
type ProvisionReceipt struct {
	OperationID string
	UserID      int64
}

// ProfileProvisioner calls User Service's private provisioning operations.
type ProfileProvisioner interface {
	Provision(ctx context.Context, request ProvisionRequest) (ProvisionReceipt, error)
	ReceiptByUser(ctx context.Context, userID int64) (ProvisionReceipt, error)
}

// ProvisioningTx stores the provisioning operation with its new account.
type ProvisioningTx interface {
	CreateProvisioningOperation(ctx context.Context, operation model.ProvisioningOperation) error
}

// ProvisioningRepository reads and finishes stored provisioning operations.
type ProvisioningRepository interface {
	// ProvisioningOperation returns the operation for userID or ErrNotFound.
	ProvisioningOperation(ctx context.Context, userID int64) (model.ProvisioningOperation, error)
	// ActivateProvisionedAccount marks the operation COMPLETED and a
	// PROVISIONING account ACTIVE in one transaction. It is idempotent for an
	// already ACTIVE account and returns ErrNotFound when the account is
	// gone or DELETING.
	ActivateProvisionedAccount(ctx context.Context, userID int64, operationID string) (model.User, error)
	// RecordProvisioningAttempt counts one attempt and stores a short error
	// code; terminal marks the operation FAILED.
	RecordProvisioningAttempt(ctx context.Context, userID int64, code string, terminal bool) error
	// DiscardProvisionalAccount deletes a PROVISIONING account and its
	// operation, freeing the email and username for a new registration.
	DiscardProvisionalAccount(ctx context.Context, userID int64) error
}

// WithProfileProvisioner makes registration create PROVISIONING accounts and
// issue sessions only after User Service confirms the profile.
func WithProfileProvisioner(provisioner ProfileProvisioner) Option {
	return func(s *AuthService) { s.provisioner = provisioner }
}

func (s *AuthService) provisioningEnabled() bool {
	return s.provisioner != nil
}

// newProvisioningOperation builds the canonical request stored with a new account.
func newProvisioningOperation(user model.User) (model.ProvisioningOperation, error) {
	operationID, err := security.NewUUID()
	if err != nil {
		return model.ProvisioningOperation{}, fmt.Errorf("generate provisioning operation id: %w", err)
	}

	return model.ProvisioningOperation{
		UserID:           user.ID,
		OperationID:      operationID,
		Username:         user.Username,
		AccountCreatedAt: user.CreatedAt.UTC().Truncate(time.Microsecond),
		State:            model.ProvisioningPending,
	}, nil
}

// completeProvisioning sends the stored request and activates the account
// when User Service returns the receipt for this operation.
//
// Flow:
//
//  1. Send the stored operation unchanged. 201/200 activate the account.
//  2. USER_ALREADY_PROVISIONED reads the receipt by user_id; the same
//     operation of record activates the account, another one is terminal.
//  3. USERNAME_TAKEN or INVALID_USERNAME discards the provisional account so
//     registration fails without a usable partial account.
//  4. Outages leave the operation PENDING for an identical retry.
//  5. Any other answer is terminal and recorded for investigation.
//
// Errors:
//
//   - ErrUsernameTaken, ErrInvalidUsername: the account was discarded.
//   - ErrServiceUnavailable: retry later with the same operation.
//   - ErrProvisioningFailed: terminal; no session may be issued.
func (s *AuthService) completeProvisioning(ctx context.Context, store ProvisioningRepository, operation model.ProvisioningOperation) (model.User, error) {
	switch operation.State {
	case model.ProvisioningCompleted:
		return s.activate(ctx, store, operation)
	case model.ProvisioningFailed:
		return model.User{}, ErrProvisioningFailed
	}

	receipt, err := s.provisioner.Provision(ctx, ProvisionRequest{
		OperationID: operation.OperationID,
		UserID:      operation.UserID,
		Username:    operation.Username,
		CreatedAt:   operation.AccountCreatedAt,
	})
	if errors.Is(err, ErrProvisionAlreadyProvisioned) {
		receipt, err = s.provisioner.ReceiptByUser(ctx, operation.UserID)
	}
	switch {
	case err == nil && receipt.OperationID == operation.OperationID && receipt.UserID == operation.UserID:
		return s.activate(ctx, store, operation)
	case err == nil:
		return model.User{}, s.failProvisioning(ctx, store, operation.UserID, "OPERATION_OF_RECORD_MISMATCH")
	case errors.Is(err, ErrProvisionUsernameTaken), errors.Is(err, ErrProvisionUsernameInvalid):
		if discardErr := store.DiscardProvisionalAccount(ctx, operation.UserID); discardErr != nil {
			return model.User{}, discardErr
		}
		if errors.Is(err, ErrProvisionUsernameTaken) {
			return model.User{}, ErrUsernameTaken
		}
		return model.User{}, ErrInvalidUsername
	case errors.Is(err, ErrProvisionUnavailable):
		if recordErr := store.RecordProvisioningAttempt(ctx, operation.UserID, "SERVICE_UNAVAILABLE", false); recordErr != nil {
			return model.User{}, errors.Join(ErrServiceUnavailable, recordErr)
		}
		return model.User{}, ErrServiceUnavailable
	default:
		return model.User{}, s.failProvisioning(ctx, store, operation.UserID, provisioningCode(err))
	}
}

func (s *AuthService) activate(ctx context.Context, store ProvisioningRepository, operation model.ProvisioningOperation) (model.User, error) {
	user, err := store.ActivateProvisionedAccount(ctx, operation.UserID, operation.OperationID)
	if errors.Is(err, ErrNotFound) {
		return model.User{}, ErrProvisioningFailed
	}

	return user, err
}

func (s *AuthService) failProvisioning(ctx context.Context, store ProvisioningRepository, userID int64, code string) error {
	if err := store.RecordProvisioningAttempt(ctx, userID, code, true); err != nil {
		return errors.Join(ErrProvisioningFailed, err)
	}

	return ErrProvisioningFailed
}

// provisioningCode maps a provisioner error to a short stored code; it never
// stores response bodies or credentials.
func provisioningCode(err error) string {
	switch {
	case errors.Is(err, ErrProvisionUserDeleted):
		return "USER_DELETED"
	case errors.Is(err, ErrProvisionNotFound):
		return "PROVISIONING_NOT_FOUND"
	case errors.Is(err, ErrProvisionRejected):
		return "REJECTED"
	default:
		return "UNEXPECTED"
	}
}

// resumeProvisioning continues the stored operation of a PROVISIONING account.
func (s *AuthService) resumeProvisioning(ctx context.Context, user model.User) (model.User, error) {
	store, ok := s.repository.(ProvisioningRepository)
	if !s.provisioningEnabled() || !ok {
		return model.User{}, ErrServiceUnavailable
	}
	operation, err := store.ProvisioningOperation(ctx, user.ID)
	if errors.Is(err, ErrNotFound) {
		return model.User{}, ErrProvisioningFailed
	}
	if err != nil {
		return model.User{}, err
	}

	return s.completeProvisioning(ctx, store, operation)
}
