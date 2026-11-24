package service

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrAccountNotFound reports that Auth holds no account for the user.
	ErrAccountNotFound = errors.New("auth account not found")
	// ErrMembershipUnavailable reports that Game Session cannot answer a
	// membership read now.
	ErrMembershipUnavailable = errors.New("game session membership read unavailable")
	// ErrOwnerInactive reports that the session's owner has no active profile
	// or account: deletion is recorded, the account is not ACTIVE, or either
	// record is missing.
	ErrOwnerInactive = errors.New("session owner is not active")
)

// AccountActive is the only Auth account status an own profile is served for.
const AccountActive = "ACTIVE"

// Account is the owner-only metadata Auth holds.
type Account struct {
	Email     string
	Status    string
	CreatedAt time.Time
}

// AccountReader reads account metadata from Auth.
type AccountReader interface {
	// Account returns userID's metadata. It returns ErrAccountNotFound when
	// Auth holds no account and ErrUnavailable when Auth cannot answer now.
	Account(ctx context.Context, userID int64) (Account, error)
}

// OwnStats are the counters shown to their owner, including penalty points.
type OwnStats struct {
	PublicStats
	PenaltyPoints int
}

// OwnStatsReader reads the owner's counters from Stats Service.
type OwnStatsReader interface {
	// OwnStats returns userID's counters. Any error means they are unknown.
	OwnStats(ctx context.Context, userID int64) (OwnStats, error)
}

// Membership is the owner's live room link. Both fields are nil together.
type Membership struct {
	RoomID     *int64
	APIVersion *string
}

// MembershipReader reads the live room membership owned by Game Session.
type MembershipReader interface {
	// CurrentMembership returns userID's room, or both fields nil when the
	// user is in no room. It returns ErrMembershipUnavailable when Game
	// Session cannot answer now.
	CurrentMembership(ctx context.Context, userID int64) (Membership, error)
}

// NoMembership reports no active room. It is used outside production when
// Game Session is not configured.
type NoMembership struct{}

// CurrentMembership implements MembershipReader.
func (NoMembership) CurrentMembership(context.Context, int64) (Membership, error) {
	return Membership{}, nil
}

// OwnProfile is the Get own profile response.
type OwnProfile struct {
	CanonicalProfile
	Email      string
	Stats      OwnStats
	Membership Membership
}

// OwnProfiles answers the own profile route.
type OwnProfiles struct {
	profiles    *ProfileReadService
	accounts    AccountReader
	stats       OwnStatsReader
	memberships MembershipReader
}

// NewOwnProfiles returns the own profile service.
func NewOwnProfiles(profiles *ProfileReadService, accounts AccountReader, stats OwnStatsReader,
	memberships MembershipReader) *OwnProfiles {
	return &OwnProfiles{profiles: profiles, accounts: accounts, stats: stats, memberships: memberships}
}

// Read returns the session owner's profile. Username and avatar come from
// User's records, email and creation time from Auth, counters and penalty
// points from Stats, and the room link from Game Session.
//
// Errors:
//
//   - ErrOwnerInactive: deletion is recorded, the profile or account is
//     missing, or the account is not ACTIVE.
//   - ErrUnavailable: the database, Auth, Stats or Game Session could not
//     answer. A dependency failure is never shown as zero counters or as no
//     active room.
//   - Other errors: an unexpected answer from a dependency.
func (s *OwnProfiles) Read(ctx context.Context, userID int64) (OwnProfile, error) {
	profile, err := s.profiles.CanonicalProfile(ctx, userID)
	var deleted *UserDeletedError
	if errors.As(err, &deleted) || errors.Is(err, ErrUserNotFound) {
		return OwnProfile{}, fmt.Errorf("%w: %w", ErrOwnerInactive, err)
	}
	if err != nil {
		return OwnProfile{}, err
	}
	account, err := s.accounts.Account(ctx, userID)
	if errors.Is(err, ErrAccountNotFound) {
		return OwnProfile{}, fmt.Errorf("%w: %w", ErrOwnerInactive, err)
	}
	if err != nil {
		return OwnProfile{}, err
	}
	if account.Status != AccountActive {
		return OwnProfile{}, fmt.Errorf("%w: account status %s", ErrOwnerInactive, account.Status)
	}
	stats, err := s.stats.OwnStats(ctx, userID)
	if err != nil {
		return OwnProfile{}, fmt.Errorf("%w: stats: %w", ErrUnavailable, err)
	}
	membership, err := s.membership(ctx, userID)
	if err != nil {
		return OwnProfile{}, err
	}
	profile.CreatedAt = account.CreatedAt

	return OwnProfile{CanonicalProfile: profile, Email: account.Email, Stats: stats, Membership: membership}, nil
}

// membership reads and checks the room link. Game Session serves only v2
// rooms; the contract also allows v1 for a legacy room.
func (s *OwnProfiles) membership(ctx context.Context, userID int64) (Membership, error) {
	membership, err := s.memberships.CurrentMembership(ctx, userID)
	if errors.Is(err, ErrMembershipUnavailable) {
		return Membership{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if err != nil {
		return Membership{}, err
	}
	if (membership.RoomID == nil) != (membership.APIVersion == nil) {
		return Membership{}, errors.New("inconsistent Game membership")
	}
	if membership.RoomID != nil && (*membership.RoomID <= 0 || (*membership.APIVersion != "v1" && *membership.APIVersion != "v2")) {
		return Membership{}, errors.New("invalid Game membership")
	}

	return membership, nil
}
