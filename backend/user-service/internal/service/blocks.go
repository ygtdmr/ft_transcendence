package service

import (
	"context"
	"errors"
	"strconv"
	"time"
)

// ErrCannotBlockSelf reports a block or unblock aimed at the caller.
var ErrCannotBlockSelf = errors.New("cannot block yourself")

// BlockPlacement describes what a newly placed block changed.
type BlockPlacement struct {
	Blocker StoredProfile
	Blocked StoredProfile
	// CancelledRequest is the pending request the block cancelled, if any. Its
	// state is FriendRequestCancelled.
	CancelledRequest *StoredFriendRequest
	// FriendshipSuspended is true when the block suspended an active friendship.
	FriendshipSuspended bool
}

// BlockEventBuilder builds the events recorded with a newly placed block.
type BlockEventBuilder func(BlockPlacement) []OutboxEvent

// RestoreEventBuilder builds the events recorded when lifting a block
// reactivates the friendship of the two users.
type RestoreEventBuilder func(first, second StoredProfile) []OutboxEvent

// BlockStore applies each block change and its events in one transaction.
type BlockStore interface {
	// PlaceBlock records the block and returns when it started. Placing an
	// existing block changes nothing, records no event and returns its
	// original time. It returns ErrUserNotFound when either user is missing
	// or being deleted.
	PlaceBlock(ctx context.Context, blockerID, blockedID int64, events BlockEventBuilder) (time.Time, error)
	// LiftBlock removes only blockerID's own block. Lifting a block that does
	// not exist changes nothing and records no event. It returns
	// ErrUserNotFound when either user is missing or being deleted.
	LiftBlock(ctx context.Context, blockerID, blockedID int64, events RestoreEventBuilder) error
}

// Blocks answers the block routes.
type Blocks struct {
	store   BlockStore
	avatars AvatarURLs
}

// NewBlocks returns the block service.
func NewBlocks(store BlockStore, avatars AvatarURLs) *Blocks {
	return &Blocks{store: store, avatars: avatars}
}

// Block makes blockerID block targetID. In one transaction it cancels the
// pair's pending request, suspends their friendship and records the events
// describing those changes. It is idempotent.
//
// Errors:
//
//   - *ValidationError: targetID is not positive.
//   - ErrCannotBlockSelf: targetID equals blockerID.
//   - ErrUserNotFound: the target does not exist or is being deleted.
//   - ErrUnavailable or other database errors.
func (s *Blocks) Block(ctx context.Context, blockerID, targetID int64) (time.Time, error) {
	if err := validateBlockTarget(blockerID, targetID); err != nil {
		return time.Time{}, err
	}

	return s.store.PlaceBlock(ctx, blockerID, targetID, placementEvents)
}

// Unblock lifts blockerID's own block on targetID. The friendship becomes
// active again only when neither user blocks the other. It is idempotent.
//
// Errors are the same as for Block.
func (s *Blocks) Unblock(ctx context.Context, blockerID, targetID int64) error {
	if err := validateBlockTarget(blockerID, targetID); err != nil {
		return err
	}

	return s.store.LiftBlock(ctx, blockerID, targetID, s.restoredEvents)
}

func validateBlockTarget(blockerID, targetID int64) error {
	if targetID <= 0 {
		return &ValidationError{Field: "user_id", Reason: "must be a positive integer"}
	}
	if targetID == blockerID {
		return ErrCannotBlockSelf
	}

	return nil
}

// friendshipEvent identifies the unordered pair, so every change to one
// friendship publishes in order.
func friendshipEvent(first, second int64, eventType string, payload map[string]any) OutboxEvent {
	return OutboxEvent{
		AggregateType: "friendship",
		AggregateID:   strconv.FormatInt(min(first, second), 10) + ":" + strconv.FormatInt(max(first, second), 10),
		EventType:     eventType,
		Payload:       payload,
	}
}

// placementEvents never names the blocker in an event bound for user sockets.
// Only EventRelationshipBlocked carries the direction. It comes before
// EventFriendRemoved in the friendship's order, so Channel Service knows the
// access reason for each user before it sees the friendship end.
func placementEvents(placement BlockPlacement) []OutboxEvent {
	var events []OutboxEvent
	if request := placement.CancelledRequest; request != nil {
		events = append(events, friendRequestEvent(*request, EventFriendRequestResolved, resolvedPayload(*request)))
	}
	if placement.FriendshipSuspended {
		blocker, blocked := placement.Blocker.UserID, placement.Blocked.UserID
		events = append(events,
			friendshipEvent(blocker, blocked, EventRelationshipBlocked, map[string]any{
				"blocker_id": blocker, "blocked_id": blocked,
			}),
			friendRemovedEvent(blocker, blocked))
	}

	return events
}

// restoredEvents carries both users, because each recipient's socket shows the
// other user as friend.
func (s *Blocks) restoredEvents(first, second StoredProfile) []OutboxEvent {
	if first.UserID > second.UserID {
		first, second = second, first
	}

	return []OutboxEvent{friendshipEvent(first.UserID, second.UserID, EventFriendRestored, map[string]any{
		"users": []FriendUser{friendUser(s.avatars, first), friendUser(s.avatars, second)},
	})}
}
