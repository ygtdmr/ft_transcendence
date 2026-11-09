package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	userpostgres "github.com/melmut-42/ft_transcendence/backend/user-service/internal/repository/postgres"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

type friendListData struct {
	FriendCount int  `json:"friend_count"`
	Limit       int  `json:"limit"`
	Offset      int  `json:"offset"`
	HasMore     bool `json:"has_more"`
	Friends     []struct {
		UserID    int64  `json:"user_id"`
		Username  string `json:"username"`
		AvatarURL string `json:"avatar_url"`
		IsOnline  bool   `json:"is_online"`
	} `json:"friends"`
}

func listFriends(t *testing.T, handler http.Handler, user int64, query string) friendListData {
	t.Helper()
	response := friendCall(handler, http.MethodGet, "/api/v1/friends"+query, "", session(user))
	if response.status != http.StatusOK {
		t.Fatalf("list = %d %s", response.status, response.raw)
	}
	var data friendListData
	if err := json.Unmarshal(response.Data, &data); err != nil {
		t.Fatalf("list data = %s", response.Data)
	}
	if data.Friends == nil {
		t.Fatalf("friends must be an array: %s", response.Data)
	}
	return data
}

func friendIDs(data friendListData) string {
	ids := make([]string, 0, len(data.Friends))
	for _, friend := range data.Friends {
		ids = append(ids, strconv.FormatInt(friend.UserID, 10))
	}
	return strings.Join(ids, ",")
}

func removeFriend(handler http.Handler, user, friend int64) friendResponse {
	return friendCall(handler, http.MethodDelete, "/api/v1/friends/"+strconv.FormatInt(friend, 10), "", session(user))
}

// TestFriendListPostgres checks listing and paging on real records.
//
// Expected output:
//
//   - Both users see the friendship after acceptance; a pending request is not listed.
//   - Friends order case-insensitively by username.
//   - friend_count, limit, offset and has_more describe the page, including an
//     offset past the end.
//   - A user with no friends gets an empty array.
func TestFriendListPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2, 3, 4, 5, 6)
	handler := blockHandler(t, pool)
	mustExec(t, pool, `UPDATE profiles SET username = 'Zed' WHERE user_id = 2`)
	mustExec(t, pool, `UPDATE profiles SET username = 'alice' WHERE user_id = 3`)
	mustExec(t, pool, `UPDATE profiles SET username = 'Bob' WHERE user_id = 4`)
	befriend(t, handler, 1, 2)
	befriend(t, handler, 3, 1)
	befriend(t, handler, 1, 4)
	mustSend(t, handler, 1, 5)

	all := listFriends(t, handler, 1, "")
	if friendIDs(all) != "3,4,2" || all.FriendCount != 3 || all.Limit != 50 || all.Offset != 0 || all.HasMore {
		t.Fatalf("list = %+v", all)
	}
	if first := all.Friends[0]; first.Username != "alice" || first.AvatarURL != "/static/avatars/default/3.svg" || first.IsOnline {
		t.Fatalf("first friend = %+v", first)
	}
	if other := listFriends(t, handler, 2, ""); friendIDs(other) != "1" || other.FriendCount != 1 {
		t.Fatalf("other side = %+v", other)
	}

	if page := listFriends(t, handler, 1, "?limit=2"); friendIDs(page) != "3,4" || page.FriendCount != 3 || !page.HasMore || page.Limit != 2 {
		t.Fatalf("first page = %+v", page)
	}
	if page := listFriends(t, handler, 1, "?limit=2&offset=2"); friendIDs(page) != "2" || page.HasMore || page.Offset != 2 {
		t.Fatalf("last page = %+v", page)
	}
	if page := listFriends(t, handler, 1, "?offset=9"); len(page.Friends) != 0 || page.FriendCount != 3 || page.HasMore || page.Offset != 9 {
		t.Fatalf("past the end = %+v", page)
	}
	if page := listFriends(t, handler, 1, "?limit=100"); page.Limit != 100 || page.FriendCount != 3 {
		t.Fatalf("largest limit = %+v", page)
	}
	if empty := listFriends(t, handler, 6, ""); empty.FriendCount != 0 || empty.HasMore {
		t.Fatalf("no friends = %+v", empty)
	}
}

// TestFriendListHidesInactivePostgres checks who is withheld from the list.
//
// Expected output:
//
//   - A block in either direction hides the friend from both lists and the count.
//   - The friend returns once neither user blocks the other.
//   - A friend being deleted is withheld.
func TestFriendListHidesInactivePostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2, 3)
	handler := blockHandler(t, pool)
	befriend(t, handler, 1, 2)
	befriend(t, handler, 1, 3)

	mustBlock(t, handler, 2, 1)
	if list := listFriends(t, handler, 1, ""); friendIDs(list) != "3" || list.FriendCount != 1 {
		t.Fatalf("blocked side = %+v", list)
	}
	if list := listFriends(t, handler, 2, ""); list.FriendCount != 0 {
		t.Fatalf("blocker side = %+v", list)
	}
	mustBlock(t, handler, 1, 2)
	mustUnblock(t, handler, 2, 1)
	if list := listFriends(t, handler, 1, ""); list.FriendCount != 1 {
		t.Fatalf("one block left = %+v", list)
	}
	mustUnblock(t, handler, 1, 2)
	if list := listFriends(t, handler, 1, ""); friendIDs(list) != "2,3" {
		t.Fatalf("after unblock = %+v", list)
	}

	mustExec(t, pool, `INSERT INTO deletion_operations (operation_id, user_id) VALUES ('3f9b2c71-5d4e-4a86-9c1b-7e2a6d0f8b53', 3)`)
	if list := listFriends(t, handler, 1, ""); friendIDs(list) != "2" || list.FriendCount != 1 {
		t.Fatalf("deleting friend = %+v", list)
	}
}

// TestRemoveFriendPostgres checks removal on real records.
//
// Expected output:
//
//   - Removal is symmetric, records one friend.removed and ends the verdict.
//   - A second removal, the other side, a stranger and the caller's own id are
//     404 NOT_FRIENDS and record nothing.
//   - A suspended friendship is 404 NOT_FRIENDS and stays stored.
//   - A new request and acceptance create the friendship again.
func TestRemoveFriendPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2, 3, 4)
	handler := blockHandler(t, pool)
	verdicts := verdictHandler(userpostgres.NewFriendshipVerdicts(pool))
	befriend(t, handler, 2, 1)
	wantVerdict(t, verdicts, 1, 2, verdictTrue)

	if response := removeFriend(handler, 1, 2); response.status != http.StatusNoContent || response.raw != "" {
		t.Fatalf("remove = %d %q", response.status, response.raw)
	}
	if listFriends(t, handler, 1, "").FriendCount != 0 || listFriends(t, handler, 2, "").FriendCount != 0 {
		t.Fatal("the friendship must leave both lists")
	}
	if got := count(t, pool, `SELECT count(*) FROM friendships`); got != 0 {
		t.Fatalf("friendships = %d", got)
	}
	removed := outboxPayloads(t, pool, service.EventFriendRemoved)
	if len(removed) != 1 || removed[0]["user_low"] != float64(1) || removed[0]["user_high"] != float64(2) ||
		payloadKeys(removed[0]) != "user_high,user_low" {
		t.Fatalf("removed events = %v", removed)
	}
	wantVerdict(t, verdicts, 1, 2, verdictFalse)

	for _, pair := range [][2]int64{{1, 2}, {2, 1}, {1, 3}, {1, 1}} {
		expectCode(t, removeFriend(handler, pair[0], pair[1]), http.StatusNotFound, "NOT_FRIENDS")
	}
	if got := len(outboxPayloads(t, pool, service.EventFriendRemoved)); got != 1 {
		t.Fatalf("removed events after refusals = %d", got)
	}

	befriend(t, handler, 3, 4)
	mustBlock(t, handler, 3, 4)
	expectCode(t, removeFriend(handler, 3, 4), http.StatusNotFound, "NOT_FRIENDS")
	if got := count(t, pool, `SELECT count(*) FROM friendships WHERE user_low = 3 AND user_high = 4 AND NOT active`); got != 1 {
		t.Fatal("a suspended friendship must stay stored")
	}

	befriend(t, handler, 1, 2)
	wantVerdict(t, verdicts, 1, 2, verdictTrue)
}

// TestRemoveFriendRollbackPostgres checks that removal and its event are atomic.
//
// Expected output: when the event cannot be recorded, the friendship stays active.
func TestRemoveFriendRollbackPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2)
	befriend(t, blockHandler(t, pool), 1, 2)
	bad := service.OutboxEvent{AggregateType: "friendship", AggregateID: "1:2", EventType: "not.in.catalog", Payload: map[string]any{}}

	if err := userpostgres.NewFriends(pool).RemoveFriendship(context.Background(), 1, 2, bad); !errors.Is(err, service.ErrInvalidEvent) {
		t.Fatalf("RemoveFriendship = %v, want ErrInvalidEvent", err)
	}
	if got := count(t, pool, `SELECT count(*) FROM friendships WHERE active`); got != 1 {
		t.Fatal("the friendship must stay active after rollback")
	}
}

// TestRemoveFriendConcurrencyPostgres checks races on one user pair.
//
// Expected output:
//
//   - Racing removals from both sides give one 204 and one event.
//   - A removal racing a send never leaves a pending request beside an active friendship.
//   - A removal racing a block leaves one recorded end of the friendship; the
//     row stays only when the block won.
//   - A removal racing an accept deletes the friendship only when the accept came first.
func TestRemoveFriendConcurrencyPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2, 3, 4, 5, 6, 7, 8)
	handler := blockHandler(t, pool)

	befriend(t, handler, 1, 2)
	var removals []func() int
	for n := range 8 {
		from, to := int64(1), int64(2)
		if n%2 == 1 {
			from, to = 2, 1
		}
		removals = append(removals, func() int { return removeFriend(handler, from, to).status })
	}
	wins := 0
	for _, status := range runTogether(removals...) {
		switch status {
		case http.StatusNoContent:
			wins++
		case http.StatusNotFound:
		default:
			t.Fatalf("concurrent removal = %d", status)
		}
	}
	if got := len(outboxPayloads(t, pool, service.EventFriendRemoved)); wins != 1 || got != 1 {
		t.Fatalf("wins = %d, events = %d; want one each", wins, got)
	}

	for round := range 15 {
		mustExec(t, pool, `DELETE FROM friend_requests WHERE requester_id IN (3, 4)`)
		mustExec(t, pool, `DELETE FROM friendships WHERE user_low = 3`)
		befriend(t, handler, 3, 4)
		statuses := runTogether(
			func() int { return removeFriend(handler, 3, 4).status },
			func() int { return sendTo(handler, 4, 3).status },
		)
		if statuses[0] != http.StatusNoContent || (statuses[1] != http.StatusCreated && statuses[1] != http.StatusConflict) {
			t.Fatalf("round %d: remove = %d, send = %d", round, statuses[0], statuses[1])
		}
		pending := count(t, pool, `SELECT count(*) FROM friend_requests WHERE requester_id = 4 AND state = 'pending'`)
		active := count(t, pool, `SELECT count(*) FROM friendships WHERE user_low = 3 AND active`)
		if pending+active > 1 || active != 0 {
			t.Fatalf("round %d: pending = %d, active = %d", round, pending, active)
		}
	}

	for round := range 15 {
		mustExec(t, pool, `DELETE FROM blocks WHERE blocker_id IN (5, 6)`)
		mustExec(t, pool, `DELETE FROM friendships WHERE user_low = 5`)
		mustExec(t, pool, `DELETE FROM friend_requests WHERE requester_id IN (5, 6)`)
		befriend(t, handler, 5, 6)
		mustExec(t, pool, `DELETE FROM outbox WHERE aggregate_id = '5:6'`)
		statuses := runTogether(
			func() int { return removeFriend(handler, 5, 6).status },
			func() int { return putBlock(handler, 6, 5).status },
		)
		if statuses[1] != http.StatusOK {
			t.Fatalf("round %d: block = %d", round, statuses[1])
		}
		removed := count(t, pool, `SELECT count(*) FROM outbox WHERE aggregate_id = '5:6' AND event_type = 'friend.removed'`)
		rows := count(t, pool, `SELECT count(*) FROM friendships WHERE user_low = 5 AND user_high = 6`)
		active := count(t, pool, `SELECT count(*) FROM friendships WHERE user_low = 5 AND user_high = 6 AND active`)
		wantRows := 1
		if statuses[0] == http.StatusNoContent {
			wantRows = 0
		} else if statuses[0] != http.StatusNotFound {
			t.Fatalf("round %d: remove = %d", round, statuses[0])
		}
		if removed != 1 || rows != wantRows || active != 0 {
			t.Fatalf("round %d: remove = %d, removed = %d, rows = %d, active = %d", round, statuses[0], removed, rows, active)
		}
	}

	for round := range 15 {
		mustExec(t, pool, `DELETE FROM friendships WHERE user_low = 7`)
		request := mustSend(t, handler, 7, 8)
		statuses := runTogether(
			func() int {
				return friendCall(handler, http.MethodPost, resolvePath(request, "accept"), "", session(8)).status
			},
			func() int { return removeFriend(handler, 7, 8).status },
		)
		if statuses[0] != http.StatusOK {
			t.Fatalf("round %d: accept = %d", round, statuses[0])
		}
		wantActive := 0
		if statuses[1] == http.StatusNotFound {
			wantActive = 1
		} else if statuses[1] != http.StatusNoContent {
			t.Fatalf("round %d: remove = %d", round, statuses[1])
		}
		if got := count(t, pool, `SELECT count(*) FROM friendships WHERE user_low = 7 AND user_high = 8 AND active`); got != wantActive {
			t.Fatalf("round %d: active = %d, want %d", round, got, wantActive)
		}
	}
}
