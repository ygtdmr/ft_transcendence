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

type userSearchStoreStub struct {
	matches []service.StoredUserMatch
	err     error
	calls   int
	query   string
	limit   int
	offset  int
}

func (s *userSearchStoreStub) SearchUsers(_ context.Context, _ int64, query string, limit, offset int) ([]service.StoredUserMatch, error) {
	s.calls++
	s.query, s.limit, s.offset = query, limit, offset
	return s.matches, s.err
}

func userSearchRequest(store *userSearchStoreStub, target, cookie string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	controller.NewUserSearch(service.NewUserSearch(store, service.AvatarURLs{}), blockSessions{}).RegisterRoutes(engine)
	request := httptest.NewRequest(http.MethodGet, target, nil)
	if cookie != "" {
		request.AddCookie(&http.Cookie{Name: "ft_session", Value: cookie})
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

// TestProjectRelationship checks every combination that decides a relationship.
//
// Expected output: a viewer's own block wins, then friendship, then the pending
// request, and only the two request states carry an id.
func TestProjectRelationship(t *testing.T) {
	for _, tt := range []struct {
		name    string
		facts   service.RelationshipFacts
		want    string
		request int64
	}{
		{"nothing", service.RelationshipFacts{}, service.RelationshipNone, 0},
		{"viewer blocks", service.RelationshipFacts{ViewerBlocks: true}, service.RelationshipBlocked, 0},
		{"friends", service.RelationshipFacts{Friends: true}, service.RelationshipFriends, 0},
		{"sent", service.RelationshipFacts{PendingRequestID: 7, PendingFromViewer: true}, service.RelationshipRequestSent, 7},
		{"received", service.RelationshipFacts{PendingRequestID: 8}, service.RelationshipRequestReceived, 8},
		{"block beats friendship", service.RelationshipFacts{ViewerBlocks: true, Friends: true}, service.RelationshipBlocked, 0},
		{"block beats request", service.RelationshipFacts{ViewerBlocks: true, PendingRequestID: 9}, service.RelationshipBlocked, 0},
		{"friendship beats request", service.RelationshipFacts{Friends: true, PendingRequestID: 9}, service.RelationshipFriends, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, id := service.ProjectRelationship(tt.facts)
			var request int64
			if id != nil {
				request = *id
			}
			if got != tt.want || request != tt.request {
				t.Fatalf("got %s %d, want %s %d", got, request, tt.want, tt.request)
			}
		})
	}
}

// TestUserSearchRefusals checks every refusal.
//
// Expected output: each case is 401 or 422 with its field, and invalid input
// never reaches the store.
func TestUserSearchRefusals(t *testing.T) {
	const path = "/api/v1/users/search"
	for _, tt := range []struct {
		name, query, field string
	}{
		{"missing q", "", "q"},
		{"empty q", "?q=", "q"},
		{"spaces only", "?q=%20%20", "q"},
		{"too long", "?q=" + strings.Repeat("a", 21), "q"},
		{"repeated q", "?q=a&q=b", "q"},
		{"nul byte", "?q=a%00b", "q"},
		{"invalid utf8", "?q=%ff", "q"},
		{"limit zero", "?q=a&limit=0", "limit"},
		{"limit too big", "?q=a&limit=51", "limit"},
		{"limit text", "?q=a&limit=x", "limit"},
		{"limit repeated", "?q=a&limit=1&limit=2", "limit"},
		{"offset negative", "?q=a&offset=-1", "offset"},
		{"offset text", "?q=a&offset=x", "offset"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &userSearchStoreStub{}
			recorder := userSearchRequest(store, path+tt.query, "valid")
			var body struct {
				Error struct {
					Code    string         `json:"code"`
					Details map[string]any `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("body %q: %v", recorder.Body.String(), err)
			}
			if recorder.Code != http.StatusUnprocessableEntity || body.Error.Code != "VALIDATION_ERROR" || body.Error.Details["field"] != tt.field {
				t.Fatalf("response = %d %s, want 422 field %s", recorder.Code, recorder.Body.String(), tt.field)
			}
			if store.calls != 0 {
				t.Fatal("a refused request reached the store")
			}
		})
	}

	store := &userSearchStoreStub{}
	if recorder := userSearchRequest(store, path+"?q=a", ""); recorder.Code != http.StatusUnauthorized || store.calls != 0 {
		t.Fatalf("no session = %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestUserSearchResponse checks defaults, the response shape and failures.
//
// Expected output:
//
//   - Limit 20 and offset 0 apply when absent; the store gets one extra row.
//   - The extra row becomes has_more and is not returned.
//   - q is trimmed, and 20 characters of any script are accepted.
//   - An outage is 503.
func TestUserSearchResponse(t *testing.T) {
	store := &userSearchStoreStub{matches: []service.StoredUserMatch{
		{Profile: service.StoredProfile{UserID: 43, Username: "red_agent", AvatarKind: "default"}},
		{Profile: service.StoredProfile{UserID: 46, Username: "extra_red", AvatarKind: "default"},
			Facts: service.RelationshipFacts{PendingRequestID: 7, PendingFromViewer: true}},
	}}
	recorder := userSearchRequest(store, "/api/v1/users/search?q=%20red%20", "valid")
	want := `{"data":{"has_more":false,"limit":20,"offset":0,"results":[` +
		`{"avatar_url":"/static/avatars/default/43.svg","friend_request_id":null,"relationship":"NONE","user_id":43,"username":"red_agent"},` +
		`{"avatar_url":"/static/avatars/default/46.svg","friend_request_id":7,"relationship":"REQUEST_SENT","user_id":46,"username":"extra_red"}]}}`
	if recorder.Code != http.StatusOK || recorder.Body.String() != want {
		t.Fatalf("search = %d %s\nwant %s", recorder.Code, recorder.Body.String(), want)
	}
	if store.query != "red" || store.limit != 21 || store.offset != 0 {
		t.Fatalf("store got %q limit %d offset %d", store.query, store.limit, store.offset)
	}

	more := userSearchRequest(store, "/api/v1/users/search?q=red&limit=1", "valid")
	if !strings.Contains(more.Body.String(), `"has_more":true`) || strings.Contains(more.Body.String(), "extra_red") {
		t.Fatalf("extra row = %s", more.Body.String())
	}

	if longest := userSearchRequest(&userSearchStoreStub{}, "/api/v1/users/search?q="+strings.Repeat("%C3%A9", 20), "valid"); longest.Code != http.StatusOK {
		t.Fatalf("20 characters = %d %s", longest.Code, longest.Body.String())
	}

	outage := userSearchRequest(&userSearchStoreStub{err: fmt.Errorf("%w: test", service.ErrUnavailable)}, "/api/v1/users/search?q=red", "valid")
	if outage.Code != http.StatusServiceUnavailable || strings.Contains(outage.Body.String(), "results") {
		t.Fatalf("outage = %d %s", outage.Code, outage.Body.String())
	}
}
