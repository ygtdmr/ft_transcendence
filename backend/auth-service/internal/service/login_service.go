package service

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/model"
	"golang.org/x/crypto/bcrypt"
)

// A valid default-cost hash gives unknown accounts the same password-check work.
const missingAccountHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

// Login verifies the existing password and commits a new independent session.
func (s *AuthService) Login(ctx context.Context, input LoginInput) (AuthResult, error) {
	if s.sessions == nil || s.tokens == nil {
		return AuthResult{}, ErrSessionUnavailable
	}
	input.Email = strings.TrimSpace(input.Email)
	if err := validateLogin(input); err != nil {
		return AuthResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return AuthResult{}, err
	}
	user, err := s.sessions.FindUserByEmail(ctx, input.Email)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return AuthResult{}, err
	}
	if err := compareLoginPassword(user, input.Password); err != nil {
		return AuthResult{}, err
	}
	switch user.Status {
	case model.AccountActive:
	case model.AccountProvisioning:
		// A registration interrupted by an outage continues its stored operation.
		user, err = s.resumeProvisioning(ctx, user)
		if errors.Is(err, ErrUsernameTaken) || errors.Is(err, ErrInvalidUsername) {
			// The provisional account was discarded; it no longer exists.
			return AuthResult{}, ErrInvalidCredentials
		}
		if err != nil {
			return AuthResult{}, err
		}
	default:
		// Accounts being deleted never receive a new session.
		return AuthResult{}, ErrInvalidCredentials
	}
	result, err := s.createLoginSession(ctx, user)
	if errors.Is(err, ErrAccountUnavailable) {
		// Deletion began after the account was read; no session is issued.
		return AuthResult{}, ErrInvalidCredentials
	}
	return result, err
}

func validateLogin(input LoginInput) error {
	address, err := mail.ParseAddress(input.Email)
	if err != nil || address.Address != input.Email || len(input.Email) > 254 {
		return ErrInvalidCredentials
	}
	if len(input.Password) == 0 || len(input.Password) > 72 {
		return ErrInvalidCredentials
	}
	return nil
}

func compareLoginPassword(user model.User, password string) error {
	// Unknown accounts and Google-only accounts (no password) take the same
	// time as a wrong password and give the same answer.
	if user.ID <= 0 || user.PasswordHash == "" {
		_ = bcrypt.CompareHashAndPassword([]byte(missingAccountHash), []byte(password))
		return ErrInvalidCredentials
	}
	err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password))
	if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		return ErrInvalidCredentials
	}
	if err != nil {
		return fmt.Errorf("verify password: %w", err)
	}
	return nil
}

func (s *AuthService) createLoginSession(ctx context.Context, user model.User) (AuthResult, error) {
	credentials, err := s.tokens.Issue(user.ID)
	if err != nil {
		return AuthResult{}, fmt.Errorf("issue login credentials: %w", err)
	}
	err = s.sessions.WithSessionTransaction(ctx, func(tx SessionTx) error {
		if err := tx.CreateSession(ctx, credentials.Session); err != nil {
			return err
		}
		return tx.CreateRefreshToken(ctx, credentials.Refresh)
	})
	if err != nil {
		return AuthResult{}, err
	}
	return AuthResult{User: user, Credentials: credentials}, nil
}
