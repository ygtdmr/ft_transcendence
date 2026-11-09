package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/model"
)

// Errors a ProfileReader returns for User Service answers.
var (
	ErrProfileUnavailable = errors.New("user service profile read unavailable")
	ErrProfileDeleted     = errors.New("user service: profile deleted")
)

// ProfileReader reads the canonical username owned by User Service.
type ProfileReader interface {
	// CanonicalUsername returns the current username of userID. It returns
	// ErrProfileUnavailable when User Service cannot answer now and
	// ErrProfileDeleted when account deletion is recorded.
	CanonicalUsername(ctx context.Context, userID int64) (string, error)
}

// WithProfileReader makes login and current-session answer with the username
// User Service holds now. Without it the name stored at registration is used.
func WithProfileReader(reader ProfileReader) Option {
	return func(s *AuthService) { s.profiles = reader }
}

// withCanonicalUsername replaces the stored name with the current one. It
// never falls back to the stored name, so an outage is ErrServiceUnavailable.
func (s *AuthService) withCanonicalUsername(ctx context.Context, user model.User) (model.User, error) {
	if s.profiles == nil {
		return user, nil
	}
	username, err := s.profiles.CanonicalUsername(ctx, user.ID)
	if errors.Is(err, ErrProfileUnavailable) {
		return model.User{}, fmt.Errorf("%w: %w", ErrServiceUnavailable, err)
	}
	if err != nil {
		return model.User{}, err
	}
	user.Username = username

	return user, nil
}
