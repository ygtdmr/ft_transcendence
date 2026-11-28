package service

import (
	"context"
	"fmt"
	"time"
)

const readinessTimeout = 2 * time.Second

// ReadinessProbe checks one dependency that the service needs to answer requests.
type ReadinessProbe interface {
	Ping(ctx context.Context) error
}

// HealthService answers whether the service can use what it depends on.
type HealthService struct {
	database ReadinessProbe
}

// NewHealth returns a health service that probes the supplied database.
func NewHealth(database ReadinessProbe) *HealthService {
	return &HealthService{database: database}
}

// Ready verifies that the service can use its database.
//
// Parameters:
//
//   - ctx: Controls the check; it is also bounded by a fixed two second timeout.
//
// It returns nil when the database answers.
//
// Errors:
//
//   - ErrUnavailable: The database did not answer in time or returned an error.
//     The driver error is wrapped for logs but is never part of a public response.
func (s *HealthService) Ready(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, readinessTimeout)
	defer cancel()

	if err := s.database.Ping(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	return nil
}
