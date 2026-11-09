package service

import (
	"time"

	"game-session/game"
	"game-session/room"
)

func (s *RoomService) SelectRole(roomID int64, userID int64, team game.Team, role game.Role, now time.Time) (room.SeatResult, error) {
	var result room.SeatResult
	err := s.store.Transaction(func(tx RoomStore) error {
		r, members, currentGame, err := loadRoom(tx, roomID)
		if err != nil {
			return err
		}
		result, err = r.SelectRole(room.RoomInput{Members: members, CurrentGame: currentGame}, userID, team, role, now)
		if err != nil {
			return err
		}
		if r.Status == room.StatusInGame {
			return tx.SaveSeatInGame(r, result.Member, currentGame)
		}
		return tx.SaveMember(r, result.Member)
	})
	if err != nil {
		return room.SeatResult{}, err
	}
	return result, nil
}

func (s *RoomService) SelectTeam(roomID int64, userID int64, team game.Team, now time.Time) (room.SeatResult, error) {
	var result room.SeatResult
	err := s.store.Transaction(func(tx RoomStore) error {
		r, members, currentGame, err := loadRoom(tx, roomID)
		if err != nil {
			return err
		}
		result, err = r.SelectTeam(room.RoomInput{Members: members, CurrentGame: currentGame}, userID, team, now)
		if err != nil {
			return err
		}
		if r.Status == room.StatusInGame {
			err = tx.SaveSeatInGame(r, result.Member, currentGame)
		} else {
			err = tx.SaveMember(r, result.Member)
		}
		return err
	})
	if err != nil {
		return room.SeatResult{}, err
	}
	return result, nil
}

func (s *RoomService) SetReady(roomID int64, userID int64, ready bool, now time.Time) (room.ReadyResult, error) {
	var result room.ReadyResult
	err := s.store.Transaction(func(tx RoomStore) error {
		r, members, err := tx.RoomByID(roomID)
		if err != nil {
			return err
		}
		result, err = r.SetReady(room.RoomInput{Members: members}, userID, ready, now)
		if err != nil {
			return err
		}
		return tx.SaveMember(r, result.Member)
	})
	if err != nil {
		return room.ReadyResult{}, err
	}
	return result, nil
}

func (s *RoomService) UpdateSettings(roomID int64, userID int64, update room.SettingsUpdate) (room.RoomSettings, error) {
	var settings room.RoomSettings
	err := s.store.Transaction(func(tx RoomStore) error {
		r, members, err := tx.RoomByID(roomID)
		if err != nil {
			return err
		}
		settings, err = r.UpdateSettings(room.RoomInput{Members: members}, userID, update)
		if err != nil {
			return err
		}
		return tx.SaveRoom(r)
	})
	if err != nil {
		return room.RoomSettings{}, err
	}
	return settings, nil
}

func (s *RoomService) Kick(roomID int64, hostUserID int64, targetUserID int64, now time.Time) (room.LeaveResult, error) {
	var result room.LeaveResult
	err := s.store.Transaction(func(tx RoomStore) error {
		r, members, currentGame, err := loadRoom(tx, roomID)
		if err != nil {
			return err
		}
		result, err = r.Kick(room.RoomInput{Members: members, CurrentGame: currentGame}, hostUserID, targetUserID, now)
		if err != nil {
			return err
		}
		gameToSave := currentGame
		if !result.GamePaused {
			gameToSave = nil
		}
		return tx.SaveLeave(r, targetUserID, gameToSave, nil)
	})
	if err != nil {
		return room.LeaveResult{}, err
	}
	return result, nil
}

func (s *RoomService) ReturnToLobby(roomID int64, userID int64, now time.Time) (room.ReturnResult, error) {
	var result room.ReturnResult
	err := s.store.Transaction(func(tx RoomStore) error {
		r, members, err := tx.RoomByID(roomID)
		if err != nil {
			return err
		}
		result, err = r.ReturnToLobby(room.RoomInput{Members: members}, userID, now)
		if err != nil {
			return err
		}
		return tx.SaveMember(r, result.Member)
	})
	if err != nil {
		return room.ReturnResult{}, err
	}
	return result, nil
}

func (s *RoomService) StartGame(roomID int64, now time.Time) (*game.Game, error) {
	var newGame *game.Game
	err := s.store.Transaction(func(tx RoomStore) error {
		r, members, err := tx.RoomByID(roomID)
		if err != nil {
			return err
		}
		pack, err := game.DefaultWordPack()
		if err != nil {
			return err
		}
		gameID, err := tx.NextGameID()
		if err != nil {
			return err
		}
		var inGame []room.RoomMember
		newGame, inGame, err = r.StartGame(room.RoomInput{Members: members}, pack.Words, gameID, now)
		if err != nil {
			return err
		}
		return tx.SaveGameStart(r, inGame, newGame)
	})
	if err != nil {
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

func (s *RoomService) ExpirePostGame(roomID int64, now time.Time) (room.PostGameTimeoutResult, error) {
	var result room.PostGameTimeoutResult
	err := s.store.Transaction(func(tx RoomStore) error {
		r, members, err := tx.RoomByID(roomID)
		if err != nil {
			return err
		}
		result, err = r.ExpirePostGame(room.RoomInput{Members: members}, now)
		if err != nil {
			return err
		}
		if len(result.RemovedUserIDs) == 0 {
			return nil
		}
		return tx.SaveExpiredPostGame(r, result.RemovedUserIDs)
	})
	if err != nil {
		return room.PostGameTimeoutResult{}, err
	}
	return result, nil
}
