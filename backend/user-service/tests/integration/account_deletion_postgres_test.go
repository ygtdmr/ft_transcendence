package integration_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/controller"
	userpostgres "github.com/melmut-42/ft_transcendence/backend/user-service/internal/repository/postgres"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/server"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

// participantLog records every participant call in order and can fail a step.
type participantLog struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]error
}

func (p *participantLog) call(step string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.fail[step]; err != nil {
		return err
	}
	p.calls = append(p.calls, step)
	return nil
}

func (p *participantLog) setFail(step string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail == nil {
		p.fail = map[string]error{}
	}
	if err == nil {
		delete(p.fail, step)
		return
	}
	p.fail[step] = err
}

func (p *participantLog) steps() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.calls)
}

type authParticipant struct{ log *participantLog }

func (a authParticipant) BeginDeletion(context.Context, string, int64) error {
	return a.log.call("begin")
}
func (a authParticipant) CompleteDeletion(context.Context, string) error {
	return a.log.call("complete")
}

type cleanerParticipant struct {
	log  *participantLog
	name string
}

func (c cleanerParticipant) CleanUpAccount(context.Context, string, int64) error {
	return c.log.call(c.name)
}

type removedFiles struct {
	mu   sync.Mutex
	keys []string
}

func (r *removedFiles) Remove(key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys = append(r.keys, key)
	return nil
}

func deletionSetup(t *testing.T, pool *pgxpool.Pool) (http.Handler, *service.AccountDeletions, *participantLog, *removedFiles) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	log := &participantLog{}
	files := &removedFiles{}
	deletions := service.NewAccountDeletions(userpostgres.NewDeletions(pool), userpostgres.NewProfile(pool),
		authParticipant{log}, cleanerParticipant{log, "game"}, cleanerParticipant{log, "stats"}, files)
	health := controller.NewHealth(service.NewHealth(userpostgres.NewHealth(pool)))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := server.New("0", health, logger, controller.NewAccountDeletion(deletions, fakeSessions{})).Handler
	return handler, deletions, log, files
}

func deleteAccount(handler http.Handler, owner int64, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/users/me", strings.NewReader(body))
	request.AddCookie(&http.Cookie{Name: "ft_session", Value: session(owner)})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func clearsBothCookies(response *httptest.ResponseRecorder) bool {
	cookies := response.Result().Cookies()
	cleared := map[string]string{}
	for _, cookie := range cookies {
		if cookie.MaxAge < 0 && cookie.Value == "" && cookie.HttpOnly && cookie.Secure {
			cleared[cookie.Name] = cookie.Path
		}
	}
	return cleared["ft_session"] == "/" && cleared["ft_refresh"] == "/api/v1/auth/refresh"
}

// TestAccountDeletionPostgres checks a complete deletion.
//
// Expected output: 204 with both cookies cleared; participants called in
// contract order; the profile, friendships, friend requests, blocks and
// provisioning record are gone while reports remain; friends and request
// peers receive friend.removed and a cancelled friend.request.resolved; the
// uploaded avatar file is removed; the operation is completed.
func TestAccountDeletionPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 3, 7, 9, 12, 15)
	mustExec(t, pool, `UPDATE profiles SET avatar_kind = 'upload', avatar_ref = '3-abc.png' WHERE user_id = 3`)
	mustExec(t, pool, `INSERT INTO friendships (user_low, user_high, initiated_by) VALUES (3, 7, 3)`)
	mustExec(t, pool, `INSERT INTO friend_requests (requester_id, addressee_id) VALUES (9, 3)`)
	mustExec(t, pool, `INSERT INTO blocks (blocker_id, blocked_id) VALUES (3, 12)`)
	mustExec(t, pool, `INSERT INTO provisioning_operations (operation_id, user_id, payload_fingerprint)
		VALUES ('55555555-5555-5555-5555-555555555555', 3, repeat('a', 64))`)
	mustExec(t, pool, `INSERT INTO reports (reporter_id, target_id, reason) VALUES (15, 3, 'cheating')`)
	handler, _, log, files := deletionSetup(t, pool)

	response := deleteAccount(handler, 3, `{"confirm_username":"  PLAYER_3 "}`)
	if response.Code != http.StatusNoContent || !clearsBothCookies(response) {
		t.Fatalf("delete = %d %s %v", response.Code, response.Body, response.Result().Cookies())
	}
	if got := log.steps(); !slices.Equal(got, []string{"begin", "game", "stats", "complete"}) {
		t.Fatalf("participant order = %v", got)
	}
	for table, where := range map[string]string{
		"profiles":                "user_id = 3",
		"friendships":             "user_low = 3 OR user_high = 3",
		"friend_requests":         "requester_id = 3 OR addressee_id = 3",
		"blocks":                  "blocker_id = 3 OR blocked_id = 3",
		"provisioning_operations": "user_id = 3",
	} {
		if n := count(t, pool, "SELECT count(*) FROM "+table+" WHERE "+where); n != 0 {
			t.Fatalf("%s keeps %d rows of the deleted user", table, n)
		}
	}
	if n := count(t, pool, "SELECT count(*) FROM reports WHERE target_id = 3"); n != 1 {
		t.Fatalf("reports = %d, want the report kept", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM outbox WHERE event_type = 'friend.removed'
		AND payload->>'user_low' = '3' AND payload->>'user_high' = '7'`); n != 1 {
		t.Fatalf("friend.removed events = %d", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM outbox WHERE event_type = 'friend.request.resolved'
		AND payload->>'status' = 'CANCELLED' AND payload->>'from_user_id' = '9'`); n != 1 {
		t.Fatalf("cancelled request events = %d", n)
	}
	if !slices.Equal(files.keys, []string{"3-abc.png"}) {
		t.Fatalf("removed files = %v", files.keys)
	}
	if n := count(t, pool, `SELECT count(*) FROM deletion_operations WHERE user_id = 3 AND state = 'completed'
		AND auth_begun_at IS NOT NULL AND game_cleaned_at IS NOT NULL AND stats_cleaned_at IS NOT NULL
		AND user_cleaned_at IS NOT NULL AND completed_at IS NOT NULL`); n != 1 {
		t.Fatal("operation is not completed with every step recorded")
	}
	if n := count(t, pool, "SELECT count(*) FROM profiles"); n != 4 {
		t.Fatalf("other profiles = %d, want 4", n)
	}
}

// TestAccountDeletionRefusalsPostgres checks requests refused before the
// operation starts.
//
// Expected output: a wrong or missing confirmation is 422
// CONFIRMATION_MISMATCH, a malformed body is 400 and no session is 401; none
// records an operation, calls a participant or clears a cookie.
func TestAccountDeletionRefusalsPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 3)
	handler, _, log, _ := deletionSetup(t, pool)

	for _, tt := range []struct {
		name, body string
		status     int
		code       string
	}{
		{"wrong username", `{"confirm_username":"player_4"}`, http.StatusUnprocessableEntity, "CONFIRMATION_MISMATCH"},
		{"missing username", `{}`, http.StatusUnprocessableEntity, "CONFIRMATION_MISMATCH"},
		{"unknown field", `{"confirm_username":"player_3","force":true}`, http.StatusBadRequest, "VALIDATION_ERROR"},
		{"two values", `{"confirm_username":"player_3"}{}`, http.StatusBadRequest, "VALIDATION_ERROR"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response := deleteAccount(handler, 3, tt.body)
			if response.Code != tt.status || !strings.Contains(response.Body.String(), tt.code) || len(response.Result().Cookies()) != 0 {
				t.Fatalf("delete = %d %s", response.Code, response.Body)
			}
		})
	}
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/users/me", strings.NewReader(`{"confirm_username":"player_3"}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("no session = %d", recorder.Code)
	}
	if n := count(t, pool, "SELECT count(*) FROM deletion_operations"); n != 0 || len(log.steps()) != 0 {
		t.Fatalf("operations = %d, calls = %v", n, log.steps())
	}
}

// TestAccountDeletionResumesPostgres checks outages and permanent rejections.
//
// Expected output: a Stats outage answers 503 with cookies cleared and keeps
// the confirmed steps; the worker resumes after the outage without repeating
// them. A permanent Auth rejection stops the operation as failed, and the
// worker leaves it alone.
func TestAccountDeletionResumesPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 3, 4)
	handler, deletions, log, _ := deletionSetup(t, pool)

	log.setFail("stats", fmt.Errorf("%w: stats down", service.ErrUnavailable))
	response := deleteAccount(handler, 3, `{"confirm_username":"player_3"}`)
	if response.Code != http.StatusServiceUnavailable || !clearsBothCookies(response) {
		t.Fatalf("outage = %d %s", response.Code, response.Body)
	}
	if n := count(t, pool, `SELECT count(*) FROM deletion_operations WHERE user_id = 3 AND state = 'in_progress'
		AND attempts = 1 AND auth_begun_at IS NOT NULL AND game_cleaned_at IS NOT NULL AND stats_cleaned_at IS NULL
		AND next_attempt_at > now()`); n != 1 {
		t.Fatal("outage did not keep progress and schedule a retry")
	}
	if n := count(t, pool, "SELECT count(*) FROM profiles WHERE user_id = 3"); n != 1 {
		t.Fatal("profile deleted before Stats cleanup")
	}

	log.setFail("stats", nil)
	if ran, err := deletions.ResumeDue(context.Background(), 10); err != nil || ran != 0 {
		t.Fatalf("resume before due = %d, %v", ran, err)
	}
	mustExec(t, pool, "UPDATE deletion_operations SET next_attempt_at = now() WHERE user_id = 3")
	if ran, err := deletions.ResumeDue(context.Background(), 10); err != nil || ran != 1 {
		t.Fatalf("resume = %d, %v", ran, err)
	}
	if got := log.steps(); !slices.Equal(got, []string{"begin", "game", "stats", "complete"}) {
		t.Fatalf("participant calls = %v", got)
	}
	if n := count(t, pool, "SELECT count(*) FROM deletion_operations WHERE user_id = 3 AND state = 'completed'"); n != 1 {
		t.Fatal("resumed operation did not complete")
	}

	log.setFail("begin", fmt.Errorf("%w: deletion conflict", service.ErrParticipantRejected))
	if response := deleteAccount(handler, 4, `{"confirm_username":"player_4"}`); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("rejected = %d %s", response.Code, response.Body)
	}
	mustExec(t, pool, "UPDATE deletion_operations SET next_attempt_at = now() WHERE user_id = 4")
	if ran, err := deletions.ResumeDue(context.Background(), 10); err != nil || ran != 0 {
		t.Fatalf("failed operation resumed = %d, %v", ran, err)
	}
	if n := count(t, pool, `SELECT count(*) FROM deletion_operations WHERE user_id = 4 AND state = 'failed'
		AND last_error LIKE '%deletion conflict%'`); n != 1 {
		t.Fatal("permanent rejection did not stop the operation")
	}
}

// TestAccountDeletionRetryResumesPostgres checks a second request for a
// recorded operation whose session is still valid.
//
// Expected output: the retry resumes the same operation without checking the
// confirmation, and the local cleanup runs once.
func TestAccountDeletionRetryResumesPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 3, 7)
	mustExec(t, pool, `INSERT INTO friendships (user_low, user_high, initiated_by) VALUES (3, 7, 7)`)
	handler, _, log, _ := deletionSetup(t, pool)

	log.setFail("complete", fmt.Errorf("%w: auth down", service.ErrUnavailable))
	if response := deleteAccount(handler, 3, `{"confirm_username":"player_3"}`); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("first = %d %s", response.Code, response.Body)
	}
	log.setFail("complete", nil)
	if response := deleteAccount(handler, 3, `{"confirm_username":"anything"}`); response.Code != http.StatusNoContent {
		t.Fatalf("retry = %d %s", response.Code, response.Body)
	}
	if n := count(t, pool, "SELECT count(*) FROM deletion_operations"); n != 1 {
		t.Fatalf("operations = %d, want one", n)
	}
	if n := count(t, pool, "SELECT count(*) FROM outbox WHERE event_type = 'friend.removed'"); n != 1 {
		t.Fatalf("friend.removed events = %d, want one", n)
	}
	if got := log.steps(); !slices.Equal(got, []string{"begin", "game", "stats", "complete"}) {
		t.Fatalf("participant calls = %v", got)
	}
}
