package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrConfirmationMismatch reports that confirm_username is missing or is
	// not the current username. Nothing was written.
	ErrConfirmationMismatch = errors.New("confirm_username does not match the current username")
	// ErrDeletionInProgress reports that a deletion operation is recorded but
	// has not finished yet. The worker keeps resuming it.
	ErrDeletionInProgress = errors.New("account deletion is in progress")
	// ErrParticipantRejected reports a permanent answer from a deletion
	// participant: a conflict, a missing record or a refused credential. The
	// operation stops and needs investigation.
	ErrParticipantRejected = errors.New("account deletion participant rejected the request")
)

// Deletion operation states stored by DeletionStore.
const (
	DeletionPending   = "pending"
	DeletionCompleted = "completed"
	DeletionFailed    = "failed"
)

// Deletion steps, in the order the coordinator runs them.
const (
	StepAuthBegin    = "auth_begin"
	StepGameCleanup  = "game_cleanup"
	StepStatsCleanup = "stats_cleanup"
	StepUserCleanup  = "user_cleanup"
)

// DeletionOperation is the coordinator's durable record for one user.
type DeletionOperation struct {
	OperationID  string
	UserID       int64
	State        string
	Attempts     int
	AuthBegun    bool
	GameCleaned  bool
	StatsCleaned bool
	UserCleaned  bool
}

// UserCleanup is what the local cleanup removed, for work done after commit.
type UserCleanup struct {
	// UploadKey names the uploaded avatar file to remove, or is empty.
	UploadKey string
}

// DeletionStore keeps deletion operations and applies User's own cleanup.
type DeletionStore interface {
	// Start returns the user's deletion operation, recording a new one under
	// the account lock when none exists. Created reports a new record.
	Start(ctx context.Context, userID int64) (op DeletionOperation, created bool, err error)
	// Operation returns the user's deletion operation, or ErrUserNotFound.
	Operation(ctx context.Context, userID int64) (DeletionOperation, error)
	// MarkStep records that a participant step is confirmed. It is idempotent.
	MarkStep(ctx context.Context, operationID, step string) error
	// CleanUpUser deletes the profile, avatar reference, friendships, friend
	// requests, blocks and provisioning record of the operation's user in one
	// transaction, records the events from events, and marks the step. A
	// repeated call after the step is marked does nothing.
	CleanUpUser(ctx context.Context, operationID string, events DeletionEventBuilder) (UserCleanup, error)
	// Complete marks the operation completed.
	Complete(ctx context.Context, operationID string) error
	// RecordFailure stores the error and the next attempt time. permanent
	// stops automatic retries.
	RecordFailure(ctx context.Context, operationID, message string, next time.Time, permanent bool) error
	// Due returns at most limit unfinished operations whose next attempt is due.
	Due(ctx context.Context, limit int) ([]DeletionOperation, error)
}

// DeletionEventBuilder returns the social events the local cleanup records:
// friendIDs are active friends, requests are pending friend requests.
type DeletionEventBuilder func(userID int64, friendIDs []int64, requests []StoredFriendRequest) []OutboxEvent

// DeletionAuth is Auth's part of the workflow.
type DeletionAuth interface {
	// BeginDeletion blocks authentication and revokes every session.
	BeginDeletion(ctx context.Context, operationID string, userID int64) error
	// CompleteDeletion removes the Auth account after every other step.
	CompleteDeletion(ctx context.Context, operationID string) error
}

// AccountCleaner is one participant's cleanup of its own records.
type AccountCleaner interface {
	CleanUpAccount(ctx context.Context, operationID string, userID int64) error
}

// FileRemover deletes an uploaded avatar file. A missing file is not an error.
type FileRemover interface {
	Remove(key string) error
}

// AccountDeletions coordinates DELETE /api/v1/users/me.
//
// Participant errors that wrap ErrUnavailable are retried with backoff; errors
// that wrap ErrParticipantRejected stop the operation. Any other error is
// treated as transient.
type AccountDeletions struct {
	store    DeletionStore
	profiles ProfileReader
	auth     DeletionAuth
	game     AccountCleaner
	stats    AccountCleaner
	files    FileRemover
	// OnFileError reports a file that could not be removed after commit.
	OnFileError func(key string, err error)
	now         func() time.Time
}

// NewAccountDeletions returns the coordinator. game may be nil outside
// production, which skips the Game Session step; files may be nil when no
// avatar storage is configured.
func NewAccountDeletions(store DeletionStore, profiles ProfileReader, auth DeletionAuth, game, stats AccountCleaner,
	files FileRemover) *AccountDeletions {
	return &AccountDeletions{store: store, profiles: profiles, auth: auth, game: game, stats: stats, files: files, now: time.Now}
}

// Request validates the typed confirmation, records the operation and runs it.
//
// A user whose deletion is already recorded resumes that operation without a
// confirmation check, because the profile may already be gone.
//
// Errors:
//
//   - ErrConfirmationMismatch: nothing was written.
//   - ErrUnavailable before the operation is recorded: nothing was written.
//   - ErrDeletionInProgress: the operation is recorded but did not finish; it
//     wraps the cause.
func (s *AccountDeletions) Request(ctx context.Context, userID int64, confirmUsername string) error {
	stored, err := s.profiles.StoredProfile(ctx, userID)
	var deleted *UserDeletedError
	switch {
	case errors.As(err, &deleted):
		op, err := s.store.Operation(ctx, userID)
		if err != nil {
			return err
		}
		return s.run(ctx, op)
	case err != nil:
		return err
	case !strings.EqualFold(strings.TrimSpace(confirmUsername), stored.Username):
		return ErrConfirmationMismatch
	}
	op, _, err := s.store.Start(ctx, userID)
	if err != nil {
		return err
	}

	return s.run(ctx, op)
}

// ResumeDue runs every due unfinished operation, at most limit at a time. It
// returns how many operations it ran and the first store error.
func (s *AccountDeletions) ResumeDue(ctx context.Context, limit int) (int, error) {
	ops, err := s.store.Due(ctx, limit)
	if err != nil {
		return 0, err
	}
	for _, op := range ops {
		_ = s.run(ctx, op)
	}

	return len(ops), nil
}

// run executes the remaining steps in contract order: Auth Begin, Game Session
// and Stats cleanup, User's own cleanup, then Auth Complete.
func (s *AccountDeletions) run(ctx context.Context, op DeletionOperation) error {
	switch op.State {
	case DeletionCompleted:
		return nil
	case DeletionFailed:
		return fmt.Errorf("%w: operation %s stopped", ErrDeletionInProgress, op.OperationID)
	}
	if err := s.steps(ctx, op); err != nil {
		permanent := errors.Is(err, ErrParticipantRejected)
		next := s.now().Add(deletionBackoff(op.Attempts))
		if recordErr := s.store.RecordFailure(context.WithoutCancel(ctx), op.OperationID, err.Error(), next, permanent); recordErr != nil {
			err = errors.Join(err, recordErr)
		}
		return fmt.Errorf("%w: %w", ErrDeletionInProgress, err)
	}

	return nil
}

func (s *AccountDeletions) steps(ctx context.Context, op DeletionOperation) error {
	if !op.AuthBegun {
		if err := s.auth.BeginDeletion(ctx, op.OperationID, op.UserID); err != nil {
			return fmt.Errorf("auth begin: %w", err)
		}
		if err := s.store.MarkStep(ctx, op.OperationID, StepAuthBegin); err != nil {
			return err
		}
	}
	if !op.GameCleaned {
		if s.game != nil {
			if err := s.game.CleanUpAccount(ctx, op.OperationID, op.UserID); err != nil {
				return fmt.Errorf("game cleanup: %w", err)
			}
		}
		if err := s.store.MarkStep(ctx, op.OperationID, StepGameCleanup); err != nil {
			return err
		}
	}
	if !op.StatsCleaned {
		if err := s.stats.CleanUpAccount(ctx, op.OperationID, op.UserID); err != nil {
			return fmt.Errorf("stats cleanup: %w", err)
		}
		if err := s.store.MarkStep(ctx, op.OperationID, StepStatsCleanup); err != nil {
			return err
		}
	}
	if !op.UserCleaned {
		cleanup, err := s.store.CleanUpUser(ctx, op.OperationID, deletionEvents)
		if err != nil {
			return fmt.Errorf("user cleanup: %w", err)
		}
		s.removeFile(cleanup.UploadKey)
	}
	if err := s.auth.CompleteDeletion(ctx, op.OperationID); err != nil {
		return fmt.Errorf("auth complete: %w", err)
	}

	return s.store.Complete(ctx, op.OperationID)
}

// removeFile deletes the uploaded avatar after the profile row is gone. A
// failure leaves an unused file and is only reported.
func (s *AccountDeletions) removeFile(key string) {
	if key == "" || s.files == nil {
		return
	}
	if err := s.files.Remove(key); err != nil && s.OnFileError != nil {
		s.OnFileError(key, err)
	}
}

// deletionBackoff doubles from 5 seconds up to 5 minutes.
func deletionBackoff(attempts int) time.Duration {
	delay := 5 * time.Second
	for range min(max(attempts, 0), 6) {
		delay *= 2
	}

	return min(delay, 5*time.Minute)
}

// deletionEvents tells each active friend that the friendship ended and each
// pending request's other side that the request was cancelled, using the
// same events as an ordinary removal or withdrawal.
func deletionEvents(userID int64, friendIDs []int64, requests []StoredFriendRequest) []OutboxEvent {
	events := make([]OutboxEvent, 0, len(friendIDs)+len(requests))
	for _, request := range requests {
		request.State = FriendRequestCancelled
		events = append(events, friendRequestEvent(request, EventFriendRequestResolved, resolvedPayload(request)))
	}
	for _, friendID := range friendIDs {
		events = append(events, friendRemovedEvent(userID, friendID))
	}

	return events
}
