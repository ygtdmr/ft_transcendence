package model

import "time"

// Account deletion states.
const (
	DeletionBegun     = "BEGUN"
	DeletionCompleted = "COMPLETED"
)

// AccountDeletion is Auth's durable record of one coordinated account
// deletion. It outlives the account and holds no personal data.
type AccountDeletion struct {
	OperationID string
	UserID      int64
	State       string
	BegunAt     time.Time
	CompletedAt *time.Time
}
