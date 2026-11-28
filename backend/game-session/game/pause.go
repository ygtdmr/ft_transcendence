package game

import (
	"fmt"
	"time"
)

type TeamStaffing struct {
	MissingRoles []Role     `json:"missing_roles"`
	DeadlineAt   *time.Time `json:"deadline_at"`
}

type StaffingView struct {
	Red  TeamStaffing `json:"red"`
	Blue TeamStaffing `json:"blue"`
}

func (g *Game) calculateDeadline(missingTeam []Role, current *time.Time, now time.Time) *time.Time {
	if len(missingTeam) == 0 {
		return nil
	}
	if current != nil {
		return current
	}
	deadline := now.Add(g.staffingTimeout)
	return &deadline
}

func (g *Game) pauseForPlayers(departed Player, now time.Time) {
	g.phaseBeforePause = copyPtr(&g.currentTurn.Phase)
	g.currentTurn.Phase = PhasePausedForPlayers
	if g.currentTurn.DeadlineAt != nil {
		remaining := max(g.currentTurn.DeadlineAt.Sub(now), 0)
		g.remainingOnPause = &remaining
		g.currentTurn.DeadlineAt = nil
	}

	g.addHistory(HistoryEntry{
		Type:     HistoryGamePaused,
		Team:     copyPtr(&departed.Team),
		UserID:   copyPtr(&departed.UserID),
		Username: copyPtr(&departed.Username),
	}, now)
}

func (g *Game) resumeAfterPlayersRestored(now time.Time) {
	g.currentTurn.Phase = *g.phaseBeforePause
	g.phaseBeforePause = nil
	if g.remainingOnPause != nil {
		deadline := now.Add(*g.remainingOnPause)
		g.currentTurn.DeadlineAt = &deadline
		g.remainingOnPause = nil
	}

	g.addHistory(HistoryEntry{
		Type: HistoryGameResumed,
		Team: copyPtr(&g.currentTurn.Team),
	}, now)
}

func (g *Game) updatePauseState(hasMissing bool, departed *Player, now time.Time) error {
	isPaused := g.currentTurn.Phase == PhasePausedForPlayers
	if hasMissing && !isPaused {
		if departed == nil {
			return fmt.Errorf("%w: a departed player is required to pause the game", ErrInvalidArgument)
		}
		g.pauseForPlayers(*departed, now)
	}
	if !hasMissing && isPaused {
		g.resumeAfterPlayersRestored(now)
	}
	return nil
}

func (g *Game) UpdateStaffing(redMissing []Role, blueMissing []Role, departed *Player, now time.Time) error {
	if deadlinePassed(g.redStaffingDeadline, now) || deadlinePassed(g.blueStaffingDeadline, now) {
		return ErrStaffingDeadlinePassed
	}
	if err := g.ValidateGameState(); err != nil {
		return err
	}
	if g.currentTurn.Phase == PhaseGameOver {
		return ErrGameAlreadyFinished
	}

	newRedMissing := append([]Role{}, redMissing...)
	newBlueMissing := append([]Role{}, blueMissing...)

	redDeadline := g.calculateDeadline(newRedMissing, g.redStaffingDeadline, now)
	blueDeadline := g.calculateDeadline(newBlueMissing, g.blueStaffingDeadline, now)

	hasMissing := len(newRedMissing) > 0 || len(newBlueMissing) > 0
	if err := g.updatePauseState(hasMissing, departed, now); err != nil {
		return err
	}
	g.redMissing = newRedMissing
	g.blueMissing = newBlueMissing
	g.redStaffingDeadline = redDeadline
	g.blueStaffingDeadline = blueDeadline
	return nil
}

func (g *Game) IsPaused() bool {
	return g.currentTurn.Phase == PhasePausedForPlayers
}

func (g *Game) cancelForInsufficientPlayers(now time.Time) error {
	if err := g.ValidateGameState(); err != nil {
		return err
	}

	g.finish(nil, EndReasonInsufficientPlayers, now)
	g.phaseBeforePause = nil
	g.remainingOnPause = nil
	g.redMissing = []Role{}
	g.blueMissing = []Role{}
	g.redStaffingDeadline = nil
	g.blueStaffingDeadline = nil

	return nil
}

func (g *Game) CancelIfStaffingExpired(now time.Time) (bool, error) {
	if g.currentTurn.Phase != PhasePausedForPlayers {
		return false, nil
	}
	if deadlinePassed(g.redStaffingDeadline, now) || deadlinePassed(g.blueStaffingDeadline, now) {
		if err := g.cancelForInsufficientPlayers(now); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}
