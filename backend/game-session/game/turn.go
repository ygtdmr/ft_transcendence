package game

import (
	"time"
)

func (g *Game) startTurnTimer(now time.Time) {
	g.currentTurn.DeadlineAt = nil
	if g.turnTimer != nil {
		deadline := now.Add(*g.turnTimer)
		g.currentTurn.DeadlineAt = &deadline
	}
}

func (g *Game) switchTurn(reason ChangeReason, now time.Time) {
	previousTeam := g.currentTurn.Team
	g.currentTurn.Team = opposite(g.currentTurn.Team)
	g.currentTurn.Phase = PhaseWaitingForClue
	g.currentTurn.Clue = nil
	g.currentTurn.GuessesRemaining = nil
	g.startTurnTimer(now)

	reasonText := string(reason)
	g.addHistory(HistoryEntry{
		Type:         HistoryTurnChanged,
		Team:         copyPtr(&g.currentTurn.Team),
		PreviousTeam: &previousTeam,
		Reason:       &reasonText,
	}, now)
}

func (g *Game) PassTurn(player *Player, now time.Time) (PassResult, error) {
	if err := validateTurnAction(g, player, PhaseGuessing, RoleOperative, now); err != nil {
		return PassResult{}, err
	}
	g.addHistory(HistoryEntry{
		Type:     HistoryTurnPassed,
		Team:     copyPtr(&player.Team),
		UserID:   copyPtr(&player.UserID),
		Username: copyPtr(&player.Username),
	}, now)

	g.switchTurn(ChangeReasonPassed, now)
	return PassResult{
		GameID:       g.gameID,
		Score:        g.Score(),
		CurrentTurn:  g.currentTurn.clone(),
		ChangeReason: ChangeReasonPassed,
	}, nil
}

func deadlinePassed(deadline *time.Time, now time.Time) bool {
	if deadline == nil {
		return false
	}
	return !now.Before(*deadline)
}

func (g *Game) PassTurnIfExpired(now time.Time) (*PassResult, error) {
	if err := g.ValidateGameState(); err != nil {
		return nil, err
	}
	if g.currentTurn.Phase != PhaseWaitingForClue && g.currentTurn.Phase != PhaseGuessing {
		return nil, nil
	}
	if !deadlinePassed(g.currentTurn.DeadlineAt, now) {
		return nil, nil
	}
	g.switchTurn(ChangeReasonTurnTimerExpired, now)
	return &PassResult{
		GameID:       g.gameID,
		Score:        g.Score(),
		CurrentTurn:  g.currentTurn.clone(),
		ChangeReason: ChangeReasonTurnTimerExpired,
	}, nil
}
