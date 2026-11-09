package room

import (
	"game-session/game"
	"time"
)

const emptyRoomTimeout = 300 * time.Second

type DepartureReason string

const (
	DepartureExited         DepartureReason = "EXITED"
	DepartureSessionEnded   DepartureReason = "SESSION_ENDED"
	DepartureDisconnected   DepartureReason = "DISCONNECTED"
	DepartureKickedByHost   DepartureReason = "KICKED_BY_HOST"
	DepartureAccountDeleted DepartureReason = "ACCOUNT_DELETED"
)

type Penalty struct {
	UserID int64
	RoomID int64
	GameID int64
	Role   game.Role
	Reason DepartureReason
}

type LeaveResult struct {
	DepartedMember     RoomMember
	Reason             DepartureReason
	CountdownCancelled bool
	HostUserID         *int64
	PostGameCompleted  bool
	GamePaused         bool
	Penalty            *Penalty
}

func (r *Room) hostAfterDeparture(remaining []RoomMember) *int64 {
	if len(remaining) == 0 {
		return nil
	}
	if r.HostUserID != nil {
		for _, member := range remaining {
			if member.UserID == *r.HostUserID {
				return r.HostUserID
			}
		}
	}
	ordered := membersInJoinOrder(remaining)
	for _, member := range ordered {
		if member.State == StateInLobby {
			return copyPtr(&member.UserID)
		}
	}
	return copyPtr(&ordered[0].UserID)
}

func (r *Room) Leave(input RoomInput, userID int64, reason DepartureReason, now time.Time) (LeaveResult, error) {
	if r.Status == StatusClosed {
		return LeaveResult{}, ErrRoomNotFound
	}
	departed := findMember(input.Members, userID)
	if departed == nil {
		return LeaveResult{}, ErrNotRoomMember
	}
	if reason == DepartureDisconnected {
		if departed.GraceEndsAt == nil || now.Before(*departed.GraceEndsAt) {
			return LeaveResult{}, ErrGraceNotExpired
		}
	}
	return r.removeMember(input, *departed, reason, now)
}

func (r *Room) Kick(input RoomInput, hostUserID int64, targetUserID int64, now time.Time) (LeaveResult, error) {
	if r.Status == StatusClosed {
		return LeaveResult{}, ErrRoomNotFound
	}
	if findMember(input.Members, hostUserID) == nil {
		return LeaveResult{}, ErrNotRoomMember
	}
	if r.HostUserID == nil || *r.HostUserID != hostUserID {
		return LeaveResult{}, ErrNotHost
	}
	if hostUserID == targetUserID {
		return LeaveResult{}, ErrCannotKickSelf
	}
	target := findMember(input.Members, targetUserID)
	if target == nil {
		return LeaveResult{}, ErrTargetNotRoomMember
	}
	return r.removeMember(input, *target, DepartureKickedByHost, now)
}

func withoutMember(members []RoomMember, userID int64) []RoomMember {
	remaining := make([]RoomMember, 0, len(members))
	for _, member := range members {
		if member.UserID != userID {
			remaining = append(remaining, member)
		}
	}
	return remaining
}

func (r *Room) finishPostGameIfNoPending(remaining []RoomMember, departed RoomMember) bool {
	if departed.State != StatePostGame {
		return false
	}
	if hasPostGamePending(remaining) {
		return false
	}
	r.PostGameEndsAt = nil
	if r.Status == StatusPostGame {
		r.Status = StatusWaiting
	}
	return true
}

func (r *Room) departurePenalty(player game.Player, gameID int64, reason DepartureReason) *Penalty {
	if reason != DepartureExited && reason != DepartureSessionEnded {
		return nil
	}

	return &Penalty{
		UserID: player.UserID,
		RoomID: r.RoomID,
		GameID: gameID,
		Role:   player.Role,
		Reason: reason,
	}
}

func (r *Room) handleGameDeparture(input RoomInput, remaining []RoomMember, departed RoomMember, reason DepartureReason, now time.Time) (LeaveResult, error) {
	result := LeaveResult{
		DepartedMember: departed,
		Reason:         reason,
	}

	if r.Status != StatusInGame || departed.Team == nil || departed.Role == nil {
		return result, nil
	}
	if input.CurrentGame == nil {
		return LeaveResult{}, ErrGameNotFound
	}
	player := game.Player{UserID: departed.UserID, Username: departed.Username, Team: *departed.Team, Role: *departed.Role}
	if err := sendTeamStatusToGame(input.CurrentGame, remaining, &player, now); err != nil {
		return LeaveResult{}, err
	}
	result.GamePaused = input.CurrentGame.IsPaused()
	result.Penalty = r.departurePenalty(player, input.CurrentGame.GameID(), reason)

	return result, nil
}

func (r *Room) removeMember(input RoomInput, departed RoomMember, reason DepartureReason, now time.Time,
) (LeaveResult, error) {
	remaining := withoutMember(input.Members, departed.UserID)

	result, err := r.handleGameDeparture(input, remaining, departed, reason, now)
	if err != nil {
		return LeaveResult{}, err
	}

	result.CountdownCancelled = r.cancelCountDown()
	result.PostGameCompleted = r.finishPostGameIfNoPending(remaining, departed)

	r.HostUserID = r.hostAfterDeparture(remaining)
	result.HostUserID = copyPtr(r.HostUserID)

	if len(remaining) == 0 {
		closeAt := now.Add(emptyRoomTimeout)
		r.EmptySince = &now
		r.CloseAt = &closeAt
	}

	return result, nil
}

func (r *Room) CloseIfEmpty(input RoomInput, now time.Time) bool {
	if r.Status == StatusClosed {
		return false
	}
	if len(input.Members) > 0 {
		r.EmptySince = nil
		r.CloseAt = nil
		return false
	}
	if r.CloseAt == nil || now.Before(*r.CloseAt) {
		return false
	}
	r.Status = StatusClosed
	return true
}
