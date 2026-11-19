package websocketprotocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"game-session/room"
)

func decodePayload(payload json.RawMessage, into any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		field := "payload"
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) && typeErr.Field != "" {
			field = "payload." + typeErr.Field
		} else if strings.HasPrefix(err.Error(), "json: unknown field ") {
			field = "payload." + strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), "\"")
		}
		return errPayloadField{field: field, message: field + " is missing, has the wrong type or is not allowed."}
	}
	return nil
}

func decodeSettingsUpdate(payload json.RawMessage) (room.SettingsUpdate, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return room.SettingsUpdate{}, errPayloadField{field: "payload", message: "payload must be an object."}
	}
	var update room.SettingsUpdate
	for name, raw := range fields {
		switch name {
		case "max_players":
			var value int
			if err := json.Unmarshal(raw, &value); err != nil {
				return room.SettingsUpdate{}, errPayloadField{field: "payload.max_players", message: "payload.max_players must be an integer."}
			}
			update.MaxPlayers = &value
		case "turn_timer_seconds":
			update.UpdateTurnTimer = true
			if string(raw) == "null" {
				update.TurnTimerSeconds = nil
				continue
			}
			var value room.TurnTimer
			if err := json.Unmarshal(raw, &value); err != nil {
				return room.SettingsUpdate{}, errPayloadField{field: "payload.turn_timer_seconds", message: "payload.turn_timer_seconds must be one of null, 60, 90, 120."}
			}
			update.TurnTimerSeconds = &value
		case "language":
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				return room.SettingsUpdate{}, errPayloadField{field: "payload.language", message: "payload.language must be a string."}
			}
			update.Language = &value
		default:
			return room.SettingsUpdate{}, errPayloadField{field: "payload." + name, message: "payload." + name + " is not allowed."}
		}
	}
	return update, nil
}

// Payload should be empty for lobby.return and turn.pass
func requireEmptyPayload(payload json.RawMessage) error {
	return decodePayload(payload, &struct{}{})
}
