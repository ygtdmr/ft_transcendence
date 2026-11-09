package testutil

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/model"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
)

type state struct {
	Users                []model.User
	Sessions             []model.Session
	Refresh              []model.RefreshToken
	Revocations          []service.SessionRevocation
	DeliveredRevocations map[string]bool
}

// MemoryRepository implements account and session transactions in memory for tests.
type MemoryRepository struct {
	state
	mu        sync.Mutex
	FailAt    string
	CommitErr error
	Calls     int
}

// WithTransaction simulates commit and rollback using a private draft.
//
// Parameters:
//
//   - r: In-memory repository holding committed state and injected failures.
//   - ctx: Interface-compatible context; this test double does not inspect it.
//   - fn: Callback receiving a draft AuthTx.
//
// Flow:
//
//  1. Increment the transaction-call counter and copy the current record slices.
//  2. Run fn against the draft; return immediately on callback or injected commit errors.
//  3. Publish the draft state only after both steps succeed.
//
// Expected output:
//
//   - Success returns nil and publishes the draft records.
//   - Failure returns the callback or commit error and preserves the original state.
func (r *MemoryRepository) WithTransaction(ctx context.Context, fn func(service.AuthTx) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Calls++
	draft := r.draft()
	if err := fn(draft); err != nil {
		return err
	}

	if r.CommitErr != nil {
		return r.CommitErr
	}

	r.state = draft.state
	return nil
}

type memoryTx struct {
	state
	FailAt string
}

// CreateUser simulates account insertion and identity conflicts.
//
// Parameters:
//
//   - t: Draft memoryTx receiving the user; this receiver is not a testing.T.
//   - _: Context accepted for interface compatibility and ignored.
//   - user: Account containing identity fields and its password hash.
//
// Flow:
//
//  1. Apply an injected user-stage failure, then check email case-insensitively.
//  2. For a new identity, assign a sequential ID and UTC creation time, then append it.
//
// Expected output:
//
//   - Success returns the stored user and nil.
//   - Failure returns a zero User and an injected error or ErrEmailTaken.
func (t *memoryTx) CreateUser(_ context.Context, user model.User) (model.User, error) {
	if t.FailAt == "user" {
		return model.User{}, errors.New("private database error")
	}

	for _, current := range t.Users {
		if strings.EqualFold(current.Email, user.Email) {
			return model.User{}, service.ErrEmailTaken
		}

	}

	user.ID, user.CreatedAt = int64(len(t.Users)+1), time.Now().UTC()
	if user.Status == "" {
		user.Status = model.AccountActive
	}
	t.Users = append(t.Users, user)
	return user, nil
}

// CreateSession simulates session insertion into the transaction draft.
//
// Parameters:
//
//   - t: Draft memoryTx receiving the session.
//   - _: Context accepted for interface compatibility and ignored.
//   - session: Session record to append.
//
// Flow:
//
//  1. Return the injected session-stage error when configured.
//  2. Otherwise append session to the draft session slice.
//
// Expected output:
//
//   - nil and one appended session, or an error without an appended session.
func (t *memoryTx) CreateSession(_ context.Context, session model.Session) error {
	if t.FailAt == "session" {
		return errors.New("private session error")
	}

	t.Sessions = append(t.Sessions, session)
	return nil
}

// CreateRefreshToken simulates refresh-token insertion into the draft.
//
// Parameters:
//
//   - t: Draft memoryTx receiving the refresh-token record.
//   - _: Context accepted for interface compatibility and ignored.
//   - refresh: Refresh-token metadata to append.
//
// Flow:
//
//  1. Return the injected refresh-stage error when configured.
//  2. Otherwise append refresh to the draft refresh-token slice.
//
// Expected output:
//
//   - nil and one appended refresh record, or an error without an appended record.
func (t *memoryTx) CreateRefreshToken(_ context.Context, refresh model.RefreshToken) error {
	if t.FailAt == "refresh" {
		return errors.New("private refresh error")
	}

	t.Refresh = append(t.Refresh, refresh)
	return nil
}
