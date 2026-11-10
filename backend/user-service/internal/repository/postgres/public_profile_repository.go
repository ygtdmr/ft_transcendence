package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

// PublicProfileRepository reads one profile with the viewer's relationship facts.
type PublicProfileRepository struct {
	pool *pgxpool.Pool
}

// NewPublicProfiles returns the public profile repository.
func NewPublicProfiles(pool *pgxpool.Pool) *PublicProfileRepository {
	return &PublicProfileRepository{pool: pool}
}

// readPublicProfile reads the profile and the viewer's relationship facts in
// one snapshot, using the same facts as user search. A pair has at most one
// pending request, so the join cannot repeat the row.
const readPublicProfile = `
	SELECT p.user_id, p.username, p.avatar_kind, COALESCE(p.avatar_ref, ''),
	       EXISTS (SELECT 1 FROM blocks b WHERE b.blocker_id = $1 AND b.blocked_id = p.user_id),
	       EXISTS (SELECT 1 FROM friendships f
	               WHERE f.user_low = LEAST($1::bigint, p.user_id)
	                 AND f.user_high = GREATEST($1::bigint, p.user_id) AND f.active),
	       COALESCE(q.id, 0), COALESCE(q.requester_id = $1, false)
	FROM profiles p
	LEFT JOIN friend_requests q
	       ON q.user_low = LEAST($1::bigint, p.user_id)
	      AND q.user_high = GREATEST($1::bigint, p.user_id)
	      AND q.state = 'pending'
	WHERE p.user_id = $2
	  AND NOT EXISTS (SELECT 1 FROM deletion_operations d WHERE d.user_id = p.user_id)`

// PublicProfile implements service.PublicProfileStore.
func (r *PublicProfileRepository) PublicProfile(ctx context.Context, viewerID, userID int64) (service.StoredUserMatch, error) {
	var match service.StoredUserMatch
	err := r.pool.QueryRow(ctx, readPublicProfile, viewerID, userID).Scan(
		&match.Profile.UserID, &match.Profile.Username, &match.Profile.AvatarKind, &match.Profile.AvatarRef,
		&match.Facts.ViewerBlocks, &match.Facts.Friends, &match.Facts.PendingRequestID, &match.Facts.PendingFromViewer)
	if errors.Is(err, pgx.ErrNoRows) {
		return service.StoredUserMatch{}, service.ErrUserNotFound
	}
	if err != nil {
		return service.StoredUserMatch{}, classify(err)
	}

	return match, nil
}
