package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"time"
)

const maxPenaltyEventID = 128

// ErrInvalidPenalty reports a penalty event that can never be stored. A
// consumer must not retry it.
var ErrInvalidPenalty = errors.New("invalid penalty event")

// PlayerPenalized is the payload of Game Session's player.penalized record.
// Points is the amount Game Session evaluated; this service never computes it.
type PlayerPenalized struct {
	UserID      int64     `json:"user_id"`
	RoomID      int64     `json:"room_id"`
	GameID      int64     `json:"game_id"`
	Role        string    `json:"role"`
	Reason      string    `json:"reason"`
	Points      *int64    `json:"points"`
	PenalizedAt time.Time `json:"penalized_at"`
}

func (p PlayerPenalized) validate() error {
	switch {
	case p.UserID <= 0:
		return invalidPenalty("user_id")
	case p.RoomID <= 0:
		return invalidPenalty("room_id")
	case p.GameID <= 0:
		return invalidPenalty("game_id")
	case p.Role != "SPYMASTER" && p.Role != "OPERATIVE":
		return invalidPenalty("role")
	case p.Reason != "EXITED" && p.Reason != "SESSION_ENDED":
		return invalidPenalty("reason")
	case p.Points == nil || *p.Points < 0 || *p.Points > math.MaxInt32:
		return invalidPenalty("points")
	case p.PenalizedAt.IsZero():
		return invalidPenalty("penalized_at")
	}

	return nil
}

func invalidPenalty(field string) error {
	return fmt.Errorf("%w: %s", ErrInvalidPenalty, field)
}

// PenaltyStore records penalties.
type PenaltyStore interface {
	// RecordPenalty stores the event and adds its points in one transaction. It
	// returns false when eventID is already recorded, and changes nothing then.
	RecordPenalty(ctx context.Context, eventID string, penalty PlayerPenalized) (bool, error)
}

// PenaltyIngest applies player.penalized records.
type PenaltyIngest struct {
	store PenaltyStore
}

// NewPenaltyIngest returns the player.penalized handler.
func NewPenaltyIngest(store PenaltyStore) *PenaltyIngest {
	return &PenaltyIngest{store: store}
}

// Ingest validates one record and applies it at most once per eventID. It
// returns false for a duplicate, which a consumer acknowledges as done.
//
// Errors:
//
//   - ErrInvalidPenalty: the record can never be stored.
//   - ErrUnavailable or other database errors: nothing was stored; retry.
func (s *PenaltyIngest) Ingest(ctx context.Context, eventID string, payload []byte) (bool, error) {
	if eventID == "" || len(eventID) > maxPenaltyEventID {
		return false, invalidPenalty("event_id")
	}
	var penalty PlayerPenalized
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&penalty); err != nil {
		return false, invalidPenalty("payload")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return false, invalidPenalty("payload")
	}
	if err := penalty.validate(); err != nil {
		return false, err
	}

	return s.store.RecordPenalty(ctx, eventID, penalty)
}
