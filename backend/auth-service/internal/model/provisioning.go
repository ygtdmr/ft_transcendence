package model

import "time"

// Provisioning operation states.
const (
	ProvisioningPending   = "PENDING"
	ProvisioningCompleted = "COMPLETED"
	ProvisioningFailed    = "FAILED"
)

// ProvisioningOperation is Auth's durable record of the User Service profile
// provisioning request for one account. The request fields never change.
type ProvisioningOperation struct {
	UserID           int64
	OperationID      string
	Username         string
	AccountCreatedAt time.Time
	State            string
	Attempts         int
}
