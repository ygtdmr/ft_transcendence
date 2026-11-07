package service

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"time"
)

// ErrUserNotFound reports that no profile exists for the requested user.
var ErrUserNotFound = errors.New("user not found")

// Avatar kinds stored on a profile.
const (
	AvatarDefault = "default"
	AvatarPreset  = "preset"
	AvatarUpload  = "upload"
)

// StoredProfile is a profile row as the store reads it.
type StoredProfile struct {
	UserID     int64
	Username   string
	AvatarKind string
	AvatarRef  string
	CreatedAt  time.Time
}

// CanonicalProfile is the current profile other services compose into their responses.
type CanonicalProfile struct {
	UserID    int64
	Username  string
	AvatarURL string
	CreatedAt time.Time
}

// ProfileReader reads the current profile row.
type ProfileReader interface {
	// StoredProfile returns *UserDeletedError when deletion is recorded,
	// ErrUserNotFound when no profile exists, and ErrUnavailable on outage.
	StoredProfile(ctx context.Context, userID int64) (StoredProfile, error)
}

// AvatarURLs builds public avatar URLs from stored avatar references.
type AvatarURLs struct {
	// Base is the public origin, such as https://example.com, or empty for
	// root-relative URLs.
	Base string
}

// URL returns the avatar URL for a stored avatar.
//
// A default avatar is derived from userID; presets and uploads use their stored
// reference, escaped as one path segment.
func (a AvatarURLs) URL(kind, ref string, userID int64) string {
	switch kind {
	case AvatarPreset:
		return a.Base + "/avatars/presets/" + url.PathEscape(ref) + ".png"
	case AvatarUpload:
		return a.Base + "/static/avatars/uploaded/" + url.PathEscape(ref)
	default:
		return a.Base + "/static/avatars/default/" + strconv.FormatInt(userID, 10) + ".svg"
	}
}

type ProfileReadService struct {
	store   ProfileReader
	avatars AvatarURLs
}

// NewProfileRead returns the service answering canonical profile reads.
func NewProfileRead(store ProfileReader, avatars AvatarURLs) *ProfileReadService {
	return &ProfileReadService{store: store, avatars: avatars}
}

// CanonicalProfile returns the most recent committed profile for userID.
//
// Errors:
//
//   - *ValidationError: userID is not positive.
//   - *UserDeletedError: account deletion is recorded for userID.
//   - ErrUserNotFound: no profile exists, including users whose provisioning
//     never committed.
//   - ErrUnavailable or other errors: the store failed.
func (s *ProfileReadService) CanonicalProfile(ctx context.Context, userID int64) (CanonicalProfile, error) {
	if userID <= 0 {
		return CanonicalProfile{}, &ValidationError{Field: "user_id", Reason: "must be a positive integer"}
	}
	stored, err := s.store.StoredProfile(ctx, userID)
	if err != nil {
		return CanonicalProfile{}, err
	}

	return CanonicalProfile{
		UserID:    stored.UserID,
		Username:  stored.Username,
		AvatarURL: s.avatars.URL(stored.AvatarKind, stored.AvatarRef, stored.UserID),
		CreatedAt: stored.CreatedAt,
	}, nil
}
