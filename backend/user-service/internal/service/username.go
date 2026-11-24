package service

import (
	"context"
	"strconv"
	"strings"
)

// ProfileEventBuilder builds user.profile.updated for a changed profile and
// the users who should receive it.
type ProfileEventBuilder func(profile StoredProfile, recipients []int64) OutboxEvent

// UsernameStore applies a username change in one transaction.
type UsernameStore interface {
	// Rename sets userID's username and records event in the same
	// transaction. An identical username changes nothing.
	//
	// Errors: ErrUsernameTaken, *UserDeletedError, ErrUserNotFound,
	// ErrOutcomeUnknown, ErrUnavailable.
	Rename(ctx context.Context, userID int64, username string, event ProfileEventBuilder) error
}

// Usernames answers the username part of PATCH /api/v1/users/me.
type Usernames struct {
	store UsernameStore
	urls  AvatarURLs
}

// NewUsernames returns the username service.
func NewUsernames(store UsernameStore, urls AvatarURLs) *Usernames {
	return &Usernames{store: store, urls: urls}
}

// Rename trims raw and applies the registration username rule:
// ^[A-Za-z0-9_]{3,20}$, unique without regard to letter case. A change of
// letter case only is a change.
//
// Errors:
//
//   - ErrInvalidUsername: the trimmed value breaks the rule; nothing is written.
//   - Store errors; see UsernameStore.Rename.
func (s *Usernames) Rename(ctx context.Context, userID int64, raw string) error {
	username := strings.TrimSpace(raw)
	if !usernamePattern.MatchString(username) {
		return ErrInvalidUsername
	}

	return s.store.Rename(ctx, userID, username, s.event)
}

func (s *Usernames) event(profile StoredProfile, recipients []int64) OutboxEvent {
	return profileUpdatedEvent(s.urls, profile, recipients)
}

// profileUpdatedEvent carries the socket fields plus who should receive them,
// which only User Service knows.
func profileUpdatedEvent(urls AvatarURLs, profile StoredProfile, recipients []int64) OutboxEvent {
	return OutboxEvent{
		AggregateType: "profile",
		AggregateID:   strconv.FormatInt(profile.UserID, 10),
		EventType:     EventUserProfileUpdated,
		Payload: map[string]any{
			"user_id":            profile.UserID,
			"username":           profile.Username,
			"avatar_url":         urls.URL(profile.AvatarKind, profile.AvatarRef, profile.UserID),
			"recipient_user_ids": recipients,
		},
	}
}
