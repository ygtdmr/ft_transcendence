package service

import (
	"context"
	"time"
)

const winsPerLevel = 4

// Level returns the progression level for a win count.
func Level(wins int) int {
	return wins/winsPerLevel + 1
}

// UserStats holds one user's counters. A user with no records has zero counters.
type UserStats struct {
	UserID        int64
	Level         int
	Wins          int
	Losses        int
	MatchesPlayed int
	PenaltyPoints int
}

// Opponent is one player of the opposing team, under the name recorded with the match.
type Opponent struct {
	UserID   int64
	Username string
}

// MatchSummary is one finished match from one user's side.
type MatchSummary struct {
	GameID     int64
	RoomID     int64
	Team       string
	Role       string
	Opponents  []Opponent
	Result     string
	EndReason  string
	ScoreRed   int
	ScoreBlue  int
	FinishedAt time.Time
}

// StatsStore reads counters and match history.
type StatsStore interface {
	// Counters returns wins, losses and penalty points, all zero for an unknown user.
	Counters(ctx context.Context, userID int64) (wins, losses, penaltyPoints int, err error)
	// MatchHistory returns up to limit matches finished before the optional
	// time, newest first.
	MatchHistory(ctx context.Context, userID int64, limit int, before *time.Time) ([]MatchSummary, error)
}

// StatsReads answers counter and match history reads.
type StatsReads struct {
	store StatsStore
}

// NewStatsReads returns the read service.
func NewStatsReads(store StatsStore) *StatsReads {
	return &StatsReads{store: store}
}

// UserStats returns one user's counters and derived level.
//
// Errors:
//
//   - ErrUnavailable or other database errors; never reported as zero counters.
func (s *StatsReads) UserStats(ctx context.Context, userID int64) (UserStats, error) {
	wins, losses, penalties, err := s.store.Counters(ctx, userID)
	if err != nil {
		return UserStats{}, err
	}

	return UserStats{UserID: userID, Level: Level(wins), Wins: wins, Losses: losses,
		MatchesPlayed: wins + losses, PenaltyPoints: penalties}, nil
}

// MatchHistory returns one user's matches, newest first.
func (s *StatsReads) MatchHistory(ctx context.Context, userID int64, limit int, before *time.Time) ([]MatchSummary, error) {
	return s.store.MatchHistory(ctx, userID, limit, before)
}
