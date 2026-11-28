package game

import (
	"time"
)

type CardRecord struct {
	CardID   int
	Word     string
	Color    CardColor
	Revealed bool
}

type GameRecord struct {
	GameID       int64
	StartingTeam Team
	CurrentTurn  CurrentTurn
	Timing       TimingRecord
	Staffing     StaffingRecord
	Result       ResultRecord
	StartedAt    time.Time
	Cards        []CardRecord
	History      []HistoryEntry
}

type TimingRecord struct {
	TurnTimerSeconds   *int
	PhaseBeforePause   *Phase
	RemainingOnPauseMs *int64
}

type StaffingRecord struct {
	StaffingTimeoutSeconds int
	Red                    TeamStaffing
	Blue                   TeamStaffing
}

type ResultRecord struct {
	Winner     *Team
	EndReason  *EndReason
	FinishedAt *time.Time
}

func (b *Board) toCardRecords() []CardRecord {
	cards := make([]CardRecord, len(b.cards))
	for i, card := range b.cards {
		cards[i] = CardRecord{
			CardID:   card.cardID,
			Word:     card.word,
			Color:    card.color,
			Revealed: card.revealed,
		}
	}
	return cards
}

func (g *Game) ToRecord() GameRecord {
	return GameRecord{
		GameID:       g.gameID,
		StartingTeam: g.board.startingTeam,
		CurrentTurn:  g.currentTurn.clone(),
		Timing:       g.toTimingRecord(),
		Staffing:     g.toStaffingRecord(),
		Result: ResultRecord{
			Winner:     copyPtr(g.winner),
			EndReason:  copyPtr(g.endReason),
			FinishedAt: copyPtr(g.finishedAt),
		},
		StartedAt: g.startedAt,
		Cards:     g.board.toCardRecords(),
		History:   g.cloneHistory(),
	}
}

func (g *Game) toTimingRecord() TimingRecord {
	var turnTimerSeconds *int
	if g.turnTimer != nil {
		seconds := int(g.turnTimer.Seconds())
		turnTimerSeconds = &seconds
	}

	var remainingOnPauseMs *int64
	if g.remainingOnPause != nil {
		millis := g.remainingOnPause.Milliseconds()
		remainingOnPauseMs = &millis
	}

	return TimingRecord{
		TurnTimerSeconds:   turnTimerSeconds,
		PhaseBeforePause:   copyPtr(g.phaseBeforePause),
		RemainingOnPauseMs: remainingOnPauseMs,
	}
}

func (g *Game) toStaffingRecord() StaffingRecord {
	view := g.staffingView()
	return StaffingRecord{
		StaffingTimeoutSeconds: int(g.staffingTimeout.Seconds()),
		Red:                    view.Red,
		Blue:                   view.Blue,
	}
}

func boardFromRecords(startingTeam Team, cards []CardRecord) (*Board, error) {
	if len(cards) != BoardSize {
		return nil, ErrInvalidBoard
	}
	board := &Board{startingTeam: startingTeam}
	seen := [BoardSize]bool{}
	for _, record := range cards {
		if record.CardID < 1 || record.CardID > BoardSize ||
			seen[record.CardID-1] {
			return nil, ErrInvalidBoard
		}
		seen[record.CardID-1] = true
		board.cards[record.CardID-1] = card{
			cardID:   record.CardID,
			word:     record.Word,
			color:    record.Color,
			revealed: record.Revealed,
		}
	}
	return board, nil
}

func fromTimingRecord(record TimingRecord) (*time.Duration, *time.Duration) {
	var turnTimer *time.Duration
	if record.TurnTimerSeconds != nil {
		timer := time.Duration(*record.TurnTimerSeconds) * time.Second
		turnTimer = &timer
	}

	var remainingOnPause *time.Duration
	if record.RemainingOnPauseMs != nil {
		remaining := time.Duration(*record.RemainingOnPauseMs) * time.Millisecond
		remainingOnPause = &remaining
	}

	return turnTimer, remainingOnPause
}

func FromRecord(record GameRecord) (*Game, error) {
	board, err := boardFromRecords(record.StartingTeam, record.Cards)
	if err != nil {
		return nil, err
	}

	turnTimer, remainingOnPause := fromTimingRecord(record.Timing)

	game := &Game{
		gameID:               record.GameID,
		board:                board,
		currentTurn:          record.CurrentTurn.clone(),
		winner:               copyPtr(record.Result.Winner),
		endReason:            copyPtr(record.Result.EndReason),
		finishedAt:           copyPtr(record.Result.FinishedAt),
		phaseBeforePause:     copyPtr(record.Timing.PhaseBeforePause),
		turnTimer:            turnTimer,
		remainingOnPause:     remainingOnPause,
		startedAt:            record.StartedAt,
		history:              append([]HistoryEntry{}, record.History...),
		staffingTimeout:      time.Duration(record.Staffing.StaffingTimeoutSeconds) * time.Second,
		redMissing:           append([]Role{}, record.Staffing.Red.MissingRoles...),
		blueMissing:          append([]Role{}, record.Staffing.Blue.MissingRoles...),
		redStaffingDeadline:  copyPtr(record.Staffing.Red.DeadlineAt),
		blueStaffingDeadline: copyPtr(record.Staffing.Blue.DeadlineAt),
	}
	if err := game.ValidateGameState(); err != nil {
		return nil, err
	}
	return game, nil
}
