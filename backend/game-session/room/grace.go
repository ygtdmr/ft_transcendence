package room

import "time"

const lobbyGraceTimeout = 30 * time.Second
const gameGraceTimeout = 60 * time.Second

func (r *Room) MarkDisconnected(input RoomInput, userID int64, now time.Time) (RoomMember, error) {
	if r.Status == StatusClosed {
		return RoomMember{}, ErrRoomNotFound
	}
	member := findMember(input.Members, userID)
	if member == nil {
		return RoomMember{}, ErrNotRoomMember
	}
	if member.GraceEndsAt != nil {
		return *member, nil
	}
	if member.State == StatePostGame {
		return *member, nil
	}

	timeout := lobbyGraceTimeout
	if r.Status == StatusInGame {
		timeout = gameGraceTimeout
	}
	deadline := now.Add(timeout)

	updated := *member
	updated.GraceEndsAt = &deadline
	return updated, nil
}

func (r *Room) MarkReconnected(input RoomInput, userID int64, now time.Time) (RoomMember, error) {
	if r.Status == StatusClosed {
		return RoomMember{}, ErrRoomNotFound
	}
	member := findMember(input.Members, userID)
	if member == nil {
		return RoomMember{}, ErrNotRoomMember
	}
	if member.GraceEndsAt == nil {
		return *member, nil
	}
	if !now.Before(*member.GraceEndsAt) {
		return RoomMember{}, ErrInvalidRoomState
	}

	updated := *member
	updated.GraceEndsAt = nil
	return updated, nil
}
