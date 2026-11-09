package unit_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/service"
)

// TestLevelMatchesContractFixtures checks the formula against the Bruno examples.
func TestLevelMatchesContractFixtures(t *testing.T) {
	for wins, want := range map[int]int{0: 1, 3: 1, 4: 2, 9: 3, 12: 4} {
		if got := service.Level(wins); got != want {
			t.Fatalf("Level(%d) = %d, want %d", wins, got, want)
		}
	}
}

type penaltyStoreSpy struct{ calls int }

func (s *penaltyStoreSpy) RecordPenalty(context.Context, string, service.PlayerPenalized) (bool, error) {
	s.calls++
	return true, nil
}

func validPenalty() map[string]any {
	return map[string]any{"user_id": 42, "room_id": 1001, "game_id": 5001, "role": "OPERATIVE",
		"reason": "EXITED", "points": 3, "penalized_at": "2026-09-10T08:50:00Z"}
}

// TestPenaltyIngestRefusesInvalidRecords checks every refused field.
//
// Expected output: ErrInvalidPenalty, and the store is never called.
func TestPenaltyIngestRefusesInvalidRecords(t *testing.T) {
	cases := map[string]any{
		"user_id": 0, "room_id": -1, "game_id": 0, "role": "HOST", "reason": "KICKED",
		"points": -1, "penalized_at": nil,
	}
	for field, value := range cases {
		t.Run(field, func(t *testing.T) {
			payload := validPenalty()
			payload[field] = value
			raw, _ := json.Marshal(payload)
			spy := &penaltyStoreSpy{}
			if _, err := service.NewPenaltyIngest(spy).Ingest(context.Background(), "event-1", raw); !errors.Is(err, service.ErrInvalidPenalty) || spy.calls != 0 {
				t.Fatalf("Ingest() = %v, calls = %d", err, spy.calls)
			}
		})
	}
}

// TestPenaltyIngestNeedsEvaluatedPoints checks a record without points and without an id.
func TestPenaltyIngestNeedsEvaluatedPoints(t *testing.T) {
	payload := validPenalty()
	delete(payload, "points")
	raw, _ := json.Marshal(payload)
	spy := &penaltyStoreSpy{}
	if _, err := service.NewPenaltyIngest(spy).Ingest(context.Background(), "event-1", raw); !errors.Is(err, service.ErrInvalidPenalty) {
		t.Fatalf("missing points = %v", err)
	}
	valid, _ := json.Marshal(validPenalty())
	if _, err := service.NewPenaltyIngest(spy).Ingest(context.Background(), "", valid); !errors.Is(err, service.ErrInvalidPenalty) || spy.calls != 0 {
		t.Fatalf("missing id = %v, calls = %d", err, spy.calls)
	}
}
