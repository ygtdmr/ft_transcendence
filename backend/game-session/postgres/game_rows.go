package postgres

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"
	"time"

	"game-session/game"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type roleList []game.Role

func (l roleList) literal() string {
	parts := make([]string, len(l))
	for i, r := range l {
		parts[i] = string(r)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func (roleList) GormDataType() string { return "text[]" }

func (l roleList) Value() (driver.Value, error) { return l.literal(), nil }

func (l roleList) GormValue(_ context.Context, _ *gorm.DB) clause.Expr {
	return clause.Expr{SQL: "?::text[]", Vars: []any{l.literal()}}
}

func (l *roleList) Scan(src any) error {
	var text string
	switch v := src.(type) {
	case nil:
		*l = roleList{}
		return nil
	case string:
		text = v
	case []byte:
		text = string(v)
	default:
		return fmt.Errorf("roleList: unsupported type %T", src)
	}
	*l = roleList{}
	text = strings.Trim(text, "{}")
	if text == "" {
		return nil
	}
	for _, part := range strings.Split(text, ",") {
		*l = append(*l, game.Role(strings.Trim(part, `"`)))
	}
	return nil
}

type jsonb []byte

func (j jsonb) GormValue(_ context.Context, _ *gorm.DB) clause.Expr {
	return clause.Expr{SQL: "?::jsonb", Vars: []any{string(j)}}
}

func (j *jsonb) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*j = nil
	case string:
		*j = jsonb(v)
	case []byte:
		*j = append(jsonb{}, v...)
	default:
		return fmt.Errorf("jsonb: unsupported type %T", src)
	}
	return nil
}

var _ driver.Valuer = jsonb(nil)

func (j jsonb) Value() (driver.Value, error) { return string(j), nil }

type gameRow struct {
	GameID                 int64 `gorm:"primaryKey"`
	RoomID                 int64
	StartingTeam           game.Team
	Phase                  game.Phase
	CurrentTeam            game.Team
	ClueWord               *string
	ClueNumber             *int
	GuessesRemaining       *int
	TurnDeadlineAt         *time.Time
	TurnTimerSeconds       *int
	PhaseBeforePause       *game.Phase
	RemainingOnPauseMs     *int64
	StaffingTimeoutSeconds int
	RedMissingRoles        roleList
	BlueMissingRoles       roleList
	RedStaffingDeadlineAt  *time.Time
	BlueStaffingDeadlineAt *time.Time
	Winner                 *game.Team
	EndReason              *game.EndReason
	StartedAt              time.Time
	FinishedAt             *time.Time
}

func (gameRow) TableName() string { return "games" }

var gameUpdateColumns = []string{
	"phase", "current_team", "clue_word", "clue_number", "guesses_remaining", "turn_deadline_at",
	"turn_timer_seconds", "phase_before_pause", "remaining_on_pause_ms",
	"red_missing_roles", "blue_missing_roles", "red_staffing_deadline_at", "blue_staffing_deadline_at",
	"winner", "end_reason", "finished_at",
}

type gameCardRow struct {
	GameID   int64 `gorm:"primaryKey"`
	CardID   int   `gorm:"primaryKey"`
	Word     string
	Color    game.CardColor
	Revealed bool
}

func (gameCardRow) TableName() string { return "game_cards" }

type gameHistoryRow struct {
	GameID       int64 `gorm:"primaryKey"`
	Seq          int   `gorm:"primaryKey"`
	Type         game.HistoryEntryType
	At           time.Time
	Team         *game.Team
	UserID       *int64
	Username     *string
	ClueWord     *string
	ClueNumber   *int
	CardID       *int
	Word         *string
	Color        *game.CardColor
	PreviousTeam *game.Team
	Reason       *string
}

func (gameHistoryRow) TableName() string { return "game_history" }

type gamePlayerRow struct {
	GameID int64 `gorm:"primaryKey"`
	UserID int64 `gorm:"primaryKey"`
	Team   game.Team
	Role   game.Role
}

func (gamePlayerRow) TableName() string { return "game_players" }

func toGameRow(roomID int64, rec game.GameRecord) gameRow {
	row := gameRow{
		GameID:                 rec.GameID,
		RoomID:                 roomID,
		StartingTeam:           rec.StartingTeam,
		Phase:                  rec.CurrentTurn.Phase,
		CurrentTeam:            rec.CurrentTurn.Team,
		GuessesRemaining:       rec.CurrentTurn.GuessesRemaining,
		TurnDeadlineAt:         rec.CurrentTurn.DeadlineAt,
		TurnTimerSeconds:       rec.Timing.TurnTimerSeconds,
		PhaseBeforePause:       rec.Timing.PhaseBeforePause,
		RemainingOnPauseMs:     rec.Timing.RemainingOnPauseMs,
		StaffingTimeoutSeconds: rec.Staffing.StaffingTimeoutSeconds,
		RedMissingRoles:        roleList(rec.Staffing.Red.MissingRoles),
		BlueMissingRoles:       roleList(rec.Staffing.Blue.MissingRoles),
		RedStaffingDeadlineAt:  rec.Staffing.Red.DeadlineAt,
		BlueStaffingDeadlineAt: rec.Staffing.Blue.DeadlineAt,
		Winner:                 rec.Result.Winner,
		EndReason:              rec.Result.EndReason,
		StartedAt:              rec.StartedAt,
		FinishedAt:             rec.Result.FinishedAt,
	}
	if rec.CurrentTurn.Clue != nil {
		word, number := rec.CurrentTurn.Clue.Word, rec.CurrentTurn.Clue.Number
		row.ClueWord, row.ClueNumber = &word, &number
	}
	return row
}

func toCardRows(gameID int64, cards []game.CardRecord) []gameCardRow {
	rows := make([]gameCardRow, len(cards))
	for i, c := range cards {
		rows[i] = gameCardRow{GameID: gameID, CardID: c.CardID, Word: c.Word, Color: c.Color, Revealed: c.Revealed}
	}
	return rows
}

func toHistoryRows(gameID int64, history []game.HistoryEntry) []gameHistoryRow {
	rows := make([]gameHistoryRow, len(history))
	for i, h := range history {
		row := gameHistoryRow{
			GameID: gameID, Seq: h.Seq, Type: h.Type, At: h.At, Team: h.Team,
			UserID: h.UserID, Username: h.Username, CardID: h.CardID, Word: h.Word,
			Color: h.Color, PreviousTeam: h.PreviousTeam, Reason: h.Reason,
		}
		if h.Clue != nil {
			word, number := h.Clue.Word, h.Clue.Number
			row.ClueWord, row.ClueNumber = &word, &number
		}
		rows[i] = row
	}
	return rows
}

func fromGameRows(row gameRow, cards []gameCardRow, history []gameHistoryRow) game.GameRecord {
	rec := game.GameRecord{
		GameID:       row.GameID,
		StartingTeam: row.StartingTeam,
		CurrentTurn: game.CurrentTurn{
			Team:             row.CurrentTeam,
			Phase:            row.Phase,
			GuessesRemaining: row.GuessesRemaining,
			DeadlineAt:       row.TurnDeadlineAt,
		},
		Timing: game.TimingRecord{
			TurnTimerSeconds:   row.TurnTimerSeconds,
			PhaseBeforePause:   row.PhaseBeforePause,
			RemainingOnPauseMs: row.RemainingOnPauseMs,
		},
		Staffing: game.StaffingRecord{
			StaffingTimeoutSeconds: row.StaffingTimeoutSeconds,
			Red:                    game.TeamStaffing{MissingRoles: []game.Role(row.RedMissingRoles), DeadlineAt: row.RedStaffingDeadlineAt},
			Blue:                   game.TeamStaffing{MissingRoles: []game.Role(row.BlueMissingRoles), DeadlineAt: row.BlueStaffingDeadlineAt},
		},
		Result:    game.ResultRecord{Winner: row.Winner, EndReason: row.EndReason, FinishedAt: row.FinishedAt},
		StartedAt: row.StartedAt,
		Cards:     make([]game.CardRecord, len(cards)),
		History:   make([]game.HistoryEntry, len(history)),
	}
	if row.ClueWord != nil && row.ClueNumber != nil {
		rec.CurrentTurn.Clue = &game.Clue{Word: *row.ClueWord, Number: *row.ClueNumber}
	}
	for i, c := range cards {
		rec.Cards[i] = game.CardRecord{CardID: c.CardID, Word: c.Word, Color: c.Color, Revealed: c.Revealed}
	}
	for i, h := range history {
		entry := game.HistoryEntry{
			Seq: h.Seq, Type: h.Type, At: h.At, Team: h.Team, UserID: h.UserID, Username: h.Username,
			CardID: h.CardID, Word: h.Word, Color: h.Color, PreviousTeam: h.PreviousTeam, Reason: h.Reason,
		}
		if h.ClueWord != nil && h.ClueNumber != nil {
			entry.Clue = &game.Clue{Word: *h.ClueWord, Number: *h.ClueNumber}
		}
		rec.History[i] = entry
	}
	return rec
}
