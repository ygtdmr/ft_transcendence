package service

import (
	"context"
	"errors"
	"time"
)

// deferSessionRevocation prevents failing oldest notifications from pinning each batch.
func (s *AuthService) deferSessionRevocation(ctx context.Context, sessionID string) error {
	retryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	return s.revocations.ScheduleSessionRevocationRetry(retryCtx, sessionID, time.Now().UTC().Add(30*time.Second))
}

// DeliverPendingRevocations retries committed notifications independently of HTTP retries.
// Gateway adapters must close sockets idempotently, including duplicate deliveries.
func (s *AuthService) DeliverPendingRevocations(ctx context.Context) error {
	if s.lifecycle == nil {
		return nil
	}
	if s.revocations == nil {
		return ErrSessionUnavailable
	}
	events, err := s.revocations.PendingSessionRevocations(ctx, 100)
	if err != nil {
		return err
	}
	var failures []error
	for _, event := range events {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if err := s.notifySessionRevoked(ctx, event.UserID, event.SessionID, event.Cause); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
