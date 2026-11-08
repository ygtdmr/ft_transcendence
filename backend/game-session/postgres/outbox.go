package postgres

import (
	"encoding/json"
	"time"

	"game-session/game"
	"game-session/room"

	"gorm.io/gorm"
)

type outboxRow struct {
	EventID       string `gorm:"primaryKey;default:gen_random_uuid()"`
	RoomID        int64
	EventType     string
	OutboxContent jsonb
	CreatedAt     time.Time `gorm:"autoCreateTime"`
	PublishedAt   *time.Time
	RoomVersion   int64
}

func (outboxRow) TableName() string { return "outbox_events" }

type finalizedPlayerEvent struct {
	UserID int64     `json:"user_id"`
	Team   game.Team `json:"team"`
	Role   game.Role `json:"role"`
}

type matchFinalizedEvent struct {
	GameID     int64                  `json:"game_id"`
	RoomID     int64                  `json:"room_id"`
	Winner     game.Team              `json:"winner"`
	Loser      game.Team              `json:"loser"`
	EndReason  game.EndReason         `json:"end_reason"`
	Score      game.Score             `json:"score"`
	StartedAt  time.Time              `json:"started_at"`
	FinishedAt time.Time              `json:"finished_at"`
	Players    []finalizedPlayerEvent `json:"players"`
}

func toMatchFinalizedEvent(lastGame *room.LastGameView) matchFinalizedEvent {
	event := matchFinalizedEvent{
		GameID:     lastGame.GameID,
		RoomID:     lastGame.RoomID,
		Winner:     lastGame.Winner,
		Loser:      lastGame.Loser,
		EndReason:  lastGame.EndReason,
		Score:      lastGame.Score,
		StartedAt:  lastGame.StartedAt,
		FinishedAt: lastGame.FinishedAt,
		Players:    make([]finalizedPlayerEvent, len(lastGame.Players)),
	}
	for i, p := range lastGame.Players {
		event.Players[i] = finalizedPlayerEvent{UserID: p.UserID, Team: p.Team, Role: p.Role}
	}
	return event
}

// INSERT INTO outbox_events (room_id, event_type, outbox_content, room_version) VALUES (1, 'match.finalized', '{"game_id": 7, "winner": "RED"}', 12);
func insertOutbox(tx *gorm.DB, r *room.Room, eventType string, payload any) error {
	content, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	row := outboxRow{RoomID: r.RoomID, EventType: eventType, OutboxContent: jsonb(content), RoomVersion: int64(r.RoomVersion)}
	return tx.Omit("event_id", "created_at").Create(&row).Error
}
