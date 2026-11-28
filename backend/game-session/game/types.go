package game

import "time"

type Team string

const (
	TeamRed  Team = "RED"
	TeamBlue Team = "BLUE"
)

type Player struct {
	UserID   int64
	Username string
	Team     Team
	Role     Role
}

type CardColor string

const (
	CardColorRed      CardColor = "RED"
	CardColorBlue     CardColor = "BLUE"
	CardColorNeutral  CardColor = "NEUTRAL"
	CardColorAssassin CardColor = "ASSASSIN"
)

type Role string

const (
	RoleSpymaster Role = "SPYMASTER"
	RoleOperative Role = "OPERATIVE"
)

type GameStatus string

const (
	GameStatusInProgress GameStatus = "IN_PROGRESS"
	GameStatusFinished   GameStatus = "GAME_FINISHED"
	GameStatusCancelled  GameStatus = "GAME_CANCELLED"
)

type Phase string

const (
	PhaseWaitingForClue   Phase = "WAITING_FOR_CLUE"
	PhaseGuessing         Phase = "GUESSING"
	PhaseGameOver         Phase = "GAME_OVER"
	PhasePausedForPlayers Phase = "PAUSED_FOR_PLAYERS"
)

type EndReason string

const (
	EndReasonAllTeamCardsRevealed EndReason = "ALL_TEAM_CARDS_REVEALED"
	EndReasonAssassinRevealed     EndReason = "ASSASSIN_REVEALED"
	EndReasonInsufficientPlayers  EndReason = "INSUFFICIENT_PLAYERS"
)

type Clue struct {
	Word   string `json:"word"`
	Number int    `json:"number"`
}

type Score struct {
	Red  int `json:"red"`
	Blue int `json:"blue"`
}

type CurrentTurn struct {
	Team             Team       `json:"team"`
	Phase            Phase      `json:"phase"`
	Clue             *Clue      `json:"clue"`
	GuessesRemaining *int       `json:"guesses_remaining"`
	DeadlineAt       *time.Time `json:"deadline_at"`
}

func (t CurrentTurn) clone() CurrentTurn {
	copied := t
	copied.Clue = copyPtr(t.Clue)
	copied.GuessesRemaining = copyPtr(t.GuessesRemaining)
	copied.DeadlineAt = copyPtr(t.DeadlineAt)
	return copied
}

type CardView struct {
	CardID   int        `json:"card_id"`
	Word     string     `json:"word"`
	Color    *CardColor `json:"color"`
	Revealed bool       `json:"revealed"`
}

func newCardView(card card) CardView {
	view := CardView{
		CardID:   card.cardID,
		Word:     card.word,
		Revealed: card.revealed,
	}
	if card.revealed {
		c := card.color
		view.Color = &c
	}
	return view
}

type ChangeReason string

const (
	ChangeReasonPassed               ChangeReason = "PASSED"
	ChangeReasonGuessesExhausted     ChangeReason = "GUESSES_EXHAUSTED"
	ChangeReasonOpponentCardRevealed ChangeReason = "OPPONENT_CARD_REVEALED"
	ChangeReasonNeutralCardRevealed  ChangeReason = "NEUTRAL_CARD_REVEALED"
	ChangeReasonTurnTimerExpired     ChangeReason = "TURN_TIMER_EXPIRED"
)

type GuessResult struct {
	GameID         int64         `json:"game_id"`
	Card           CardView      `json:"card"`
	GuessingTeam   Team          `json:"guessing_team"`
	IsCorrectGuess bool          `json:"-"`
	Score          Score         `json:"score"`
	CurrentTurn    CurrentTurn   `json:"current_turn"`
	ChangeReason   *ChangeReason `json:"-"`
	Winner         *Team         `json:"winner"`
	EndReason      *EndReason    `json:"end_reason"`
	FinishedAt     *time.Time    `json:"-"`
}

type PassResult struct {
	GameID       int64        `json:"game_id"`
	Score        Score        `json:"score"`
	CurrentTurn  CurrentTurn  `json:"current_turn"`
	ChangeReason ChangeReason `json:"-"`
}
