package testutil

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/model"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
)

var _ service.RevocationRepository = (*MemoryRepository)(nil)

func (t *memoryTx) enqueueSessionRevocation(session model.Session) error {
	if t.FailAt == "outbox" {
		return errors.New("private revocation outbox error")
	}
	for _, event := range t.Revocations {
		if event.SessionID == session.ID {
			return nil
		}
	}
	t.Revocations = append(t.Revocations, service.SessionRevocation{
		SessionID:     session.ID,
		UserID:        session.UserID,
		Cause:         service.RevocationSessionRevoked,
		RevokedAt:     *session.RevokedAt,
		NextAttemptAt: time.Now().UTC(),
	})
	return nil
}

func (r *MemoryRepository) PendingSessionRevocations(ctx context.Context, limit int) ([]service.SessionRevocation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 1000 {
		return nil, errors.New("revocation batch limit must be between 1 and 1000")
	}
	if r.FailAt == "pending_revocations" {
		return nil, errors.New("private pending revocations error")
	}
	var pending []service.SessionRevocation
	now := time.Now().UTC()
	for _, event := range r.Revocations {
		if !r.DeliveredRevocations[event.SessionID] && !event.NextAttemptAt.After(now) {
			pending = append(pending, event)
		}
	}
	sortRevocations(pending)
	if len(pending) > limit {
		pending = pending[:limit]
	}
	return pending, nil
}

func sortRevocations(events []service.SessionRevocation) {
	slices.SortFunc(events, func(a, b service.SessionRevocation) int {
		if compared := a.NextAttemptAt.Compare(b.NextAttemptAt); compared != 0 {
			return compared
		}
		if compared := a.RevokedAt.Compare(b.RevokedAt); compared != 0 {
			return compared
		}
		return strings.Compare(a.SessionID, b.SessionID)
	})
}

func (r *MemoryRepository) MarkSessionRevocationDelivered(ctx context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.FailAt == "mark_revocation" {
		return errors.New("private delivery marker error")
	}
	for _, event := range r.Revocations {
		if event.SessionID != id {
			continue
		}
		if r.DeliveredRevocations == nil {
			r.DeliveredRevocations = make(map[string]bool)
		}
		r.DeliveredRevocations[id] = true
		return nil
	}
	return service.ErrNotFound
}

func (r *MemoryRepository) ScheduleSessionRevocationRetry(ctx context.Context, id string, next time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.FailAt == "schedule_revocation" {
		return errors.New("private revocation scheduling error")
	}
	for i, event := range r.Revocations {
		if event.SessionID != id {
			continue
		}
		if !r.DeliveredRevocations[id] {
			r.Revocations[i].NextAttemptAt = next.UTC()
		}
		return nil
	}
	return service.ErrNotFound
}
