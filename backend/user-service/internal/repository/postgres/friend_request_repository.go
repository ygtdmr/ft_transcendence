package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

var _ service.FriendRequestStore = (*FriendRequestRepository)(nil)

// lockPair serializes all relationship changes of one unordered user pair for
// the transaction. It always comes before any friend_requests row lock, the
// same order the block and friend request triggers use, so they cannot deadlock.
const lockPair = "SELECT users.lock_user_pair($1, $2)"

const pairBlocked = `SELECT EXISTS (SELECT 1 FROM blocks
	WHERE (blocker_id = $1 AND blocked_id = $2) OR (blocker_id = $2 AND blocked_id = $1))`

const activeFriendship = `SELECT EXISTS (SELECT 1 FROM friendships
	WHERE user_low = LEAST($1::bigint, $2::bigint) AND user_high = GREATEST($1::bigint, $2::bigint) AND active)`

// FriendRequestRepository stores friend requests and their outbox events.
type FriendRequestRepository struct {
	pool *pgxpool.Pool
}

// NewFriendRequests returns a friend request store; pool ownership stays with the caller.
func NewFriendRequests(pool *pgxpool.Pool) *FriendRequestRepository {
	return &FriendRequestRepository{pool: pool}
}

func inTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return classify(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := fn(tx); err != nil {
		return classify(err)
	}

	return classify(tx.Commit(ctx))
}

// CreateFriendRequest implements service.FriendRequestStore.
//
// Under the pair lock it checks, in order: the recipient exists and is not
// being deleted, no block exists, no active friendship exists, and no request
// is pending in either direction. It then inserts the request and records its
// event in the same transaction.
func (r *FriendRequestRepository) CreateFriendRequest(ctx context.Context, senderID, recipientID int64,
	event service.FriendEventBuilder) (service.StoredFriendRequest, error) {
	var stored service.StoredFriendRequest
	err := inTx(ctx, r.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, lockPair, senderID, recipientID); err != nil {
			return err
		}
		sender, recipient, err := loadPair(ctx, tx, senderID, recipientID)
		if err != nil {
			return err
		}
		if err := refuseSend(ctx, tx, senderID, recipientID); err != nil {
			return err
		}
		stored = service.StoredFriendRequest{From: sender, To: recipient, State: service.FriendRequestPending}
		err = tx.QueryRow(ctx, `INSERT INTO friend_requests (requester_id, addressee_id)
			VALUES ($1, $2) RETURNING id, created_at`, senderID, recipientID).Scan(&stored.ID, &stored.CreatedAt)
		if err != nil {
			return err
		}
		stored.CreatedAt = stored.CreatedAt.UTC()
		_, err = AppendEvent(ctx, tx, event(stored))
		return err
	})
	if err != nil {
		return service.StoredFriendRequest{}, err
	}

	return stored, nil
}

// refuseSend returns why the pair cannot have a new request, or nil.
func refuseSend(ctx context.Context, tx pgx.Tx, senderID, recipientID int64) error {
	var blocked, friends bool
	if err := tx.QueryRow(ctx, pairBlocked, senderID, recipientID).Scan(&blocked); err != nil {
		return err
	}
	if blocked {
		return service.ErrRelationshipBlocked
	}
	err := tx.QueryRow(ctx, activeFriendship, senderID, recipientID).Scan(&friends)
	if err != nil {
		return err
	}
	if friends {
		return service.ErrAlreadyFriends
	}
	var requestID, requesterID int64
	err = tx.QueryRow(ctx, `SELECT id, requester_id FROM friend_requests
		WHERE user_low = LEAST($1::bigint, $2::bigint) AND user_high = GREATEST($1::bigint, $2::bigint) AND state = 'pending'`,
		senderID, recipientID).Scan(&requestID, &requesterID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}

	return &service.PendingRequestError{RequestID: requestID, Outgoing: requesterID == senderID}
}

// ResolveFriendRequest implements service.FriendRequestStore.
//
// The pair ids never change, so they are read first to take the pair lock, and
// the state is decided on the locked row. Racing resolutions therefore make one
// transition and record one event; the others find the request not pending.
// Accepting updates the request before inserting the friendship, as the
// friend_requests guard requires.
func (r *FriendRequestRepository) ResolveFriendRequest(ctx context.Context, requestID, callerID int64, state string,
	event service.FriendEventBuilder) (service.StoredFriendRequest, error) {
	var stored service.StoredFriendRequest
	err := inTx(ctx, r.pool, func(tx pgx.Tx) error {
		var requesterID, addresseeID int64
		err := tx.QueryRow(ctx, `SELECT requester_id, addressee_id FROM friend_requests WHERE id = $1`, requestID).
			Scan(&requesterID, &addresseeID)
		if errors.Is(err, pgx.ErrNoRows) {
			return service.ErrRequestNotFound
		}
		if err != nil {
			return err
		}
		owner := addresseeID
		if state == service.FriendRequestCancelled {
			owner = requesterID
		}
		if owner != callerID {
			return service.ErrRequestNotFound
		}
		if _, err := tx.Exec(ctx, lockPair, requesterID, addresseeID); err != nil {
			return err
		}

		if state == service.FriendRequestAccepted {
			// A block cancels pending requests, so this comes before the state check.
			var blocked bool
			if err := tx.QueryRow(ctx, pairBlocked, requesterID, addresseeID).Scan(&blocked); err != nil {
				return err
			}
			if blocked {
				return service.ErrRelationshipBlocked
			}
		}

		var current string
		err = tx.QueryRow(ctx, `SELECT state, created_at FROM friend_requests WHERE id = $1 FOR UPDATE`, requestID).
			Scan(&current, &stored.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return service.ErrRequestNotFound
		}
		if err != nil {
			return err
		}
		if current != service.FriendRequestPending {
			return service.ErrRequestNotFound
		}
		// A user being deleted cannot gain a friend, but their request can
		// still be declined or cancelled.
		load := loadPairForResolution
		if state == service.FriendRequestAccepted {
			load = loadPair
		}
		stored.From, stored.To, err = load(ctx, tx, requesterID, addresseeID)
		if errors.Is(err, service.ErrUserNotFound) {
			return service.ErrRequestNotFound
		}
		if err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `UPDATE friend_requests SET state = $2, resolved_at = clock_timestamp() WHERE id = $1`,
			requestID, state); err != nil {
			return err
		}
		if state == service.FriendRequestAccepted {
			if _, err := tx.Exec(ctx, `INSERT INTO friendships (user_low, user_high, initiated_by)
				VALUES (LEAST($1::bigint, $2::bigint), GREATEST($1::bigint, $2::bigint), $1)`, requesterID, addresseeID); err != nil {
				return err
			}
		}
		stored.ID, stored.State, stored.CreatedAt = requestID, state, stored.CreatedAt.UTC()
		_, err = AppendEvent(ctx, tx, event(stored))
		return err
	})
	if err != nil {
		return service.StoredFriendRequest{}, err
	}

	return stored, nil
}

// PendingFriendRequests implements service.FriendRequestStore. A request whose
// other user is being deleted is withheld.
func (r *FriendRequestRepository) PendingFriendRequests(ctx context.Context, userID int64) ([]service.StoredFriendRequest, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT q.id, q.state, q.created_at,
		       f.user_id, f.username, f.avatar_kind, COALESCE(f.avatar_ref, ''),
		       t.user_id, t.username, t.avatar_kind, COALESCE(t.avatar_ref, '')
		FROM friend_requests q
		JOIN profiles f ON f.user_id = q.requester_id
		JOIN profiles t ON t.user_id = q.addressee_id
		WHERE q.state = 'pending' AND (q.requester_id = $1 OR q.addressee_id = $1)
		  AND NOT EXISTS (SELECT 1 FROM deletion_operations d
		                  WHERE d.user_id IN (q.requester_id, q.addressee_id))
		ORDER BY q.created_at DESC, q.id DESC`, userID)
	if err != nil {
		return nil, classify(err)
	}
	requests, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (service.StoredFriendRequest, error) {
		var request service.StoredFriendRequest
		err := row.Scan(&request.ID, &request.State, &request.CreatedAt,
			&request.From.UserID, &request.From.Username, &request.From.AvatarKind, &request.From.AvatarRef,
			&request.To.UserID, &request.To.Username, &request.To.AvatarKind, &request.To.AvatarRef)
		request.CreatedAt = request.CreatedAt.UTC()
		return request, err
	})
	if err != nil {
		return nil, classify(err)
	}

	return requests, nil
}

// loadPair reads both profiles and keeps them from being deleted until commit.
// It returns service.ErrUserNotFound when either is missing or being deleted.
func loadPair(ctx context.Context, tx pgx.Tx, firstID, secondID int64) (first, second service.StoredProfile, err error) {
	var deleting bool
	err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM deletion_operations WHERE user_id IN ($1, $2))`,
		firstID, secondID).Scan(&deleting)
	if err != nil {
		return first, second, err
	}
	if deleting {
		return first, second, service.ErrUserNotFound
	}

	return loadPairForResolution(ctx, tx, firstID, secondID)
}

// loadPairForResolution reads both profiles even when one is being deleted, so
// a pending request can still be declined or cancelled.
func loadPairForResolution(ctx context.Context, tx pgx.Tx, firstID, secondID int64) (first, second service.StoredProfile, err error) {
	rows, err := tx.Query(ctx, `SELECT user_id, username, avatar_kind, COALESCE(avatar_ref, '')
		FROM profiles WHERE user_id = ANY($1) FOR KEY SHARE`, []int64{firstID, secondID})
	if err != nil {
		return first, second, err
	}
	profiles, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (service.StoredProfile, error) {
		var profile service.StoredProfile
		return profile, row.Scan(&profile.UserID, &profile.Username, &profile.AvatarKind, &profile.AvatarRef)
	})
	if err != nil {
		return first, second, err
	}
	for _, profile := range profiles {
		switch profile.UserID {
		case firstID:
			first = profile
		case secondID:
			second = profile
		}
	}
	if first.UserID == 0 || second.UserID == 0 {
		return first, second, service.ErrUserNotFound
	}

	return first, second, nil
}
