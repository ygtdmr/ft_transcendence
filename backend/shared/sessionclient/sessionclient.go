// Package sessionclient validates a browser session through the Auth Service's
// private GET /internal/v1/sessions/current, as the private service contract
// defines it (scope session.validate). It implements identity.SessionValidator,
// so a gateway or BFF wires it straight into identity.Authenticate.
//
// Only the ft_session credential is forwarded, as a cookie, never moved to a
// header, body or URL, never logged. The verdict maps exactly as the contract
// requires:
//
//   - 200: the Identity from data.user_id, data.session_id, data.session_expires_at;
//   - 401 UNAUTHORIZED: identity.ErrInvalidSession (the only "logged out" answer);
//   - 503, 5xx, network failure, timeout, an unreadable answer:
//     identity.ErrUnavailable, never a logout;
//   - 401 SERVICE_UNAUTHENTICATED or 403 SERVICE_FORBIDDEN: identity.ErrServiceRejected
//     (this service's own credential is wrong: a configuration fault).
package sessionclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/identity"
	"github.com/melmut-42/ft_transcendence/backend/shared/privateclient"
)

const path = "/internal/v1/sessions/current"

var _ identity.SessionValidator = (*Client)(nil)

// Client asks Auth for session verdicts.
type Client struct {
	private *privateclient.Client
}

// New wraps a privateclient.Client configured with this service's credential for
// the Auth Service.
func New(private *privateclient.Client) *Client { return &Client{private: private} }

// Validate implements identity.SessionValidator.
func (c *Client) Validate(ctx context.Context, sessionCredential string) (identity.Identity, error) {
	if sessionCredential == "" {
		return identity.Identity{}, identity.ErrInvalidSession
	}
	var data struct {
		UserID           int64  `json:"user_id"`
		SessionID        string `json:"session_id"`
		SessionExpiresAt string `json:"session_expires_at"`
	}
	err := c.private.Do(ctx, privateclient.Request{Method: http.MethodGet, Path: path, SessionCookie: sessionCredential}, &data)
	switch {
	case err == nil:
	case errors.Is(err, privateclient.ErrServiceRejected):
		return identity.Identity{}, identity.ErrServiceRejected
	case errors.Is(err, privateclient.ErrInvalidRequest):
		// The browser sent a value that cannot be a session cookie: not a session.
		return identity.Identity{}, identity.ErrInvalidSession
	case errors.Is(err, privateclient.ErrUnavailable):
		return identity.Identity{}, identity.ErrUnavailable
	default:
		var remote *privateclient.RemoteError
		if errors.As(err, &remote) && remote.Status == http.StatusUnauthorized && remote.Code == "UNAUTHORIZED" {
			return identity.Identity{}, identity.ErrInvalidSession
		}
		// Any other refusal means this caller and Auth disagree about the contract.
		return identity.Identity{}, fmt.Errorf("sessionclient: unexpected answer from the session verifier: %w", err)
	}

	expires, err := time.Parse(time.RFC3339Nano, data.SessionExpiresAt)
	if err != nil || data.UserID <= 0 || data.SessionID == "" {
		return identity.Identity{}, fmt.Errorf("%w: malformed verdict", identity.ErrUnavailable)
	}
	return identity.Identity{UserID: data.UserID, SessionID: data.SessionID, ExpiresAt: expires.UTC()}, nil
}
