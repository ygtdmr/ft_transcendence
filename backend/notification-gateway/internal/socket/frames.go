package socket

import (
	"encoding/json"
	"time"
)

// Close codes and reasons this gateway uses. 4401 is the contract's code for
// an explicitly revoked session; the others are RFC 6455 codes.
const (
	CloseNormal          = 1000
	CloseGoingAway       = 1001
	ClosePolicyViolation = 1008
	CloseSessionRevoked  = 4401
)

// Fixed frames from the Notifications WebSocket v2 contract.
var (
	// invalidEventFrame answers any client frame; the socket stays open.
	invalidEventFrame = []byte(`{"type":"error","payload":{"code":"INVALID_EVENT"}}`)
	// UnauthorizedFrame precedes the 4401 close of a revoked session.
	UnauthorizedFrame = []byte(`{"type":"error","payload":{"code":"UNAUTHORIZED"}}`)
)

// timestampLayout is ISO 8601 UTC with second precision, as in the contract
// examples ("2026-09-10T08:40:00Z").
const timestampLayout = "2006-01-02T15:04:05Z"

type readyPayload struct {
	UserID      int64  `json:"user_id"`
	ConnectedAt string `json:"connected_at"`
}

type readyFrame struct {
	Type    string       `json:"type"`
	SentAt  string       `json:"sent_at"`
	Payload readyPayload `json:"payload"`
}

// readyMessage is the first event on every socket: notifications.ready with
// the authenticated user and the registration time.
func readyMessage(userID int64, connectedAt time.Time) []byte {
	at := connectedAt.UTC().Format(timestampLayout)
	msg, _ := json.Marshal(readyFrame{
		Type:    "notifications.ready",
		SentAt:  at,
		Payload: readyPayload{UserID: userID, ConnectedAt: at},
	})

	return msg
}
