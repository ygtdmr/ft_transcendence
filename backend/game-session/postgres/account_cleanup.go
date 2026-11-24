package postgres

import (
	"errors"
	"game-session/service"
	"gorm.io/gorm"
	"time"
)

type accountCleanupRow struct {
	OperationID string `gorm:"primaryKey"`
	UserID      int64
	CompletedAt time.Time
}

func (accountCleanupRow) TableName() string { return "account_cleanups" }

// SELECT operation_id, user_id, completed_at FROM account_cleanups WHERE user_id = 42;
func (s *Store) LookupAccountCleanup(userID int64) (service.AccountCleanup, bool, error) {
	var row accountCleanupRow
	err := s.db.Where("user_id = ?", userID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return service.AccountCleanup{}, false, nil
	}
	if err != nil {
		return service.AccountCleanup{}, false, err
	}
	return service.AccountCleanup{OperationID: row.OperationID, UserID: row.UserID, CompletedAt: row.CompletedAt}, true, nil
}

// INSERT INTO account_cleanups (operation_id, user_id, completed_at) VALUES ('...', 42, now());
func (s *Store) SaveAccountCleanup(cleanup service.AccountCleanup) error {
	row := accountCleanupRow{OperationID: cleanup.OperationID, UserID: cleanup.UserID, CompletedAt: cleanup.CompletedAt}
	err := s.db.Create(&row).Error
	if isUniqueViolation(err, constraintOneCleanupPerOperation) {
		return service.ErrAccountCleanupOperationConflict
	}
	return err
}
