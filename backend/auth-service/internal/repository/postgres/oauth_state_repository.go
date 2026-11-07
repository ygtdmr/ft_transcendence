package postgres

import (
	"context"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
)

var _ service.OAuthStateStore = (*AuthRepository)(nil)

// CreateOAuthState stores one state hash and removes states that expired.
//
// Errors:
//
//   - service.ErrUnavailable or other database errors.
func (r *AuthRepository) CreateOAuthState(ctx context.Context, stateHash string, expiresAt time.Time) error {
	_, err := r.pool.Exec(ctx, `
		WITH expired AS (DELETE FROM oauth_states WHERE expires_at < now())
		INSERT INTO oauth_states (state_hash, expires_at) VALUES ($1, $2)`, stateHash, expiresAt)

	return classifyUnavailable(err)
}
