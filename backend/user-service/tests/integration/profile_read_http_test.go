package integration_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/controller"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/middleware/serviceauth"
	userpostgres "github.com/melmut-42/ft_transcendence/backend/user-service/internal/repository/postgres"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/server"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

// profileHandler wires provisioning and profile reads on one private listener.
func profileHandler(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	matrix := serviceauth.DefaultMatrix()
	matrix["game-session"] = []string{"friend.verdict"}
	auth := serviceauth.New(map[string][]string{"auth-service": {authCredential}, "game-session": {otherCredential}}, matrix)
	repository := userpostgres.NewProfile(pool)

	return server.NewInternal("0", slog.New(slog.NewTextHandler(io.Discard, nil)),
		controller.NewInternalProvisioning(service.NewProfile(repository), auth),
		controller.NewInternalProfile(service.NewProfileRead(repository, service.AvatarURLs{Base: "https://example.com"}), auth)).Handler
}

// TestCanonicalProfileRead checks the private profile read against real records.
//
// Expected output:
//
//   - An active user returns exactly user_id, username, avatar_url and the
//     provisioned created_at; later username and avatar changes show at once.
//   - A user without a profile is 404 USER_NOT_FOUND; a recorded deletion is
//     409 USER_DELETED; a bad id is 422; the wrong caller is 403.
func TestCanonicalProfileRead(t *testing.T) {
	pool := schemaPool(t)
	handler := profileHandler(t, pool)
	if got := provision(handler, opID(1), payload(42, "Alice_1")).Code; got != http.StatusCreated {
		t.Fatalf("provision status = %d", got)
	}
	read := func(path string) (int, map[string]any, errorBody) {
		response := call(handler, http.MethodGet, path, authCredential, "")
		var data struct {
			Data map[string]any `json:"data"`
		}
		if response.Code == http.StatusOK {
			decodeJSON(t, response.Body.Bytes(), &data)
			return response.Code, data.Data, errorBody{}
		}
		return response.Code, nil, decodeError(t, response)
	}

	status, data, _ := read("/internal/v1/profiles/42")
	if status != http.StatusOK || len(data) != 4 || data["user_id"] != float64(42) || data["username"] != "Alice_1" ||
		data["avatar_url"] != "https://example.com/static/avatars/default/42.svg" || data["created_at"] != accountCreated {
		t.Fatalf("profile = %d %v", status, data)
	}

	mustExec(t, pool, "UPDATE profiles SET username = 'Renamed', avatar_kind = 'preset', avatar_ref = 'cyborg-cat' WHERE user_id = 42")
	if _, data, _ := read("/internal/v1/profiles/42"); data["username"] != "Renamed" ||
		data["avatar_url"] != "https://example.com/avatars/presets/cyborg-cat.png" {
		t.Fatalf("profile after change = %v, want the current values", data)
	}

	if status, _, body := read("/internal/v1/profiles/7"); status != http.StatusNotFound ||
		body.Error.Code != "USER_NOT_FOUND" || body.Error.Details["user_id"] != float64(7) {
		t.Fatalf("missing profile = %d %+v", status, body)
	}
	mustExec(t, pool, "INSERT INTO deletion_operations (operation_id, user_id) VALUES ('11111111-1111-4111-8111-111111111111', 42)")
	if status, _, body := read("/internal/v1/profiles/42"); status != http.StatusConflict ||
		body.Error.Code != "USER_DELETED" || body.Error.Details["user_id"] != float64(42) {
		t.Fatalf("deleting profile = %d %+v", status, body)
	}
	for _, raw := range []string{"0", "-1", "abc"} {
		if status, _, body := read("/internal/v1/profiles/" + raw); status != http.StatusUnprocessableEntity ||
			body.Error.Details["field"] != "user_id" {
			t.Fatalf("user_id %q = %d %+v", raw, status, body)
		}
	}

	forbidden := call(handler, http.MethodGet, "/internal/v1/profiles/42", otherCredential, "")
	if forbidden.Code != http.StatusForbidden || errorCode(t, forbidden) != "SERVICE_FORBIDDEN" {
		t.Fatalf("wrong caller = %d %s", forbidden.Code, forbidden.Body)
	}
	if strings.Contains(call(handler, http.MethodGet, "/internal/v1/profiles/42", "", "").Body.String(), "Renamed") {
		t.Fatal("unauthenticated read leaked profile data")
	}
}

func decodeJSON(t *testing.T, body []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatalf("body %q is not JSON: %v", body, err)
	}
}
