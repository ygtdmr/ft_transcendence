package service

import (
	"context"
	"fmt"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/model"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/security"
	"golang.org/x/crypto/bcrypt"
)

// AuthTx exposes account writes within a single repository transaction.
type AuthTx interface {
	CreateUser(context.Context, model.User) (model.User, error)
	CreateSession(context.Context, model.Session) error
	CreateRefreshToken(context.Context, model.RefreshToken) error
}

// AuthRepository commits only when fn succeeds; any failure rolls back its writes.
type AuthRepository interface {
	WithTransaction(ctx context.Context, fn func(AuthTx) error) error
}

// CredentialIssuer creates a session and credentials for a persisted user ID.
type CredentialIssuer interface {
	Issue(userID int64) (security.Credentials, error)
}

type RegisterInput struct {
	Email    string
	Username string
	Password string `json:"-"`
}

// AuthResult is an internal result; controllers send credentials only as cookies.
type AuthResult struct {
	User        model.User           `json:"user"`
	Credentials security.Credentials `json:"-"`
}

type AuthService struct {
	repository  AuthRepository
	issuer      CredentialIssuer
	sessions    SessionRepository
	tokens      SessionCredentialIssuer
	verifier    AccessVerifier
	lifecycle   SessionLifecycle
	revocations RevocationRepository
	provisioner ProfileProvisioner
	profiles    ProfileReader
}

// NewAuth returns an auth service using the supplied transaction and token providers.
func NewAuth(repository AuthRepository, issuer CredentialIssuer, options ...Option) *AuthService {
	s := &AuthService{repository: repository, issuer: issuer}
	s.sessions, _ = repository.(SessionRepository)
	s.tokens, _ = issuer.(SessionCredentialIssuer)
	s.verifier, _ = issuer.(AccessVerifier)
	s.revocations, _ = repository.(RevocationRepository)
	for _, option := range options {
		option(s)
	}
	return s
}

// Register validates credentials and creates an account with its session.
//
// Parameters:
//
//   - ctx: Controls repository operations; password hashing is not interruptible once started.
//   - input: Email, username and plaintext password for the new account.
//
// It returns the persisted user and issued credentials only after commit.
// On failure, it returns a zero AuthResult; failed transaction steps roll back their writes.
//
// Errors:
//
//   - ErrInvalidEmail: The supplied email is invalid.
//   - ErrInvalidUsername: The supplied username is invalid.
//   - ErrInvalidPassword: The password violates the length constraints.
//   - ErrEmailTaken: Another account already uses the email.
//   - ErrUsernameTaken: Another account already uses the username.
//   - Context errors: The operation was canceled or its deadline expired.
//   - Hashing errors: Password hashing failed; the underlying error is wrapped.
//   - Credential errors: Credential issuance failed; the underlying error is wrapped.
//   - Repository errors: A database operation or transaction failed.
func (s *AuthService) Register(ctx context.Context, input RegisterInput) (AuthResult, error) {
	input, err := ValidateRegister(input)
	if err != nil {
		return AuthResult{}, err
	}

	if err := ctx.Err(); err != nil {
		return AuthResult{}, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		return AuthResult{}, fmt.Errorf("hash password: %w", err)
	}

	if s.provisioningEnabled() {
		return s.registerWithProvisioning(ctx, input, string(hash))
	}

	var result AuthResult
	err = s.repository.WithTransaction(ctx, func(tx AuthTx) error {
		var err error
		result, err = s.registerAccount(ctx, tx, model.User{
			Email: input.Email, Username: input.Username, PasswordHash: string(hash),
		})
		return err
	})
	if err != nil {
		return AuthResult{}, err
	}
	return result, nil
}

func (s *AuthService) registerAccount(ctx context.Context, tx AuthTx, account model.User) (AuthResult, error) {
	user, err := tx.CreateUser(ctx, account)
	if err != nil {
		return AuthResult{}, err
	}
	credentials, err := s.issuer.Issue(user.ID)
	if err != nil {
		return AuthResult{}, fmt.Errorf("issue credentials: %w", err)
	}
	if err := tx.CreateSession(ctx, credentials.Session); err != nil {
		return AuthResult{}, err
	}
	if err := tx.CreateRefreshToken(ctx, credentials.Refresh); err != nil {
		return AuthResult{}, err
	}
	return AuthResult{User: user, Credentials: credentials}, nil
}
