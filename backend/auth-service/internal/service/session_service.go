package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/model"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/security"
)

// Authenticate verifies the JWT and rechecks the authoritative session owner/state.
func (s *AuthService) Authenticate(ctx context.Context, raw string) (Identity, error) {
	if s.sessions == nil || s.verifier == nil {
		return Identity{}, ErrSessionUnavailable
	}
	claims, err := s.verifier.VerifyAccess(raw)
	if errors.Is(err, security.ErrInvalidAccessToken) {
		return Identity{}, ErrUnauthorized
	}
	if err != nil {
		return Identity{}, err
	}
	user, session, err := s.sessions.FindSessionUser(ctx, claims.SessionID)
	if errors.Is(err, ErrNotFound) {
		return Identity{}, ErrUnauthorized
	}
	if err != nil {
		return Identity{}, err
	}
	if session.RevokedAt != nil || session.UserID != claims.UserID || user.ID != claims.UserID || user.Status != model.AccountActive {
		return Identity{}, ErrUnauthorized
	}
	return Identity{User: user, SessionID: session.ID, ExpiresAt: claims.ExpiresAt}, nil
}

// CurrentSession obtains live room membership through the optional Game adapter.
func (s *AuthService) CurrentSession(ctx context.Context, identity Identity) (SessionResult, error) {
	if err := s.checkIdentity(ctx, identity); err != nil {
		return SessionResult{}, err
	}
	membership, err := s.currentMembership(ctx, identity.User.ID)
	if err != nil {
		return SessionResult{}, err
	}
	return SessionResult{
		User:         SessionUser{ID: identity.User.ID, Username: identity.User.Username},
		ActiveRoomID: membership.RoomID, ActiveRoomAPIVersion: membership.APIVersion,
		SessionExpiresAt: identity.ExpiresAt,
	}, nil
}

func (s *AuthService) checkIdentity(ctx context.Context, identity Identity) error {
	if s.sessions == nil {
		return ErrSessionUnavailable
	}
	if identity.User.ID <= 0 || identity.SessionID == "" || !identity.ExpiresAt.After(time.Now()) {
		return ErrUnauthorized
	}
	user, session, err := s.sessions.FindSessionUser(ctx, identity.SessionID)
	if errors.Is(err, ErrNotFound) {
		return ErrUnauthorized
	}
	if err != nil {
		return err
	}
	if session.RevokedAt != nil || session.UserID != identity.User.ID || user.ID != identity.User.ID || user.Status != model.AccountActive {
		return ErrUnauthorized
	}
	return nil
}

func (s *AuthService) currentMembership(ctx context.Context, userID int64) (Membership, error) {
	if s.lifecycle == nil {
		return Membership{}, nil
	}
	membership, err := s.lifecycle.CurrentMembership(ctx, userID)
	if err != nil {
		return Membership{}, err
	}
	if (membership.RoomID == nil) != (membership.APIVersion == nil) {
		return Membership{}, fmt.Errorf("inconsistent Game membership")
	}
	if membership.RoomID != nil && (*membership.RoomID <= 0 || (*membership.APIVersion != "v1" && *membership.APIVersion != "v2")) {
		return Membership{}, fmt.Errorf("invalid Game membership")
	}
	return membership, nil
}

// Logout ends Game/Chat activity before committing credential revocation.
func (s *AuthService) Logout(ctx context.Context, identity Identity) error {
	if err := s.checkIdentity(ctx, identity); err != nil {
		return err
	}
	if s.lifecycle != nil {
		if err := s.lifecycle.EndSession(ctx, identity.User.ID, identity.SessionID); err != nil {
			return err
		}
	}
	err := s.sessions.WithSessionTransaction(ctx, func(tx SessionTx) error {
		return tx.RevokeSession(ctx, identity.SessionID, time.Now().UTC())
	})
	if err != nil {
		return err
	}
	return s.notifySessionRevoked(ctx, identity.User.ID, identity.SessionID, RevocationSessionRevoked)
}

// notifySessionRevoked closes sockets only after authoritative revocation commits.
func (s *AuthService) notifySessionRevoked(ctx context.Context, userID int64, sessionID, cause string) error {
	if s.lifecycle == nil {
		return nil
	}
	if s.revocations == nil {
		return ErrSessionUnavailable
	}
	notifyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := s.lifecycle.SessionRevoked(notifyCtx, userID, sessionID, cause)
	if err == nil {
		err = s.revocations.MarkSessionRevocationDelivered(notifyCtx, sessionID)
	}
	if err != nil {
		return errors.Join(err, s.deferSessionRevocation(ctx, sessionID))
	}
	return nil
}
