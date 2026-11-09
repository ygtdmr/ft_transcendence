package unit_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/service"
)

type matchStoreStub struct {
	last   service.MatchFinalized
	result bool
	err    error
	calls  int
}

func (s *matchStoreStub) RecordFinalizedMatch(_ context.Context, match service.MatchFinalized) (bool, error) {
	s.calls++
	s.last = match
	return s.result, s.err
}

func validMatch() map[string]any {
	return map[string]any{
		"game_id": 5001, "room_id": 1001, "winner": "RED", "loser": "BLUE",
		"end_reason": "ALL_TEAM_CARDS_REVEALED", "score": map[string]any{"red": 9, "blue": 5},
		"started_at": "2026-09-10T08:30:00Z", "finished_at": "2026-09-10T08:50:00Z",
		"players": []any{
			map[string]any{"user_id": 42, "username": "player_one", "team": "RED", "role": "SPYMASTER"},
			map[string]any{"user_id": 44, "username": "blue_master", "team": "BLUE", "role": "SPYMASTER"},
		},
	}
}

func matchPayload(t *testing.T, change func(map[string]any)) []byte {
	t.Helper()
	match := validMatch()
	if change != nil {
		change(match)
	}
	raw, err := json.Marshal(match)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func setPlayer(match map[string]any, index int, key string, value any) {
	match["players"].([]any)[index].(map[string]any)[key] = value
}

// TestIngestAcceptsFinalizedMatches checks valid events.
//
// Expected output: each is passed to the store and its result is returned,
// including a zero score, the forfeit reason and unknown extra fields.
func TestIngestAcceptsFinalizedMatches(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"normal":      nil,
		"assassin":    func(m map[string]any) { m["end_reason"] = "ASSASSIN_REVEALED" },
		"forfeit":     func(m map[string]any) { m["end_reason"] = "PLAYER_FORFEIT" },
		"zero score":  func(m map[string]any) { m["score"] = map[string]any{"red": 0, "blue": 0} },
		"same time":   func(m map[string]any) { m["finished_at"] = m["started_at"] },
		"extra field": func(m map[string]any) { m["board"] = "ignored" },
		"blue wins":   func(m map[string]any) { m["winner"], m["loser"] = "BLUE", "RED" },
	} {
		t.Run(name, func(t *testing.T) {
			store := &matchStoreStub{result: true}
			recorded, err := service.NewMatchIngest(store).Ingest(context.Background(), matchPayload(t, change))
			if err != nil || !recorded || store.calls != 1 {
				t.Fatalf("Ingest = %v, %v, calls %d", recorded, err, store.calls)
			}
		})
	}

	store := &matchStoreStub{}
	recorded, err := service.NewMatchIngest(store).Ingest(context.Background(), matchPayload(t, nil))
	if err != nil || recorded {
		t.Fatalf("duplicate = %v, %v", recorded, err)
	}
	if store.last.GameID != 5001 || store.last.Players[0].Username != "player_one" || store.last.Score.Red != 9 {
		t.Fatalf("stored = %+v", store.last)
	}
}

// TestIngestRefusesInvalidMatches checks every kind of bad event.
//
// Expected output: each returns ErrInvalidMatch and never reaches the store.
func TestIngestRefusesInvalidMatches(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"no game id":          func(m map[string]any) { delete(m, "game_id") },
		"zero game id":        func(m map[string]any) { m["game_id"] = 0 },
		"text game id":        func(m map[string]any) { m["game_id"] = "5001" },
		"fraction game id":    func(m map[string]any) { m["game_id"] = 1.5 },
		"no room id":          func(m map[string]any) { delete(m, "room_id") },
		"no winner":           func(m map[string]any) { delete(m, "winner") },
		"bad winner":          func(m map[string]any) { m["winner"] = "GREEN" },
		"same teams":          func(m map[string]any) { m["loser"] = "RED" },
		"cancelled":           func(m map[string]any) { m["end_reason"] = "INSUFFICIENT_PLAYERS" },
		"no end reason":       func(m map[string]any) { delete(m, "end_reason") },
		"no score":            func(m map[string]any) { delete(m, "score") },
		"negative score":      func(m map[string]any) { m["score"] = map[string]any{"red": -1, "blue": 5} },
		"no start":            func(m map[string]any) { delete(m, "started_at") },
		"no finish":           func(m map[string]any) { delete(m, "finished_at") },
		"finish before start": func(m map[string]any) { m["finished_at"] = "2026-09-10T08:00:00Z" },
		"bad time":            func(m map[string]any) { m["started_at"] = "yesterday" },
		"no players":          func(m map[string]any) { delete(m, "players") },
		"one team only":       func(m map[string]any) { setPlayer(m, 1, "team", "RED") },
		"missing username":    func(m map[string]any) { setPlayer(m, 0, "username", "") },
		"bad username":        func(m map[string]any) { setPlayer(m, 0, "username", "a b") },
		"long username":       func(m map[string]any) { setPlayer(m, 0, "username", strings.Repeat("a", 21)) },
		"zero user":           func(m map[string]any) { setPlayer(m, 0, "user_id", 0) },
		"repeated user":       func(m map[string]any) { setPlayer(m, 1, "user_id", 42) },
		"bad role":            func(m map[string]any) { setPlayer(m, 0, "role", "CAPTAIN") },
		"bad player team":     func(m map[string]any) { setPlayer(m, 0, "team", "GREEN") },
	} {
		t.Run(name, func(t *testing.T) {
			store := &matchStoreStub{}
			recorded, err := service.NewMatchIngest(store).Ingest(context.Background(), matchPayload(t, change))
			if !errors.Is(err, service.ErrInvalidMatch) || recorded || store.calls != 0 {
				t.Fatalf("Ingest = %v, %v, calls %d", recorded, err, store.calls)
			}
		})
	}

	for name, raw := range map[string]string{"empty": "", "not json": "{", "two objects": "{}{}", "array": "[]"} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.NewMatchIngest(&matchStoreStub{}).Ingest(context.Background(), []byte(raw)); !errors.Is(err, service.ErrInvalidMatch) {
				t.Fatalf("Ingest = %v", err)
			}
		})
	}
}

// TestIngestReportsStoreFailure checks that an outage can be retried.
//
// Expected output: the store error is returned and is not ErrInvalidMatch.
func TestIngestReportsStoreFailure(t *testing.T) {
	store := &matchStoreStub{err: fmt.Errorf("%w: test", service.ErrUnavailable)}
	_, err := service.NewMatchIngest(store).Ingest(context.Background(), matchPayload(t, nil))
	if !errors.Is(err, service.ErrUnavailable) || errors.Is(err, service.ErrInvalidMatch) {
		t.Fatalf("Ingest = %v", err)
	}
}
