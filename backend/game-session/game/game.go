package game

import (
	cryptorand "crypto/rand"
	"fmt"
	"math/rand/v2"
	"time"
)

type Game struct {
	gameID               int64
	board                *Board
	currentTurn          CurrentTurn
	winner               *Team
	endReason            *EndReason
	phaseBeforePause     *Phase
	turnTimer            *time.Duration
	remainingOnPause     *time.Duration
	startedAt            time.Time
	finishedAt           *time.Time
	history              []HistoryEntry
	staffingTimeout      time.Duration
	redMissing           []Role
	blueMissing          []Role
	redStaffingDeadline  *time.Time
	blueStaffingDeadline *time.Time
}

type GameSettings struct {
	Language        string
	TurnTimer       *time.Duration
	StaffingTimeout time.Duration
	GameID          int64
}

func newSecureSeed() ([32]byte, error) {
	var seed [32]byte
	_, err := cryptorand.Read(seed[:])
	if err != nil {
		return [32]byte{}, fmt.Errorf("%w: %v", ErrGenerateSeed, err)
	}
	return seed, nil
}

func validateSettings(settings GameSettings) error {
	if settings.TurnTimer != nil && *settings.TurnTimer <= 0 {
		return fmt.Errorf("%w: turn timer must be positive", ErrInvalidArgument)
	}
	if settings.StaffingTimeout <= 0 {
		return fmt.Errorf("%w: staffing timeout must be positive", ErrInvalidArgument)
	}
	if settings.GameID <= 0 {
		return fmt.Errorf("%w: game ID must be positive", ErrInvalidArgument)
	}
	return nil
}

func newGameFromBoard(board *Board, settings GameSettings, now time.Time) *Game {
	game := &Game{
		gameID: settings.GameID,
		board:  board,
		currentTurn: CurrentTurn{
			Team:  board.startingTeam,
			Phase: PhaseWaitingForClue,
		},
		turnTimer:       copyPtr(settings.TurnTimer),
		staffingTimeout: settings.StaffingTimeout,
		redMissing:      []Role{},
		blueMissing:     []Role{},
		startedAt:       now,
	}
	game.startTurnTimer(now)

	startingTeam := board.startingTeam
	game.addHistory(HistoryEntry{
		Type: HistoryGameStarted,
		Team: &startingTeam,
	}, now)
	return game
}

func NewGameWithSeed(wordPool []Word, settings GameSettings, seed [32]byte, now time.Time) (*Game, error) {
	if err := validateSettings(settings); err != nil {
		return nil, err
	}
	r := rand.New(rand.NewChaCha8(seed))
	board, err := NewBoard(wordPool, settings.Language, r)
	if err != nil {
		return nil, err
	}
	return newGameFromBoard(board, settings, now), nil
}

func NewGame(wordPool []Word, settings GameSettings, now time.Time) (*Game, error) {
	seed, err := newSecureSeed()
	if err != nil {
		return nil, err
	}

	return NewGameWithSeed(wordPool, settings, seed, now)
}

func (g *Game) cloneGameResult() (*Team, *EndReason, *time.Time) {
	return copyPtr(g.winner), copyPtr(g.endReason), copyPtr(g.finishedAt)
}

func (g *Game) Score() Score {
	redScore := 0
	blueScore := 0

	for _, card := range g.board.cards {
		if !card.revealed {
			continue
		}
		switch card.color {
		case CardColorRed:
			redScore++
		case CardColorBlue:
			blueScore++
		}
	}

	return Score{
		Red:  redScore,
		Blue: blueScore,
	}
}

func (g *Game) GiveClue(player *Player, clue *Clue, now time.Time) (CurrentTurn, error) {
	if err := validateTurnAction(g, player, PhaseWaitingForClue, RoleSpymaster, now); err != nil {
		return CurrentTurn{}, err
	}
	cleaned, err := validateClue(g.board, clue)
	if err != nil {
		return CurrentTurn{}, err
	}

	guesses := clue.Number + 1
	g.currentTurn.Clue = &cleaned
	g.currentTurn.Phase = PhaseGuessing
	g.currentTurn.GuessesRemaining = &guesses

	g.addHistory(HistoryEntry{
		Type:     HistoryClueGiven,
		Team:     copyPtr(&player.Team),
		UserID:   copyPtr(&player.UserID),
		Username: copyPtr(&player.Username),
		Clue:     copyPtr(&cleaned),
	}, now)

	return g.currentTurn.clone(), nil
}

func (g *Game) finish(winner *Team, reason EndReason, now time.Time) {
	g.currentTurn.Phase = PhaseGameOver
	g.currentTurn.Clue = nil
	g.currentTurn.GuessesRemaining = nil
	g.currentTurn.DeadlineAt = nil
	g.winner = copyPtr(winner)
	g.endReason = &reason
	g.finishedAt = &now

	reasonText := string(reason)
	g.addHistory(HistoryEntry{
		Type:   HistoryGameEnded,
		Team:   copyPtr(winner),
		Reason: &reasonText,
	}, now)
}

func (g *Game) GameID() int64 {
	return g.gameID
}

func (g *Game) Status() GameStatus {
	if g.currentTurn.Phase == PhaseGameOver {
		if *g.endReason == EndReasonInsufficientPlayers {
			return GameStatusCancelled
		}
		return GameStatusFinished
	}
	return GameStatusInProgress
}
