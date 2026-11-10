package unit_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/controller"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

type friendStoreStub struct {
	page        service.StoredFriendPage
	err         error
	listCalls   int
	removeCalls int
	limit       int
	offset      int
}

func (s *friendStoreStub) FriendPage(_ context.Context, _ int64, limit, offset int) (service.StoredFriendPage, error) {
	s.listCalls++
	s.limit, s.offset = limit, offset
	return s.page, s.err
}

func (s *friendStoreStub) RemoveFriendship(context.Context, int64, int64, service.OutboxEvent) error {
	s.removeCalls++
	return s.err
}

type presenceStub struct {
	online map[int64]bool
	err    error
}

func (p presenceStub) Online(_ context.Context, userIDs []int64) (map[int64]bool, error) {
	if p.err != nil {
		return nil, p.err
	}
	online := map[int64]bool{}
	for _, id := range userIDs {
		online[id] = p.online[id]
	}
	return online, nil
}

func friendsRequest(store *friendStoreStub, presence service.PresenceReader, method, target, cookie string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	controller.NewFriends(service.NewFriends(store, service.AvatarURLs{}, presence), blockSessions{}).RegisterRoutes(engine)
	request := httptest.NewRequest(method, target, nil)
	if cookie != "" {
		request.AddCookie(&http.Cookie{Name: "ft_session", Value: cookie})
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

// TestFriendsRefusals checks every refusal on the friend routes.
//
// Expected output: each case returns its status, code and field, and invalid
// input never reaches the store.
func TestFriendsRefusals(t *testing.T) {
	for _, tt := range []struct {
		name, method, target, cookie string
		storeErr                     error
		status                       int
		code                         string
		field                        string
		reachesStore                 bool
	}{
		{"list without session", http.MethodGet, "/api/v1/friends", "", nil, http.StatusUnauthorized, "UNAUTHORIZED", "", false},
		{"limit zero", http.MethodGet, "/api/v1/friends?limit=0", "valid", nil, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "limit", false},
		{"limit too big", http.MethodGet, "/api/v1/friends?limit=101", "valid", nil, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "limit", false},
		{"limit text", http.MethodGet, "/api/v1/friends?limit=x", "valid", nil, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "limit", false},
		{"limit empty", http.MethodGet, "/api/v1/friends?limit=", "valid", nil, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "limit", false},
		{"limit repeated", http.MethodGet, "/api/v1/friends?limit=1&limit=2", "valid", nil, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "limit", false},
		{"offset negative", http.MethodGet, "/api/v1/friends?offset=-1", "valid", nil, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "offset", false},
		{"offset text", http.MethodGet, "/api/v1/friends?offset=x", "valid", nil, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "offset", false},
		{"list outage", http.MethodGet, "/api/v1/friends", "valid", fmt.Errorf("%w: test", service.ErrUnavailable), http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "", true},
		{"remove without session", http.MethodDelete, "/api/v1/friends/43", "", nil, http.StatusUnauthorized, "UNAUTHORIZED", "", false},
		{"remove text id", http.MethodDelete, "/api/v1/friends/abc", "valid", nil, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "user_id", false},
		{"remove zero id", http.MethodDelete, "/api/v1/friends/0", "valid", nil, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "user_id", false},
		{"remove outage", http.MethodDelete, "/api/v1/friends/43", "valid", fmt.Errorf("%w: test", service.ErrUnavailable), http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &friendStoreStub{err: tt.storeErr}
			recorder := friendsRequest(store, service.NoPresence{}, tt.method, tt.target, tt.cookie)
			var body struct {
				Error struct {
					Code    string         `json:"code"`
					Details map[string]any `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("body %q: %v", recorder.Body.String(), err)
			}
			if recorder.Code != tt.status || body.Error.Code != tt.code {
				t.Fatalf("response = %d %s, want %d %s", recorder.Code, recorder.Body.String(), tt.status, tt.code)
			}
			if tt.field != "" && body.Error.Details["field"] != tt.field {
				t.Fatalf("details = %v, want field %s", body.Error.Details, tt.field)
			}
			if reached := store.listCalls+store.removeCalls > 0; reached != tt.reachesStore {
				t.Fatalf("store reached = %v, want %v", reached, tt.reachesStore)
			}
		})
	}
}

// TestFriendsListResponse checks defaults, the response shape and presence.
//
// Expected output:
//
//   - Limit 50 and offset 0 apply when absent.
//   - has_more compares the page end with the total.
//   - A presence failure is 503, never an offline friend.
func TestFriendsListResponse(t *testing.T) {
	store := &friendStoreStub{page: service.StoredFriendPage{Total: 3, Friends: []service.StoredProfile{
		{UserID: 43, Username: "red_agent", AvatarKind: "default"},
		{UserID: 44, Username: "blue_master", AvatarKind: "default"},
	}}}
	presence := presenceStub{online: map[int64]bool{43: true}}

	recorder := friendsRequest(store, presence, http.MethodGet, "/api/v1/friends", "valid")
	want := `{"data":{"friend_count":3,"friends":[` +
		`{"avatar_url":"/static/avatars/default/43.svg","is_online":true,"user_id":43,"username":"red_agent"},` +
		`{"avatar_url":"/static/avatars/default/44.svg","is_online":false,"user_id":44,"username":"blue_master"}],` +
		`"has_more":true,"limit":50,"offset":0}}`
	if recorder.Code != http.StatusOK || recorder.Body.String() != want {
		t.Fatalf("list = %d %s\nwant %s", recorder.Code, recorder.Body.String(), want)
	}
	if store.limit != 50 || store.offset != 0 {
		t.Fatalf("store got limit %d offset %d", store.limit, store.offset)
	}

	last := friendsRequest(store, presence, http.MethodGet, "/api/v1/friends?limit=2&offset=1", "valid")
	if !strings.Contains(last.Body.String(), `"has_more":false`) || store.limit != 2 || store.offset != 1 {
		t.Fatalf("last page = %s, limit %d offset %d", last.Body.String(), store.limit, store.offset)
	}

	failing := presenceStub{err: fmt.Errorf("%w: registry", service.ErrUnavailable)}
	outage := friendsRequest(store, failing, http.MethodGet, "/api/v1/friends", "valid")
	if outage.Code != http.StatusServiceUnavailable || strings.Contains(outage.Body.String(), "is_online") {
		t.Fatalf("presence outage = %d %s", outage.Code, outage.Body.String())
	}
}

// TestFriendsRemoveResponses checks the remove results.
//
// Expected output: success is 204 with no body, and no friendship is 404
// NOT_FRIENDS naming the user.
func TestFriendsRemoveResponses(t *testing.T) {
	removed := friendsRequest(&friendStoreStub{}, service.NoPresence{}, http.MethodDelete, "/api/v1/friends/43", "valid")
	if removed.Code != http.StatusNoContent || removed.Body.Len() != 0 {
		t.Fatalf("remove = %d %q", removed.Code, removed.Body.String())
	}

	missing := friendsRequest(&friendStoreStub{err: service.ErrNotFriends}, service.NoPresence{}, http.MethodDelete, "/api/v1/friends/9999", "valid")
	want := `{"error":{"code":"NOT_FRIENDS","message":"User 9999 is not a friend.","details":{"user_id":9999}}}`
	if missing.Code != http.StatusNotFound || strings.TrimSpace(missing.Body.String()) != want {
		t.Fatalf("not friends = %d %s", missing.Code, missing.Body.String())
	}
}
