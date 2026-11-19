package websocketprotocol

import (
	"errors"

	"game-session/game"
	"game-session/room"
	"game-session/roomaction"
)

// package internal errors
type errUnknownType struct{ messageType string }

func (e errUnknownType) Error() string { return "unknown message type " + e.messageType }

type errPayloadField struct {
	field   string
	message string
}

func (e errPayloadField) Error() string { return e.message }

// closeCode == 0 means the connection should not be closed, just send an error reply.
type errorKind struct {
	code      string
	message   string
	closeCode int
}

var (
	// room errors
	errorRoomNotFound            = errorKind{"ROOM_NOT_FOUND", "The room was not found.", 4404}
	errorNotRoomMember           = errorKind{"NOT_ROOM_MEMBER", "The user is not a member of this room.", 4403}
	errorPostGameDeadlineExpired = errorKind{"POST_GAME_DEADLINE_EXPIRED", "The post-game decision deadline has passed.", 4403}
	errorTargetNotRoomMember     = errorKind{"TARGET_NOT_ROOM_MEMBER", "The target user is not a member of this room.", 0}
	errorInvalidRoomState        = errorKind{"INVALID_ROOM_STATE", "This action is not allowed in the current room state.", 0}
	errorNotHost                 = errorKind{"NOT_HOST", "Only the host may do this.", 0}
	errorCannotKickSelf          = errorKind{"CANNOT_KICK_SELF", "The host cannot kick themselves.", 0}
	errorPostGamePending         = errorKind{"POST_GAME_PENDING", "A member is still deciding after the last game.", 0}
	errorTeamRequired            = errorKind{"TEAM_REQUIRED", "Select a team first.", 0}
	errorRoleRequired            = errorKind{"ROLE_REQUIRED", "Select a role first.", 0}
	errorRoleConflict            = errorKind{"ROLE_CONFLICT", "That team already has a Spymaster.", 0}
	errorInvalidPayload          = errorKind{"INVALID_PAYLOAD", "The payload has an invalid value.", 0}

	// game errors
	errorRoleForbidden       = errorKind{"ROLE_FORBIDDEN", "Your role may not perform this action.", 0}
	errorNotYourTurn         = errorKind{"NOT_YOUR_TURN", "It is not your team's turn.", 0}
	errorTurnExpired         = errorKind{"NOT_YOUR_TURN", "The turn timer expired before the command arrived.", 0}
	errorInvalidClueNumber   = errorKind{"INVALID_CLUE_NUMBER", "payload.number must be an integer from 1 through 9.", 0}
	errorInvalidClue         = errorKind{"INVALID_CLUE", "payload.word is not an acceptable clue.", 0}
	errorInvalidCard         = errorKind{"INVALID_CARD", "That card does not exist in this game.", 0}
	errorCardAlreadyRevealed = errorKind{"CARD_ALREADY_REVEALED", "That card has already been revealed.", 0}
	errorNoGuessesRemaining  = errorKind{"NO_GUESSES_REMAINING", "Your team has no guesses remaining in this turn.", 0}
	errorGameAlreadyFinished = errorKind{"GAME_ALREADY_FINISHED", "The game has already finished.", 0}

	// envelope and ledger errors (parseEnvelope, checkCommandReplay, runCommand)
	errorInvalidEnvelope   = errorKind{"INVALID_EVENT", "The message is not a valid JSON envelope.", 0}
	errorRequestIDRequired = errorKind{"INVALID_EVENT", "request_id is required for every client message.", 0}
	errorRequestIDInvalid  = errorKind{"INVALID_EVENT", "request_id must be 1..64 visible ASCII characters.", 0}
	errorTypeRequired      = errorKind{"INVALID_EVENT", "type is required for every client message.", 0}
	errorPayloadRequired   = errorKind{"INVALID_EVENT", "payload is required and must be a JSON object.", 0}
	errorRequestIDReused   = errorKind{"INVALID_EVENT", "request_id was already used with a different command.", 0}
	errorUnknownType       = errorKind{"INVALID_EVENT", "Message type is not supported.", 0}

	// server errors
	errorInternal = errorKind{"INTERNAL_ERROR", "Unexpected server error.", 0}
)

func errorReplyFor(requestID *string, userID int64, roomID int64, err error) (*ErrorReply, []roomaction.SocketClosure) {
	kind, details, found := payloadErrorKind(err)
	if !found {
		kind, details, found = roomErrorKind(err, roomID, userID)
	}
	if !found {
		kind, details, found = gameErrorKind(err)
	}
	if !found {
		kind = errorInternal
	}
	reply := newError(requestID, kind, details)
	if kind.closeCode == 0 {
		return reply, nil
	}
	return reply, []roomaction.SocketClosure{{UserID: userID, Code: kind.closeCode, Reason: kind.code}}
}

// Unknown type and payload field errors
func payloadErrorKind(err error) (errorKind, map[string]any, bool) {
	var unknownType errUnknownType
	if errors.As(err, &unknownType) {
		return errorUnknownType, map[string]any{"type": unknownType.messageType}, true
	}
	var payloadField errPayloadField
	if errors.As(err, &payloadField) {
		return errorInvalidPayload, map[string]any{"field": payloadField.field, "reason": payloadField.message}, true
	}
	return errorKind{}, nil, false
}

// Room errors
func roomErrorKind(err error, roomID int64, userID int64) (errorKind, map[string]any, bool) {
	switch {
	case errors.Is(err, room.ErrRoomNotFound):
		return errorRoomNotFound, map[string]any{"room_id": roomID}, true
	case errors.Is(err, room.ErrNotRoomMember):
		return errorNotRoomMember, map[string]any{"room_id": roomID, "user_id": userID}, true
	case errors.Is(err, room.ErrPostGameDeadlineExpired):
		return errorPostGameDeadlineExpired, nil, true
	case errors.Is(err, room.ErrTargetNotRoomMember):
		return errorTargetNotRoomMember, nil, true
	case errors.Is(err, room.ErrInvalidRoomState), errors.Is(err, room.ErrGameNotFound):
		return errorInvalidRoomState, nil, true
	case errors.Is(err, room.ErrNotHost):
		return errorNotHost, nil, true
	case errors.Is(err, room.ErrCannotKickSelf):
		return errorCannotKickSelf, nil, true
	case errors.Is(err, room.ErrPostGamePending):
		return errorPostGamePending, nil, true
	case errors.Is(err, room.ErrTeamRequired):
		return errorTeamRequired, map[string]any{"user_id": userID}, true
	case errors.Is(err, room.ErrRoleRequired):
		return errorRoleRequired, map[string]any{"user_id": userID}, true
	case errors.Is(err, room.ErrRoleConflict):
		return errorRoleConflict, nil, true
	case errors.Is(err, room.ErrInvalidPayload), errors.Is(err, room.ErrValidationError):
		return errorInvalidPayload, nil, true
	}
	return errorKind{}, nil, false
}

// Game errors
func gameErrorKind(err error) (errorKind, map[string]any, bool) {
	switch {
	case errors.Is(err, game.ErrInvalidRoomState):
		return errorInvalidRoomState, nil, true
	case errors.Is(err, game.ErrRoleForbidden):
		return errorRoleForbidden, nil, true
	case errors.Is(err, game.ErrNotYourTurn):
		return errorNotYourTurn, nil, true
	case errors.Is(err, game.ErrTurnExpired):
		return errorTurnExpired, map[string]any{"reason": "TURN_TIMER_EXPIRED"}, true
	case errors.Is(err, game.ErrInvalidClueNumber):
		return errorInvalidClueNumber, map[string]any{"field": "payload.number", "min": 1, "max": 9}, true
	case errors.Is(err, game.ErrInvalidClue):
		reason := "INVALID_FORMAT"
		if errors.Is(err, game.ErrClueWordOnBoard) {
			reason = "WORD_ON_BOARD"
		}
		return errorInvalidClue, map[string]any{"field": "payload.word", "reason": reason}, true
	case errors.Is(err, game.ErrInvalidCard):
		return errorInvalidCard, nil, true
	case errors.Is(err, game.ErrCardAlreadyRevealed):
		return errorCardAlreadyRevealed, nil, true
	case errors.Is(err, game.ErrNoGuessesRemaining):
		return errorNoGuessesRemaining, nil, true
	case errors.Is(err, game.ErrGameAlreadyFinished):
		return errorGameAlreadyFinished, nil, true
	}
	return errorKind{}, nil, false
}

func newError(requestID *string, kind errorKind, details map[string]any) *ErrorReply {
	if details == nil {
		details = map[string]any{}
	}
	return &ErrorReply{Type: "error", RequestID: requestID, Error: ErrorDetail{Code: kind.code, Message: kind.message, Details: details}}
}
