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

func (s *RoomService) GiveClue(tx RoomStore, roomID int64, userID int64, clue game.Clue, now time.Time) (game.CurrentTurn, error) {
	r, members, currentGame, err := loadGame(tx, roomID)
	if err != nil {
		return game.CurrentTurn{}, err
	}
	player, err := s.loadPlayer(members, userID)
	if err != nil {
		return game.CurrentTurn{}, err
	}
	turn, moveErr := currentGame.GiveClue(player, &clue, now)
	if err := finishMove(tx, r, members, currentGame, now, moveErr); err != nil {
		return game.CurrentTurn{}, err
	}
	if moveErr != nil {
		return game.CurrentTurn{}, moveErr
	}
	return turn, nil
}

func (s *RoomService) GuessCard(tx RoomStore, roomID int64, userID int64, cardID int, now time.Time) (game.GuessResult, error) {
	r, members, currentGame, err := loadGame(tx, roomID)
	if err != nil {
		return game.GuessResult{}, err
	}
	player, err := s.loadPlayer(members, userID)
	if err != nil {
		return game.GuessResult{}, err
	}
	result, moveErr := currentGame.GuessCard(player, cardID, now)
	if err := finishMove(tx, r, members, currentGame, now, moveErr); err != nil {
		return game.GuessResult{}, err
	}
	if moveErr != nil {
		return game.GuessResult{}, moveErr
	}
	return result, nil
}

func (s *RoomService) PassTurn(tx RoomStore, roomID int64, userID int64, now time.Time) (game.PassResult, error) {
	r, members, currentGame, err := loadGame(tx, roomID)
	if err != nil {
		return game.PassResult{}, err
	}
	player, err := s.loadPlayer(members, userID)
	if err != nil {
		return game.PassResult{}, err
	}
	result, moveErr := currentGame.PassTurn(player, now)
	if err := finishMove(tx, r, members, currentGame, now, moveErr); err != nil {
		return game.PassResult{}, err
	}
	if moveErr != nil {
		return game.PassResult{}, moveErr
	}
	return result, nil
}

func (s *RoomService) PassTurnIfExpired(tx RoomStore, roomID int64, now time.Time) (*game.PassResult, error) {
	_, _, currentGame, err := loadGame(tx, roomID)
	if err != nil {
		return nil, err
	}
	result, err := currentGame.PassTurnIfExpired(now)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, nil
	}
	if err := tx.SaveGame(currentGame); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *RoomService) CancelIfStaffingExpired(tx RoomStore, roomID int64, now time.Time) (bool, error) {
	r, members, currentGame, err := loadGame(tx, roomID)
	if err != nil {
		return false, err
	}
	cancelled, err := currentGame.CancelIfStaffingExpired(now)
	if err != nil {
		return false, err
	}
	if !cancelled {
		return false, nil
	}
	if _, err := endGame(tx, r, members, currentGame, now); err != nil {
		return false, err
	}
	return true, nil
}
