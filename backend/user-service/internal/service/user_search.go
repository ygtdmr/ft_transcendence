package service

import (
	"context"
	"strings"
	"unicode/utf8"
)

// User search paging limits from the public contract.
const (
	DefaultUserSearchLimit = 20
	MaxUserSearchLimit     = 50
	maxUserSearchQuery     = 20
)

// Relationships a viewer can have with another user. A block placed by the
// other user reads as RelationshipNone.
const (
	RelationshipNone            = "NONE"
	RelationshipRequestSent     = "REQUEST_SENT"
	RelationshipRequestReceived = "REQUEST_RECEIVED"
	RelationshipFriends         = "FRIENDS"
	RelationshipBlocked         = "BLOCKED"
)

// RelationshipFacts are the records that decide a viewer's relationship.
type RelationshipFacts struct {
	// ViewerBlocks is true when the viewer blocks the other user. A block in
	// the other direction is deliberately not a fact here.
	ViewerBlocks bool
	Friends      bool
	// PendingRequestID is the pair's pending request, or 0 when none exists.
	PendingRequestID int64
	// PendingFromViewer is true when the viewer sent that request.
	PendingFromViewer bool
}

// ProjectRelationship returns the viewer's relationship and the pending request
// id, which is set only for REQUEST_SENT and REQUEST_RECEIVED.
func ProjectRelationship(facts RelationshipFacts) (string, *int64) {
	switch {
	case facts.ViewerBlocks:
		return RelationshipBlocked, nil
	case facts.Friends:
		return RelationshipFriends, nil
	case facts.PendingRequestID == 0:
		return RelationshipNone, nil
	}
	id := facts.PendingRequestID
	if facts.PendingFromViewer {
		return RelationshipRequestSent, &id
	}

	return RelationshipRequestReceived, &id
}

// StoredUserMatch is one search match with the viewer's relationship facts.
type StoredUserMatch struct {
	Profile StoredProfile
	Facts   RelationshipFacts
}

// UserSearchStore finds users by username.
type UserSearchStore interface {
	// SearchUsers returns up to limit users whose username contains query,
	// ignoring case. Prefix matches come first, then username, then user_id.
	// The viewer and users being deleted are not included.
	SearchUsers(ctx context.Context, viewerID int64, query string, limit, offset int) ([]StoredUserMatch, error)
}

// UserSearchResult is one match as clients see it.
type UserSearchResult struct {
	FriendUser
	Relationship    string
	FriendRequestID *int64
}

// UserSearchPage is one page of search results.
type UserSearchPage struct {
	Limit   int
	Offset  int
	HasMore bool
	Results []UserSearchResult
}

// UserSearch answers the username search route.
type UserSearch struct {
	store   UserSearchStore
	avatars AvatarURLs
}

// NewUserSearch returns the search service.
func NewUserSearch(store UserSearchStore, avatars AvatarURLs) *UserSearch {
	return &UserSearch{store: store, avatars: avatars}
}

// Search returns one page of users matching query, seen from viewerID.
//
// Errors:
//
//   - *ValidationError: query, limit or offset is out of range.
//   - ErrUnavailable or other database errors.
func (s *UserSearch) Search(ctx context.Context, viewerID int64, query string, limit, offset int) (UserSearchPage, error) {
	query = strings.TrimSpace(query)
	if !validUserSearchQuery(query) {
		return UserSearchPage{}, &ValidationError{Field: "q", Reason: "must be 1 to 20 characters"}
	}
	if limit < 1 || limit > MaxUserSearchLimit {
		return UserSearchPage{}, &ValidationError{Field: "limit", Reason: "must be between 1 and 50"}
	}
	if offset < 0 {
		return UserSearchPage{}, &ValidationError{Field: "offset", Reason: "must not be negative"}
	}
	// One extra row shows whether another page exists.
	matches, err := s.store.SearchUsers(ctx, viewerID, query, limit+1, offset)
	if err != nil {
		return UserSearchPage{}, err
	}
	hasMore := len(matches) > limit
	if hasMore {
		matches = matches[:limit]
	}
	results := make([]UserSearchResult, 0, len(matches))
	for _, match := range matches {
		relationship, requestID := ProjectRelationship(match.Facts)
		results = append(results, UserSearchResult{FriendUser: friendUser(s.avatars, match.Profile),
			Relationship: relationship, FriendRequestID: requestID})
	}

	return UserSearchPage{Limit: limit, Offset: offset, HasMore: hasMore, Results: results}, nil
}

// validUserSearchQuery refuses text PostgreSQL cannot store as a parameter.
func validUserSearchQuery(query string) bool {
	if !utf8.ValidString(query) || strings.ContainsRune(query, 0) {
		return false
	}
	length := utf8.RuneCountInString(query)

	return length >= 1 && length <= maxUserSearchQuery
}
