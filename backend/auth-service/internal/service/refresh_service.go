package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/model"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/security"
)

type refreshOutcome struct {
	credentials security.Credentials
	rejected    bool
	revoked     *model.Session
}

// Refresh serializes token rotation; reuse revocation must commit despite a 401.
func (s *AuthService) Refresh(ctx context.Context, raw string) (security.Credentials, error) {
	if s.sessions == nil || s.tokens == nil {
		return security.Credentials{}, ErrSessionUnavailable
	}
	if len(raw) != 43 {
		return security.Credentials{}, ErrSessionExpired
	}
	hash := sha256.Sum256([]byte(raw))
	var result refreshOutcome
	err := s.sessions.WithSessionTransaction(ctx, func(tx SessionTx) error {
		var err error
		result, err = s.refreshLocked(ctx, tx, hex.EncodeToString(hash[:]))
		return err
	})
	if err != nil {
		return security.Credentials{}, err
	}
	return s.finishRefresh(ctx, result)
}

// finishRefresh notifies Gateways after a committed reuse revocation.
//
// The family revocation and its notification record commit before this runs, and
// DeliverPendingRevocations retries undelivered records. A Gateway outage therefore
// still answers SESSION_EXPIRED, with the delivery error joined to it.
// Missing revocation storage is a configuration fault and is returned unchanged.
func (s *AuthService) finishRefresh(ctx context.Context, result refreshOutcome) (security.Credentials, error) {
	if result.revoked != nil {
		err := s.notifySessionRevoked(ctx, result.revoked.UserID, result.revoked.ID, RevocationSessionRevoked)
		if errors.Is(err, ErrSessionUnavailable) {
			return security.Credentials{}, err
		}
		if err != nil {
			return security.Credentials{}, errors.Join(ErrSessionExpired, err)
		}
	}
	if result.rejected {
		return security.Credentials{}, ErrSessionExpired
	}
	return result.credentials, nil
}

func (s *AuthService) refreshLocked(ctx context.Context, tx SessionTx, hash string) (refreshOutcome, error) {
	token, session, err := tx.FindRefreshToken(ctx, hash)
	if errors.Is(err, ErrNotFound) {
		return refreshOutcome{rejected: true}, nil
	}
	if err != nil {
		return refreshOutcome{}, err
	}
	now := time.Now().UTC()
	if token.RotatedAt != nil {
		err := tx.RevokeSession(ctx, token.SessionID, now)
		return refreshOutcome{rejected: true, revoked: &session}, err
	}
	if token.RevokedAt != nil || session.RevokedAt != nil || !token.ExpiresAt.After(now) {
		return refreshOutcome{rejected: true}, nil
	}
	credentials, err := s.rotateCredentials(ctx, tx, token, session, now)
	return refreshOutcome{credentials: credentials}, err
}

func (s *AuthService) rotateCredentials(ctx context.Context, tx SessionTx, token model.RefreshToken, session model.Session, now time.Time) (security.Credentials, error) {
	credentials, err := s.tokens.Rotate(session.UserID, session.ID)
	if err != nil {
		return security.Credentials{}, err
	}
	if err := tx.CreateRefreshToken(ctx, credentials.Refresh); err != nil {
		return security.Credentials{}, err
	}
	if err := tx.RotateRefreshToken(ctx, token.ID, credentials.Refresh.ID, now); err != nil {
		return security.Credentials{}, err
	}
	return credentials, nil
}
