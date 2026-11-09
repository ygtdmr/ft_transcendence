package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
)

const maxConcurrentAvatarDecodes = 4

var (
	// ErrInvalidAvatarPreset reports a preset id outside the catalog.
	ErrInvalidAvatarPreset = errors.New("invalid avatar preset")
	// ErrAvatarFileNotFound reports no stored file for a key.
	ErrAvatarFileNotFound = errors.New("avatar file not found")
	// ErrOutcomeUnknown reports a commit whose result could not be learned.
	ErrOutcomeUnknown = errors.New("commit outcome unknown")
)

// avatarPresets is the fixed catalog, in the order clients show it.
var avatarPresets = []string{
	"alien", "astronaut", "aviator-fox", "blond-sunglasses", "brain-robot", "cyborg-cat",
	"glasses-boy", "headphones-girl", "orc", "punk", "scholar-owl",
}

var avatarKeyPattern = regexp.MustCompile(`^[1-9][0-9]{0,18}-[0-9a-f]{32}\.png$`)

// ValidAvatarKey reports whether key has the only form the service generates.
func ValidAvatarKey(key string) bool {
	return avatarKeyPattern.MatchString(key)
}

// AvatarPresetEntry is one catalog entry.
type AvatarPresetEntry struct {
	PresetID  string
	AvatarURL string
}

// AvatarEventBuilder builds the event recorded with an avatar change. profile
// holds the new avatar. recipients are the users who see the change.
type AvatarEventBuilder func(profile StoredProfile, recipients []int64) OutboxEvent

// AvatarStore applies an avatar change and its event in one transaction.
type AvatarStore interface {
	// SetAvatar makes kind and ref the user's avatar and returns the profile as
	// it was before. Setting the current avatar changes nothing and records no
	// event. It returns ErrUserNotFound or *UserDeletedError when refused, and
	// an error matching ErrOutcomeUnknown when the commit result is not known.
	SetAvatar(ctx context.Context, userID int64, kind, ref string, event AvatarEventBuilder) (StoredProfile, error)
}

// AvatarFiles stores uploaded images under generated keys.
type AvatarFiles interface {
	// Store writes image under key so a reader sees all of it or nothing.
	Store(key string, image []byte) error
	// Remove deletes the file. A missing file is not an error.
	Remove(key string) error
}

// Avatars answers the avatar routes.
type Avatars struct {
	store     AvatarStore
	files     AvatarFiles
	urls      AvatarURLs
	decodes   chan struct{}
	onCleanup func(key string, err error)
}

// NewAvatars returns the avatar service. onCleanup receives a file that could
// not be removed after its avatar was replaced; it may be nil.
func NewAvatars(store AvatarStore, files AvatarFiles, urls AvatarURLs, onCleanup func(key string, err error)) *Avatars {
	return &Avatars{store: store, files: files, urls: urls,
		decodes: make(chan struct{}, maxConcurrentAvatarDecodes), onCleanup: onCleanup}
}

// Presets returns the catalog.
func (s *Avatars) Presets() []AvatarPresetEntry {
	presets := make([]AvatarPresetEntry, 0, len(avatarPresets))
	for _, id := range avatarPresets {
		presets = append(presets, AvatarPresetEntry{PresetID: id, AvatarURL: s.urls.URL(AvatarPreset, id, 0)})
	}

	return presets
}

// SelectPreset makes presetID the user's avatar and returns its URL.
//
// Errors:
//
//   - ErrInvalidAvatarPreset: presetID is not in the catalog.
//   - ErrUserNotFound, *UserDeletedError: the user has no usable profile.
//   - ErrUnavailable or other database errors.
func (s *Avatars) SelectPreset(ctx context.Context, userID int64, presetID string) (string, error) {
	if !slices.Contains(avatarPresets, presetID) {
		return "", ErrInvalidAvatarPreset
	}
	previous, err := s.store.SetAvatar(ctx, userID, AvatarPreset, presetID, s.profileEvent)
	if err != nil {
		return "", err
	}
	s.removeReplaced(previous, "")

	return s.urls.URL(AvatarPreset, presetID, userID), nil
}

// Upload validates raw, stores it under a new key and makes it the user's
// avatar. Each upload has its own file, so concurrent uploads cannot overwrite
// each other. The replaced file is removed only after the commit.
//
// Errors:
//
//   - ErrInvalidImage: see NormalizeAvatar.
//   - ErrUserNotFound, *UserDeletedError: the user has no usable profile; the
//     new file is removed.
//   - ErrUnavailable or other file and database errors. The new file is kept
//     only when the commit result is unknown, because the profile may name it.
func (s *Avatars) Upload(ctx context.Context, userID int64, raw []byte) (string, error) {
	normalized, err := s.normalize(ctx, raw)
	if err != nil {
		return "", err
	}
	key, err := newAvatarKey(userID)
	if err != nil {
		return "", err
	}
	if err := s.files.Store(key, normalized); err != nil {
		return "", err
	}
	previous, err := s.store.SetAvatar(ctx, userID, AvatarUpload, key, s.profileEvent)
	if err != nil {
		if !errors.Is(err, ErrOutcomeUnknown) {
			s.remove(key)
		}
		return "", err
	}
	s.removeReplaced(previous, key)

	return s.urls.URL(AvatarUpload, key, userID), nil
}

// normalize limits how many images are decoded at once, because decoding holds
// the full pixel data in memory.
func (s *Avatars) normalize(ctx context.Context, raw []byte) ([]byte, error) {
	select {
	case s.decodes <- struct{}{}:
		defer func() { <-s.decodes }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	return NormalizeAvatar(raw)
}

func (s *Avatars) removeReplaced(previous StoredProfile, current string) {
	if previous.AvatarKind == AvatarUpload && previous.AvatarRef != current {
		s.remove(previous.AvatarRef)
	}
}

func (s *Avatars) remove(key string) {
	if err := s.files.Remove(key); err != nil && s.onCleanup != nil {
		s.onCleanup(key, err)
	}
}

func newAvatarKey(userID int64) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}

	return fmt.Sprintf("%d-%s.png", userID, hex.EncodeToString(random[:])), nil
}

// profileEvent carries the socket fields plus who should receive them, which
// only User Service knows.
func (s *Avatars) profileEvent(profile StoredProfile, recipients []int64) OutboxEvent {
	return OutboxEvent{
		AggregateType: "profile",
		AggregateID:   strconv.FormatInt(profile.UserID, 10),
		EventType:     EventUserProfileUpdated,
		Payload: map[string]any{
			"user_id":            profile.UserID,
			"username":           profile.Username,
			"avatar_url":         s.urls.URL(profile.AvatarKind, profile.AvatarRef, profile.UserID),
			"recipient_user_ids": recipients,
		},
	}
}

// DefaultAvatarSVG returns the deterministic default image for userID. It is
// built only from the number, so no user text can reach the markup.
func DefaultAvatarSVG(userID int64) []byte {
	hue := (userID % 360) * 137 % 360

	return fmt.Appendf(nil, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">`+
		`<rect width="64" height="64" fill="hsl(%d,55%%,45%%)"/>`+
		`<circle cx="32" cy="25" r="12" fill="hsl(%d,70%%,88%%)"/>`+
		`<path d="M10 64c2-16 12-24 22-24s20 8 22 24z" fill="hsl(%d,70%%,88%%)"/></svg>`, hue, hue, hue)
}
