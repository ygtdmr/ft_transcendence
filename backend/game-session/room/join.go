package room

import (
	"time"
)

func (r *Room) canJoin(input RoomInput) error {
	switch r.Status {
	case StatusWaiting:
		if hasPostGamePending(input.Members) {
			return ErrRoomNotJoinable
		}
	case StatusInGame:
		if input.CurrentGame == nil {
			return ErrGameNotFound
		}
		if !input.CurrentGame.IsPaused() {
			return ErrRoomNotJoinable
		}
	case StatusCountDown, StatusPostGame:
		return ErrRoomNotJoinable
	case StatusClosed:
		return ErrRoomNotFound
	default:
		return ErrInvalidRoomState
	}
	if len(input.Members) >= r.MaxPlayers {
		return ErrRoomFull
	}
	return nil
}

func (r *Room) Join(input RoomInput, userID int64, now time.Time) (RoomMember, error) {
	if err := r.canJoin(input); err != nil {
		return RoomMember{}, err
	}
	if findMember(input.Members, userID) != nil {
		return RoomMember{}, ErrAlreadyInRoom
	}

	state := StateInLobby
	if r.Status == StatusInGame {
		state = StateInGame
	}

	if len(input.Members) == 0 {
		r.HostUserID = copyPtr(&userID)
		r.EmptySince = nil
		r.CloseAt = nil
	}

	return RoomMember{
		RoomID:   r.RoomID,
		UserID:   userID,
		State:    state,
		Team:     nil,
		Role:     nil,
		Ready:    false,
		JoinedAt: now,
	}, nil
}

func (r *Room) IsJoinable(input RoomInput) bool {
	return r.canJoin(input) == nil
}
