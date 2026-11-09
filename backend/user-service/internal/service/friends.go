package service

import (
	"context"
	"errors"
)

// Friend list paging limits from the public contract.
const (
	DefaultFriendPageLimit = 50
	MaxFriendPageLimit     = 100
)

// ErrNotFriends reports no active friendship between the two users.
var ErrNotFriends = errors.New("not friends")

// StoredFriendPage is one page of a user's active friends.
type StoredFriendPage struct {
	// Total counts every active friend, not only this page.
	Total   int
	Friends []StoredProfile
}

// FriendStore reads and removes active friendships.
type FriendStore interface {
	// FriendPage returns one page ordered case-insensitively by username, then
	// user_id. Suspended friendships and users being deleted are not included.
	FriendPage(ctx context.Context, userID int64, limit, offset int) (StoredFriendPage, error)
	// RemoveFriendship deletes the pair's active friendship and records event
	// in the same transaction. It returns ErrNotFriends when none exists.
	RemoveFriendship(ctx context.Context, userID, friendID int64, event OutboxEvent) error
}

// FriendPage is one page of the List friends response.
type FriendPage struct {
	Total   int
	Limit   int
	Offset  int
	HasMore bool
	Friends []Friend
}

// Friends answers the friend list and removal routes.
type Friends struct {
	store    FriendStore
	avatars  AvatarURLs
	presence PresenceReader
}

// NewFriends returns the friend service.
func NewFriends(store FriendStore, avatars AvatarURLs, presence PresenceReader) *Friends {
	return &Friends{store: store, avatars: avatars, presence: presence}
}

// List returns one page of userID's active friends with their presence.
//
// Errors:
//
//   - *ValidationError: limit is outside 1..100 or offset is negative.
//   - ErrUnavailable or other database or presence errors. A presence failure
//     is returned, never reported as offline.
func (s *Friends) List(ctx context.Context, userID int64, limit, offset int) (FriendPage, error) {
	if limit < 1 || limit > MaxFriendPageLimit {
		return FriendPage{}, &ValidationError{Field: "limit", Reason: "must be between 1 and 100"}
	}
	if offset < 0 {
		return FriendPage{}, &ValidationError{Field: "offset", Reason: "must not be negative"}
	}
	stored, err := s.store.FriendPage(ctx, userID, limit, offset)
	if err != nil {
		return FriendPage{}, err
	}
	friends := make([]Friend, 0, len(stored.Friends))
	for _, profile := range stored.Friends {
		online, err := s.presence.IsOnline(ctx, profile.UserID)
		if err != nil {
			return FriendPage{}, err
		}
		friends = append(friends, Friend{FriendUser: friendUser(s.avatars, profile), IsOnline: online})
	}

	return FriendPage{Total: stored.Total, Limit: limit, Offset: offset,
		HasMore: offset+len(friends) < stored.Total, Friends: friends}, nil
}

// Remove deletes the active friendship both ways and records friend.removed in
// the same transaction.
//
// Errors:
//
//   - *ValidationError: friendID is not positive.
//   - ErrNotFriends: no active friendship exists, including one suspended by a block.
//   - ErrUnavailable or other database errors.
func (s *Friends) Remove(ctx context.Context, userID, friendID int64) error {
	if friendID <= 0 {
		return &ValidationError{Field: "user_id", Reason: "must be a positive integer"}
	}

	return s.store.RemoveFriendship(ctx, userID, friendID, friendRemovedEvent(userID, friendID))
}

// friendRemovedEvent never says who ended the friendship or why.
func friendRemovedEvent(first, second int64) OutboxEvent {
	return friendshipEvent(first, second, EventFriendRemoved, map[string]any{
		"user_low": min(first, second), "user_high": max(first, second),
	})
}
