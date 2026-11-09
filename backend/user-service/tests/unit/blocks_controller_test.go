package unit_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/client/authclient"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/controller"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

// blockSessions accepts only "valid", as user 42.
type blockSessions struct{}

func (blockSessions) ValidateSession(_ context.Context, session string) (int64, error) {
	if session != "valid" {
		return 0, authclient.ErrSessionInvalid
	}
	return 42, nil
}

type blockStoreStub struct {
	err    error
	placed int
	lifted int
}

func (s *blockStoreStub) PlaceBlock(context.Context, int64, int64, service.BlockEventBuilder) (time.Time, error) {
	s.placed++
	return time.Date(2026, 9, 10, 8, 50, 0, 0, time.UTC), s.err
}

func (s *blockStoreStub) LiftBlock(context.Context, int64, int64, service.RestoreEventBuilder) error {
	s.lifted++
	return s.err
}

func blockRequest(store *blockStoreStub, method, target, body, cookie string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	controller.NewBlocks(service.NewBlocks(store, service.AvatarURLs{}), blockSessions{}).RegisterRoutes(engine)
	request := httptest.NewRequest(method, "/api/v1/users/"+target+"/block", strings.NewReader(body))
	if cookie != "" {
		request.AddCookie(&http.Cookie{Name: "ft_session", Value: cookie})
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

// TestBlockRouteRefusals checks every refusal on both block routes.
//
// Expected output: each case returns its status and code, with the documented
// field or user, and invalid input never reaches the store.
func TestBlockRouteRefusals(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		for _, tt := range []struct {
			name, target, cookie string
			storeErr             error
			status               int
			code                 string
			detailKey            string
			reachesStore         bool
		}{
			{"no session", "45", "", nil, http.StatusUnauthorized, "UNAUTHORIZED", "", false},
			{"text id", "abc", "valid", nil, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "field", false},
			{"zero id", "0", "valid", nil, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "field", false},
			{"negative id", "-1", "valid", nil, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "field", false},
			{"overflow id", "99999999999999999999", "valid", nil, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "field", false},
			{"self", "42", "valid", nil, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "field", false},
			{"unknown user", "45", "valid", service.ErrUserNotFound, http.StatusNotFound, "USER_NOT_FOUND", "user_id", true},
			{"database down", "45", "valid", fmt.Errorf("%w: test", service.ErrUnavailable), http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "", true},
		} {
			t.Run(method+" "+tt.name, func(t *testing.T) {
				store := &blockStoreStub{err: tt.storeErr}
				recorder := blockRequest(store, method, tt.target, "", tt.cookie)
				var body struct {
					Error struct {
						Code    string         `json:"code"`
						Message string         `json:"message"`
						Details map[string]any `json:"details"`
					} `json:"error"`
				}
				if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
					t.Fatalf("body %q: %v", recorder.Body.String(), err)
				}
				if recorder.Code != tt.status || body.Error.Code != tt.code {
					t.Fatalf("response = %d %s, want %d %s", recorder.Code, recorder.Body.String(), tt.status, tt.code)
				}
				if _, found := body.Error.Details[tt.detailKey]; tt.detailKey != "" && !found {
					t.Fatalf("details = %v, want key %s", body.Error.Details, tt.detailKey)
				}
				if tt.name == "self" && body.Error.Message != "You cannot block yourself." {
					t.Fatalf("message = %q", body.Error.Message)
				}
				if reached := store.placed+store.lifted > 0; reached != tt.reachesStore {
					t.Fatalf("store reached = %v, want %v", reached, tt.reachesStore)
				}
			})
		}
	}
}

// TestBlockRouteSuccess checks the documented success responses.
//
// Expected output:
//
//   - PUT is 200 with user_id and blocked_at.
//   - PUT with a body is 400 and changes nothing.
//   - DELETE is 204 with an empty body.
func TestBlockRouteSuccess(t *testing.T) {
	store := &blockStoreStub{}
	placed := blockRequest(store, http.MethodPut, "45", "", "valid")
	var body struct {
		Data struct {
			UserID    int64  `json:"user_id"`
			BlockedAt string `json:"blocked_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(placed.Body.Bytes(), &body); err != nil || placed.Code != http.StatusOK {
		t.Fatalf("block = %d %s", placed.Code, placed.Body.String())
	}
	if blockedAt, err := time.Parse(time.RFC3339Nano, body.Data.BlockedAt); err != nil ||
		body.Data.UserID != 45 || !blockedAt.Equal(time.Date(2026, 9, 10, 8, 50, 0, 0, time.UTC)) {
		t.Fatalf("data = %+v", body.Data)
	}

	before := store.placed
	withBody := blockRequest(store, http.MethodPut, "45", "{}", "valid")
	if withBody.Code != http.StatusBadRequest || !strings.Contains(withBody.Body.String(), `"field":"body"`) || store.placed != before {
		t.Fatalf("block with body = %d %s", withBody.Code, withBody.Body.String())
	}

	lifted := blockRequest(store, http.MethodDelete, "45", "", "valid")
	if lifted.Code != http.StatusNoContent || lifted.Body.Len() != 0 || store.lifted != 1 {
		t.Fatalf("unblock = %d %q", lifted.Code, lifted.Body.String())
	}
}

// TestBlocksRefuseSelfWithoutStore checks the service rule directly.
//
// Expected output: both operations return ErrCannotBlockSelf and touch no record.
func TestBlocksRefuseSelfWithoutStore(t *testing.T) {
	store := &blockStoreStub{}
	blocks := service.NewBlocks(store, service.AvatarURLs{})
	if _, err := blocks.Block(context.Background(), 7, 7); !errors.Is(err, service.ErrCannotBlockSelf) {
		t.Fatalf("Block = %v", err)
	}
	if err := blocks.Unblock(context.Background(), 7, 7); !errors.Is(err, service.ErrCannotBlockSelf) {
		t.Fatalf("Unblock = %v", err)
	}
	if store.placed+store.lifted != 0 {
		t.Fatal("self block reached the store")
	}
}
