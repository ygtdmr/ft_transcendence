package integration_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

type searchData struct {
	Limit   int  `json:"limit"`
	Offset  int  `json:"offset"`
	HasMore bool `json:"has_more"`
	Results []struct {
		UserID          int64  `json:"user_id"`
		Username        string `json:"username"`
		AvatarURL       string `json:"avatar_url"`
		Relationship    string `json:"relationship"`
		FriendRequestID *int64 `json:"friend_request_id"`
	} `json:"results"`
}

func searchUsers(t *testing.T, handler http.Handler, viewer int64, query string) searchData {
	t.Helper()
	response := friendCall(handler, http.MethodGet, "/api/v1/users/search?q="+query, "", session(viewer))
	if response.status != http.StatusOK {
		t.Fatalf("search %q = %d %s", query, response.status, response.raw)
	}
	var data searchData
	if err := json.Unmarshal(response.Data, &data); err != nil {
		t.Fatalf("search data = %s", response.Data)
	}
	if data.Results == nil {
		t.Fatalf("results must be an array: %s", response.Data)
	}
	return data
}

func searchIDs(data searchData) string {
	ids := make([]string, 0, len(data.Results))
	for _, result := range data.Results {
		ids = append(ids, strconv.FormatInt(result.UserID, 10))
	}
	return strings.Join(ids, ",")
}

func rename(t *testing.T, pool *pgxpool.Pool, names map[int64]string) {
	t.Helper()
	for id, name := range names {
		mustExec(t, pool, `UPDATE profiles SET username = $2 WHERE user_id = $1`, id, name)
	}
}

// TestUserSearchMatchingPostgres checks matching, ordering and paging.
//
// Expected output:
//
//   - A case-insensitive substring matches; prefix matches come first, then username.
//   - The caller and users being deleted are omitted.
//   - limit, offset and has_more describe the page, including past the end.
func TestUserSearchMatchingPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2, 3, 4, 5, 6, 7)
	rename(t, pool, map[int64]string{1: "red_viewer", 2: "red_agent", 3: "extra_red", 4: "RED_Zed", 5: "blue_master", 6: "zz_red", 7: "red_leaving"})
	handler := blockHandler(t, pool)
	mustExec(t, pool, `INSERT INTO deletion_operations (operation_id, user_id) VALUES ('3f9b2c71-5d4e-4a86-9c1b-7e2a6d0f8b53', 7)`)

	all := searchUsers(t, handler, 1, "ReD")
	if searchIDs(all) != "2,4,3,6" || all.Limit != 20 || all.Offset != 0 || all.HasMore {
		t.Fatalf("search = %+v", all)
	}
	if first := all.Results[0]; first.Username != "red_agent" || first.AvatarURL != "/static/avatars/default/2.svg" {
		t.Fatalf("first result = %+v", first)
	}
	if page := searchUsers(t, handler, 1, "red&limit=2"); searchIDs(page) != "2,4" || !page.HasMore || page.Limit != 2 {
		t.Fatalf("first page = %+v", page)
	}
	if page := searchUsers(t, handler, 1, "red&limit=2&offset=2"); searchIDs(page) != "3,6" || page.HasMore || page.Offset != 2 {
		t.Fatalf("last page = %+v", page)
	}
	if page := searchUsers(t, handler, 1, "red&offset=9"); len(page.Results) != 0 || page.HasMore {
		t.Fatalf("past the end = %+v", page)
	}
	if page := searchUsers(t, handler, 1, "red&limit=50"); page.Limit != 50 {
		t.Fatalf("largest limit = %+v", page)
	}
	if none := searchUsers(t, handler, 1, "nobody"); len(none.Results) != 0 {
		t.Fatalf("no match = %+v", none)
	}
	if trimmed := searchUsers(t, handler, 1, url.QueryEscape("  blue  ")); searchIDs(trimmed) != "5" {
		t.Fatalf("trimmed = %+v", trimmed)
	}
}

// TestUserSearchTreatsQueryAsTextPostgres checks SQL pattern characters.
//
// Expected output: "_" matches only an underscore, and "%" and "\" match nothing.
func TestUserSearchTreatsQueryAsTextPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2, 3)
	rename(t, pool, map[int64]string{1: "viewer", 2: "red_agent", 3: "redXagent"})
	handler := blockHandler(t, pool)

	if got := searchUsers(t, handler, 1, "d_a"); searchIDs(got) != "2" {
		t.Fatalf("underscore = %+v", got)
	}
	for _, pattern := range []string{"%", "red%", `\`} {
		if got := searchUsers(t, handler, 1, url.QueryEscape(pattern)); len(got.Results) != 0 {
			t.Fatalf("pattern %q = %+v", pattern, got)
		}
	}
}

// TestUserSearchRelationshipsPostgres checks each relationship against real records.
//
// Expected output:
//
//   - The viewer sees NONE, REQUEST_SENT, REQUEST_RECEIVED, FRIENDS and BLOCKED
//     as the records say, and friend_request_id only for the two request states.
//   - The other side of each request sees the opposite state with the same id.
//   - A user who blocked the viewer reads as NONE with the same fields as a
//     stranger, and a suspended friendship is never FRIENDS.
func TestUserSearchRelationshipsPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	handler := blockHandler(t, pool)

	sent := mustSend(t, handler, 1, 3)
	received := mustSend(t, handler, 4, 1)
	befriend(t, handler, 1, 5)
	mustBlock(t, handler, 1, 6)
	mustBlock(t, handler, 7, 1)
	befriend(t, handler, 1, 8)
	mustBlock(t, handler, 8, 1)
	befriend(t, handler, 1, 9)
	mustBlock(t, handler, 1, 9)

	want := map[int64]struct {
		relationship string
		request      int64
	}{
		2: {"NONE", 0},
		3: {"REQUEST_SENT", sent},
		4: {"REQUEST_RECEIVED", received},
		5: {"FRIENDS", 0},
		6: {"BLOCKED", 0},
		7: {"NONE", 0},
		8: {"NONE", 0},
		9: {"BLOCKED", 0},
	}
	results := searchUsers(t, handler, 1, "player_")
	if len(results.Results) != len(want) {
		t.Fatalf("results = %+v", results)
	}
	for _, result := range results.Results {
		expected := want[result.UserID]
		var request int64
		if result.FriendRequestID != nil {
			request = *result.FriendRequestID
		}
		if result.Relationship != expected.relationship || request != expected.request {
			t.Fatalf("user %d = %s request %d, want %+v", result.UserID, result.Relationship, request, expected)
		}
	}

	seenBy := func(viewer, target int64) (string, *int64) {
		for _, result := range searchUsers(t, handler, viewer, "player_").Results {
			if result.UserID == target {
				return result.Relationship, result.FriendRequestID
			}
		}
		t.Fatalf("user %d does not see user %d", viewer, target)
		return "", nil
	}
	if relationship, id := seenBy(3, 1); relationship != "REQUEST_RECEIVED" || id == nil || *id != sent {
		t.Fatalf("recipient sees %s %v", relationship, id)
	}
	if relationship, id := seenBy(4, 1); relationship != "REQUEST_SENT" || id == nil || *id != received {
		t.Fatalf("sender sees %s %v", relationship, id)
	}
	if relationship, _ := seenBy(6, 1); relationship != "NONE" {
		t.Fatalf("blocked viewer sees %s", relationship)
	}
	if relationship, _ := seenBy(7, 1); relationship != "BLOCKED" {
		t.Fatalf("blocker sees %s", relationship)
	}

	response := friendCall(handler, http.MethodGet, "/api/v1/users/search?q=player_", "", session(1))
	var raw struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(response.Data, &raw); err != nil {
		t.Fatal(err)
	}
	var stranger, blockedBy map[string]any
	for _, result := range raw.Results {
		if payloadKeys(result) != "avatar_url,friend_request_id,relationship,user_id,username" {
			t.Fatalf("result fields = %v", result)
		}
		switch result["user_id"] {
		case float64(2):
			stranger = result
		case float64(7):
			blockedBy = result
		}
	}
	if stranger["relationship"] != blockedBy["relationship"] || stranger["friend_request_id"] != blockedBy["friend_request_id"] {
		t.Fatalf("a block shows: %v / %v", stranger, blockedBy)
	}
	if strings.Contains(strings.ToLower(response.raw), "email") {
		t.Fatalf("response mentions email: %s", response.raw)
	}
}

// TestUserSearchFollowsChangesPostgres checks that results read current records.
//
// Expected output: unblocking and removing a friend change the next result.
func TestUserSearchFollowsChangesPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2)
	handler := blockHandler(t, pool)
	relationship := func() string { return searchUsers(t, handler, 1, "player_2").Results[0].Relationship }

	befriend(t, handler, 1, 2)
	if got := relationship(); got != "FRIENDS" {
		t.Fatalf("after accept = %s", got)
	}
	mustBlock(t, handler, 2, 1)
	if got := relationship(); got != "NONE" {
		t.Fatalf("after the other user blocks = %s", got)
	}
	mustUnblock(t, handler, 2, 1)
	if got := relationship(); got != "FRIENDS" {
		t.Fatalf("after unblock = %s", got)
	}
	if response := removeFriend(handler, 1, 2); response.status != http.StatusNoContent {
		t.Fatalf("remove = %d %s", response.status, response.raw)
	}
	if got := relationship(); got != "NONE" {
		t.Fatalf("after removal = %s", got)
	}
}
