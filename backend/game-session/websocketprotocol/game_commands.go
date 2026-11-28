package websocketprotocol

import (
	"encoding/json"
	"time"

	"game-session/game"
	"game-session/roomaction"
	"game-session/service"
)

type submitCluePayload struct {
	Word   *string `json:"word"`
	Number *int    `json:"number"`
}

func (handler *CommandHandler) submitClue(tx service.RoomStore, roomID int64, userID int64, payload json.RawMessage, now time.Time) (roomaction.Outcome, error) {
	var decodedPayload submitCluePayload
	if err := decodePayload(payload, &decodedPayload); err != nil {
		return roomaction.Outcome{}, err
	}
	if decodedPayload.Word == nil {
		return roomaction.Outcome{}, errPayloadField{field: "payload.word", message: "payload.word must be a string."}
	}
	if decodedPayload.Number == nil {
		return roomaction.Outcome{}, errPayloadField{field: "payload.number", message: "payload.number must be an integer."}
	}
	clue := game.Clue{Word: *decodedPayload.Word, Number: *decodedPayload.Number}
	return handler.actions.SubmitClue(tx, roomID, userID, clue, now)
}

type guessCardPayload struct {
	CardID *int `json:"card_id"`
}

func (handler *CommandHandler) guessCard(tx service.RoomStore, roomID int64, userID int64, payload json.RawMessage, now time.Time) (roomaction.Outcome, error) {
	var decodedPayload guessCardPayload
	if err := decodePayload(payload, &decodedPayload); err != nil {
		return roomaction.Outcome{}, err
	}
	if decodedPayload.CardID == nil {
		return roomaction.Outcome{}, errPayloadField{field: "payload.card_id", message: "payload.card_id must be an integer."}
	}
	return handler.actions.GuessCard(tx, roomID, userID, *decodedPayload.CardID, now)
}

func (handler *CommandHandler) passTurn(tx service.RoomStore, roomID int64, userID int64, payload json.RawMessage, now time.Time) (roomaction.Outcome, error) {
	if err := requireEmptyPayload(payload); err != nil {
		return roomaction.Outcome{}, err
	}
	return handler.actions.PassTurn(tx, roomID, userID, now)
}
