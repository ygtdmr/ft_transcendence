package service

import (
	"time"

	"game-session/room"
)

func (s *RoomService) MarkDisconnected(roomID int64, userID int64, now time.Time) (room.RoomMember, error) {
	var member room.RoomMember
	err := s.store.Transaction(func(tx RoomStore) error {
		r, members, err := tx.RoomByID(roomID)
		if err != nil {
			return err
		}
		member, err = r.MarkDisconnected(room.RoomInput{Members: members}, userID, now)
		if err != nil {
			return err
		}
		return tx.SaveMember(r, member)
	})
	if err != nil {
		return room.RoomMember{}, err
	}
	return member, nil
}

func (s *RoomService) MarkReconnected(roomID int64, userID int64, now time.Time) (room.RoomMember, error) {
	var member room.RoomMember
	err := s.store.Transaction(func(tx RoomStore) error {
		r, members, err := tx.RoomByID(roomID)
		if err != nil {
			return err
		}
		member, err = r.MarkReconnected(room.RoomInput{Members: members}, userID, now)
		if err != nil {
			return err
		}
		return tx.SaveMember(r, member)
	})
	if err != nil {
		return room.RoomMember{}, err
	}
	return member, nil
}
