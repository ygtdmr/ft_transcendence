package room

import (
	"game-session/game"
	"time"
)

const postGameTimeout = 60 * time.Second

type PostGameView struct {
	DeadlineAt     time.Time `json:"deadline_at"`
	PendingUserIDs []int64   `json:"pending_user_ids"`
}

func (r *Room) postGame(members []RoomMember) *PostGameView {
	if r.PostGameEndsAt == nil {
		return nil
	}
	var pendingUserIDs []int64
	for _, member := range members {
		if member.State == StatePostGame {
			pendingUserIDs = append(pendingUserIDs, member.UserID)
		}
	}
	if len(pendingUserIDs) == 0 {
		return nil
	}
	return &PostGameView{
		DeadlineAt:     *r.PostGameEndsAt,
		PendingUserIDs: pendingUserIDs,
	}
}

type GameEndResult struct {
	Members    []RoomMember
	LastGame   *LastGameView
	RoomClosed bool
}

func (r *Room) HandleGameEnd(input RoomInput, now time.Time) (GameEndResult, error) {
	if r.Status != StatusInGame {
		return GameEndResult{}, ErrInvalidRoomState
	}
	if input.CurrentGame == nil {
		return GameEndResult{}, ErrGameNotFound
	}
	status := input.CurrentGame.Status()
	if status == game.GameStatusInProgress {
		return GameEndResult{}, ErrInvalidRoomState
	}
	if status == game.GameStatusCancelled {
		r.Status = StatusClosed
		return GameEndResult{RoomClosed: true}, nil
	}

	lastGame, err := prepareLastGameView(r.RoomID, input.CurrentGame, input.Members, now)
	if err != nil {
		return GameEndResult{}, err
	}

	deadline := now.Add(postGameTimeout)
	r.Status = StatusPostGame
	r.PostGameEndsAt = &deadline

	return GameEndResult{
		Members:  membersWithState(input.Members, StatePostGame, false),
		LastGame: lastGame,
	}, nil
}

type ReturnResult struct {
	Member            RoomMember
	PostGameCompleted bool
}

func (r *Room) validateReturn(members []RoomMember, userID int64, now time.Time) (*RoomMember, error) {
	if r.Status == StatusClosed {
		return nil, ErrRoomNotFound
	}
	member := findMember(members, userID)
	if member == nil {
		return nil, ErrNotRoomMember
	}
	if member.State != StatePostGame {
		return nil, ErrInvalidRoomState
	}
	if r.PostGameEndsAt != nil && !now.Before(*r.PostGameEndsAt) {
		return nil, ErrPostGameDeadlineExpired
	}
	return member, nil
}

func (r *Room) ReturnToLobby(input RoomInput, userID int64, now time.Time) (ReturnResult, error) {
	member, err := r.validateReturn(input.Members, userID, now)
	if err != nil {
		return ReturnResult{}, err
	}

	updated := *member
	updated.State = StateInLobby
	updated.Ready = false

	completed := !hasPostGamePending(withoutMember(input.Members, userID))
	if completed {
		r.PostGameEndsAt = nil
	}
	r.Status = StatusWaiting
	return ReturnResult{
		Member:            updated,
		PostGameCompleted: completed,
	}, nil
}

type PostGameTimeoutResult struct {
	RemovedUserIDs []int64
}

func (r *Room) ExpirePostGame(input RoomInput, now time.Time) (PostGameTimeoutResult, error) {
	if r.PostGameEndsAt == nil || now.Before(*r.PostGameEndsAt) {
		return PostGameTimeoutResult{}, nil
	}

	var removed []int64
	remaining := make([]RoomMember, 0, len(input.Members))
	for _, current := range input.Members {
		if current.State == StatePostGame {
			removed = append(removed, current.UserID)
		} else {
			remaining = append(remaining, current)
		}
	}

	r.PostGameEndsAt = nil
	r.Status = StatusWaiting
	r.HostUserID = r.hostAfterDeparture(remaining)
	if len(remaining) == 0 {
		closeAt := now.Add(emptyRoomTimeout)
		r.EmptySince = &now
		r.CloseAt = &closeAt
	}
	return PostGameTimeoutResult{RemovedUserIDs: removed}, nil
}
