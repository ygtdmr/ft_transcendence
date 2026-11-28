package service

import (
	"time"

	"game-session/game"
	"game-session/room"
)

func (s *RoomService) SelectRole(tx RoomStore, roomID int64, userID int64, team game.Team, role game.Role, now time.Time) (room.SeatResult, error) {
	r, members, currentGame, err := loadRoom(tx, roomID)
	if err != nil {
		return room.SeatResult{}, err
	}
	result, err := r.SelectRole(room.RoomInput{Members: members, CurrentGame: currentGame}, userID, team, role, now)
	if err != nil {
		return room.SeatResult{}, err
	}
	if r.Status == room.StatusInGame {
		err = tx.SaveSeatInGame(r, result.Member, currentGame)
	} else {
		err = tx.SaveMember(r, result.Member)
	}
	if err != nil {
		return room.SeatResult{}, err
	}
	return result, nil
}

func (s *RoomService) SelectTeam(tx RoomStore, roomID int64, userID int64, team game.Team, now time.Time) (room.SeatResult, error) {
	r, members, currentGame, err := loadRoom(tx, roomID)
	if err != nil {
		return room.SeatResult{}, err
	}
	result, err := r.SelectTeam(room.RoomInput{Members: members, CurrentGame: currentGame}, userID, team, now)
	if err != nil {
		return room.SeatResult{}, err
	}
	if r.Status == room.StatusInGame {
		err = tx.SaveSeatInGame(r, result.Member, currentGame)
	} else {
		err = tx.SaveMember(r, result.Member)
	}
	if err != nil {
		return room.SeatResult{}, err
	}
	return result, nil
}

func (s *RoomService) SetReady(tx RoomStore, roomID int64, userID int64, ready bool, now time.Time) (room.ReadyResult, error) {
	r, members, err := tx.RoomByID(roomID)
	if err != nil {
		return room.ReadyResult{}, err
	}
	result, err := r.SetReady(room.RoomInput{Members: members}, userID, ready, now)
	if err != nil {
		return room.ReadyResult{}, err
	}
	if err := tx.SaveMember(r, result.Member); err != nil {
		return room.ReadyResult{}, err
	}
	return result, nil
}

func (s *RoomService) UpdateSettings(tx RoomStore, roomID int64, userID int64, update room.SettingsUpdate) (room.RoomSettings, error) {
	r, members, err := tx.RoomByID(roomID)
	if err != nil {
		return room.RoomSettings{}, err
	}
	settings, err := r.UpdateSettings(room.RoomInput{Members: members}, userID, update)
	if err != nil {
		return room.RoomSettings{}, err
	}
	if err := tx.SaveRoom(r); err != nil {
		return room.RoomSettings{}, err
	}
	return settings, nil
}

func (s *RoomService) Kick(tx RoomStore, roomID int64, hostUserID int64, targetUserID int64, now time.Time) (room.LeaveResult, error) {
	r, members, currentGame, err := loadRoom(tx, roomID)
	if err != nil {
		return room.LeaveResult{}, err
	}
	result, err := r.Kick(room.RoomInput{Members: members, CurrentGame: currentGame}, hostUserID, targetUserID, now)
	if err != nil {
		return room.LeaveResult{}, err
	}
	gameToSave := currentGame
	if !result.GamePaused {
		gameToSave = nil
	}
	if err := tx.SaveLeave(r, targetUserID, gameToSave, nil); err != nil {
		return room.LeaveResult{}, err
	}
	return result, nil
}

func (s *RoomService) ReturnToLobby(tx RoomStore, roomID int64, userID int64, now time.Time) (room.ReturnResult, error) {
	r, members, err := tx.RoomByID(roomID)
	if err != nil {
		return room.ReturnResult{}, err
	}
	result, err := r.ReturnToLobby(room.RoomInput{Members: members}, userID, now)
	if err != nil {
		return room.ReturnResult{}, err
	}
	if err := tx.SaveMember(r, result.Member); err != nil {
		return room.ReturnResult{}, err
	}
	return result, nil
}

func (s *RoomService) StartGame(tx RoomStore, roomID int64, now time.Time) (*game.Game, error) {
	r, members, err := tx.RoomByID(roomID)
	if err != nil {
		return nil, err
	}
	pack, err := game.DefaultWordPack()
	if err != nil {
		return nil, err
	}
	gameID, err := tx.NextGameID()
	if err != nil {
		return nil, err
	}
	newGame, inGame, err := r.StartGame(room.RoomInput{Members: members}, pack.Words, gameID, now)
	if err != nil {
		return nil, err
	}
	if err := tx.SaveGameStart(r, inGame, newGame); err != nil {
		return nil, err
	}
	return newGame, nil
}

func endGame(tx RoomStore, r *room.Room, members []room.RoomMember, currentGame *game.Game, now time.Time) (room.GameEndResult, error) {
	result, err := r.HandleGameEnd(room.RoomInput{Members: members, CurrentGame: currentGame}, now)
	if err != nil {
		return room.GameEndResult{}, err
	}
	if err := tx.SaveGameEnd(r, result.Members, currentGame, result.LastGame); err != nil {
		return room.GameEndResult{}, err
	}
	return result, nil
}

func (s *RoomService) ExpirePostGame(tx RoomStore, roomID int64, now time.Time) (room.PostGameTimeoutResult, error) {
	r, members, err := tx.RoomByID(roomID)
	if err != nil {
		return room.PostGameTimeoutResult{}, err
	}
	result, err := r.ExpirePostGame(room.RoomInput{Members: members}, now)
	if err != nil {
		return room.PostGameTimeoutResult{}, err
	}
	if len(result.RemovedUserIDs) == 0 {
		return result, nil
	}
	if err := tx.SaveExpiredPostGame(r, result.RemovedUserIDs); err != nil {
		return room.PostGameTimeoutResult{}, err
	}
	return result, nil
}
