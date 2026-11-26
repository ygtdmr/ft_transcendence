// Package gamesession calls Game Session's room.socket routes (contract
// collections/internal/v1/service-rest-api/09 - Game Gateway Calls):
//
//   - PUT    /internal/v1/rooms/{room_id}/connections/{user_id}  socket opened
//   - DELETE /internal/v1/rooms/{room_id}/connections/{user_id}  last socket closed
//   - DELETE /internal/v1/room-connections                       gateway started
//   - POST   /internal/v1/rooms/{room_id}/commands               forward a command
//
// Game Session decides membership and runs every command; this client only
// carries the calls and classifies their outcomes.
package gamesession

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/privateclient"
)

// Verdicts of Report socket opened that refuse the handshake with the same
// status and code.
var (
	ErrNotMember    = errors.New("gamesession: user is not a member of the room")
	ErrRoomNotFound = errors.New("gamesession: room not found")
	ErrRoomClosed   = errors.New("gamesession: room is closed")
)

// ErrUnavailable covers every other failure: an outage, a timeout, a 5xx, an
// unexpected answer or a refused service credential. It is never a membership
// verdict; the handshake answers 503.
var ErrUnavailable = errors.New("gamesession: unavailable")

// Closure tells the Gateway to close every socket of UserID for the room.
type Closure struct {
	UserID int64  `json:"user_id"`
	Code   int    `json:"code"`
	Reason string `json:"reason"`
}

// CommandResult is Game Session's answer to a forwarded command.
type CommandResult struct {
	// Reply is the ack or error message, written to the sending socket as is.
	Reply json.RawMessage `json:"reply"`
	// Closures are applied after the reply.
	Closures []Closure `json:"closures"`
}

// Client calls Game Session. It is safe for concurrent use.
type Client struct {
	pc *privateclient.Client
	// Attempts is how many times a retryable failure is tried in total.
	Attempts int
	// Backoff is the wait before the second attempt; it doubles each time.
	Backoff time.Duration
}

// New wraps a privateclient.Client holding the gateway's credential for Game
// Session.
func New(pc *privateclient.Client) *Client {
	return &Client{pc: pc, Attempts: 3, Backoff: 100 * time.Millisecond}
}

// Connected reports an opened socket during the handshake.
//
// Errors: ErrNotMember, ErrRoomNotFound, ErrRoomClosed, or ErrUnavailable.
func (c *Client) Connected(ctx context.Context, roomID, userID int64) error {
	err := c.retry(ctx, privateclient.Request{Method: http.MethodPut, Path: connectionPath(roomID, userID)}, nil)
	var remote *privateclient.RemoteError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &remote) && remote.Status == http.StatusForbidden && remote.Code == "NOT_ROOM_MEMBER":
		return ErrNotMember
	case errors.As(err, &remote) && remote.Status == http.StatusNotFound && remote.Code == "ROOM_NOT_FOUND":
		return ErrRoomNotFound
	case errors.As(err, &remote) && remote.Status == http.StatusConflict && remote.Code == "INVALID_ROOM_STATE":
		return ErrRoomClosed
	default:
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
}

// Disconnected reports that the user's last socket for the room closed. A
// member or room that no longer exists has nothing to do and is not an error.
func (c *Client) Disconnected(ctx context.Context, roomID, userID int64) error {
	err := c.retry(ctx, privateclient.Request{Method: http.MethodDelete, Path: connectionPath(roomID, userID)}, nil)
	var remote *privateclient.RemoteError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &remote) && (remote.Code == "NOT_ROOM_MEMBER" || remote.Code == "ROOM_NOT_FOUND"):
		return nil
	default:
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
}

// ResetConnections reports that this gateway started without sockets. It is
// tried once; the caller repeats it until it succeeds.
func (c *Client) ResetConnections(ctx context.Context) error {
	err := c.pc.Do(ctx, privateclient.Request{Method: http.MethodDelete, Path: "/internal/v1/room-connections"}, nil)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	return nil
}

// Command forwards raw, the client's message exactly as received, with the
// identity of the validated session.
func (c *Client) Command(ctx context.Context, roomID, userID int64, sessionID string, raw []byte) (CommandResult, error) {
	var result CommandResult
	err := c.retry(ctx, privateclient.Request{
		Method:  http.MethodPost,
		Path:    "/internal/v1/rooms/" + strconv.FormatInt(roomID, 10) + "/commands",
		RawBody: raw,
		Header: http.Header{
			"X-User-Id":    {strconv.FormatInt(userID, 10)},
			"X-Session-Id": {strings.ToLower(sessionID)},
		},
	}, &result)
	if err != nil {
		return CommandResult{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if len(result.Reply) == 0 || result.Reply[0] != '{' {
		return CommandResult{}, fmt.Errorf("%w: answer without a reply object", ErrUnavailable)
	}

	return result, nil
}

// retry sends req again after a retryable failure (an outage, a timeout: the
// outcome is unknown, and every call here is safe to repeat identically).
func (c *Client) retry(ctx context.Context, req privateclient.Request, out any) error {
	wait := c.Backoff
	var err error
	for attempt := 1; ; attempt++ {
		err = c.pc.Do(ctx, req, out)
		if err == nil || !privateclient.Retryable(err) || attempt >= c.Attempts {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
		wait *= 2
	}
}

func connectionPath(roomID, userID int64) string {
	return "/internal/v1/rooms/" + strconv.FormatInt(roomID, 10) + "/connections/" + strconv.FormatInt(userID, 10)
}
