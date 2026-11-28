package game

import "time"

type HistoryEntryType string

const (
	HistoryGameStarted  HistoryEntryType = "GAME_STARTED"
	HistoryClueGiven    HistoryEntryType = "CLUE_GIVEN"
	HistoryCardRevealed HistoryEntryType = "CARD_REVEALED"
	HistoryTurnPassed   HistoryEntryType = "TURN_PASSED"
	HistoryTurnChanged  HistoryEntryType = "TURN_CHANGED"
	HistoryGamePaused   HistoryEntryType = "GAME_PAUSED"
	HistoryGameResumed  HistoryEntryType = "GAME_RESUMED"
	HistoryGameEnded    HistoryEntryType = "GAME_ENDED"
)

type HistoryEntry struct {
	Seq  int              `json:"seq"`
	Type HistoryEntryType `json:"type"`
	At   time.Time        `json:"at"`
	Team *Team            `json:"team"`

	UserID       *int64     `json:"user_id,omitempty"`
	Username     *string    `json:"username,omitempty"`
	Clue         *Clue      `json:"clue,omitempty"`
	CardID       *int       `json:"card_id,omitempty"`
	Word         *string    `json:"word,omitempty"`
	Color        *CardColor `json:"color,omitempty"`
	PreviousTeam *Team      `json:"previous_team,omitempty"`
	Reason       *string    `json:"reason,omitempty"`
}

func (g *Game) addHistory(entry HistoryEntry, now time.Time) {
	entry.Seq = len(g.history) + 1
	entry.At = now
	g.history = append(g.history, entry)
}

func (g *Game) cloneHistory() []HistoryEntry {
	history := make([]HistoryEntry, len(g.history))
	copy(history, g.history)
	return history
}
