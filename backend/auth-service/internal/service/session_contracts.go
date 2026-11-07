package service

import (
	"context"
	"errors"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/model"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/security"
)

var (
	ErrNotFound           = errors.New("account or session record was not found")
	ErrInvalidCredentials = errors.New("email or password is incorrect")
	ErrUnauthorized       = errors.New("a valid active session is required")
	ErrSessionExpired     = errors.New("the refresh token is invalid or expired; log in again")
	ErrSessionUnavailable = errors.New("session dependencies are not configured")
	// ErrUnavailable reports that the database could not be reached.
	ErrUnavailable = errors.New("service dependency unavailable")
)

// SessionRepository separates session reads and transactions from account registration.
type SessionRepository interface {
	FindUserByEmail(context.Context, string) (model.User, error)
	FindSessionUser(context.Context, string) (model.User, model.Session, error)
	WithSessionTransaction(context.Context, func(SessionTx) error) error
}

// AccountReader reads one account by id for private metadata reads.
type AccountReader interface {
	FindAccount(context.Context, int64) (model.User, error)
}

// SessionTx locks a session before its refresh rows, serializing rotation and revocation.
type SessionTx interface {
	CreateSession(context.Context, model.Session) error
	CreateRefreshToken(context.Context, model.RefreshToken) error
	FindRefreshToken(context.Context, string) (model.RefreshToken, model.Session, error)
	RotateRefreshToken(context.Context, string, string, time.Time) error
	RevokeSession(context.Context, string, time.Time) error
}

type SessionCredentialIssuer interface {
	Issue(int64) (security.Credentials, error)
	Rotate(int64, string) (security.Credentials, error)
}

type AccessVerifier interface {
	VerifyAccess(string) (security.AccessIdentity, error)
}

type LoginInput struct {
	Email    string
	Password string `json:"-"`
}

// Identity is attached only after JWT verification and an active-session lookup.
type Identity struct {
	User      model.User
	SessionID string
	ExpiresAt time.Time
}

type SessionUser struct {
	ID       int64  `json:"user_id"`
	Username string `json:"username"`
}

type SessionResult struct {
	User                 SessionUser `json:"user"`
	ActiveRoomID         *int64      `json:"active_room_id"`
	ActiveRoomAPIVersion *string     `json:"active_room_api_version"`
	SessionExpiresAt     time.Time   `json:"session_expires_at"`
}

type Membership struct {
	RoomID     *int64
	APIVersion *string
}

// SessionLifecycle bridges Game membership and Game/Chat departure and socket closure.
// EndSession must be idempotent: a database commit may fail after the external call.
// SessionRevoked receives the revocation cause (RevocationSessionRevoked or
// RevocationAccountDeleted) so Gateways can tell account deletion apart.
type SessionLifecycle interface {
	CurrentMembership(context.Context, int64) (Membership, error)
	EndSession(context.Context, int64, string) error
	SessionRevoked(ctx context.Context, userID int64, sessionID, cause string) error
}

// SessionRevocation remains pending until Gateways acknowledge socket closure.
type SessionRevocation struct {
	SessionID     string
	UserID        int64
	Cause         string
	RevokedAt     time.Time
	NextAttemptAt time.Time
}

type RevocationRepository interface {
	PendingSessionRevocations(context.Context, int) ([]SessionRevocation, error)
	MarkSessionRevocationDelivered(context.Context, string) error
	ScheduleSessionRevocationRetry(context.Context, string, time.Time) error
}

type Option func(*AuthService)

// WithSessionLifecycle connects the live Game/Chat owner to session HTTP operations.
func WithSessionLifecycle(lifecycle SessionLifecycle) Option {
	return func(s *AuthService) { s.lifecycle = lifecycle }
}
