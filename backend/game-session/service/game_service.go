package service

import (
	"errors"
	"time"

	"game-session/game"
	"game-session/room"
)

func loadGame(tx RoomStore, roomID int64) (*room.Room, []room.RoomMember, *game.Game, error) {
	r, members, currentGame, err := loadRoom(tx, roomID)
	if err != nil {
		return nil, nil, nil, err
	}
	if currentGame == nil {
		return nil, nil, nil, room.ErrGameNotFound
	}
	return r, members, currentGame, nil
}

func (s *RoomService) loadPlayer(members []room.RoomMember, userID int64) (*game.Player, error) {
	for _, member := range members {
		if member.UserID != userID {
			continue
		}
		if member.Team == nil || member.Role == nil {
			return nil, room.ErrRoleRequired
		}
		profiles, err := s.profiles.ProfilesByUserIDs([]int64{userID})
		if err != nil {
			return nil, err
		}
		profile, ok := profiles[userID]
		if !ok {
			return nil, room.ErrMemberProfileNotFound
		}
		return &game.Player{
			UserID:   member.UserID,
			Username: profile.Username,
			Team:     *member.Team,
			Role:     *member.Role,
		}, nil
	}
	return nil, room.ErrNotRoomMember
}

func saveMove(tx RoomStore, r *room.Room, members []room.RoomMember, currentGame *game.Game, now time.Time) error {
	if currentGame.Status() != game.GameStatusInProgress {
		_, err := endGame(tx, r, members, currentGame, now)
		return err
	}
	return tx.SaveGame(currentGame)
}

func finishMove(tx RoomStore, r *room.Room, members []room.RoomMember, currentGame *game.Game, now time.Time, moveErr error) error {
	if errors.Is(moveErr, game.ErrTurnExpired) {
		return tx.SaveGame(currentGame)
	}
	if moveErr != nil {
		return moveErr
	}
	return saveMove(tx, r, members, currentGame, now)
}

func (s *RoomService) GiveClue(roomID int64, userID int64, clue game.Clue, now time.Time) (game.CurrentTurn, error) {
	var turn game.CurrentTurn
	var moveErr error
	err := s.store.Transaction(func(tx RoomStore) error {
		r, members, currentGame, err := loadGame(tx, roomID)
		if err != nil {
			return err
		}
		player, err := s.loadPlayer(members, userID)
		if err != nil {
			return err
		}
		turn, moveErr = currentGame.GiveClue(player, &clue, now)
		return finishMove(tx, r, members, currentGame, now, moveErr)
	})
	if err != nil {
		return game.CurrentTurn{}, err
	}
	if moveErr != nil {
		return game.CurrentTurn{}, moveErr
	}
	return turn, nil
}

func (s *RoomService) GuessCard(roomID int64, userID int64, cardID int, now time.Time) (game.GuessResult, error) {
	var result game.GuessResult
	var moveErr error
	err := s.store.Transaction(func(tx RoomStore) error {
		r, members, currentGame, err := loadGame(tx, roomID)
		if err != nil {
			return err
		}
		player, err := s.loadPlayer(members, userID)
		if err != nil {
			return err
		}
		result, moveErr = currentGame.GuessCard(player, cardID, now)
		return finishMove(tx, r, members, currentGame, now, moveErr)
	})
	if err != nil {
		return game.GuessResult{}, err
	}
	if moveErr != nil {
		return game.GuessResult{}, moveErr
	}
	return result, nil
}

func (s *RoomService) PassTurn(roomID int64, userID int64, now time.Time) (game.PassResult, error) {
	var result game.PassResult
	var moveErr error
	err := s.store.Transaction(func(tx RoomStore) error {
		r, members, currentGame, err := loadGame(tx, roomID)
		if err != nil {
			return err
		}
		player, err := s.loadPlayer(members, userID)
		if err != nil {
			return err
		}
		result, moveErr = currentGame.PassTurn(player, now)
		return finishMove(tx, r, members, currentGame, now, moveErr)
	})
	if err != nil {
		return game.PassResult{}, err
	}
	if moveErr != nil {
		return game.PassResult{}, moveErr
	}
	return result, nil
}

func (s *RoomService) PassTurnIfExpired(roomID int64, now time.Time) (*game.PassResult, error) {
	var result *game.PassResult
	err := s.store.Transaction(func(tx RoomStore) error {
		_, _, currentGame, err := loadGame(tx, roomID)
		if err != nil {
			return err
		}
		result, err = currentGame.PassTurnIfExpired(now)
		if err != nil {
			return err
		}
		if result == nil {
			return nil
		}
		return tx.SaveGame(currentGame)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *RoomService) CancelIfStaffingExpired(roomID int64, now time.Time) (bool, error) {
	cancelled := false
	err := s.store.Transaction(func(tx RoomStore) error {
		r, members, currentGame, err := loadGame(tx, roomID)
		if err != nil {
			return err
		}
		cancelled, err = currentGame.CancelIfStaffingExpired(now)
		if err != nil {
			return err
		}
		if !cancelled {
			return nil
		}
		_, err = endGame(tx, r, members, currentGame, now)
		return err
	})
	if err != nil {
		return false, err
	}
	return cancelled, nil
}
