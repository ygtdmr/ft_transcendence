package service

import (
	"context"
	"errors"
	"strconv"
	"time"
)

// Friend request states stored in friend_requests.state.
const (
	FriendRequestPending   = "pending"
	FriendRequestAccepted  = "accepted"
	FriendRequestDeclined  = "declined"
	FriendRequestCancelled = "cancelled"
)

var (
	// ErrRelationshipBlocked reports that either user blocks the other.
	ErrRelationshipBlocked = errors.New("relationship blocked")
	// ErrAlreadyFriends reports an active friendship.
	ErrAlreadyFriends = errors.New("already friends")
	// ErrRequestNotFound reports no pending request with this id for the caller.
	ErrRequestNotFound = errors.New("friend request not found")
)

// PendingRequestError reports that a pending request already joins the pair.
type PendingRequestError struct {
	RequestID int64
	// Outgoing is true when the sender already sent it; false when the other
	// user sent it to the sender.
	Outgoing bool
}

func (e *PendingRequestError) Error() string { return "a friend request is already pending" }

// StoredFriendRequest is a friend request with both users' profile rows.
type StoredFriendRequest struct {
	ID        int64
	From      StoredProfile
	To        StoredProfile
	State     string
	CreatedAt time.Time
}

// FriendEventBuilder builds the event recorded in the request's transaction.
type FriendEventBuilder func(StoredFriendRequest) OutboxEvent

// FriendRequestStore applies each friend request change in one transaction.
type FriendRequestStore interface {
	// CreateFriendRequest returns ErrUserNotFound, ErrRelationshipBlocked,
	// ErrAlreadyFriends or *PendingRequestError when the request is refused.
	CreateFriendRequest(ctx context.Context, senderID, recipientID int64, event FriendEventBuilder) (StoredFriendRequest, error)
	// ResolveFriendRequest moves a pending request to state, and creates the
	// friendship when state is FriendRequestAccepted. Only the recipient may
	// accept or decline; only the sender may cancel. It returns
	// ErrRequestNotFound or ErrRelationshipBlocked when refused.
	ResolveFriendRequest(ctx context.Context, requestID, callerID int64, state string, event FriendEventBuilder) (StoredFriendRequest, error)
	// PendingFriendRequests returns the user's pending requests, newest first.
	PendingFriendRequests(ctx context.Context, userID int64) ([]StoredFriendRequest, error)
}

// FriendUser is one side of a friend request as clients see it.
type FriendUser struct {
	UserID    int64  `json:"user_id"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url"`
}

// FriendRequest is a pending friend request as clients see it.
type FriendRequest struct {
	ID        int64
	From      FriendUser
	To        FriendUser
	Status    string
	CreatedAt time.Time
}

// Friend is a List friends entry.
type Friend struct {
	FriendUser
	IsOnline bool
}

// FriendRequests answers the friend request routes.
type FriendRequests struct {
	store    FriendRequestStore
	avatars  AvatarURLs
	presence PresenceReader
}

// NewFriendRequests returns the friend request service.
func NewFriendRequests(store FriendRequestStore, avatars AvatarURLs, presence PresenceReader) *FriendRequests {
	return &FriendRequests{store: store, avatars: avatars, presence: presence}
}

// Send creates a pending request from senderID to recipientID and records
// friend.request.received in the same transaction.
//
// Errors:
//
//   - *ValidationError: recipientID is not positive or equals senderID.
//   - ErrUserNotFound, ErrRelationshipBlocked, ErrAlreadyFriends,
//     *PendingRequestError: the request is refused; nothing changed.
//   - ErrUnavailable or other database errors.
func (s *FriendRequests) Send(ctx context.Context, senderID, recipientID int64) (FriendRequest, error) {
	if recipientID <= 0 {
		return FriendRequest{}, &ValidationError{Field: "user_id", Reason: "must be a positive integer"}
	}
	if recipientID == senderID {
		return FriendRequest{}, &ValidationError{Field: "user_id", Reason: "must not be your own user"}
	}
	stored, err := s.store.CreateFriendRequest(ctx, senderID, recipientID, s.receivedEvent)
	if err != nil {
		return FriendRequest{}, err
	}

	return s.view(stored), nil
}

// List returns the pending requests sent to and by userID, newest first.
func (s *FriendRequests) List(ctx context.Context, userID int64) (incoming, outgoing []FriendRequest, err error) {
	stored, err := s.store.PendingFriendRequests(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	incoming, outgoing = []FriendRequest{}, []FriendRequest{}
	for _, request := range stored {
		if request.To.UserID == userID {
			incoming = append(incoming, s.view(request))
		} else {
			outgoing = append(outgoing, s.view(request))
		}
	}

	return incoming, outgoing, nil
}

// Accept resolves a request addressed to callerID and creates the friendship.
// It returns the new friend. The sender's presence is read before the
// friendship is committed, so an unknown presence changes nothing and the
// caller can retry the same request.
//
// Errors:
//
//   - ErrRequestNotFound, ErrRelationshipBlocked: nothing changed.
//   - ErrUnavailable or other errors. A presence failure before the commit
//     leaves the request pending.
func (s *FriendRequests) Accept(ctx context.Context, requestID, callerID int64) (Friend, error) {
	pending, err := s.store.PendingFriendRequests(ctx, callerID)
	if err != nil {
		return Friend{}, err
	}
	var online map[int64]bool
	for _, request := range pending {
		if request.ID == requestID && request.To.UserID == callerID {
			if online, err = s.presence.Online(ctx, []int64{request.From.UserID}); err != nil {
				return Friend{}, err
			}
			break
		}
	}
	stored, err := s.store.ResolveFriendRequest(ctx, requestID, callerID, FriendRequestAccepted, s.resolvedEvent)
	if err != nil {
		return Friend{}, err
	}
	friend := s.user(stored.From)
	isOnline, known := online[friend.UserID]
	if !known {
		// The request became pending after the read above; the friendship is
		// committed, so a presence failure here cannot be undone.
		after, err := s.presence.Online(ctx, []int64{friend.UserID})
		if err != nil {
			return Friend{}, err
		}
		isOnline = after[friend.UserID]
	}

	return Friend{FriendUser: friend, IsOnline: isOnline}, nil
}

// Decline resolves a request addressed to callerID without a friendship.
func (s *FriendRequests) Decline(ctx context.Context, requestID, callerID int64) error {
	_, err := s.store.ResolveFriendRequest(ctx, requestID, callerID, FriendRequestDeclined, s.resolvedEvent)

	return err
}

// Cancel withdraws a request sent by callerID.
func (s *FriendRequests) Cancel(ctx context.Context, requestID, callerID int64) error {
	_, err := s.store.ResolveFriendRequest(ctx, requestID, callerID, FriendRequestCancelled, s.resolvedEvent)

	return err
}

func (s *FriendRequests) user(profile StoredProfile) FriendUser {
	return friendUser(s.avatars, profile)
}

func friendUser(avatars AvatarURLs, profile StoredProfile) FriendUser {
	return FriendUser{UserID: profile.UserID, Username: profile.Username,
		AvatarURL: avatars.URL(profile.AvatarKind, profile.AvatarRef, profile.UserID)}
}

func (s *FriendRequests) view(stored StoredFriendRequest) FriendRequest {
	return FriendRequest{ID: stored.ID, From: s.user(stored.From), To: s.user(stored.To),
		Status: requestStatus(stored.State), CreatedAt: stored.CreatedAt}
}

// requestStatus returns the public status for a stored state.
func requestStatus(state string) string {
	switch state {
	case FriendRequestAccepted:
		return "ACCEPTED"
	case FriendRequestDeclined:
		return "DECLINED"
	case FriendRequestCancelled:
		return "CANCELLED"
	default:
		return "PENDING"
	}
}

func friendRequestEvent(stored StoredFriendRequest, eventType string, payload map[string]any) OutboxEvent {
	return OutboxEvent{
		AggregateType: "friend_request",
		AggregateID:   strconv.FormatInt(stored.ID, 10),
		EventType:     eventType,
		Payload:       payload,
	}
}

// receivedEvent carries the request exactly as the chat socket shows it.
func (s *FriendRequests) receivedEvent(stored StoredFriendRequest) OutboxEvent {
	view := s.view(stored)

	return friendRequestEvent(stored, EventFriendRequestReceived, map[string]any{"request": map[string]any{
		"request_id": view.ID,
		"from_user":  view.From,
		"to_user":    view.To,
		"status":     view.Status,
		"created_at": view.CreatedAt.UTC().Format(time.RFC3339Nano),
	}})
}

// resolvedEvent carries the socket fields. An accepted request also carries
// both users, because each recipient's socket shows the other user as friend.
func (s *FriendRequests) resolvedEvent(stored StoredFriendRequest) OutboxEvent {
	payload := resolvedPayload(stored)
	if stored.State == FriendRequestAccepted {
		payload["from_user"] = s.user(stored.From)
		payload["to_user"] = s.user(stored.To)
	}

	return friendRequestEvent(stored, EventFriendRequestResolved, payload)
}

func resolvedPayload(stored StoredFriendRequest) map[string]any {
	return map[string]any{
		"request_id":   stored.ID,
		"from_user_id": stored.From.UserID,
		"to_user_id":   stored.To.UserID,
		"status":       requestStatus(stored.State),
	}
}
