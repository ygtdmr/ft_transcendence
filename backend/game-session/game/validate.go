package game

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

func (g *Game) hasResult() bool {
	return g.winner != nil || g.endReason != nil || g.finishedAt != nil
}

func (g *Game) validateRunningTimer() error {
	if g.remainingOnPause != nil {
		return fmt.Errorf("%w: remaining time is kept only while paused", ErrInvalidGameState)
	}
	if (g.turnTimer == nil) != (g.currentTurn.DeadlineAt == nil) {
		return fmt.Errorf("%w: deadline must exist exactly when the turn timer is set", ErrInvalidGameState)
	}
	return nil
}

func (g *Game) validateWaitingForClue() error {
	if g.currentTurn.Clue != nil || g.currentTurn.GuessesRemaining != nil {
		return fmt.Errorf("%w: clue and guesses must be nil while waiting for clue", ErrInvalidGameState)
	}
	if g.hasResult() {
		return fmt.Errorf("%w: running game cannot have a result", ErrInvalidGameState)
	}
	return g.validateRunningTimer()
}

func (g *Game) validateGuessing() error {
	if g.currentTurn.Clue == nil || g.currentTurn.GuessesRemaining == nil {
		return fmt.Errorf("%w: clue and guesses are required while guessing", ErrInvalidGameState)
	}
	if *g.currentTurn.GuessesRemaining <= 0 {
		return fmt.Errorf("%w: no guesses remaining", ErrNoGuessesRemaining)
	}
	if g.hasResult() {
		return fmt.Errorf("%w: running game cannot have a result", ErrInvalidGameState)
	}
	return g.validateRunningTimer()
}

func (g *Game) validatePaused() error {
	if g.phaseBeforePause == nil {
		return fmt.Errorf("%w: phase before pause is missing", ErrInvalidGameState)
	}
	if *g.phaseBeforePause != PhaseWaitingForClue && *g.phaseBeforePause != PhaseGuessing {
		return fmt.Errorf("%w: invalid phase before pause %q", ErrInvalidGameState, *g.phaseBeforePause)
	}
	if g.hasResult() {
		return fmt.Errorf("%w: paused game cannot have a result", ErrInvalidGameState)
	}
	if g.currentTurn.DeadlineAt != nil {
		return fmt.Errorf("%w: deadline must be nil while paused", ErrInvalidGameState)
	}
	if (g.turnTimer == nil) != (g.remainingOnPause == nil) {
		return fmt.Errorf("%w: remaining time must exist exactly when the turn timer is set", ErrInvalidGameState)
	}
	return nil
}

func (g *Game) validateGameOver() error {
	if g.finishedAt == nil {
		return fmt.Errorf("%w: finished time is required when game is over", ErrInvalidGameState)
	}
	if g.endReason == nil {
		return fmt.Errorf("%w: end reason is required when game is over", ErrInvalidGameState)
	}
	if *g.endReason == EndReasonInsufficientPlayers && g.winner != nil {
		return fmt.Errorf("%w: cancelled game cannot have a winner", ErrInvalidWinner)
	}
	if *g.endReason != EndReasonInsufficientPlayers && g.winner == nil {
		return fmt.Errorf("%w: winner is required when game is finished", ErrInvalidWinner)
	}
	if g.currentTurn.Clue != nil || g.currentTurn.GuessesRemaining != nil {
		return fmt.Errorf("%w: clue and guesses must be nil when game is over", ErrInvalidGameState)
	}
	if g.currentTurn.DeadlineAt != nil || g.remainingOnPause != nil {
		return fmt.Errorf("%w: finished game cannot have a turn timer", ErrInvalidGameState)
	}
	return nil
}

func (g *Game) ValidateGameState() error {
	if g.board == nil {
		return ErrInvalidBoard
	}
	if g.winner != nil && *g.winner != TeamRed && *g.winner != TeamBlue {
		return ErrInvalidWinner
	}
	switch g.currentTurn.Phase {
	case PhaseWaitingForClue:
		return g.validateWaitingForClue()
	case PhaseGuessing:
		return g.validateGuessing()
	case PhasePausedForPlayers:
		return g.validatePaused()
	case PhaseGameOver:
		return g.validateGameOver()
	default:
		return fmt.Errorf("%w: unknown phase %q", ErrInvalidGameState, g.currentTurn.Phase)
	}
}

func validateTurnAction(g *Game, player *Player, phase Phase, role Role, now time.Time) error {
	if err := g.ValidateGameState(); err != nil {
		return err
	}
	if player == nil {
		return fmt.Errorf("%w: player is required", ErrInvalidArgument)
	}
	expired, err := g.PassTurnIfExpired(now)
	if err != nil {
		return err
	}
	switch {
	case g.currentTurn.Phase == PhaseGameOver:
		err = ErrGameAlreadyFinished
	case g.currentTurn.Phase != phase:
		err = fmt.Errorf("%w: expected %q, got %q", ErrInvalidRoomState, phase, g.currentTurn.Phase)
	case player.Team != g.currentTurn.Team:
		err = ErrNotYourTurn
	case player.Role != role:
		err = ErrRoleForbidden
	}
	if err != nil && expired != nil {
		return fmt.Errorf("%w (%w)", ErrTurnExpired, err)
	}
	return err
}

func validateClue(board *Board, clue *Clue) (Clue, error) {
	if clue == nil {
		return Clue{}, fmt.Errorf("%w: clue is required", ErrInvalidArgument)
	}
	word := strings.ToLower(strings.TrimSpace(clue.Word))
	if word == "" {
		return Clue{}, fmt.Errorf("%w: %w: clue word cannot be empty", ErrInvalidClue, ErrClueInvalidFormat)
	}
	if len(strings.Fields(word)) != 1 {
		return Clue{}, fmt.Errorf("%w: %w: clue word cannot contain spaces", ErrInvalidClue, ErrClueInvalidFormat)
	}
	if utf8.RuneCountInString(word) > 30 {
		return Clue{}, fmt.Errorf("%w: %w: clue word cannot exceed 30 characters", ErrInvalidClue, ErrClueInvalidFormat)
	}
	if clue.Number < 1 || clue.Number > 9 {
		return Clue{}, fmt.Errorf("%w: clue number must be between 1 and 9", ErrInvalidClueNumber)
	}
	for _, card := range board.cards {
		if !card.revealed && strings.ToLower(card.word) == word {
			return Clue{}, fmt.Errorf("%w: %w: %q is already on the board as unrevealed", ErrInvalidClue, ErrClueWordOnBoard, word)
		}
	}
	return Clue{Word: word, Number: clue.Number}, nil
}
