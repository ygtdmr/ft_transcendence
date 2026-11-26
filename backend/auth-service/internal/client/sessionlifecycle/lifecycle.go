// Package sessionlifecycle joins the Game Session departure and the gateway
// revocation notification into one service.SessionLifecycle.
package sessionlifecycle

import (
	"context"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
)

// DepartureRecorder records a logout as a Game Session departure.
type DepartureRecorder interface {
	EndSession(ctx context.Context, userID int64, sessionID string) error
}

// RevocationNotifier tells every gateway instance that a session was revoked.
type RevocationNotifier interface {
	SessionRevoked(ctx context.Context, userID int64, sessionID, cause string) error
}

// Lifecycle calls Game Session for logout departures and the gateways for
// revocations. A nil part is not configured and is skipped, which only
// development and test configurations allow.
type Lifecycle struct {
	Departures DepartureRecorder
	Gateways   RevocationNotifier
}

var _ service.SessionLifecycle = Lifecycle{}

// Configured reports whether any part is set.
func (l Lifecycle) Configured() bool {
	return l.Departures != nil || l.Gateways != nil
}

// EndSession records the logout departure in Game Session.
func (l Lifecycle) EndSession(ctx context.Context, userID int64, sessionID string) error {
	if l.Departures == nil {
		return nil
	}
	return l.Departures.EndSession(ctx, userID, sessionID)
}

// SessionRevoked closes the session's sockets on every gateway instance.
func (l Lifecycle) SessionRevoked(ctx context.Context, userID int64, sessionID, cause string) error {
	if l.Gateways == nil {
		return nil
	}
	return l.Gateways.SessionRevoked(ctx, userID, sessionID, cause)
}
