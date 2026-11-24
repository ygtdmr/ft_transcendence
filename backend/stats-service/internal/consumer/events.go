// Package consumer applies Game Session's events from the broker.
package consumer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/service"
)

// Event types this service applies or deliberately ignores.
const (
	EventMatchFinalized = "match.finalized"
	EventPlayerPenalize = "player.penalized"
	EventGameCancelled  = "game.cancelled"
)

// Outcome tells the runner how to settle one delivery.
type Outcome int

const (
	// Ack: applied, a duplicate, or an event this service does not use.
	Ack Outcome = iota
	// Reject: can never be applied; it goes to the queue's dead-letter
	// exchange, if the topology configures one, and is not redelivered.
	Reject
	// Retry: a dependency failed; redeliver later.
	Retry
)

// MatchIngester applies match.finalized payloads.
type MatchIngester interface {
	Ingest(ctx context.Context, payload []byte) (bool, error)
}

// PenaltyIngester applies player.penalized payloads.
type PenaltyIngester interface {
	Ingest(ctx context.Context, eventID string, payload []byte) (bool, error)
}

// Handler decodes the event envelope and dispatches its payload.
//
// The envelope is the one the services' outbox relays publish:
//
//	{"event_id": string, "event_type": string, "occurred_at": string, "payload": object}
//
// event_id is the identity consumers deduplicate by.
type Handler struct {
	matches   MatchIngester
	penalties PenaltyIngester
}

// NewHandler returns the event handler.
func NewHandler(matches MatchIngester, penalties PenaltyIngester) *Handler {
	return &Handler{matches: matches, penalties: penalties}
}

type envelope struct {
	EventID   string          `json:"event_id"`
	EventType string          `json:"event_type"`
	Payload   json.RawMessage `json:"payload"`
}

// Handle settles one message body. The returned error explains a Reject or a
// Retry for logging; it is nil for Ack.
func (h *Handler) Handle(ctx context.Context, body []byte) (Outcome, string, error) {
	var event envelope
	if err := json.Unmarshal(body, &event); err != nil || event.EventID == "" || event.EventType == "" {
		return Reject, event.EventType, errors.New("malformed event envelope")
	}
	payload := bytes.TrimSpace(event.Payload)
	if len(payload) == 0 || payload[0] != '{' {
		return Reject, event.EventType, errors.New("event payload is not a JSON object")
	}

	var err error
	switch event.EventType {
	case EventMatchFinalized:
		_, err = h.matches.Ingest(ctx, payload)
	case EventPlayerPenalize:
		_, err = h.penalties.Ingest(ctx, event.EventID, payload)
	default:
		// game.cancelled and every other type carry nothing for statistics.
		return Ack, event.EventType, nil
	}
	switch {
	case err == nil:
		return Ack, event.EventType, nil
	case errors.Is(err, service.ErrInvalidMatch), errors.Is(err, service.ErrInvalidPenalty):
		return Reject, event.EventType, err
	default:
		return Retry, event.EventType, err
	}
}
