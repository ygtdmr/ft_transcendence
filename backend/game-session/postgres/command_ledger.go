package postgres

import (
	"errors"

	"gorm.io/gorm"
)

type commandLedgerRow struct {
	RoomID      int64  `gorm:"primaryKey;autoIncrement:false"`
	SessionID   string `gorm:"primaryKey"`
	RequestID   string `gorm:"primaryKey"`
	CommandText string
	Response    jsonb
}

func (commandLedgerRow) TableName() string { return "command_ledger" }

// SELECT command_text, response FROM command_ledger WHERE room_id = 1 AND session_id = '...' AND request_id = 'r1';
func (s *Store) LookupCommandResult(roomID int64, sessionID string, requestID string) (commandText string, response []byte, found bool, err error) {
	var row commandLedgerRow
	err = s.db.Where("room_id = ? AND session_id = ? AND request_id = ?", roomID, sessionID, requestID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, err
	}
	return row.CommandText, []byte(row.Response), true, nil
}

// INSERT INTO command_ledger (room_id, session_id, request_id, command_text, response) VALUES (...);
func (s *Store) SaveCommandResult(roomID int64, sessionID string, requestID string, commandText string, response []byte) error {
	row := commandLedgerRow{RoomID: roomID, SessionID: sessionID, RequestID: requestID, CommandText: commandText, Response: jsonb(response)}
	return s.db.Create(&row).Error
}

// DELETE FROM command_ledger WHERE room_id = 1;
func (s *Store) DeleteCommandResults(roomID int64) error {
	return s.db.Where("room_id = ?", roomID).Delete(&commandLedgerRow{}).Error
}
