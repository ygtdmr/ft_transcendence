package unit_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/consumer"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/service"
)

type ingestStub struct {
	err     error
	calls   int
	eventID string
	payload string
}

func (s *ingestStub) Ingest(_ context.Context, payload []byte) (bool, error) {
	s.calls++
	s.payload = string(payload)
	return s.err == nil, s.err
}

type penaltyStub struct{ ingestStub }

func (s *penaltyStub) Ingest(_ context.Context, eventID string, payload []byte) (bool, error) {
	s.calls++
	s.eventID, s.payload = eventID, string(payload)
	return s.err == nil, s.err
}

func event(eventType, payload string) []byte {
	return fmt.Appendf(nil, `{"event_id":"8d3c0f8e-5b5e-4c39-9d43-0a7c1f2e6b11","event_type":%q,"occurred_at":"2026-09-10T08:50:00Z","payload":%s}`,
		eventType, payload)
}

// TestEventHandler checks envelope decoding, dispatch and outcomes.
//
// Expected output: match.finalized and player.penalized reach their ingesters
// (the penalty with the envelope's event_id); other types are acknowledged
// untouched; malformed envelopes and invalid payloads are rejected; any other
// ingest error is retried.
func TestEventHandler(t *testing.T) {
	for _, tt := range []struct {
		name            string
		body            []byte
		matchErr        error
		penaltyErr      error
		want            consumer.Outcome
		matches, penals int
	}{
		{"match", event("match.finalized", `{"game_id":5001}`), nil, nil, consumer.Ack, 1, 0},
		{"penalty", event("player.penalized", `{"user_id":42}`), nil, nil, consumer.Ack, 0, 1},
		{"cancelled ignored", event("game.cancelled", `{"game_id":5001}`), nil, nil, consumer.Ack, 0, 0},
		{"unknown ignored", event("room.created", `{}`), nil, nil, consumer.Ack, 0, 0},
		{"invalid match", event("match.finalized", `{}`), fmt.Errorf("%w: winner", service.ErrInvalidMatch), nil, consumer.Reject, 1, 0},
		{"invalid penalty", event("player.penalized", `{}`), nil, fmt.Errorf("%w: points", service.ErrInvalidPenalty), consumer.Reject, 0, 1},
		{"database down", event("match.finalized", `{}`), fmt.Errorf("%w: test", service.ErrUnavailable), nil, consumer.Retry, 1, 0},
		{"unexpected error", event("player.penalized", `{}`), nil, errors.New("boom"), consumer.Retry, 0, 1},
		{"not json", []byte(`nope`), nil, nil, consumer.Reject, 0, 0},
		{"no event id", []byte(`{"event_type":"match.finalized","payload":{}}`), nil, nil, consumer.Reject, 0, 0},
		{"payload not object", event("match.finalized", `[1]`), nil, nil, consumer.Reject, 0, 0},
		{"payload missing", []byte(`{"event_id":"x","event_type":"match.finalized"}`), nil, nil, consumer.Reject, 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			matches := &ingestStub{err: tt.matchErr}
			penalties := &penaltyStub{ingestStub{err: tt.penaltyErr}}
			outcome, _, err := consumer.NewHandler(matches, penalties).Handle(context.Background(), tt.body)
			if outcome != tt.want || matches.calls != tt.matches || penalties.calls != tt.penals {
				t.Fatalf("outcome = %v (%v), calls = %d/%d", outcome, err, matches.calls, penalties.calls)
			}
			if (outcome == consumer.Ack) != (err == nil) {
				t.Fatalf("outcome %v with error %v", outcome, err)
			}
			if tt.penals == 1 && penalties.eventID != "8d3c0f8e-5b5e-4c39-9d43-0a7c1f2e6b11" {
				t.Fatalf("penalty event id = %q", penalties.eventID)
			}
		})
	}
}
