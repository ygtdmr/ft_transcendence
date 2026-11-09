package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/controller"
	userpostgres "github.com/melmut-42/ft_transcendence/backend/user-service/internal/repository/postgres"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/server"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

func blockHandler(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	avatars := service.AvatarURLs{}
	requests := service.NewFriendRequests(userpostgres.NewFriendRequests(pool), avatars, service.NoPresence{})
	blocks := service.NewBlocks(userpostgres.NewBlocks(pool), avatars)
	health := controller.NewHealth(service.NewHealth(userpostgres.NewHealth(pool)))
	return server.New("0", health, logger,
		controller.NewFriendRequests(requests, fakeSessions{}),
		controller.NewBlocks(blocks, fakeSessions{}),
		controller.NewFriends(service.NewFriends(userpostgres.NewFriends(pool), avatars, service.NoPresence{}), fakeSessions{}),
		controller.NewUserSearch(service.NewUserSearch(userpostgres.NewUserSearch(pool), avatars), fakeSessions{})).Handler
}

func blockPath(target int64) string { return fmt.Sprintf("/api/v1/users/%d/block", target) }

func putBlock(handler http.Handler, blocker, target int64) friendResponse {
	return friendCall(handler, http.MethodPut, blockPath(target), "", session(blocker))
}

func deleteBlock(handler http.Handler, blocker, target int64) friendResponse {
	return friendCall(handler, http.MethodDelete, blockPath(target), "", session(blocker))
}

func mustBlock(t *testing.T, handler http.Handler, blocker, target int64) string {
	t.Helper()
	response := putBlock(handler, blocker, target)
	if response.status != http.StatusOK {
		t.Fatalf("block %d->%d = %d %s", blocker, target, response.status, response.raw)
	}
	var data struct {
		UserID    int64  `json:"user_id"`
		BlockedAt string `json:"blocked_at"`
	}
	if err := json.Unmarshal(response.Data, &data); err != nil || data.UserID != target || data.BlockedAt == "" {
		t.Fatalf("block data = %s", response.Data)
	}
	return data.BlockedAt
}

func mustUnblock(t *testing.T, handler http.Handler, blocker, target int64) {
	t.Helper()
	if response := deleteBlock(handler, blocker, target); response.status != http.StatusNoContent || response.raw != "" {
		t.Fatalf("unblock %d->%d = %d %q", blocker, target, response.status, response.raw)
	}
}

func befriend(t *testing.T, handler http.Handler, from, to int64) {
	t.Helper()
	request := mustSend(t, handler, from, to)
	if response := friendCall(handler, http.MethodPost, resolvePath(request, "accept"), "", session(to)); response.status != http.StatusOK {
		t.Fatalf("accept = %d %s", response.status, response.raw)
	}
}

func payloadKeys(payload map[string]any) string {
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// TestBlockCancelsRequestPostgres checks a block between users with a pending request.
//
// Expected output:
//
//   - The request becomes cancelled and one CANCELLED event is recorded with
//     the usual fields, whichever side blocked.
//   - No friendship event is recorded, because no friendship existed.
//   - Unblocking leaves the request cancelled and creates no friendship.
//   - After unblocking, a new request can be sent.
func TestBlockCancelsRequestPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2)
	handler := blockHandler(t, pool)

	request := mustSend(t, handler, 1, 2)
	mustBlock(t, handler, 2, 1)

	if got := count(t, pool, `SELECT count(*) FROM friend_requests WHERE id = $1 AND state = 'cancelled'`, request); got != 1 {
		t.Fatal("the pending request was not cancelled")
	}
	resolved := outboxPayloads(t, pool, service.EventFriendRequestResolved)
	if len(resolved) != 1 || resolved[0]["status"] != "CANCELLED" || resolved[0]["request_id"] != float64(request) ||
		resolved[0]["from_user_id"] != float64(1) || resolved[0]["to_user_id"] != float64(2) ||
		payloadKeys(resolved[0]) != "from_user_id,request_id,status,to_user_id" {
		t.Fatalf("resolved events = %v", resolved)
	}
	if got := count(t, pool, `SELECT count(*) FROM outbox WHERE event_type IN ('friend.removed', 'friend.restored', 'relationship.blocked')`); got != 0 {
		t.Fatalf("friendship events = %d, want 0", got)
	}

	mustUnblock(t, handler, 2, 1)
	if got := count(t, pool, `SELECT count(*) FROM friend_requests WHERE state = 'pending'`); got != 0 {
		t.Fatalf("pending requests after unblock = %d", got)
	}
	if got := count(t, pool, `SELECT count(*) FROM friendships`); got != 0 {
		t.Fatalf("friendships after unblock = %d", got)
	}
	if got := len(outboxPayloads(t, pool, service.EventFriendRestored)); got != 0 {
		t.Fatalf("restored events = %d", got)
	}
	mustSend(t, handler, 1, 2)
}

// TestBlockSuspendsAndRestoresPostgres follows one friendship through two blocks.
//
// Expected output:
//
//   - A block keeps the row inactive and records friend.removed, which does not
//     name the blocker, and relationship.blocked, which does.
//   - A repeated block returns the original time and records nothing.
//   - The friendship returns only after both blocks are lifted, with one
//     friend.restored carrying both users in id order.
//   - A repeated unblock records nothing.
//   - friend.removed, relationship.blocked and friend.restored share one aggregate.
func TestBlockSuspendsAndRestoresPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 8, 5)
	handler := blockHandler(t, pool)
	befriend(t, handler, 8, 5)
	before := count(t, pool, `SELECT count(*) FROM outbox`)

	blockedAt := mustBlock(t, handler, 8, 5)
	if got := count(t, pool, `SELECT count(*) FROM friendships WHERE user_low = 5 AND user_high = 8 AND NOT active`); got != 1 {
		t.Fatal("the friendship must stay stored and inactive")
	}
	removed := outboxPayloads(t, pool, service.EventFriendRemoved)
	if len(removed) != 1 || removed[0]["user_low"] != float64(5) || removed[0]["user_high"] != float64(8) ||
		payloadKeys(removed[0]) != "user_high,user_low" {
		t.Fatalf("removed events = %v", removed)
	}
	blocked := outboxPayloads(t, pool, service.EventRelationshipBlocked)
	if len(blocked) != 1 || blocked[0]["blocker_id"] != float64(8) || blocked[0]["blocked_id"] != float64(5) {
		t.Fatalf("relationship events = %v", blocked)
	}
	afterBlock := count(t, pool, `SELECT count(*) FROM outbox`)
	if afterBlock != before+2 {
		t.Fatalf("outbox rows = %d, want %d", afterBlock, before+2)
	}
	if got := count(t, pool, `SELECT count(*) FROM outbox a JOIN outbox b ON a.aggregate_id = b.aggregate_id
		WHERE a.event_type = 'relationship.blocked' AND b.event_type = 'friend.removed' AND a.id < b.id`); got != 1 {
		t.Fatal("relationship.blocked must come before friend.removed")
	}

	if again := mustBlock(t, handler, 8, 5); again != blockedAt {
		t.Fatalf("repeated block time = %s, want %s", again, blockedAt)
	}
	mustBlock(t, handler, 5, 8)
	if got := count(t, pool, `SELECT count(*) FROM outbox`); got != afterBlock {
		t.Fatalf("outbox rows after repeat and second block = %d, want %d", got, afterBlock)
	}

	mustUnblock(t, handler, 8, 5)
	if got := count(t, pool, `SELECT count(*) FROM friendships WHERE active`); got != 0 {
		t.Fatal("the friendship returned while the other block remains")
	}
	if got := len(outboxPayloads(t, pool, service.EventFriendRestored)); got != 0 {
		t.Fatalf("restored events with a block left = %d", got)
	}

	mustUnblock(t, handler, 5, 8)
	if got := count(t, pool, `SELECT count(*) FROM friendships WHERE active`); got != 1 {
		t.Fatal("the friendship must return after the last block is lifted")
	}
	restored := outboxPayloads(t, pool, service.EventFriendRestored)
	if len(restored) != 1 {
		t.Fatalf("restored events = %v", restored)
	}
	users := restored[0]["users"].([]any)
	first, second := users[0].(map[string]any), users[1].(map[string]any)
	if len(users) != 2 || first["user_id"] != float64(5) || second["user_id"] != float64(8) ||
		first["username"] != "player_5" || second["avatar_url"] != "/static/avatars/default/8.svg" {
		t.Fatalf("restored users = %v", users)
	}

	mustUnblock(t, handler, 5, 8)
	if got := len(outboxPayloads(t, pool, service.EventFriendRestored)); got != 1 {
		t.Fatalf("restored events after repeat = %d", got)
	}
	if got := count(t, pool, `SELECT count(*) FROM (SELECT DISTINCT aggregate_type, aggregate_id FROM outbox
		WHERE event_type IN ('friend.removed', 'relationship.blocked', 'friend.restored')) pairs`); got != 1 {
		t.Fatalf("friendship aggregates = %d, want 1", got)
	}
}

// TestUnblockLiftsOnlyOwnBlockPostgres checks who can remove a block.
//
// Expected output: the blocked user's unblock succeeds without any effect, and
// the block stays in force.
func TestUnblockLiftsOnlyOwnBlockPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2)
	handler := blockHandler(t, pool)
	befriend(t, handler, 1, 2)
	mustBlock(t, handler, 1, 2)

	mustUnblock(t, handler, 2, 1)
	if got := count(t, pool, `SELECT count(*) FROM blocks WHERE blocker_id = 1 AND blocked_id = 2`); got != 1 {
		t.Fatal("the blocked user removed the block")
	}
	if got := count(t, pool, `SELECT count(*) FROM friendships WHERE active`); got != 0 {
		t.Fatal("the friendship returned")
	}
}

// TestBlockRestrictionsPostgres checks refusals while a block exists.
//
// Expected output:
//
//   - Both sides get the same 403 RELATIONSHIP_BLOCKED, so the block's direction
//     is not revealed.
//   - Accepting a request cancelled by a block is the same generic 403.
//   - An unknown or deleting user is 404 USER_NOT_FOUND for both routes.
//   - A missing session is 401.
func TestBlockRestrictionsPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2, 3)
	handler := blockHandler(t, pool)

	request := mustSend(t, handler, 2, 1)
	mustBlock(t, handler, 1, 2)

	blocker, blockedSide := sendTo(handler, 1, 2), sendTo(handler, 2, 1)
	expectCode(t, blocker, http.StatusForbidden, "RELATIONSHIP_BLOCKED")
	expectCode(t, blockedSide, http.StatusForbidden, "RELATIONSHIP_BLOCKED")
	if strings.ReplaceAll(blocker.raw, `"user_id":2`, `"user_id":1`) != blockedSide.raw {
		t.Fatalf("responses differ: %s / %s", blocker.raw, blockedSide.raw)
	}
	expectCode(t, friendCall(handler, http.MethodPost, resolvePath(request, "accept"), "", session(1)), http.StatusForbidden, "RELATIONSHIP_BLOCKED")

	expectCode(t, putBlock(handler, 1, 404), http.StatusNotFound, "USER_NOT_FOUND")
	expectCode(t, deleteBlock(handler, 1, 404), http.StatusNotFound, "USER_NOT_FOUND")
	mustExec(t, pool, `INSERT INTO deletion_operations (operation_id, user_id) VALUES ('3f9b2c71-5d4e-4a86-9c1b-7e2a6d0f8b53', 3)`)
	expectCode(t, putBlock(handler, 1, 3), http.StatusNotFound, "USER_NOT_FOUND")
	expectCode(t, deleteBlock(handler, 1, 3), http.StatusNotFound, "USER_NOT_FOUND")
	expectCode(t, friendCall(handler, http.MethodPut, blockPath(3), "", ""), http.StatusUnauthorized, "UNAUTHORIZED")
}

// TestBlockRollbackPostgres checks that a block change and its events are atomic.
//
// Expected output: when an event cannot be recorded, the block, the cancelled
// request and the suspended friendship all roll back, and unblocking likewise
// leaves the block in place.
func TestBlockRollbackPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2, 3, 4)
	handler := blockHandler(t, pool)
	ctx := context.Background()
	store := userpostgres.NewBlocks(pool)
	bad := []service.OutboxEvent{{AggregateType: "friendship", AggregateID: "1:2", EventType: "not.in.catalog", Payload: map[string]any{}}}
	badPlacement := func(service.BlockPlacement) []service.OutboxEvent { return bad }
	badRestore := func(_, _ service.StoredProfile) []service.OutboxEvent { return bad }

	befriend(t, handler, 1, 2)
	mustSend(t, handler, 3, 4)
	outbox := count(t, pool, `SELECT count(*) FROM outbox`)

	if _, err := store.PlaceBlock(ctx, 1, 2, badPlacement); !errors.Is(err, service.ErrInvalidEvent) {
		t.Fatalf("PlaceBlock = %v, want ErrInvalidEvent", err)
	}
	if _, err := store.PlaceBlock(ctx, 3, 4, badPlacement); !errors.Is(err, service.ErrInvalidEvent) {
		t.Fatalf("PlaceBlock = %v, want ErrInvalidEvent", err)
	}
	if got := count(t, pool, `SELECT count(*) FROM blocks`); got != 0 {
		t.Fatalf("blocks after rollback = %d", got)
	}
	if got := count(t, pool, `SELECT count(*) FROM friendships WHERE active`); got != 1 {
		t.Fatal("the friendship must stay active after rollback")
	}
	if got := count(t, pool, `SELECT count(*) FROM friend_requests WHERE state = 'pending'`); got != 1 {
		t.Fatal("the request must stay pending after rollback")
	}

	mustBlock(t, handler, 1, 2)
	if err := store.LiftBlock(ctx, 1, 2, badRestore); !errors.Is(err, service.ErrInvalidEvent) {
		t.Fatalf("LiftBlock = %v, want ErrInvalidEvent", err)
	}
	if got := count(t, pool, `SELECT count(*) FROM blocks`); got != 1 {
		t.Fatal("the block must stay after rollback")
	}
	if got := count(t, pool, `SELECT count(*) FROM friendships WHERE active`); got != 0 {
		t.Fatal("the friendship must stay suspended after rollback")
	}
	if got := count(t, pool, `SELECT count(*) FROM outbox`); got != outbox+2 {
		t.Fatalf("outbox rows = %d, want %d", got, outbox+2)
	}
}

// friendshipEventTypes returns the pair's friend.removed and friend.restored
// events in order.
func friendshipEventTypes(t *testing.T, pool *pgxpool.Pool, aggregate string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT event_type FROM outbox
		WHERE aggregate_type = 'friendship' AND aggregate_id = $1 AND event_type <> 'relationship.blocked' ORDER BY id`, aggregate)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var types []string
	for rows.Next() {
		var eventType string
		if err := rows.Scan(&eventType); err != nil {
			t.Fatal(err)
		}
		types = append(types, eventType)
	}
	return types
}

// TestBlockConcurrencyPostgres checks races on one user pair.
//
// Expected output:
//
//   - Racing identical blocks store one block and record one set of events.
//   - Racing blocks and unblocks in both directions leave the friendship active
//     exactly when no block remains, and its events alternate removed/restored.
//   - A block racing a send never leaves a pending request beside the block.
//   - A block racing an accept leaves no active friendship, and the events match
//     whichever operation won.
func TestBlockConcurrencyPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2, 3, 4, 5, 6, 7, 8)
	handler := blockHandler(t, pool)

	befriend(t, handler, 1, 2)
	var same []func() int
	for range 8 {
		same = append(same, func() int { return putBlock(handler, 1, 2).status })
	}
	for _, status := range runTogether(same...) {
		if status != http.StatusOK {
			t.Fatalf("concurrent block = %d", status)
		}
	}
	if got := count(t, pool, `SELECT count(*) FROM blocks`); got != 1 {
		t.Fatalf("blocks = %d, want 1", got)
	}
	if removed, blocked := len(outboxPayloads(t, pool, service.EventFriendRemoved)), len(outboxPayloads(t, pool, service.EventRelationshipBlocked)); removed != 1 || blocked != 1 {
		t.Fatalf("removed = %d, blocked = %d; want 1 each", removed, blocked)
	}

	befriend(t, handler, 3, 4)
	for round := range 15 {
		statuses := runTogether(
			func() int { return putBlock(handler, 3, 4).status },
			func() int { return putBlock(handler, 4, 3).status },
			func() int { return deleteBlock(handler, 3, 4).status },
			func() int { return deleteBlock(handler, 4, 3).status },
			func() int { return putBlock(handler, 3, 4).status },
			func() int { return deleteBlock(handler, 3, 4).status },
		)
		for _, status := range statuses {
			if status != http.StatusOK && status != http.StatusNoContent {
				t.Fatalf("round %d status = %d", round, status)
			}
		}
		blocks := count(t, pool, `SELECT count(*) FROM blocks WHERE blocker_id IN (3, 4)`)
		active := count(t, pool, `SELECT count(*) FROM friendships WHERE user_low = 3 AND user_high = 4 AND active`)
		if (blocks == 0) != (active == 1) {
			t.Fatalf("round %d: blocks = %d, active = %d", round, blocks, active)
		}
	}
	for i, eventType := range friendshipEventTypes(t, pool, "3:4") {
		want := service.EventFriendRemoved
		if i%2 == 1 {
			want = service.EventFriendRestored
		}
		if eventType != want {
			t.Fatalf("event %d = %s, want %s", i, eventType, want)
		}
	}

	for round := range 15 {
		mustExec(t, pool, `DELETE FROM blocks WHERE blocker_id IN (5, 6)`)
		mustExec(t, pool, `DELETE FROM friend_requests WHERE requester_id IN (5, 6)`)
		statuses := runTogether(
			func() int { return sendTo(handler, 5, 6).status },
			func() int { return putBlock(handler, 6, 5).status },
		)
		if (statuses[0] != http.StatusCreated && statuses[0] != http.StatusForbidden) || statuses[1] != http.StatusOK {
			t.Fatalf("round %d: send = %d, block = %d", round, statuses[0], statuses[1])
		}
		if got := count(t, pool, `SELECT count(*) FROM friend_requests WHERE requester_id = 5 AND state = 'pending'`); got != 0 {
			t.Fatalf("round %d: %d pending requests beside a block", round, got)
		}
	}

	for round := range 15 {
		mustExec(t, pool, `DELETE FROM blocks WHERE blocker_id IN (7, 8)`)
		mustExec(t, pool, `DELETE FROM friendships WHERE user_low = 7`)
		mustExec(t, pool, `DELETE FROM outbox WHERE aggregate_id = '7:8'`)
		request := mustSend(t, handler, 7, 8)
		statuses := runTogether(
			func() int {
				return friendCall(handler, http.MethodPost, resolvePath(request, "accept"), "", session(8)).status
			},
			func() int { return putBlock(handler, 8, 7).status },
		)
		if statuses[1] != http.StatusOK {
			t.Fatalf("round %d: block = %d", round, statuses[1])
		}
		if got := count(t, pool, `SELECT count(*) FROM friendships WHERE user_low = 7 AND user_high = 8 AND active`); got != 0 {
			t.Fatalf("round %d: active friendship beside a block", round)
		}
		removed := count(t, pool, `SELECT count(*) FROM outbox WHERE aggregate_id = '7:8' AND event_type = 'friend.removed'`)
		status := ""
		if err := pool.QueryRow(context.Background(), `SELECT payload->>'status' FROM outbox
			WHERE event_type = 'friend.request.resolved' AND aggregate_id = $1`, fmt.Sprint(request)).Scan(&status); err != nil {
			t.Fatal(err)
		}
		switch statuses[0] {
		case http.StatusOK:
			if status != "ACCEPTED" || removed != 1 {
				t.Fatalf("round %d: accept won, status = %s, removed = %d", round, status, removed)
			}
		case http.StatusForbidden:
			if status != "CANCELLED" || removed != 0 {
				t.Fatalf("round %d: block won, status = %s, removed = %d", round, status, removed)
			}
		default:
			t.Fatalf("round %d: accept = %d", round, statuses[0])
		}
	}
}
