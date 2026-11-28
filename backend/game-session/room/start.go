package room

import (
	"game-session/game"
	"time"
)

func areTeamsComplete(members []RoomMember) bool {
	redMissing := missingRoles(members, game.TeamRed)
	blueMissing := missingRoles(members, game.TeamBlue)
	return len(redMissing) == 0 && len(blueMissing) == 0
}

func (r *Room) isStartableRoom(members []RoomMember) bool {
	if r.Status != StatusWaiting && r.Status != StatusCountDown {
		return false
	}
	for _, player := range members {
		if player.Team == nil || player.Role == nil {
			return false
		}
		if !player.Ready || player.State != StateInLobby {
			return false
		}
	}
	return areTeamsComplete(members)
}

type ReadyResult struct {
	Member             RoomMember
	CountdownStarted   bool
	CountdownCancelled bool
}

func (r *Room) validateReady(members []RoomMember, userID int64, ready bool) (*RoomMember, error) {
	if r.Status == StatusClosed {
		return nil, ErrRoomNotFound
	}
	if r.Status != StatusWaiting && r.Status != StatusCountDown {
		return nil, ErrInvalidRoomState
	}
	member := findMember(members, userID)
	if member == nil {
		return nil, ErrNotRoomMember
	}
	if member.State != StateInLobby {
		return nil, ErrInvalidRoomState
	}
	if ready {
		if r.Status == StatusCountDown {
			return nil, ErrInvalidRoomState
		}
		if member.Team == nil {
			return nil, ErrTeamRequired
		}
		if member.Role == nil {
			return nil, ErrRoleRequired
		}
		if hasPostGamePending(members) {
			return nil, ErrPostGamePending
		}
	}
	return member, nil
}

func (r *Room) SetReady(input RoomInput, userID int64, ready bool, now time.Time) (ReadyResult, error) {
	member, err := r.validateReady(input.Members, userID, ready)
	if err != nil {
		return ReadyResult{}, err
	}

	updated := *member
	updated.Ready = ready
	result := ReadyResult{Member: updated}

	if !ready {
		result.CountdownCancelled = r.cancelCountDown()
		return result, nil
	}

	members := replaceMember(input.Members, updated)
	if r.isStartableRoom(members) {
		r.startCountDown(now)
		result.CountdownStarted = true
	}
	return result, nil
}

func (r *Room) gameSettings(gameID int64) game.GameSettings {
	var turnTimer *time.Duration
	if r.TurnTimerSeconds != nil {
		timer := time.Duration(*r.TurnTimerSeconds) * time.Second
		turnTimer = &timer
	}

	return game.GameSettings{
		Language:        r.Language,
		TurnTimer:       turnTimer,
		StaffingTimeout: staffingTimeout,
		GameID:          gameID,
	}
}

func (r *Room) StartGame(input RoomInput, wordPool []game.Word, gameID int64, now time.Time) (*game.Game, []RoomMember, error) {
	if r.Status == StatusClosed {
		return nil, nil, ErrRoomNotFound
	}
	if r.Status != StatusCountDown || r.CountdownEndsAt == nil {
		return nil, nil, ErrInvalidRoomState
	}
	if now.Before(*r.CountdownEndsAt) {
		return nil, nil, ErrInvalidRoomState
	}
	if !r.isStartableRoom(input.Members) {
		return nil, nil, ErrRoomNotStartable
	}

	newGame, err := game.NewGame(wordPool, r.gameSettings(gameID), now)
	if err != nil {
		return nil, nil, err
	}

	r.Status = StatusInGame
	r.CountdownEndsAt = nil

	return newGame, membersWithState(input.Members, StateInGame, true), nil
}
