package service

import (
	"context"
	"fmt"
)

// PublicStats are the counters shown on a public profile. Penalty points are
// owner-only and are not part of it.
type PublicStats struct {
	Level         int
	Wins          int
	Losses        int
	MatchesPlayed int
}

// StatsReader reads a user's counters from Stats Service.
type StatsReader interface {
	// PublicStats returns userID's counters. Any error means they are unknown.
	PublicStats(ctx context.Context, userID int64) (PublicStats, error)
}

// PublicProfileStore reads one profile with the viewer's relationship facts.
type PublicProfileStore interface {
	// PublicProfile returns ErrUserNotFound when no profile exists or the
	// user's deletion is recorded.
	PublicProfile(ctx context.Context, viewerID, userID int64) (StoredUserMatch, error)
}

// PublicProfile is the Get public profile response.
type PublicProfile struct {
	FriendUser
	Stats           PublicStats
	IsOnline        bool
	Relationship    string
	FriendRequestID *int64
}

// PublicProfiles answers the public profile route.
type PublicProfiles struct {
	store    PublicProfileStore
	stats    StatsReader
	presence PresenceReader
	avatars  AvatarURLs
}

// NewPublicProfiles returns the public profile service.
func NewPublicProfiles(store PublicProfileStore, stats StatsReader, presence PresenceReader, avatars AvatarURLs) *PublicProfiles {
	return &PublicProfiles{store: store, stats: stats, presence: presence, avatars: avatars}
}

// Read returns userID's public profile as viewerID sees it. The profile and
// relationship come from User's records, the counters from Stats and
// is_online from the Gateway registries.
//
// Errors:
//
//   - *ValidationError: userID is not positive.
//   - ErrUserNotFound: no profile exists or the user is being deleted.
//   - ErrUnavailable: the database, Stats or presence could not answer. A
//     dependency failure is never shown as zero counters or offline.
func (s *PublicProfiles) Read(ctx context.Context, viewerID, userID int64) (PublicProfile, error) {
	if userID <= 0 {
		return PublicProfile{}, &ValidationError{Field: "user_id", Reason: "must be a positive integer"}
	}
	match, err := s.store.PublicProfile(ctx, viewerID, userID)
	if err != nil {
		return PublicProfile{}, err
	}
	stats, err := s.stats.PublicStats(ctx, userID)
	if err != nil {
		return PublicProfile{}, fmt.Errorf("%w: stats: %w", ErrUnavailable, err)
	}
	online, err := s.presence.Online(ctx, []int64{userID})
	if err != nil {
		return PublicProfile{}, err
	}
	relationship, requestID := ProjectRelationship(match.Facts)

	return PublicProfile{FriendUser: friendUser(s.avatars, match.Profile), Stats: stats,
		IsOnline: online[userID], Relationship: relationship, FriendRequestID: requestID}, nil
}
