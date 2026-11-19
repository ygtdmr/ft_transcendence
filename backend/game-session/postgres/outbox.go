package postgres

import (
	"encoding/json"
	"time"

	"game-session/game"
	"game-session/room"

	"gorm.io/gorm"
)

type outboxRow struct {
	EventID         string `gorm:"primaryKey;default:gen_random_uuid()"`
	RoomID          int64
	EventType       string
	OutboxContent   jsonb
	CreatedAt       time.Time `gorm:"autoCreateTime"`
	PublishedAt     *time.Time
	RoomVersion     int64
	RecipientUserID *int64
	EventSequence   *int64
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

// INSERT INTO outbox_events (room_id, event_type, outbox_content, room_version, recipient_user_id, event_sequence) VALUES (1, 'room.player.left', '{...}', 12, 43, 7);
func (s *Store) SaveRecipientEvent(r *room.Room, recipientUserID int64, sequence int64, eventType string, payload any) error {
	content, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	row := outboxRow{
		RoomID:          r.RoomID,
		EventType:       eventType,
		OutboxContent:   jsonb(content),
		RoomVersion:     int64(r.RoomVersion),
		RecipientUserID: &recipientUserID,
		EventSequence:   &sequence,
	}
	return s.db.Omit("event_id", "created_at").Create(&row).Error
}

// UPDATE rooms SET event_sequence = event_sequence + 1 WHERE room_id = 1 RETURNING event_sequence;
// The increment runs inside Postgres, so concurrent callers never get the same number; it survives restarts.
func (s *Store) NextEventSequence(roomID int64) (int64, error) {
	var sequence int64
	err := s.db.Raw("UPDATE rooms SET event_sequence = event_sequence + 1 WHERE room_id = ? RETURNING event_sequence", roomID).Scan(&sequence).Error
	return sequence, err
}

type OutboxEvent struct {
	RecipientUserID int64
	Sequence        int64
	EventType       string
	Payload         json.RawMessage
}

// SELECT recipient_user_id, event_sequence, event_type, outbox_content FROM outbox_events WHERE room_id = 1 AND recipient_user_id IS NOT NULL AND published_at IS NULL ORDER BY event_sequence;
func (s *Store) PendingRecipientEvents(roomID int64) ([]OutboxEvent, error) {
	var rows []outboxRow
	err := s.db.Where("room_id = ? AND recipient_user_id IS NOT NULL AND published_at IS NULL", roomID).Order("event_sequence").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	events := make([]OutboxEvent, 0, len(rows))
	for _, row := range rows {
		events = append(events, OutboxEvent{
			RecipientUserID: *row.RecipientUserID,
			Sequence:        *row.EventSequence,
			EventType:       row.EventType,
			Payload:         json.RawMessage(row.OutboxContent),
		})
	}
	return events, nil
}
