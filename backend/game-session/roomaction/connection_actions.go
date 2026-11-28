package roomaction

import (
	"time"

	"game-session/service"
)

func (handler *Handler) HandleConnected(tx service.RoomStore, roomID int64, userID int64, now time.Time) error {
	if _, err := handler.rooms.MarkReconnected(tx, roomID, userID, now); err != nil {
		return err
	}
	state, err := handler.loadRoomState(tx, roomID, now)
	if err != nil {
		return err
	}
	view, err := state.room.SnapshotForRoom(state.input, userID)
	if err != nil {
		return err
	}
	sequence, err := tx.NextEventSequence(roomID)
	if err != nil {
		return err
	}
	return tx.SaveRecipientEvent(state.room, userID, sequence, "room.state", map[string]any{"room": view})
}

func (handler *Handler) HandleLastSocketClosed(tx service.RoomStore, roomID int64, userID int64, now time.Time) error {
	_, err := handler.rooms.MarkDisconnected(tx, roomID, userID, now)
	return err
}

func (handler *Handler) HandleGatewayStarted(tx service.RoomStore, now time.Time) error {
	roomIDs, err := tx.ActiveRoomIDs()
	if err != nil {
		return err
	}
	for _, roomID := range roomIDs {
		_, members, err := tx.RoomByID(roomID)
		if err != nil {
			return err
		}
		for _, member := range members {
			if _, err := handler.rooms.MarkDisconnected(tx, roomID, member.UserID, now); err != nil {
				return err
			}
		}
	}
	return nil
}
