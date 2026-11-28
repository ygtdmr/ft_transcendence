package service

import (
	"time"

	"game-session/room"
)

func (s *RoomService) MarkDisconnected(tx RoomStore, roomID int64, userID int64, now time.Time) (room.RoomMember, error) {
	r, members, err := tx.RoomByID(roomID)
	if err != nil {
		return room.RoomMember{}, err
	}
	member, err := r.MarkDisconnected(room.RoomInput{Members: members}, userID, now)
	if err != nil {
		return room.RoomMember{}, err
	}
	if err := tx.SaveMember(r, member); err != nil {
		return room.RoomMember{}, err
	}
	return member, nil
}

func (s *RoomService) MarkReconnected(tx RoomStore, roomID int64, userID int64, now time.Time) (room.RoomMember, error) {
	r, members, err := tx.RoomByID(roomID)
	if err != nil {
		return room.RoomMember{}, err
	}
	member, err := r.MarkReconnected(room.RoomInput{Members: members}, userID, now)
	if err != nil {
		return room.RoomMember{}, err
	}
	if err := tx.SaveMember(r, member); err != nil {
		return room.RoomMember{}, err
	}
	return member, nil
}
