package service

import (
	"crypto/rand"
	"errors"
	"strings"
	"time"

	"game-session/game"
	"game-session/room"
)

func newRoomCode() (string, error) {
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	code := make([]byte, 6)

	if _, err := rand.Read(code); err != nil {
		return "", err
	}
	for i, value := range code {
		code[i] = alphabet[int(value)%len(alphabet)]
	}
	return string(code), nil
}

func (s *RoomService) snapshot(r *room.Room, members []room.RoomMember, input room.RoomInput, userID int64, now time.Time) (room.RoomSnapshot, error) {
	profiles, err := s.loadMemberProfiles(members)
	if err != nil {
		return room.RoomSnapshot{}, err
	}
	input.Members = members
	input.Profiles = profiles
	input.Countdown = r.Countdown(now)
	return r.SnapshotForRoom(input, userID)
}

func (s *RoomService) validateRoomCreation(userID int64, maxPlayers *int) (int, error) {
	capacity := 20
	if maxPlayers != nil {
		capacity = *maxPlayers
	}
	if capacity < 4 || capacity > 20 {
		return 0, room.ErrInvalidPayload
	}

	_, hasActiveRoom, err := s.store.ActiveRoomOf(userID)
	if err != nil {
		return 0, err
	}
	if hasActiveRoom {
		return 0, room.ErrAlreadyInRoom
	}

	return capacity, nil
}

func (s *RoomService) saveRoomWithUniqueCode(r *room.Room, host room.RoomMember) error {
	for attempt := 0; attempt < 5; attempt++ {
		code, err := newRoomCode()
		if err != nil {
			return err
		}
		r.RoomCode = code

		err = s.store.SaveNewRoom(r, host)
		if errors.Is(err, ErrRoomCodeTaken) {
			continue
		}
		return err
	}

	return ErrRoomCodeTaken
}

func (s *RoomService) Create(userID int64, maxPlayers *int, now time.Time) (room.RoomSnapshot, error) {
	capacity, err := s.validateRoomCreation(userID, maxPlayers)
	if err != nil {
		return room.RoomSnapshot{}, err
	}
	profiles, err := s.profiles.ProfilesByUserIDs([]int64{userID})
	if err != nil {
		return room.RoomSnapshot{}, err
	}
	if _, ok := profiles[userID]; !ok {
		return room.RoomSnapshot{}, room.ErrMemberProfileNotFound
	}

	r := &room.Room{
		Status:     room.StatusWaiting,
		MaxPlayers: capacity,
		Language:   "en",
		CreatedAt:  now,
	}
	host, err := r.Join(room.RoomInput{}, userID, now)
	if err != nil {
		return room.RoomSnapshot{}, err
	}
	host.Username = profiles[userID].Username
	if err := s.saveRoomWithUniqueCode(r, host); err != nil {
		return room.RoomSnapshot{}, err
	}

	host.RoomID = r.RoomID
	return r.SnapshotForRoom(room.RoomInput{
		Members:  []room.RoomMember{host},
		Profiles: profiles,
	}, userID)
}

type RoomSummary struct {
	RoomID      int64       `json:"room_id"`
	RoomCode    string      `json:"room_code"`
	Status      room.Status `json:"status"`
	PlayerCount int         `json:"player_count"`
	MaxPlayers  int         `json:"max_players"`
	Joinable    bool        `json:"joinable"`
}

func (s *RoomService) FindRoomByCode(code string) (RoomSummary, error) {
	var (
		r           *room.Room
		members     []room.RoomMember
		currentGame *game.Game
	)
	err := s.store.ReadTransaction(func(tx RoomStore) error {
		var err error
		r, members, err = tx.RoomByCode(strings.ToUpper(code))
		if err != nil {
			return err
		}
		currentGame, err = tx.LoadCurrentGame(r.RoomID)
		return err
	})
	if err != nil {
		return RoomSummary{}, err
	}
	return RoomSummary{
		RoomID:      r.RoomID,
		RoomCode:    r.RoomCode,
		Status:      r.Status,
		PlayerCount: len(members),
		MaxPlayers:  r.MaxPlayers,
		Joinable:    r.IsJoinable(room.RoomInput{Members: members, CurrentGame: currentGame}),
	}, nil
}

func (s *RoomService) CloseIfEmpty(roomID int64, now time.Time) (bool, error) {
	closed := false
	err := s.store.Transaction(func(tx RoomStore) error {
		r, members, err := tx.RoomByID(roomID)
		if err != nil {
			return err
		}
		closed = r.CloseIfEmpty(room.RoomInput{Members: members}, now)
		return tx.SaveRoom(r)
	})
	if err != nil {
		return false, err
	}
	return closed, nil
}

func (s *RoomService) Join(roomID int64, userID int64, now time.Time) (room.RoomSnapshot, error) {
	var snapshot room.RoomSnapshot
	profiles, err := s.profiles.ProfilesByUserIDs([]int64{userID})
	if err != nil {
		return room.RoomSnapshot{}, err
	}

	profile, ok := profiles[userID]
	if !ok {
		return room.RoomSnapshot{}, room.ErrMemberProfileNotFound
	}
	err = s.store.Transaction(func(tx RoomStore) error {
		r, members, currentGame, err := loadRoom(tx, roomID)
		if err != nil {
			return err
		}
		_, hasActiveRoom, err := tx.ActiveRoomOf(userID)
		if err != nil {
			return err
		}
		if hasActiveRoom {
			return room.ErrAlreadyInRoom
		}

		member, err := r.Join(room.RoomInput{Members: members, CurrentGame: currentGame}, userID, now)
		if err != nil {
			return err
		}
		member.Username = profile.Username
		if err := tx.SaveJoin(r, member); err != nil {
			return err
		}

		members = append(members, member)
		snapshot, err = s.snapshot(r, members, room.RoomInput{CurrentGame: currentGame}, userID, now)
		return err
	})
	if err != nil {
		return room.RoomSnapshot{}, err
	}
	return snapshot, nil
}

func (s *RoomService) Snapshot(roomID int64, userID int64, now time.Time) (room.RoomSnapshot, error) {
	var (
		r           *room.Room
		members     []room.RoomMember
		currentGame *game.Game
		lastGame    *room.LastGameView
	)
	err := s.store.ReadTransaction(func(tx RoomStore) error {
		var err error
		r, members, currentGame, err = loadRoom(tx, roomID)
		if err != nil {
			return err
		}
		lastGame, err = tx.LoadLastGameResult(roomID)
		return err
	})
	if err != nil {
		return room.RoomSnapshot{}, err
	}
	return s.snapshot(r, members, room.RoomInput{CurrentGame: currentGame, LastGame: lastGame}, userID, now)
}

func (s *RoomService) Leave(roomID int64, userID int64, reason room.DepartureReason, now time.Time) (room.LeaveResult, error) {
	var result room.LeaveResult
	err := s.store.Transaction(func(tx RoomStore) error {
		r, members, currentGame, err := loadRoom(tx, roomID)
		if err != nil {
			return err
		}
		result, err = r.Leave(room.RoomInput{Members: members, CurrentGame: currentGame}, userID, reason, now)
		if err != nil {
			return err
		}

		gameToSave := currentGame
		if !result.GamePaused {
			gameToSave = nil
		}
		return tx.SaveLeave(r, userID, gameToSave, result.Penalty)
	})
	if err != nil {
		return room.LeaveResult{}, err
	}
	return result, nil
}
