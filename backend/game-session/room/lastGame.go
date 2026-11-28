package room

import (
	"game-session/game"
	"time"
)

type FinalizedPlayer struct {
	UserID int64
	Team   game.Team
	Role   game.Role
}

type LastGameView struct {
	GameID     int64           `json:"game_id"`
	Status     game.GameStatus `json:"status"`
	Winner     game.Team       `json:"winner"`
	Loser      game.Team       `json:"loser"`
	EndReason  game.EndReason  `json:"end_reason"`
	Score      game.Score      `json:"score"`
	FinishedAt time.Time       `json:"finished_at"`

	RoomID    int64             `json:"-"`
	StartedAt time.Time         `json:"-"`
	Players   []FinalizedPlayer `json:"-"`
}

func newLastGame(roomID int64, snapshot game.GameSnapshot, finishedAt time.Time, members []RoomMember) *LastGameView {
	loser := game.TeamRed
	if *snapshot.Winner == game.TeamRed {
		loser = game.TeamBlue
	}
	players := make([]FinalizedPlayer, 0, len(members))
	for _, member := range members {
		if member.Team == nil || member.Role == nil {
			continue
		}
		players = append(players, FinalizedPlayer{
			UserID: member.UserID,
			Team:   *member.Team,
			Role:   *member.Role,
		})
	}
	return &LastGameView{
		GameID:     snapshot.GameID,
		Status:     game.GameStatusFinished,
		Winner:     *snapshot.Winner,
		Loser:      loser,
		EndReason:  *snapshot.EndReason,
		Score:      snapshot.Score,
		FinishedAt: finishedAt,
		RoomID:     roomID,
		StartedAt:  snapshot.StartedAt,
		Players:    players,
	}
}

func prepareLastGameView(roomID int64, currentGame *game.Game, members []RoomMember, now time.Time) (*LastGameView, error) {
	snapshot, err := currentGame.SnapshotForRole(nil)
	if err != nil {
		return nil, err
	}
	if snapshot.Winner == nil || snapshot.EndReason == nil {
		return nil, game.ErrInvalidGameState
	}
	finishedAt := now
	if snapshot.FinishedAt != nil {
		finishedAt = *snapshot.FinishedAt
	}
	return newLastGame(roomID, snapshot, finishedAt, members), nil
}
