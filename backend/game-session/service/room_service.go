package service

import (
	"crypto/rand"
	"errors"
	"strings"
	"time"

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

func (s *RoomService) validateRoomCreation(tx RoomStore, userID int64, maxPlayers *int) (int, error) {
	capacity := 20
	if maxPlayers != nil {
		capacity = *maxPlayers
	}
	if capacity < 4 || capacity > 20 {
		return 0, room.ErrInvalidPayload
	}

	_, hasActiveRoom, err := tx.ActiveRoomOf(userID)
	if err != nil {
		return 0, err
	}
	if hasActiveRoom {
		return 0, room.ErrAlreadyInRoom
	}

	return capacity, nil
}

func saveRoomWithUniqueCode(tx RoomStore, r *room.Room, host room.RoomMember) error {
	for attempt := 0; attempt < 5; attempt++ {
		code, err := newRoomCode()
		if err != nil {
			return err
		}
		r.RoomCode = code

		err = tx.SaveNewRoom(r, host)
		if errors.Is(err, ErrRoomCodeTaken) {
			continue
		}
		return err
	}

	return ErrRoomCodeTaken
}

func (s *RoomService) Create(tx RoomStore, userID int64, maxPlayers *int, now time.Time) (room.RoomSnapshot, error) {
	capacity, err := s.validateRoomCreation(tx, userID, maxPlayers)
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
	if err := saveRoomWithUniqueCode(tx, r, host); err != nil {
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

func (s *RoomService) FindRoomByCode(tx RoomStore, code string) (RoomSummary, error) {
	r, members, err := tx.RoomByCode(strings.ToUpper(code))
	if err != nil {
		return RoomSummary{}, err
	}
	currentGame, err := tx.LoadCurrentGame(r.RoomID)
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

func (s *RoomService) CloseIfEmpty(tx RoomStore, roomID int64, now time.Time) (bool, error) {
	r, members, err := tx.RoomByID(roomID)
	if err != nil {
		return false, err
	}
	closed := r.CloseIfEmpty(room.RoomInput{Members: members}, now)
	if err := tx.SaveRoom(r); err != nil {
		return false, err
	}
	return closed, nil
}

func (s *RoomService) Join(tx RoomStore, roomID int64, userID int64, now time.Time) (room.RoomSnapshot, error) {
	_, cleanedUp, err := tx.LookupAccountCleanup(userID)
	if err != nil {
		return room.RoomSnapshot{}, err
	}
	if cleanedUp {
		return room.RoomSnapshot{}, ErrAccountCleanedUp
	}

	profiles, err := s.profiles.ProfilesByUserIDs([]int64{userID})
	if err != nil {
		return room.RoomSnapshot{}, err
	}
	profile, ok := profiles[userID]
	if !ok {
		return room.RoomSnapshot{}, room.ErrMemberProfileNotFound
	}

	r, members, currentGame, err := loadRoom(tx, roomID)
	if err != nil {
		return room.RoomSnapshot{}, err
	}
	_, hasActiveRoom, err := tx.ActiveRoomOf(userID)
	if err != nil {
		return room.RoomSnapshot{}, err
	}
	if hasActiveRoom {
		return room.RoomSnapshot{}, room.ErrAlreadyInRoom
	}

	member, err := r.Join(room.RoomInput{Members: members, CurrentGame: currentGame}, userID, now)
	if err != nil {
		return room.RoomSnapshot{}, err
	}
	member.Username = profile.Username
	if err := tx.SaveJoin(r, member); err != nil {
		return room.RoomSnapshot{}, err
	}

	members = append(members, member)
	return s.snapshot(r, members, room.RoomInput{CurrentGame: currentGame}, userID, now)
}

func (s *RoomService) Snapshot(tx RoomStore, roomID int64, userID int64, now time.Time) (room.RoomSnapshot, error) {
	r, members, currentGame, err := loadRoom(tx, roomID)
	if err != nil {
		return room.RoomSnapshot{}, err
	}
	lastGame, err := tx.LoadLastGameResult(roomID)
	if err != nil {
		return room.RoomSnapshot{}, err
	}
	return s.snapshot(r, members, room.RoomInput{CurrentGame: currentGame, LastGame: lastGame}, userID, now)
}

func (s *RoomService) Leave(tx RoomStore, roomID int64, userID int64, reason room.DepartureReason, now time.Time) (room.LeaveResult, error) {
	r, members, currentGame, err := loadRoom(tx, roomID)
	if err != nil {
		return room.LeaveResult{}, err
	}
	result, err := r.Leave(room.RoomInput{Members: members, CurrentGame: currentGame}, userID, reason, now)
	if err != nil {
		return room.LeaveResult{}, err
	}
	if result.Penalty != nil {
		if s.penalty == nil {
			result.Penalty = nil
		} else {
			points, err := s.penalty.Points(result.Penalty.Role)
			if err != nil {
				return room.LeaveResult{}, err
			}
			result.Penalty.Points = points
			result.Penalty.PenalizedAt = now
		}
	}

	gameToSave := currentGame
	if !result.GamePaused {
		gameToSave = nil
	}
	if err := tx.SaveLeave(r, userID, gameToSave, result.Penalty); err != nil {
		return room.LeaveResult{}, err
	}
	return result, nil
}

func (s *RoomService) ActiveRoom(tx RoomStore, userID int64) (int64, bool, error) {
	return tx.ActiveRoomOf(userID)
}
