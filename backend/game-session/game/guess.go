package game

import (
	"time"
)

func handleTeamCard(g *Game, cardTeam Team, now time.Time) *ChangeReason {
	target := OtherTeamCardsSize
	if g.board.startingTeam == cardTeam {
		target = StartTeamCardsSize
	}
	score := g.Score()
	revealed := score.Red
	if cardTeam == TeamBlue {
		revealed = score.Blue
	}
	if revealed >= target {
		g.finish(&cardTeam, EndReasonAllTeamCardsRevealed, now)
		return nil
	}
	if g.currentTurn.Team != cardTeam {
		reason := ChangeReasonOpponentCardRevealed
		g.switchTurn(reason, now)
		return &reason
	}
	*g.currentTurn.GuessesRemaining--
	if *g.currentTurn.GuessesRemaining <= 0 {
		reason := ChangeReasonGuessesExhausted
		g.switchTurn(reason, now)
		return &reason
	}
	return nil
}

func opposite(t Team) Team {
	if t == TeamRed {
		return TeamBlue
	}
	return TeamRed
}

func handleCardColor(g *Game, color CardColor, now time.Time) *ChangeReason {
	activeTeam := g.currentTurn.Team
	switch color {
	case CardColorAssassin:
		winner := opposite(activeTeam)
		g.finish(&winner, EndReasonAssassinRevealed, now)
	case CardColorNeutral:
		reason := ChangeReasonNeutralCardRevealed
		g.switchTurn(reason, now)
		return &reason
	case CardColorRed:
		return handleTeamCard(g, TeamRed, now)
	case CardColorBlue:
		return handleTeamCard(g, TeamBlue, now)
	}
	return nil
}

func (g *Game) GuessCard(player *Player, cardID int, now time.Time) (GuessResult, error) {
	if err := validateTurnAction(g, player, PhaseGuessing, RoleOperative, now); err != nil {
		return GuessResult{}, err
	}
	card, err := g.board.reveal(cardID)
	if err != nil {
		return GuessResult{}, err
	}
	g.addHistory(HistoryEntry{
		Type:     HistoryCardRevealed,
		Team:     copyPtr(&player.Team),
		UserID:   copyPtr(&player.UserID),
		Username: copyPtr(&player.Username),
		CardID:   copyPtr(&card.cardID),
		Word:     copyPtr(&card.word),
		Color:    copyPtr(&card.color),
	}, now)

	changeReason := handleCardColor(g, card.color, now)
	winner, endReason, finishedAt := g.cloneGameResult()

	return GuessResult{
		GameID:         g.gameID,
		Card:           newCardView(card),
		GuessingTeam:   player.Team,
		IsCorrectGuess: (player.Team == TeamRed && card.color == CardColorRed) || (player.Team == TeamBlue && card.color == CardColorBlue),
		Score:          g.Score(),
		CurrentTurn:    g.currentTurn.clone(),
		ChangeReason:   changeReason,
		Winner:         winner,
		EndReason:      endReason,
		FinishedAt:     finishedAt,
	}, nil
}
