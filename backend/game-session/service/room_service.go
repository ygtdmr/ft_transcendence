package service

import (
	"crypto/rand"
	"errors"
	"strings"
	"time"

	room "game-session/room"
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
	if err := s.saveRoomWithUniqueCode(r, host); err != nil {
		return room.RoomSnapshot{}, err
	}

	host.RoomID = r.RoomID
	return s.snapshot(r, []room.RoomMember{host}, room.RoomInput{}, userID, now)
}

type RoomSummary struct {
	RoomID      int64
	RoomCode    string
	Status      room.Status
	PlayerCount int
	MaxPlayers  int
}

func (s *RoomService) FindRoomByCode(code string) (RoomSummary, error) {
	r, members, err := s.store.RoomByCode(strings.ToUpper(code))
	if err != nil {
		return RoomSummary{}, err
	}
	return RoomSummary{
		RoomID:      r.RoomID,
		RoomCode:    r.RoomCode,
		Status:      r.Status,
		PlayerCount: len(members),
		MaxPlayers:  r.MaxPlayers,
	}, nil
}

func (s *RoomService) Join(roomID int64, userID int64, now time.Time) (room.RoomSnapshot, error) {
	var snapshot room.RoomSnapshot
	err := s.store.Transaction(func(tx RoomStore) error {
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
	r, members, currentGame, err := loadRoom(s.store, roomID)
	if err != nil {
		return room.RoomSnapshot{}, err
	}
	lastGame, err := s.store.LoadLastGameResult(roomID)
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
		profiles, err := s.loadDepartedProfile(r, members, userID)
		if err != nil {
			return err
		}

		result, err = r.Leave(room.RoomInput{Members: members, CurrentGame: currentGame, Profiles: profiles}, userID, reason, now)
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
