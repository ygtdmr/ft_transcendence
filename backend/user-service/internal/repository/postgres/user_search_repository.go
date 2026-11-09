package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

var _ service.UserSearchStore = (*UserSearchRepository)(nil)

// UserSearchRepository finds users by username.
type UserSearchRepository struct {
	pool *pgxpool.Pool
}

// NewUserSearch returns a search store; pool ownership stays with the caller.
func NewUserSearch(pool *pgxpool.Pool) *UserSearchRepository {
	return &UserSearchRepository{pool: pool}
}

// searchUsers reads every match and its relationship facts from one snapshot.
// strpos treats the query as plain text, so "_" and "%" match themselves. The
// compared key is the same one the username uniqueness index uses. A pair has
// at most one pending request, so the join cannot repeat a user.
const searchUsers = `
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
	WHERE p.user_id <> $1
	  AND strpos(lower(btrim(p.username)), lower($2::text)) > 0
	  AND NOT EXISTS (SELECT 1 FROM deletion_operations d WHERE d.user_id = p.user_id)
	ORDER BY strpos(lower(btrim(p.username)), lower($2::text)) = 1 DESC, lower(btrim(p.username)), p.user_id
	LIMIT $3 OFFSET $4`

// SearchUsers implements service.UserSearchStore.
func (r *UserSearchRepository) SearchUsers(ctx context.Context, viewerID int64, query string, limit, offset int) ([]service.StoredUserMatch, error) {
	rows, err := r.pool.Query(ctx, searchUsers, viewerID, query, limit, offset)
	if err != nil {
		return nil, classify(err)
	}
	matches, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (service.StoredUserMatch, error) {
		var match service.StoredUserMatch
		err := row.Scan(&match.Profile.UserID, &match.Profile.Username, &match.Profile.AvatarKind, &match.Profile.AvatarRef,
			&match.Facts.ViewerBlocks, &match.Facts.Friends, &match.Facts.PendingRequestID, &match.Facts.PendingFromViewer)
		return match, err
	})
	if err != nil {
		return nil, classify(err)
	}

	return matches, nil
}
