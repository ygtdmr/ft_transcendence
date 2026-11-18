package websocketprotocol

import (
	"encoding/json"
	"time"

	"game-session/game"
	"game-session/roomaction"
	"game-session/service"
)

type selectTeamPayload struct {
	Team game.Team `json:"team"`
}

func (handler *CommandHandler) selectTeam(tx service.RoomStore, roomID int64, userID int64, payload json.RawMessage, now time.Time) (roomaction.Outcome, error) {
	var decodedPayload selectTeamPayload
	if err := decodePayload(payload, &decodedPayload); err != nil {
		return roomaction.Outcome{}, err
	}
	return handler.actions.SelectTeam(tx, roomID, userID, decodedPayload.Team, now)
}

type selectRolePayload struct {
	Team game.Team `json:"team"`
	Role game.Role `json:"role"`
}

func (handler *CommandHandler) selectRole(tx service.RoomStore, roomID int64, userID int64, payload json.RawMessage, now time.Time) (roomaction.Outcome, error) {
	var decodedPayload selectRolePayload
	if err := decodePayload(payload, &decodedPayload); err != nil {
		return roomaction.Outcome{}, err
	}
	return handler.actions.SelectRole(tx, roomID, userID, decodedPayload.Team, decodedPayload.Role, now)
}

type setReadyPayload struct {
	Ready *bool `json:"ready"`
}

func (handler *CommandHandler) setReady(tx service.RoomStore, roomID int64, userID int64, payload json.RawMessage, now time.Time) (roomaction.Outcome, error) {
	var decodedPayload setReadyPayload
	if err := decodePayload(payload, &decodedPayload); err != nil {
		return roomaction.Outcome{}, err
	}
	if decodedPayload.Ready == nil {
		return roomaction.Outcome{}, errPayloadField{field: "payload.ready", message: "payload.ready must be a boolean."}
	}
	return handler.actions.SetReady(tx, roomID, userID, *decodedPayload.Ready, now)
}

func (handler *CommandHandler) updateSettings(tx service.RoomStore, roomID int64, userID int64, payload json.RawMessage, now time.Time) (roomaction.Outcome, error) {
	update, err := decodeSettingsUpdate(payload)
	if err != nil {
		return roomaction.Outcome{}, err
	}
	return handler.actions.UpdateSettings(tx, roomID, userID, update, now)
}

type kickPayload struct {
	UserID *int64 `json:"user_id"`
}

func (handler *CommandHandler) kickMember(tx service.RoomStore, roomID int64, userID int64, payload json.RawMessage, now time.Time) (roomaction.Outcome, error) {
	var decodedPayload kickPayload
	if err := decodePayload(payload, &decodedPayload); err != nil {
		return roomaction.Outcome{}, err
	}
	if decodedPayload.UserID == nil || *decodedPayload.UserID <= 0 {
		return roomaction.Outcome{}, errPayloadField{field: "payload.user_id", message: "payload.user_id must be a positive integer."}
	}
	return handler.actions.KickMember(tx, roomID, userID, *decodedPayload.UserID, now)
}

func (handler *CommandHandler) returnToLobby(tx service.RoomStore, roomID int64, userID int64, payload json.RawMessage, now time.Time) (roomaction.Outcome, error) {
	if err := requireEmptyPayload(payload); err != nil {
		return roomaction.Outcome{}, err
	}
	return handler.actions.ReturnToLobby(tx, roomID, userID, now)
}
