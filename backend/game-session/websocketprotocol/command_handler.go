package websocketprotocol

import (
	"encoding/json"
	"time"

	"game-session/roomaction"
	"game-session/service"
)

type CommandHandler struct {
	actions *roomaction.Handler
	storage service.RoomStore
}

func NewCommandHandler(actions *roomaction.Handler, storage service.RoomStore) *CommandHandler {
	return &CommandHandler{actions: actions, storage: storage}
}

func replayStoredResponse(response []byte) CommandOutcome {
	var head struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(response, &head)
	if head.Type == "ack" {
		var ack AckReply
		_ = json.Unmarshal(response, &ack)
		return CommandOutcome{Ack: &ack}
	}
	var errorReply ErrorReply
	_ = json.Unmarshal(response, &errorReply)
	return CommandOutcome{Error: &errorReply}
}

func (handler *CommandHandler) runCommand(tx service.RoomStore, roomID int64, userID int64, message ClientMessage, now time.Time) (roomaction.Outcome, error) {
	switch message.Type {
	case "room.team.select":
		return handler.selectTeam(tx, roomID, userID, message.Payload, now)
	case "room.role.select":
		return handler.selectRole(tx, roomID, userID, message.Payload, now)
	case "room.ready.set":
		return handler.setReady(tx, roomID, userID, message.Payload, now)
	case "room.settings.update":
		return handler.updateSettings(tx, roomID, userID, message.Payload, now)
	case "room.member.kick":
		return handler.kickMember(tx, roomID, userID, message.Payload, now)
	case "room.lobby.return":
		return handler.returnToLobby(tx, roomID, userID, message.Payload, now)
	case "game.clue.submit":
		return handler.submitClue(tx, roomID, userID, message.Payload, now)
	case "game.card.guess":
		return handler.guessCard(tx, roomID, userID, message.Payload, now)
	case "game.turn.pass":
		return handler.passTurn(tx, roomID, userID, message.Payload, now)
	default:
		return roomaction.Outcome{}, errUnknownType{messageType: message.Type}
	}
}

func saveReplyToLedger(tx service.RoomStore, roomID int64, sessionID string, requestID string, commandText string, outcome CommandOutcome) error {
	var reply any
	if outcome.Ack != nil {
		reply = outcome.Ack
	} else if outcome.Error.Error.Code != "INTERNAL_ERROR" {
		reply = outcome.Error
	}
	if reply == nil {
		return nil
	}
	response, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	return tx.SaveCommandResult(roomID, sessionID, requestID, commandText, response)
}

func (handler *CommandHandler) executeCommand(roomID int64, userID int64, sessionID string, message ClientMessage, now time.Time) CommandOutcome {
	requestID := *message.RequestID
	commandText := message.Type + "|" + string(message.Payload)
	var outcome CommandOutcome
	err := handler.storage.Transaction(func(tx service.RoomStore) error {
		result, err := handler.runCommand(tx, roomID, userID, message, now)
		if err != nil {
			return err
		}
		if result.ActorError != nil {
			errorReply, closures := errorReplyFor(message.RequestID, userID, roomID, result.ActorError)
			outcome = CommandOutcome{Error: errorReply, Closures: closures}
		} else {
			ack := &AckReply{Type: "ack", RequestID: requestID, OK: true, Payload: result.ActorReply}
			outcome = CommandOutcome{Ack: ack}
		}
		return saveReplyToLedger(tx, roomID, sessionID, requestID, commandText, outcome)
	})
	if err != nil {
		errorReply, closures := errorReplyFor(message.RequestID, userID, roomID, err)
		outcome = CommandOutcome{Error: errorReply, Closures: closures}
		_ = saveReplyToLedger(handler.storage, roomID, sessionID, requestID, commandText, outcome)
	}
	return outcome
}

func (handler *CommandHandler) checkCommandReplay(roomID int64, sessionID string, requestID string, commandText string) (outcome CommandOutcome, isAnswerReady bool) {
	storedCommandText, storedResponse, found, err := handler.storage.LookupCommandResult(roomID, sessionID, requestID)
	if err != nil {
		return CommandOutcome{Error: newError(&requestID, errorInternal, nil)}, true
	}
	if found && storedCommandText == commandText {
		return replayStoredResponse(storedResponse), true
	}
	if found {
		return CommandOutcome{Error: newError(&requestID, errorRequestIDReused, map[string]any{"field": "request_id"})}, true
	}
	return CommandOutcome{}, false
}

func (handler *CommandHandler) HandleCommand(roomID int64, userID int64, sessionID string, rawMessage []byte, now time.Time) CommandOutcome {
	message, envelopeError := parseEnvelope(rawMessage)
	if envelopeError != nil {
		return CommandOutcome{Error: envelopeError}
	}
	requestID := *message.RequestID
	commandText := message.Type + "|" + string(message.Payload)

	if outcomeFromLedger, isAnswerReady := handler.checkCommandReplay(roomID, sessionID, requestID, commandText); isAnswerReady {
		return outcomeFromLedger
	}
	return handler.executeCommand(roomID, userID, sessionID, message, now)
}