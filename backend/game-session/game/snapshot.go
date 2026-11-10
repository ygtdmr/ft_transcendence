package game

import "time"

type GameSnapshot struct {
	GameID       int64          `json:"game_id"`
	StartingTeam Team           `json:"starting_team"`
	CurrentTurn  CurrentTurn    `json:"current_turn"`
	Score        Score          `json:"score"`
	Board        []CardView     `json:"board"`
	Winner       *Team          `json:"winner"`
	EndReason    *EndReason     `json:"end_reason"`
	Status       GameStatus     `json:"status"`
	StartedAt    time.Time      `json:"started_at"`
	FinishedAt   *time.Time     `json:"finished_at"`
	History      []HistoryEntry `json:"history"`
	Staffing     *StaffingView  `json:"staffing,omitempty"`
}

func copyPtr[T any](value *T) *T {
	if value == nil {
		return nil
	}
	valueCopy := *value
	return &valueCopy
}

func cardViewForRole(card card, role *Role, status GameStatus) CardView {
	view := newCardView(card)
	isSpymaster := role != nil && *role == RoleSpymaster
	if isSpymaster || status != GameStatusInProgress {
		view.Color = &card.color
	}
	return view
}

func (b *Board) boardViewForRole(role *Role, status GameStatus) ([]CardView, error) {
	if role != nil && *role != RoleOperative && *role != RoleSpymaster {
		return nil, ErrInvalidRole
	}
	views := make([]CardView, len(b.cards))
	for i := range b.cards {
		view := cardViewForRole(b.cards[i], role, status)
		views[i] = view
	}
	return views, nil
}

func (g *Game) staffingView() StaffingView {
	return StaffingView{
		Red: TeamStaffing{
			MissingRoles: append([]Role{}, g.redMissing...),
			DeadlineAt:   copyPtr(g.redStaffingDeadline),
		},
		Blue: TeamStaffing{
			MissingRoles: append([]Role{}, g.blueMissing...),
			DeadlineAt:   copyPtr(g.blueStaffingDeadline),
		},
	}
}

func (g *Game) SnapshotForRole(role *Role) (GameSnapshot, error) {
	view, err := g.board.boardViewForRole(role, g.Status())
	if err != nil {
		return GameSnapshot{}, err
	}
	var staffing *StaffingView
	if g.currentTurn.Phase == PhasePausedForPlayers {
		staffingView := g.staffingView()
		staffing = &staffingView
	}
	winner, endReason, finishedAt := g.cloneGameResult()
	return GameSnapshot{
		GameID:       g.gameID,
		StartingTeam: g.board.startingTeam,
		CurrentTurn:  g.currentTurn.clone(),
		Score:        g.Score(),
		Board:        view,
		Winner:       winner,
		EndReason:    endReason,
		Status:       g.Status(),
		StartedAt:    g.startedAt,
		FinishedAt:   finishedAt,
		History:      g.cloneHistory(),
		Staffing:     staffing,
	}, nil
}
