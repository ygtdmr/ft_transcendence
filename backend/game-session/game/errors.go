package game

import "errors"

var (
	ErrRoleForbidden       = errors.New("ROLE_FORBIDDEN")
	ErrNotYourTurn         = errors.New("NOT_YOUR_TURN")
	ErrInvalidRoomState    = errors.New("INVALID_ROOM_STATE")
	ErrInvalidClue         = errors.New("INVALID_CLUE")
	ErrInvalidClueNumber   = errors.New("INVALID_CLUE_NUMBER")
	ErrInvalidCard         = errors.New("INVALID_CARD")
	ErrCardAlreadyRevealed = errors.New("CARD_ALREADY_REVEALED")
	ErrNoGuessesRemaining  = errors.New("NO_GUESSES_REMAINING")
	ErrGameAlreadyFinished = errors.New("GAME_ALREADY_FINISHED")
)

// Game-specific errors
var (
	ErrNotEnoughWords         = errors.New("not enough words in word pack")
	ErrInvalidWordPack        = errors.New("invalid word pack")
	ErrInvalidBoard           = errors.New("invalid board")
	ErrNilRandomSource        = errors.New("random source cannot be nil")
	ErrGenerateSeed           = errors.New("failed to generate secure seed")
	ErrInvalidGameState       = errors.New("invalid game state")
	ErrInvalidWinner          = errors.New("invalid winner")
	ErrInvalidArgument        = errors.New("invalid argument")
	ErrInvalidRole            = errors.New("Invalid Role")
	ErrPhaseAlreadyPaused     = errors.New("Phase already paused")
	ErrClueInvalidFormat      = errors.New("INVALID_FORMAT")
	ErrClueWordOnBoard        = errors.New("WORD_ON_BOARD")
	ErrStaffingDeadlinePassed = errors.New("staffing deadline has passed")
	ErrTurnExpired            = errors.New("TURN_EXPIRED")
)
