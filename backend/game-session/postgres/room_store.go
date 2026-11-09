package postgres

import (
	"errors"

	"game-session/room"
	"game-session/service"

	"gorm.io/gorm"
)

// SELECT * FROM rooms WHERE room_id = 42 LIMIT 1 FOR UPDATE;
// SELECT * FROM room_members WHERE room_id = 42 ORDER BY joined_at, user_id;
func (s *Store) RoomByID(roomID int64) (*room.Room, []room.RoomMember, error) {
	var r room.Room
	if err := s.roomQuery().First(&r, roomID).Error; err != nil {
		return nil, nil, notFound(err, room.ErrRoomNotFound)
	}
	members, err := s.membersOf(roomID)
	if err != nil {
		return nil, nil, err
	}
	return &r, members, nil
}

// SELECT * FROM rooms WHERE room_code = 'ABC123' AND status <> 'CLOSED' LIMIT 1 FOR UPDATE;
// SELECT * FROM room_members WHERE room_id = 42 ORDER BY joined_at, user_id;
func (s *Store) RoomByCode(code string) (*room.Room, []room.RoomMember, error) {
	var r room.Room
	if err := s.roomQuery().Where("room_code = ? AND status <> ?", code, room.StatusClosed).First(&r).Error; err != nil {
		return nil, nil, notFound(err, room.ErrRoomNotFound)
	}
	members, err := s.membersOf(r.RoomID)
	if err != nil {
		return nil, nil, err
	}
	return &r, members, nil
}

// SELECT * FROM room_members WHERE room_id = 42 ORDER BY joined_at, user_id;
func (s *Store) membersOf(roomID int64) ([]room.RoomMember, error) {
	var members []room.RoomMember
	if err := s.db.Where("room_id = ?", roomID).Order("joined_at, user_id").Find(&members).Error; err != nil {
		return nil, err
	}
	return members, nil
}

// SELECT room_id FROM room_members WHERE user_id = 42 LIMIT 1;
func (s *Store) ActiveRoomOf(userID int64) (int64, bool, error) {
	var member room.RoomMember
	err := s.db.Where("user_id = ?", userID).First(&member).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return member.RoomID, true, nil
}

// INSERT INTO rooms (room_code, host_user_id, max_players) VALUES ('ABC123', 42, 8) RETURNING room_id;
// INSERT INTO room_members (room_id, user_id) VALUES (1, 42);
func (s *Store) SaveNewRoom(r *room.Room, host room.RoomMember) error {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(r).Error; err != nil {
			return err
		}
		host.RoomID = r.RoomID
		err := tx.Create(&host).Error
		return err
	})
	if isUniqueViolation(err, constraintActiveRoomCode) {
		return service.ErrRoomCodeTaken
	}
	if isUniqueViolation(err, constraintOneActiveRoom) {
		return room.ErrAlreadyInRoom
	}
	return err
}

// UPDATE rooms SET countdown_ends_at = NULL, empty_since = NULL, close_at = NULL WHERE room_id = 1;
// INSERT INTO room_members (room_id, user_id) VALUES (1, 43);
func (s *Store) SaveJoin(r *room.Room, member room.RoomMember) error {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(r).Error; err != nil {
			return err
		}
		return tx.Create(&member).Error
	})
	if isUniqueViolation(err, constraintOneActiveRoom) {
		return room.ErrAlreadyInRoom
	}
	return err
}

// UPDATE rooms SET countdown_ends_at = NULL WHERE room_id = 1;
// UPDATE room_members SET team = 'RED', role = 'SPYMASTER', ready = false WHERE user_id = 43;
func (s *Store) SaveMember(r *room.Room, member room.RoomMember) error {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(r).Error; err != nil {
			return err
		}
		return tx.Save(&member).Error
	})
	if isUniqueViolation(err, constraintOneSpymaster) {
		return room.ErrRoleConflict
	}
	return err
}

// UPDATE rooms SET max_players = 10, turn_timer_seconds = 90, language = 'tr' WHERE room_id = 1;
func (s *Store) SaveRoom(r *room.Room) error {
	return s.db.Save(r).Error
}

// UPDATE rooms SET status = 'WAITING', post_game_ends_at = NULL WHERE room_id = 1;
// DELETE FROM room_members WHERE room_id = 1 AND user_id IN (43, 44);
func (s *Store) SaveExpiredPostGame(r *room.Room, removedUserIDs []int64) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(r).Error; err != nil {
			return err
		}
		return tx.Where("room_id = ? AND user_id IN ?", r.RoomID, removedUserIDs).Delete(&room.RoomMember{}).Error
	})
}
