package service

import (
	"errors"

	"game-session/game"
	"game-session/room"
)

var ErrRoomCodeTaken = errors.New("room code already in use")

type RoomStore interface {
	Transaction(fn func(tx RoomStore) error) error
	ReadTransaction(fn func(tx RoomStore) error) error

	RoomByCode(code string) (*room.Room, []room.RoomMember, error)
	RoomByID(roomID int64) (*room.Room, []room.RoomMember, error)
	LoadCurrentGame(roomID int64) (*game.Game, error)
	LoadLastGameResult(roomID int64) (*room.LastGameView, error)
	ActiveRoomOf(userID int64) (int64, bool, error)
	NextGameID() (int64, error)

	SaveNewRoom(r *room.Room, host room.RoomMember) error
	SaveJoin(r *room.Room, member room.RoomMember) error
	SaveMember(r *room.Room, member room.RoomMember) error
	SaveSeatInGame(r *room.Room, member room.RoomMember, g *game.Game) error
	SaveRoom(r *room.Room) error
	SaveLeave(r *room.Room, departedUserID int64, g *game.Game, penalty *room.Penalty) error
	SaveGameStart(r *room.Room, members []room.RoomMember, g *game.Game) error
	SaveGame(g *game.Game) error
	SaveGameEnd(r *room.Room, members []room.RoomMember, g *game.Game, lastGame *room.LastGameView) error
	SaveExpiredPostGame(r *room.Room, removedUserIDs []int64) error

	NextEventSequence(roomID int64) (int64, error)
	SaveRecipientEvent(r *room.Room, recipientUserID int64, sequence int64, eventType string, payload any) error

	LookupCommandResult(roomID int64, sessionID string, requestID string) (commandText string, response []byte, found bool, err error)
	SaveCommandResult(roomID int64, sessionID string, requestID string, commandText string, response []byte) error
	DeleteCommandResults(roomID int64) error
}

var ErrProfileSourceUnavailable = errors.New("user-service unavailable")

type ProfileSource interface {
	ProfilesByUserIDs(userIDs []int64) (map[int64]room.Profile, error)
}

type RoomService struct {
	profiles ProfileSource
	penalty  *room.PenaltyConfig
}

func NewRoomService(profiles ProfileSource, penalty *room.PenaltyConfig) *RoomService {
	return &RoomService{profiles: profiles, penalty: penalty}
}

func loadRoom(tx RoomStore, roomID int64) (*room.Room, []room.RoomMember, *game.Game, error) {
	r, members, err := tx.RoomByID(roomID)
	if err != nil {
		return nil, nil, nil, err
	}
	currentGame, err := tx.LoadCurrentGame(roomID)
	if err != nil {
		return nil, nil, nil, err
	}
	return r, members, currentGame, nil
}

func (s *RoomService) loadMemberProfiles(members []room.RoomMember) (map[int64]room.Profile, error) {
	ids := make([]int64, 0, len(members))
	for _, member := range members {
		ids = append(ids, member.UserID)
	}
	return s.profiles.ProfilesByUserIDs(ids)
}
