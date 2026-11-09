package service

import "context"

// FriendshipVerdictReader answers from current User Service records.
type FriendshipVerdictReader interface {
	// ActiveFriendship reports whether the two users have an active friendship,
	// neither blocks the other and neither is being deleted.
	ActiveFriendship(ctx context.Context, firstID, secondID int64) (bool, error)
}

// FriendshipVerdicts answers the private active-friend verdict.
type FriendshipVerdicts struct {
	reader FriendshipVerdictReader
}

// NewFriendshipVerdicts returns the verdict service.
func NewFriendshipVerdicts(reader FriendshipVerdictReader) *FriendshipVerdicts {
	return &FriendshipVerdicts{reader: reader}
}

// Verdict reports whether actorID and targetID are active friends. False never
// says why, so a caller cannot learn that a block exists or who placed it.
//
// Errors:
//
//   - *ValidationError: an id is not positive, or both ids are equal.
//   - ErrUnavailable or other database errors. A failure is never a false verdict.
func (s *FriendshipVerdicts) Verdict(ctx context.Context, actorID, targetID int64) (bool, error) {
	if actorID <= 0 {
		return false, &ValidationError{Field: "actor_user_id", Reason: "must be a positive integer"}
	}
	if targetID <= 0 {
		return false, &ValidationError{Field: "target_user_id", Reason: "must be a positive integer"}
	}
	if actorID == targetID {
		return false, &ValidationError{Field: "target_user_id", Reason: "must differ from actor_user_id"}
	}

	return s.reader.ActiveFriendship(ctx, actorID, targetID)
}
