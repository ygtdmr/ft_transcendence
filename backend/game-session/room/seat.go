package room

import (
	"game-session/game"
	"time"
)

type SeatResult struct {
	Member             RoomMember
	CountdownCancelled bool
	GameResumed        bool
}

func hasOtherSpymaster(members []RoomMember, team game.Team, userID int64) bool {
	for _, member := range members {
		if member.UserID == userID || member.Team == nil || member.Role == nil {
			continue
		}
		if *member.Team == team && *member.Role == game.RoleSpymaster {
			return true
		}
	}
	return false
}

func (r *Room) SelectRole(input RoomInput, userID int64, team game.Team, role game.Role, now time.Time) (SeatResult, error) {
	if team == "" {
		return SeatResult{}, ErrTeamRequired
	}
	if (team != game.TeamRed && team != game.TeamBlue) ||
		!(role == game.RoleSpymaster || role == game.RoleOperative) {
		return SeatResult{}, ErrInvalidPayload
	}
	member := findMember(input.Members, userID)
	if member == nil {
		return SeatResult{}, ErrNotRoomMember
	}
	return r.takeSeat(input, *member, team, role, now)
}

func (r *Room) SelectTeam(input RoomInput, userID int64, team game.Team, now time.Time) (SeatResult, error) {
	if team != game.TeamRed && team != game.TeamBlue {
		return SeatResult{}, ErrInvalidPayload
	}
	member := findMember(input.Members, userID)
	if member == nil {
		return SeatResult{}, ErrNotRoomMember
	}
	if member.Role == nil {
		return SeatResult{}, ErrRoleRequired
	}
	return r.takeSeat(input, *member, team, *member.Role, now)
}

func (r *Room) takeSeatInLobby(member RoomMember, team game.Team, role game.Role) SeatResult {
	countdownCancelled := r.cancelCountDown()
	member.Team = &team
	member.Role = &role
	member.Ready = false
	return SeatResult{Member: member, CountdownCancelled: countdownCancelled}
}

func (r *Room) takeSeatInGame(input RoomInput, member RoomMember, team game.Team, role game.Role, now time.Time) (SeatResult, error) {
	if input.CurrentGame == nil {
		return SeatResult{}, ErrGameNotFound
	}
	if !input.CurrentGame.IsPaused() {
		return SeatResult{}, ErrInvalidRoomState
	}
	if member.Team != nil || member.Role != nil {
		return SeatResult{}, ErrInvalidRoomState
	}

	member.Team = &team
	member.Role = &role
	member.Ready = false

	members := replaceMember(input.Members, member)
	if err := sendTeamStatusToGame(input.CurrentGame, members, nil, now); err != nil {
		return SeatResult{}, err
	}
	return SeatResult{Member: member, GameResumed: !input.CurrentGame.IsPaused()}, nil
}

func (r *Room) takeSeat(input RoomInput, member RoomMember, team game.Team, role game.Role, now time.Time) (SeatResult, error) {
	if r.Status == StatusClosed {
		return SeatResult{}, ErrRoomNotFound
	}
	if r.Status == StatusPostGame {
		return SeatResult{}, ErrInvalidRoomState
	}

	if member.Team != nil && member.Role != nil &&
		*member.Team == team && *member.Role == role {
		return SeatResult{Member: member}, nil
	}

	if role == game.RoleSpymaster && hasOtherSpymaster(input.Members, team, member.UserID) {
		return SeatResult{}, ErrRoleConflict
	}

	if r.Status == StatusInGame {
		return r.takeSeatInGame(input, member, team, role, now)
	}
	return r.takeSeatInLobby(member, team, role), nil
}
