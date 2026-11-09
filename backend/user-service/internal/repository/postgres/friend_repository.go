package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

var _ service.FriendStore = (*FriendRepository)(nil)

// FriendRepository reads and removes active friendships.
type FriendRepository struct {
	pool *pgxpool.Pool
}

// NewFriends returns a friend store; pool ownership stays with the caller.
func NewFriends(pool *pgxpool.Pool) *FriendRepository {
	return &FriendRepository{pool: pool}
}

// friendPage reads the total and the page from one snapshot. The total comes
// from a row of its own, so an offset past the end still returns it: that row
// then has NULL friend columns.
const friendPage = `
	WITH friends AS (
		SELECT p.user_id, p.username, p.avatar_kind, COALESCE(p.avatar_ref, '') AS avatar_ref
		FROM friendships f
		JOIN profiles p ON p.user_id = CASE WHEN f.user_low = $1 THEN f.user_high ELSE f.user_low END
		WHERE (f.user_low = $1 OR f.user_high = $1) AND f.active
		  AND NOT EXISTS (SELECT 1 FROM deletion_operations d WHERE d.user_id = p.user_id)
	)
	SELECT total.friend_count, page.user_id, page.username, page.avatar_kind, page.avatar_ref
	FROM (SELECT count(*) AS friend_count FROM friends) total
	LEFT JOIN LATERAL (
		SELECT * FROM friends ORDER BY lower(username), user_id LIMIT $2 OFFSET $3
	) page ON true
	ORDER BY lower(page.username), page.user_id`

// FriendPage implements service.FriendStore.
func (r *FriendRepository) FriendPage(ctx context.Context, userID int64, limit, offset int) (service.StoredFriendPage, error) {
	rows, err := r.pool.Query(ctx, friendPage, userID, limit, offset)
	if err != nil {
		return service.StoredFriendPage{}, classify(err)
	}
	defer rows.Close()

	page := service.StoredFriendPage{Friends: []service.StoredProfile{}}
	for rows.Next() {
		var id *int64
		var username, avatarKind, avatarRef *string
		if err := rows.Scan(&page.Total, &id, &username, &avatarKind, &avatarRef); err != nil {
			return service.StoredFriendPage{}, classify(err)
		}
		if id == nil {
			continue
		}
		page.Friends = append(page.Friends, service.StoredProfile{
			UserID: *id, Username: *username, AvatarKind: *avatarKind, AvatarRef: *avatarRef,
		})
	}
	if err := rows.Err(); err != nil {
		return service.StoredFriendPage{}, classify(err)
	}

	return page, nil
}

// RemoveFriendship implements service.FriendStore. Only an active row is
// deleted, so a friendship suspended by a block stays stored.
func (r *FriendRepository) RemoveFriendship(ctx context.Context, userID, friendID int64, event service.OutboxEvent) error {
	return inTx(ctx, r.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, lockPair, userID, friendID); err != nil {
			return err
		}
		removed, err := tx.Exec(ctx, `DELETE FROM friendships
			WHERE user_low = LEAST($1::bigint, $2::bigint) AND user_high = GREATEST($1::bigint, $2::bigint) AND active`,
			userID, friendID)
		if err != nil {
			return err
		}
		if removed.RowsAffected() == 0 {
			return service.ErrNotFriends
		}
		_, err = AppendEvent(ctx, tx, event)
		return err
	})
}
