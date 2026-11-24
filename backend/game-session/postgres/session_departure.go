package postgres

import (
	"errors"
	"time"

	"game-session/service"

	"gorm.io/gorm"
)

type sessionDepartureRow struct {
	SessionID   string `gorm:"primaryKey"`
	UserID      int64
	CompletedAt time.Time
}

func (sessionDepartureRow) TableName() string { return "session_departures" }

// SELECT session_id, user_id, completed_at FROM session_departures WHERE session_id = '...';
func (s *Store) LookupSessionDeparture(sessionID string) (service.SessionDeparture, bool, error) {
	var row sessionDepartureRow
	err := s.db.Where("session_id = ?", sessionID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return service.SessionDeparture{}, false, nil
	}
	if err != nil {
		return service.SessionDeparture{}, false, err
	}
	return service.SessionDeparture{SessionID: row.SessionID, UserID: row.UserID, CompletedAt: row.CompletedAt}, true, nil
}

// INSERT INTO session_departures (session_id, user_id, completed_at) VALUES ('...', 42, now());
func (s *Store) SaveSessionDeparture(departure service.SessionDeparture) error {
	row := sessionDepartureRow{SessionID: departure.SessionID, UserID: departure.UserID, CompletedAt: departure.CompletedAt}
	return s.db.Create(&row).Error
}
