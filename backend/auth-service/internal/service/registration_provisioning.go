package service

import (
	"context"
	"errors"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/model"
	"golang.org/x/crypto/bcrypt"
)

// registerWithProvisioning creates a PROVISIONING account with its stored
// provisioning operation, confirms the profile with User Service, then issues
// the first session.
//
// Flow:
//
//  1. Create the account and the operation in one Auth transaction.
//  2. If the email belongs to a PROVISIONING account with the same password
//     and username, resume that account's stored operation instead.
//  3. Send the operation; only an activated account receives session cookies.
//
// Errors:
//
//   - ErrEmailTaken, ErrUsernameTaken, ErrInvalidUsername: registration fails.
//   - ErrServiceUnavailable: the account stays PROVISIONING; retrying the same
//     registration or logging in resumes the same operation.
//   - ErrProvisioningFailed: terminal; no session is issued.
func (s *AuthService) registerWithProvisioning(ctx context.Context, input RegisterInput, hash string) (AuthResult, error) {
	store, ok := s.repository.(ProvisioningRepository)
	if !ok || s.tokens == nil || s.sessions == nil {
		return AuthResult{}, ErrSessionUnavailable
	}

	var operation model.ProvisioningOperation
	err := s.repository.WithTransaction(ctx, func(tx AuthTx) error {
		provisioningTx, ok := tx.(ProvisioningTx)
		if !ok {
			return ErrSessionUnavailable
		}
		user, err := tx.CreateUser(ctx, model.User{
			Email: input.Email, Username: input.Username, PasswordHash: hash, Status: model.AccountProvisioning,
		})
		if err != nil {
			return err
		}
		operation, err = newProvisioningOperation(user)
		if err != nil {
			return err
		}
		return provisioningTx.CreateProvisioningOperation(ctx, operation)
	})
	if errors.Is(err, ErrEmailTaken) {
		return s.resumeRegistration(ctx, store, input)
	}
	if err != nil {
		return AuthResult{}, err
	}

	user, err := s.completeProvisioning(ctx, store, operation)
	if err != nil {
		return AuthResult{}, err
	}

	return s.createLoginSession(ctx, user)
}

// resumeRegistration lets a registration retried after an outage continue the
// original operation. Any other existing account keeps answering EMAIL_TAKEN.
func (s *AuthService) resumeRegistration(ctx context.Context, store ProvisioningRepository, input RegisterInput) (AuthResult, error) {
	user, err := s.sessions.FindUserByEmail(ctx, input.Email)
	if err != nil || user.Status != model.AccountProvisioning {
		return AuthResult{}, ErrEmailTaken
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(input.Password)) != nil {
		return AuthResult{}, ErrEmailTaken
	}
	operation, err := store.ProvisioningOperation(ctx, user.ID)
	if err != nil || operation.Username != input.Username {
		return AuthResult{}, ErrEmailTaken
	}

	activated, err := s.completeProvisioning(ctx, store, operation)
	if err != nil {
		return AuthResult{}, err
	}

	return s.createLoginSession(ctx, activated)
}
