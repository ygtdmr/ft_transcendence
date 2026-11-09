package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"
)

// ErrInvalidMatch reports a finalized match that can never be stored. A consumer
// must not retry it.
var ErrInvalidMatch = errors.New("invalid finalized match")

// ErrUnavailable reports that the database could not be reached.
var ErrUnavailable = errors.New("service dependency unavailable")

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{3,20}$`)

// endReasons lists the terminal causes stored in match history. A cancelled game
// is not a finalized match, so its reason is absent.
var endReasons = map[string]bool{
	"ALL_TEAM_CARDS_REVEALED": true,
	"ASSASSIN_REVEALED":       true,
	"PLAYER_FORFEIT":          true,
}

// FinalizedPlayer is one participant in the final roster. Username is the name
// recorded with the match.
type FinalizedPlayer struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	Team     string `json:"team"`
	Role     string `json:"role"`
}

// MatchScore is the final score.
type MatchScore struct {
	Red  int `json:"red"`
	Blue int `json:"blue"`
}

// MatchFinalized is the payload of Game Session's match.finalized event.
type MatchFinalized struct {
	GameID     int64             `json:"game_id"`
	RoomID     int64             `json:"room_id"`
	Winner     string            `json:"winner"`
	Loser      string            `json:"loser"`
	EndReason  string            `json:"end_reason"`
	Score      *MatchScore       `json:"score"`
	StartedAt  time.Time         `json:"started_at"`
	FinishedAt time.Time         `json:"finished_at"`
	Players    []FinalizedPlayer `json:"players"`
}

// Validate checks that the event describes one complete finished match.
func (m MatchFinalized) Validate() error {
	switch {
	case m.GameID <= 0:
		return invalidMatch("game_id")
	case m.RoomID <= 0:
		return invalidMatch("room_id")
	case !validTeam(m.Winner) || !validTeam(m.Loser) || m.Winner == m.Loser:
		return invalidMatch("winner or loser")
	case !endReasons[m.EndReason]:
		return invalidMatch("end_reason")
	case m.Score == nil || m.Score.Red < 0 || m.Score.Blue < 0:
		return invalidMatch("score")
	case m.StartedAt.IsZero() || m.FinishedAt.IsZero() || m.StartedAt.After(m.FinishedAt):
		return invalidMatch("timestamps")
	}

	return m.validatePlayers()
}

func (m MatchFinalized) validatePlayers() error {
	seen := make(map[int64]bool, len(m.Players))
	teams := map[string]bool{}
	for _, player := range m.Players {
		if player.UserID <= 0 || seen[player.UserID] {
			return invalidMatch("players user_id")
		}
		if !usernamePattern.MatchString(player.Username) {
			return invalidMatch("players username")
		}
		if !validTeam(player.Team) || (player.Role != "SPYMASTER" && player.Role != "OPERATIVE") {
			return invalidMatch("players team or role")
		}
		seen[player.UserID] = true
		teams[player.Team] = true
	}
	if !teams["RED"] || !teams["BLUE"] {
		return invalidMatch("players roster")
	}

	return nil
}

func validTeam(team string) bool {
	return team == "RED" || team == "BLUE"
}

func invalidMatch(field string) error {
	return fmt.Errorf("%w: %s", ErrInvalidMatch, field)
}

// MatchStore records finalized matches.
type MatchStore interface {
	// RecordFinalizedMatch stores the match and updates every participant's
	// counters in one transaction. It returns false when the game_id is already
	// recorded, and changes nothing in that case.
	RecordFinalizedMatch(ctx context.Context, match MatchFinalized) (bool, error)
}

// MatchIngest applies match.finalized events.
type MatchIngest struct {
	store MatchStore
}

// NewMatchIngest returns the match.finalized handler.
func NewMatchIngest(store MatchStore) *MatchIngest {
	return &MatchIngest{store: store}
}

// Ingest validates one event payload and records it at most once per game_id.
// It returns false for a duplicate, which a consumer acknowledges as done.
//
// Errors:
//
//   - ErrInvalidMatch: the payload can never be stored.
//   - ErrUnavailable or other database errors: nothing was stored; retry.
func (s *MatchIngest) Ingest(ctx context.Context, payload []byte) (bool, error) {
	match, err := decodeMatchFinalized(payload)
	if err != nil {
		return false, err
	}
	if err := match.Validate(); err != nil {
		return false, err
	}

	return s.store.RecordFinalizedMatch(ctx, match)
}

func decodeMatchFinalized(payload []byte) (MatchFinalized, error) {
	var match MatchFinalized
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&match); err != nil {
		return MatchFinalized{}, invalidMatch("payload")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return MatchFinalized{}, invalidMatch("payload")
	}

	return match, nil
}
