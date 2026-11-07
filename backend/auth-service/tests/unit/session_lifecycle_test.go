package unit_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/model"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/testutil"
)

type lifecycleSpy struct {
	repo         *testutil.MemoryRepository
	membership   service.Membership
	failure      string
	ended        []string
	closed       []string
	failSessions map[string]bool
}

func (s *lifecycleSpy) CurrentMembership(context.Context, int64) (service.Membership, error) {
	if s.failure == "membership" {
		return service.Membership{}, errors.New("private Game outage")
	}
	return s.membership, nil
}

func (s *lifecycleSpy) EndSession(_ context.Context, _ int64, id string) error {
	if s.failure == "end" {
		return errors.New("private Game departure failure")
	}
	for _, session := range s.repo.Sessions {
		if session.ID == id && session.RevokedAt != nil {
			return errors.New("departure happened after session revocation")
		}
	}
	s.ended = append(s.ended, id)
	return nil
}

func (s *lifecycleSpy) SessionRevoked(ctx context.Context, _ int64, id, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.failure == "close" || s.failSessions[id] {
		return errors.New("private Gateway unavailable")
	}
	for _, session := range s.repo.Sessions {
		if session.ID == id && session.RevokedAt == nil {
			return errors.New("socket notification happened before commit")
		}
	}
	s.closed = append(s.closed, id)
	return nil
}

func TestLiveSessionMembership(t *testing.T) {
	roomID, version := int64(1001), "v2"
	lifecycle := &lifecycleSpy{membership: service.Membership{RoomID: &roomID, APIVersion: &version}}
	auth, repo, registered := registeredSession(t, service.WithSessionLifecycle(lifecycle))
	lifecycle.repo = repo
	identity, err := auth.Authenticate(context.Background(), registered.Credentials.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	result, err := auth.CurrentSession(context.Background(), identity)
	if err != nil || result.ActiveRoomID == nil || *result.ActiveRoomID != roomID || *result.ActiveRoomAPIVersion != "v2" {
		t.Fatalf("live membership: %+v %v", result, err)
	}
	lifecycle.membership = service.Membership{}
	result, err = auth.CurrentSession(context.Background(), identity)
	if err != nil || result.ActiveRoomID != nil || result.ActiveRoomAPIVersion != nil {
		t.Fatal("membership was cached after Game cleared it")
	}
	lifecycle.membership = service.Membership{RoomID: &roomID}
	if _, err := auth.CurrentSession(context.Background(), identity); err == nil {
		t.Fatal("inconsistent Game membership was reported as valid")
	}
	lifecycle.failure = "membership"
	if _, err := auth.CurrentSession(context.Background(), identity); err == nil {
		t.Fatal("Game outage was reported as an empty membership")
	}
}

func TestLogoutLifecycleOrdering(t *testing.T) {
	lifecycle := &lifecycleSpy{}
	auth, repo, registered := registeredSession(t, service.WithSessionLifecycle(lifecycle))
	lifecycle.repo = repo
	identity, err := auth.Authenticate(context.Background(), registered.Credentials.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.Logout(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	if len(lifecycle.ended) != 1 || len(lifecycle.closed) != 1 || lifecycle.closed[0] != identity.SessionID {
		t.Fatal("logout did not depart and close the same session")
	}
	assertRejectedAccess(t, auth, registered.Credentials.AccessToken)
	if len(repo.Revocations) != 1 || !repo.DeliveredRevocations[identity.SessionID] {
		t.Fatal("successful Gateway delivery was not acknowledged")
	}
}

func TestLogoutRollbackAndDepartureFailure(t *testing.T) {
	for _, stage := range []string{"end", "revoke", "outbox", "commit"} {
		t.Run(stage, func(t *testing.T) {
			lifecycle := &lifecycleSpy{failure: stage}
			auth, repo, registered := registeredSession(t, service.WithSessionLifecycle(lifecycle))
			lifecycle.repo = repo
			identity, err := auth.Authenticate(context.Background(), registered.Credentials.AccessToken)
			if err != nil {
				t.Fatal(err)
			}
			repo.FailAt = stage
			if stage == "commit" {
				repo.CommitErr = errors.New("private commit error")
			}
			if err := auth.Logout(context.Background(), identity); err == nil {
				t.Fatal("failed logout reported success")
			}
			if repo.Sessions[0].RevokedAt != nil || repo.Refresh[0].RevokedAt != nil || len(repo.Revocations) != 0 || len(lifecycle.closed) != 0 {
				t.Fatal("failed departure or transaction changed credential state")
			}
		})
	}
}

func TestRevocationNotificationRetry(t *testing.T) {
	for _, stage := range []string{"close", "mark_revocation"} {
		t.Run(stage, func(t *testing.T) {
			lifecycle := &lifecycleSpy{failure: stage}
			auth, repo, registered := registeredSession(t, service.WithSessionLifecycle(lifecycle))
			lifecycle.repo, repo.FailAt = repo, stage
			identity, err := auth.Authenticate(context.Background(), registered.Credentials.AccessToken)
			if err != nil {
				t.Fatal(err)
			}
			if err := auth.Logout(context.Background(), identity); err == nil {
				t.Fatal("delivery outage was hidden")
			}
			assertRejectedAccess(t, auth, registered.Credentials.AccessToken)
			if len(repo.Revocations) != 1 || repo.DeliveredRevocations[identity.SessionID] {
				t.Fatal("undelivered revocation was lost")
			}
			lifecycle.failure, repo.FailAt = "", ""
			if err := repo.ScheduleSessionRevocationRetry(context.Background(), identity.SessionID, time.Now().Add(-time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := auth.DeliverPendingRevocations(context.Background()); err != nil || !repo.DeliveredRevocations[identity.SessionID] {
				t.Fatalf("committed notification was not retried: %v", err)
			}
		})
	}
}

func TestFailedRevocationsDoNotStarveLaterFamilies(t *testing.T) {
	repo, lifecycle, lastID := queuedSessionRevocations()
	auth := service.NewAuth(repo, testutil.TokenIssuer(t), service.WithSessionLifecycle(lifecycle))
	if err := auth.DeliverPendingRevocations(context.Background()); err == nil {
		t.Fatal("Gateway failures were not reported")
	}
	if repo.DeliveredRevocations[lastID] {
		t.Fatal("batch unexpectedly exceeded its bound")
	}
	if err := auth.DeliverPendingRevocations(context.Background()); err != nil {
		t.Fatalf("deferred failures pinned the next batch: %v", err)
	}
	if !repo.DeliveredRevocations[lastID] || len(lifecycle.closed) != 1 {
		t.Fatal("later family never received its revocation notification")
	}
}

func queuedSessionRevocations() (*testutil.MemoryRepository, *lifecycleSpy, string) {
	repo := &testutil.MemoryRepository{}
	lifecycle := &lifecycleSpy{repo: repo, failSessions: make(map[string]bool)}
	now := time.Now().UTC().Add(-time.Minute)
	var lastID string
	for n := 0; n < 101; n++ {
		lastID = fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
		repo.Sessions = append(repo.Sessions, model.Session{ID: lastID, UserID: 42, RevokedAt: &now})
		repo.Revocations = append(repo.Revocations, service.SessionRevocation{
			SessionID: lastID, UserID: 42, RevokedAt: now, NextAttemptAt: now,
		})
		lifecycle.failSessions[lastID] = n < 100
	}
	return repo, lifecycle, lastID
}

func TestRefreshReuseNotifiesRevocation(t *testing.T) {
	lifecycle := &lifecycleSpy{}
	auth, repo, registered := registeredSession(t, service.WithSessionLifecycle(lifecycle))
	lifecycle.repo = repo
	if _, err := auth.Refresh(context.Background(), registered.Credentials.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if len(lifecycle.closed) != 0 || len(repo.Revocations) != 0 {
		t.Fatal("ordinary rotation notified revocation")
	}
	if _, err := auth.Refresh(context.Background(), registered.Credentials.RefreshToken); !errors.Is(err, service.ErrSessionExpired) {
		t.Fatalf("reuse: %v", err)
	}
	if len(lifecycle.closed) != 1 || len(lifecycle.ended) != 0 || !repo.DeliveredRevocations[registered.Credentials.Session.ID] {
		t.Fatal("reuse did not close family sockets independently of explicit Game departure")
	}
}

type cancelAfterCommit struct {
	*testutil.MemoryRepository
	cancel context.CancelFunc
}

func (r cancelAfterCommit) WithSessionTransaction(ctx context.Context, fn func(service.SessionTx) error) error {
	err := r.MemoryRepository.WithSessionTransaction(ctx, fn)
	if err == nil {
		r.cancel()
	}
	return err
}

func TestRevocationDeliverySurvivesRequestCancellation(t *testing.T) {
	_, repo, registered := registeredSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lifecycle := &lifecycleSpy{repo: repo}
	auth := service.NewAuth(cancelAfterCommit{repo, cancel}, testutil.TokenIssuer(t), service.WithSessionLifecycle(lifecycle))
	identity, err := auth.Authenticate(ctx, registered.Credentials.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.Logout(ctx, identity); err != nil || len(lifecycle.closed) != 1 || !repo.DeliveredRevocations[identity.SessionID] {
		t.Fatalf("disconnect cancelled postcommit notification: %v", err)
	}
}
