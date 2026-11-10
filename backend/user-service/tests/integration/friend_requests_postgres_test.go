package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/client/authclient"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/controller"
	userpostgres "github.com/melmut-42/ft_transcendence/backend/user-service/internal/repository/postgres"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/server"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

const friendRequestsPath = "/api/v1/friends/requests"

// fakeSessions accepts "user-<id>"; "down" is an Auth outage, anything else is invalid.
type fakeSessions struct{}

func (fakeSessions) ValidateSession(_ context.Context, session string) (int64, error) {
	if session == "down" {
		return 0, fmt.Errorf("%w: test outage", authclient.ErrUnavailable)
	}
	raw, found := strings.CutPrefix(session, "user-")
	id, err := strconv.ParseInt(raw, 10, 64)
	if !found || err != nil {
		return 0, authclient.ErrSessionInvalid
	}
	return id, nil
}

func friendHandler(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	return friendHandlerWithPresence(t, pool, service.NoPresence{})
}

func friendHandlerWithPresence(t *testing.T, pool *pgxpool.Pool, presence service.PresenceReader) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	requests := service.NewFriendRequests(userpostgres.NewFriendRequests(pool), service.AvatarURLs{}, presence)
	health := controller.NewHealth(service.NewHealth(userpostgres.NewHealth(pool)))
	return server.New("0", health, logger, controller.NewFriendRequests(requests, fakeSessions{})).Handler
}

type friendResponse struct {
	status int
	Data   json.RawMessage `json:"data"`
	Error  struct {
		Code    string         `json:"code"`
		Details map[string]any `json:"details"`
	} `json:"error"`
	raw string
}

func friendCall(handler http.Handler, method, path, body, session string) friendResponse {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	if session != "" {
		request.AddCookie(&http.Cookie{Name: "ft_session", Value: session})
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	response := friendResponse{status: recorder.Code, raw: recorder.Body.String()}
	_ = json.Unmarshal(recorder.Body.Bytes(), &response)
	return response
}

func session(id int64) string { return "user-" + strconv.FormatInt(id, 10) }

func sendTo(handler http.Handler, from, to int64) friendResponse {
	return friendCall(handler, http.MethodPost, friendRequestsPath, fmt.Sprintf(`{"user_id":%d}`, to), session(from))
}

func mustSend(t *testing.T, handler http.Handler, from, to int64) int64 {
	t.Helper()
	response := sendTo(handler, from, to)
	if response.status != http.StatusCreated {
		t.Fatalf("send %d->%d = %d %s", from, to, response.status, response.raw)
	}
	var data struct {
		RequestID int64 `json:"request_id"`
	}
	if err := json.Unmarshal(response.Data, &data); err != nil || data.RequestID <= 0 {
		t.Fatalf("send data = %s", response.Data)
	}
	return data.RequestID
}

func resolvePath(id int64, action string) string {
	return fmt.Sprintf("%s/%d/%s", friendRequestsPath, id, action)
}

func cancelPath(id int64) string { return fmt.Sprintf("%s/%d", friendRequestsPath, id) }

func expectCode(t *testing.T, response friendResponse, status int, code string) {
	t.Helper()
	if response.status != status || response.Error.Code != code {
		t.Fatalf("response = %d %s, want %d %s", response.status, response.raw, status, code)
	}
}

// outboxPayloads returns the payloads of one event type, oldest first.
func outboxPayloads(t *testing.T, pool *pgxpool.Pool, eventType string) []map[string]any {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT payload FROM outbox WHERE event_type = $1 ORDER BY id`, eventType)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var payloads []map[string]any
	for rows.Next() {
		var payload map[string]any
		if err := rows.Scan(&payload); err != nil {
			t.Fatal(err)
		}
		payloads = append(payloads, payload)
	}
	return payloads
}

// TestFriendRequestAcceptPostgres checks send, list and accept on real records.
//
// Expected output:
//
//   - Send returns 201 with both users and PENDING, and records
//     friend.request.received with the same request.
//   - List shows it as outgoing for the sender and incoming for the recipient.
//   - Accept returns the new friend, creates one normalized friendship, records
//     friend.request.resolved with both users, and empties the lists.
//   - Accepting again is 404 REQUEST_NOT_FOUND and records nothing.
func TestFriendRequestAcceptPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 7, 3)
	handler := friendHandler(t, pool)

	sent := sendTo(handler, 7, 3)
	var request struct {
		RequestID int64 `json:"request_id"`
		FromUser  struct {
			UserID    int64  `json:"user_id"`
			Username  string `json:"username"`
			AvatarURL string `json:"avatar_url"`
		} `json:"from_user"`
		ToUser struct {
			UserID int64 `json:"user_id"`
		} `json:"to_user"`
		Status    string `json:"status"`
		CreatedAt string `json:"created_at"`
	}
	if err := json.Unmarshal(sent.Data, &request); err != nil || sent.status != http.StatusCreated {
		t.Fatalf("send = %d %s", sent.status, sent.raw)
	}
	if request.FromUser.UserID != 7 || request.FromUser.Username != "player_7" || request.ToUser.UserID != 3 ||
		request.FromUser.AvatarURL != "/static/avatars/default/7.svg" || request.Status != "PENDING" || request.CreatedAt == "" {
		t.Fatalf("send data = %s", sent.Data)
	}
	received := outboxPayloads(t, pool, service.EventFriendRequestReceived)
	if len(received) != 1 || received[0]["request"].(map[string]any)["request_id"] != float64(request.RequestID) {
		t.Fatalf("received events = %v", received)
	}

	for user, want := range map[int64][2]int{7: {0, 1}, 3: {1, 0}} {
		var lists struct {
			Incoming []json.RawMessage `json:"incoming"`
			Outgoing []json.RawMessage `json:"outgoing"`
		}
		response := friendCall(handler, http.MethodGet, friendRequestsPath, "", session(user))
		if err := json.Unmarshal(response.Data, &lists); err != nil || len(lists.Incoming) != want[0] || len(lists.Outgoing) != want[1] {
			t.Fatalf("list for %d = %d %s", user, response.status, response.raw)
		}
	}

	accepted := friendCall(handler, http.MethodPost, resolvePath(request.RequestID, "accept"), "", session(3))
	var friend map[string]any
	if err := json.Unmarshal(accepted.Data, &friend); err != nil || accepted.status != http.StatusOK ||
		friend["user_id"] != float64(7) || friend["username"] != "player_7" || friend["is_online"] != false || len(friend) != 4 {
		t.Fatalf("accept = %d %s", accepted.status, accepted.raw)
	}
	if got := count(t, pool, `SELECT count(*) FROM friendships WHERE user_low = 3 AND user_high = 7 AND initiated_by = 7 AND active`); got != 1 {
		t.Fatalf("friendships = %d, want 1", got)
	}
	if got := count(t, pool, `SELECT count(*) FROM friend_requests WHERE state = 'accepted' AND resolved_at IS NOT NULL`); got != 1 {
		t.Fatalf("accepted requests = %d, want 1", got)
	}
	resolved := outboxPayloads(t, pool, service.EventFriendRequestResolved)
	if len(resolved) != 1 || resolved[0]["status"] != "ACCEPTED" || resolved[0]["from_user_id"] != float64(7) ||
		resolved[0]["to_user_id"] != float64(3) || resolved[0]["from_user"].(map[string]any)["username"] != "player_7" ||
		resolved[0]["to_user"].(map[string]any)["username"] != "player_3" {
		t.Fatalf("resolved events = %v", resolved)
	}
	if got := count(t, pool, `SELECT count(DISTINCT aggregate_id) FROM outbox WHERE aggregate_type = 'friend_request'`); got != 1 {
		t.Fatalf("aggregates = %d, want one per request", got)
	}

	var empty struct {
		Incoming []json.RawMessage `json:"incoming"`
		Outgoing []json.RawMessage `json:"outgoing"`
	}
	listed := friendCall(handler, http.MethodGet, friendRequestsPath, "", session(3))
	if err := json.Unmarshal(listed.Data, &empty); err != nil || empty.Incoming == nil || len(empty.Incoming) != 0 || len(empty.Outgoing) != 0 {
		t.Fatalf("list after accept = %s", listed.raw)
	}
	again := friendCall(handler, http.MethodPost, resolvePath(request.RequestID, "accept"), "", session(3))
	expectCode(t, again, http.StatusNotFound, "REQUEST_NOT_FOUND")
	if again.Error.Details["request_id"] != float64(request.RequestID) {
		t.Fatalf("details = %v", again.Error.Details)
	}
	if got := count(t, pool, `SELECT count(*) FROM outbox`); got != 2 {
		t.Fatalf("outbox rows = %d, want 2", got)
	}
}

// TestFriendRequestDeclineCancelPostgres checks the other resolutions and ownership.
//
// Expected output:
//
//   - Only the recipient declines and accepts; only the sender cancels; every
//     other caller gets 404 REQUEST_NOT_FOUND and nothing changes.
//   - Decline and cancel return 204, create no friendship and record their status.
//   - The pair can send a new request afterwards.
func TestFriendRequestDeclineCancelPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2, 9)
	handler := friendHandler(t, pool)

	id := mustSend(t, handler, 1, 2)
	for name, attempt := range map[string]friendResponse{
		"sender accepts":    friendCall(handler, http.MethodPost, resolvePath(id, "accept"), "", session(1)),
		"sender declines":   friendCall(handler, http.MethodPost, resolvePath(id, "decline"), "", session(1)),
		"recipient cancels": friendCall(handler, http.MethodDelete, cancelPath(id), "", session(2)),
		"stranger accepts":  friendCall(handler, http.MethodPost, resolvePath(id, "accept"), "", session(9)),
		"stranger cancels":  friendCall(handler, http.MethodDelete, cancelPath(id), "", session(9)),
		"unknown request":   friendCall(handler, http.MethodPost, resolvePath(999999, "accept"), "", session(2)),
	} {
		if attempt.status != http.StatusNotFound || attempt.Error.Code != "REQUEST_NOT_FOUND" {
			t.Fatalf("%s = %d %s", name, attempt.status, attempt.raw)
		}
	}
	if got := count(t, pool, `SELECT count(*) FROM friend_requests WHERE state = 'pending'`); got != 1 {
		t.Fatal("refused attempts must not resolve the request")
	}

	if response := friendCall(handler, http.MethodPost, resolvePath(id, "decline"), "", session(2)); response.status != http.StatusNoContent || response.raw != "" {
		t.Fatalf("decline = %d %s", response.status, response.raw)
	}
	second := mustSend(t, handler, 2, 1)
	if response := friendCall(handler, http.MethodDelete, cancelPath(second), "", session(2)); response.status != http.StatusNoContent {
		t.Fatalf("cancel = %d %s", response.status, response.raw)
	}
	if got := count(t, pool, `SELECT count(*) FROM friendships`); got != 0 {
		t.Fatalf("friendships = %d, want 0", got)
	}
	resolved := outboxPayloads(t, pool, service.EventFriendRequestResolved)
	if len(resolved) != 2 || resolved[0]["status"] != "DECLINED" || resolved[1]["status"] != "CANCELLED" || resolved[0]["from_user"] != nil {
		t.Fatalf("resolved events = %v", resolved)
	}
	mustSend(t, handler, 1, 2)
}

// TestFriendRequestSendRefusalsPostgres checks every refusal of a new request.
//
// Expected output: each refusal has its contract status, code and details and
// writes neither a request nor an event.
func TestFriendRequestSendRefusalsPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2, 3, 4, 5, 6)
	handler := friendHandler(t, pool)

	first := mustSend(t, handler, 1, 2)
	mustExec(t, pool, `INSERT INTO blocks (blocker_id, blocked_id) VALUES (4, 1)`)
	mustExec(t, pool, `INSERT INTO friendships (user_low, user_high, initiated_by) VALUES (1, 5, 1)`)
	mustExec(t, pool, `INSERT INTO deletion_operations (operation_id, user_id) VALUES ('3f9b2c71-5d4e-4a86-9c1b-7e2a6d0f8b53', 6)`)
	requests, events := count(t, pool, `SELECT count(*) FROM friend_requests`), count(t, pool, `SELECT count(*) FROM outbox`)

	for _, tt := range []struct {
		name     string
		from, to int64
		status   int
		code     string
		details  map[string]any
	}{
		{"self", 1, 1, http.StatusUnprocessableEntity, "VALIDATION_ERROR", map[string]any{"field": "user_id"}},
		{"unknown", 1, 999, http.StatusNotFound, "USER_NOT_FOUND", map[string]any{"user_id": float64(999)}},
		{"being deleted", 1, 6, http.StatusNotFound, "USER_NOT_FOUND", map[string]any{"user_id": float64(6)}},
		{"blocked by recipient", 1, 4, http.StatusForbidden, "RELATIONSHIP_BLOCKED", map[string]any{"user_id": float64(4)}},
		{"blocking recipient", 4, 1, http.StatusForbidden, "RELATIONSHIP_BLOCKED", map[string]any{"user_id": float64(1)}},
		{"friends", 5, 1, http.StatusConflict, "ALREADY_FRIENDS", map[string]any{"user_id": float64(1)}},
		{"already sent", 1, 2, http.StatusConflict, "REQUEST_ALREADY_SENT", map[string]any{"user_id": float64(2), "request_id": float64(first)}},
		{"already received", 2, 1, http.StatusConflict, "REQUEST_ALREADY_RECEIVED", map[string]any{"user_id": float64(1), "request_id": float64(first)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response := sendTo(handler, tt.from, tt.to)
			expectCode(t, response, tt.status, tt.code)
			for key, want := range tt.details {
				if response.Error.Details[key] != want {
					t.Fatalf("details = %v, want %s=%v", response.Error.Details, key, want)
				}
			}
		})
	}
	if count(t, pool, `SELECT count(*) FROM friend_requests`) != requests || count(t, pool, `SELECT count(*) FROM outbox`) != events {
		t.Fatal("refused sends must write nothing")
	}
}

// TestFriendRequestValidationAndSessionPostgres checks input and session handling.
//
// Expected output:
//
//   - Malformed bodies and a body on resolution routes are 400; bad user_id and
//     request_id values are 422 naming the field.
//   - No session or a refused one is 401 UNAUTHORIZED; an Auth outage is 503.
func TestFriendRequestValidationAndSessionPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2)
	handler := friendHandler(t, pool)

	for _, tt := range []struct {
		name, method, path, body string
		status                   int
		field                    string
	}{
		{"malformed", http.MethodPost, friendRequestsPath, `{"user_id":`, http.StatusBadRequest, "body"},
		{"array", http.MethodPost, friendRequestsPath, `[2]`, http.StatusBadRequest, "body"},
		{"two values", http.MethodPost, friendRequestsPath, `{"user_id":2}{"user_id":2}`, http.StatusBadRequest, "body"},
		{"empty body", http.MethodPost, friendRequestsPath, ``, http.StatusBadRequest, "body"},
		{"missing", http.MethodPost, friendRequestsPath, `{}`, http.StatusUnprocessableEntity, "user_id"},
		{"string", http.MethodPost, friendRequestsPath, `{"user_id":"2"}`, http.StatusUnprocessableEntity, "user_id"},
		{"null", http.MethodPost, friendRequestsPath, `{"user_id":null}`, http.StatusUnprocessableEntity, "user_id"},
		{"fraction", http.MethodPost, friendRequestsPath, `{"user_id":1.5}`, http.StatusUnprocessableEntity, "user_id"},
		{"zero", http.MethodPost, friendRequestsPath, `{"user_id":0}`, http.StatusUnprocessableEntity, "user_id"},
		{"bad id accept", http.MethodPost, friendRequestsPath + "/abc/accept", ``, http.StatusUnprocessableEntity, "request_id"},
		{"zero id decline", http.MethodPost, friendRequestsPath + "/0/decline", ``, http.StatusUnprocessableEntity, "request_id"},
		{"negative id cancel", http.MethodDelete, friendRequestsPath + "/-1", ``, http.StatusUnprocessableEntity, "request_id"},
		{"body on accept", http.MethodPost, friendRequestsPath + "/1/accept", `{}`, http.StatusBadRequest, "body"},
		{"body on cancel", http.MethodDelete, friendRequestsPath + "/1", `{}`, http.StatusBadRequest, "body"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response := friendCall(handler, tt.method, tt.path, tt.body, session(1))
			expectCode(t, response, tt.status, "VALIDATION_ERROR")
			if response.Error.Details["field"] != tt.field {
				t.Fatalf("details = %v, want field %s", response.Error.Details, tt.field)
			}
		})
	}

	for _, route := range [][2]string{
		{http.MethodPost, friendRequestsPath}, {http.MethodGet, friendRequestsPath},
		{http.MethodPost, friendRequestsPath + "/1/accept"}, {http.MethodPost, friendRequestsPath + "/1/decline"},
		{http.MethodDelete, friendRequestsPath + "/1"},
	} {
		expectCode(t, friendCall(handler, route[0], route[1], `{"user_id":2}`, ""), http.StatusUnauthorized, "UNAUTHORIZED")
		expectCode(t, friendCall(handler, route[0], route[1], `{"user_id":2}`, "invalid"), http.StatusUnauthorized, "UNAUTHORIZED")
		expectCode(t, friendCall(handler, route[0], route[1], `{"user_id":2}`, "down"), http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE")
	}
	if got := count(t, pool, `SELECT count(*) FROM friend_requests`); got != 0 {
		t.Fatalf("rejected calls wrote %d requests", got)
	}
}

// TestFriendRequestBlockAndDeletionPostgres checks blocked and deleting pairs.
//
// Expected output:
//
//   - After a block, accept is 403 RELATIONSHIP_BLOCKED and decline is 404.
//   - When the sender is being deleted the request is withheld, accept is 404,
//     and the recipient can still decline it.
func TestFriendRequestBlockAndDeletionPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2, 3)
	handler := friendHandler(t, pool)

	blocked := mustSend(t, handler, 1, 2)
	mustExec(t, pool, `INSERT INTO blocks (blocker_id, blocked_id) VALUES (1, 2)`)
	expectCode(t, friendCall(handler, http.MethodPost, resolvePath(blocked, "accept"), "", session(2)), http.StatusForbidden, "RELATIONSHIP_BLOCKED")
	expectCode(t, friendCall(handler, http.MethodPost, resolvePath(blocked, "decline"), "", session(2)), http.StatusNotFound, "REQUEST_NOT_FOUND")

	deleting := mustSend(t, handler, 3, 2)
	mustExec(t, pool, `INSERT INTO deletion_operations (operation_id, user_id) VALUES ('3f9b2c71-5d4e-4a86-9c1b-7e2a6d0f8b53', 3)`)
	listed := friendCall(handler, http.MethodGet, friendRequestsPath, "", session(2))
	if strings.Contains(listed.raw, "player_3") {
		t.Fatalf("list exposes a deleting user: %s", listed.raw)
	}
	expectCode(t, friendCall(handler, http.MethodPost, resolvePath(deleting, "accept"), "", session(2)), http.StatusNotFound, "REQUEST_NOT_FOUND")
	if response := friendCall(handler, http.MethodPost, resolvePath(deleting, "decline"), "", session(2)); response.status != http.StatusNoContent {
		t.Fatalf("decline = %d %s", response.status, response.raw)
	}
}

// TestFriendRequestRollbackPostgres checks that a change and its event are atomic.
//
// Expected output: when the event cannot be recorded, the request, its
// resolution and the friendship all roll back.
func TestFriendRequestRollbackPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2)
	ctx := context.Background()
	store := userpostgres.NewFriendRequests(pool)
	badEvent := func(service.StoredFriendRequest) service.OutboxEvent {
		return service.OutboxEvent{AggregateType: "friend_request", AggregateID: "1", EventType: "not.in.catalog", Payload: map[string]any{}}
	}

	if _, err := store.CreateFriendRequest(ctx, 1, 2, badEvent); !errors.Is(err, service.ErrInvalidEvent) {
		t.Fatalf("create = %v, want ErrInvalidEvent", err)
	}
	if got := count(t, pool, `SELECT count(*) FROM friend_requests`); got != 0 {
		t.Fatalf("requests after rollback = %d, want 0", got)
	}

	id := mustSend(t, friendHandler(t, pool), 1, 2)
	if _, err := store.ResolveFriendRequest(ctx, id, 2, service.FriendRequestAccepted, badEvent); !errors.Is(err, service.ErrInvalidEvent) {
		t.Fatalf("resolve = %v, want ErrInvalidEvent", err)
	}
	if got := count(t, pool, `SELECT count(*) FROM friendships`); got != 0 {
		t.Fatalf("friendships after rollback = %d, want 0", got)
	}
	if got := count(t, pool, `SELECT count(*) FROM friend_requests WHERE state = 'pending'`); got != 1 {
		t.Fatal("the request must stay pending after rollback")
	}
	if got := count(t, pool, `SELECT count(*) FROM outbox`); got != 1 {
		t.Fatalf("outbox rows = %d, want only the send event", got)
	}
}

func runTogether(fns ...func() int) []int {
	statuses := make([]int, len(fns))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, fn := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			statuses[i] = fn()
		}()
	}
	close(start)
	wg.Wait()
	return statuses
}

// TestFriendRequestConcurrencyPostgres checks races on one user pair.
//
// Expected output:
//
//   - Simultaneous sends in both directions create one request and one event.
//   - Racing accepts, declines and cancels make one transition and one event,
//     and the friendship exists only if the accept won.
//   - A block racing an accept does not deadlock and leaves no active friendship.
func TestFriendRequestConcurrencyPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2, 3, 4)
	handler := friendHandler(t, pool)

	var sends []func() int
	for n := range 8 {
		from, to := int64(1), int64(2)
		if n%2 == 1 {
			from, to = 2, 1
		}
		sends = append(sends, func() int { return sendTo(handler, from, to).status })
	}
	created := 0
	for _, status := range runTogether(sends...) {
		switch status {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
		default:
			t.Fatalf("concurrent send status = %d", status)
		}
	}
	if created != 1 || count(t, pool, `SELECT count(*) FROM friend_requests`) != 1 || count(t, pool, `SELECT count(*) FROM outbox`) != 1 {
		t.Fatalf("created = %d; want one request and one event", created)
	}

	var id, requester, recipient int64
	if err := pool.QueryRow(context.Background(), `SELECT id, requester_id, addressee_id FROM friend_requests`).Scan(&id, &requester, &recipient); err != nil {
		t.Fatal(err)
	}
	resolutions := []func() int{
		func() int {
			return friendCall(handler, http.MethodPost, resolvePath(id, "accept"), "", session(recipient)).status
		},
		func() int {
			return friendCall(handler, http.MethodPost, resolvePath(id, "accept"), "", session(recipient)).status
		},
		func() int {
			return friendCall(handler, http.MethodPost, resolvePath(id, "accept"), "", session(recipient)).status
		},
		func() int {
			return friendCall(handler, http.MethodPost, resolvePath(id, "decline"), "", session(recipient)).status
		},
		func() int {
			return friendCall(handler, http.MethodPost, resolvePath(id, "decline"), "", session(recipient)).status
		},
		func() int {
			return friendCall(handler, http.MethodDelete, cancelPath(id), "", session(requester)).status
		},
		func() int {
			return friendCall(handler, http.MethodDelete, cancelPath(id), "", session(requester)).status
		},
	}
	wins := 0
	for _, status := range runTogether(resolutions...) {
		switch status {
		case http.StatusOK, http.StatusNoContent:
			wins++
		case http.StatusNotFound:
		default:
			t.Fatalf("concurrent resolve status = %d", status)
		}
	}
	resolved := outboxPayloads(t, pool, service.EventFriendRequestResolved)
	if wins != 1 || len(resolved) != 1 {
		t.Fatalf("wins = %d, resolved events = %d; want one each", wins, len(resolved))
	}
	wantFriends := 0
	if resolved[0]["status"] == "ACCEPTED" {
		wantFriends = 1
	}
	if got := count(t, pool, `SELECT count(*) FROM friendships`); got != wantFriends {
		t.Fatalf("friendships = %d for status %v", got, resolved[0]["status"])
	}

	for n := range 10 {
		a, b := int64(3), int64(4)
		mustExec(t, pool, `DELETE FROM blocks`)
		mustExec(t, pool, `DELETE FROM friendships WHERE user_low = 3`)
		request := mustSend(t, handler, a, b)
		statuses := runTogether(
			func() int {
				return friendCall(handler, http.MethodPost, resolvePath(request, "accept"), "", session(b)).status
			},
			func() int {
				if err := exec(pool, `INSERT INTO blocks (blocker_id, blocked_id) VALUES (3, 4)`); err != nil {
					t.Errorf("block in round %d: %v", n, err)
				}
				return http.StatusOK
			},
		)
		if statuses[0] != http.StatusOK && statuses[0] != http.StatusForbidden {
			t.Fatalf("accept racing block = %d", statuses[0])
		}
		if got := count(t, pool, `SELECT count(*) FROM friendships WHERE user_low = 3 AND user_high = 4 AND active`); got != 0 {
			t.Fatalf("round %d: %d active friendships remain after a block", n, got)
		}
	}
}

// failingPresence reports every user as unknown.
type failingPresence struct{}

func (failingPresence) Online(context.Context, []int64) (map[int64]bool, error) {
	return nil, fmt.Errorf("%w: test registry outage", service.ErrUnavailable)
}

// TestFriendRequestAcceptPresenceOutagePostgres checks that an unknown sender
// presence fails Accept before anything is committed.
//
// Expected output: 503 SERVICE_UNAVAILABLE with the request still pending, no
// friendship and no resolved event; retrying with presence back succeeds.
func TestFriendRequestAcceptPresenceOutagePostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 7, 3)
	handler := friendHandler(t, pool)

	sent := sendTo(handler, 7, 3)
	var request struct {
		RequestID int64 `json:"request_id"`
	}
	if err := json.Unmarshal(sent.Data, &request); err != nil || sent.status != http.StatusCreated {
		t.Fatalf("send = %d %s", sent.status, sent.raw)
	}

	outage := friendCall(friendHandlerWithPresence(t, pool, failingPresence{}), http.MethodPost,
		resolvePath(request.RequestID, "accept"), "", session(3))
	if outage.status != http.StatusServiceUnavailable || outage.Error.Code != "SERVICE_UNAVAILABLE" {
		t.Fatalf("accept during outage = %d %s", outage.status, outage.raw)
	}
	if got := count(t, pool, `SELECT count(*) FROM friend_requests WHERE state = 'pending'`); got != 1 {
		t.Fatalf("pending requests = %d, want 1", got)
	}
	if got := count(t, pool, `SELECT count(*) FROM friendships`); got != 0 {
		t.Fatalf("friendships = %d, want 0", got)
	}
	if resolved := outboxPayloads(t, pool, service.EventFriendRequestResolved); len(resolved) != 0 {
		t.Fatalf("resolved events = %v", resolved)
	}

	retry := friendCall(handler, http.MethodPost, resolvePath(request.RequestID, "accept"), "", session(3))
	if retry.status != http.StatusOK {
		t.Fatalf("retry = %d %s", retry.status, retry.raw)
	}
}
