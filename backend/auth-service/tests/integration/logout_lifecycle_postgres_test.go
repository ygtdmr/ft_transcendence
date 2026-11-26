package integration_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/client/gameclient"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/client/gatewayclient"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/client/sessionlifecycle"
	repository "github.com/melmut-42/ft_transcendence/backend/auth-service/internal/repository/postgres"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/testutil"
)

const (
	logoutGameCredential         = "auth-to-game-session-credential-0123456789"
	logoutGameGatewayCredential  = "auth-to-game-gateway-credential-0123456789"
	logoutNotificationCredential = "auth-to-notification-gateway-credential-01"
)

// gameSessionStub answers session departures like Game Session: one receipt
// per session id, 201 the first time and 200 on a replay.
type gameSessionStub struct {
	mu          sync.Mutex
	unavailable bool
	departures  map[string]int64
	calls       []string
}

func (g *gameSessionStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	sessionID := strings.TrimPrefix(r.URL.Path, "/internal/v1/session-departures/")
	g.calls = append(g.calls, sessionID)
	if r.Method != http.MethodPut || r.Header.Get("Authorization") != "Bearer "+logoutGameCredential {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if g.unavailable {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	var body struct {
		UserID int64 `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	status := http.StatusOK
	if _, ok := g.departures[sessionID]; !ok {
		g.departures[sessionID], status = body.UserID, http.StatusCreated
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
		"session_id": sessionID, "user_id": g.departures[sessionID], "state": "COMPLETED",
		"completed_at": time.Now().UTC().Format(time.RFC3339Nano),
	}})
}

// gatewayStub records revocation notifications and can fail until told otherwise.
type gatewayStub struct {
	credential string
	failing    atomic.Bool
	mu         sync.Mutex
	received   []string
}

func (g *gatewayStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if r.Method != http.MethodPut || r.Header.Get("Authorization") != "Bearer "+g.credential {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if g.failing.Load() {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	g.mu.Lock()
	g.received = append(g.received, strings.TrimPrefix(r.URL.Path, "/internal/v1/session-revocations/")+" "+string(body))
	g.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (g *gatewayStub) notifications() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.received...)
}

// TestLogoutLifecyclePostgres runs logout through the real Game Session and
// gateway clients against PostgreSQL.
//
// Expected output:
//
//   - Logout records the Game departure for its own session first, then
//     revokes only that session; the user's other session stays valid.
//   - A failing gateway does not fail the committed logout: the revocation
//     stays pending and the retry worker delivers it to every instance.
//   - A repeated logout of the revoked session is UNAUTHORIZED and sends no
//     second departure, so no second leave penalty can follow.
//   - When Game Session stays unavailable, logout is ErrServiceUnavailable and
//     revokes nothing, so the client can retry it.
func TestLogoutLifecyclePostgres(t *testing.T) {
	f := newPostgresSession(t)
	repo := repository.NewAuth(f.pool)

	game := &gameSessionStub{departures: map[string]int64{}}
	gameServer := httptest.NewServer(game)
	defer gameServer.Close()
	gameGateway := &gatewayStub{credential: logoutGameGatewayCredential}
	gameGatewayServer := httptest.NewServer(gameGateway)
	defer gameGatewayServer.Close()
	notification := &gatewayStub{credential: logoutNotificationCredential}
	notification.failing.Store(true)
	notificationServer := httptest.NewServer(notification)
	defer notificationServer.Close()

	departures, err := gameclient.New(gameServer.URL, logoutGameCredential, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	gateways, err := gatewayclient.New([]gatewayclient.Instance{
		{URL: gameGatewayServer.URL, Credential: logoutGameGatewayCredential},
		{URL: notificationServer.URL, Credential: logoutNotificationCredential},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	auth := service.NewAuth(repo, testutil.TokenIssuer(t),
		service.WithSessionLifecycle(sessionlifecycle.Lifecycle{Departures: departures, Gateways: gateways}))

	first := f.original.Credentials
	second := postgresSessionLogin(t, f).Credentials
	identity, err := auth.Authenticate(f.ctx, first.AccessToken)
	if err != nil {
		t.Fatal(err)
	}

	if err := auth.Logout(f.ctx, identity); err != nil {
		t.Fatalf("logout with a failing gateway: %v", err)
	}
	sessionID := strings.ToLower(identity.SessionID)
	if len(game.calls) != 1 || game.calls[0] != sessionID || game.departures[sessionID] != identity.User.ID {
		t.Fatalf("Game departures = %v", game.calls)
	}
	if _, err := auth.Authenticate(f.ctx, first.AccessToken); !errors.Is(err, service.ErrUnauthorized) {
		t.Fatalf("revoked session still authenticates: %v", err)
	}
	if _, err := auth.Authenticate(f.ctx, second.AccessToken); err != nil {
		t.Fatalf("other session was revoked: %v", err)
	}
	// The failed delivery is deferred, so nothing is due right away.
	assertPendingRevocations(t, f, repo, 0)
	assertUndeliveredRevocation(t, f, sessionID, true)

	notification.failing.Store(false)
	if _, err := f.pool.Exec(f.ctx, "UPDATE session_revocations SET next_attempt_at = now() - interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	if err := auth.DeliverPendingRevocations(f.ctx); err != nil {
		t.Fatalf("retry after gateway recovery: %v", err)
	}
	assertUndeliveredRevocation(t, f, sessionID, false)
	want := sessionID + ` {"user_id":` + jsonInt(identity.User.ID) + `,"cause":"SESSION_REVOKED"}`
	for name, gateway := range map[string]*gatewayStub{"game": gameGateway, "notification": notification} {
		got := gateway.notifications()
		if len(got) == 0 || got[len(got)-1] != want {
			t.Fatalf("%s gateway notifications = %v, want %s", name, got, want)
		}
	}

	if err := auth.Logout(f.ctx, identity); !errors.Is(err, service.ErrUnauthorized) {
		t.Fatalf("repeated logout = %v", err)
	}
	if len(game.calls) != 1 {
		t.Fatalf("repeated logout sent another departure: %v", game.calls)
	}

	game.mu.Lock()
	game.unavailable = true
	game.mu.Unlock()
	secondIdentity, err := auth.Authenticate(f.ctx, second.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.Logout(f.ctx, secondIdentity); !errors.Is(err, service.ErrServiceUnavailable) {
		t.Fatalf("logout during a Game outage = %v", err)
	}
	if _, err := auth.Authenticate(f.ctx, second.AccessToken); err != nil {
		t.Fatalf("failed logout revoked the session: %v", err)
	}
	assertNoRevocation(t, f, strings.ToLower(secondIdentity.SessionID))
}

func jsonInt(value int64) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func assertUndeliveredRevocation(t *testing.T, f postgresSessionFixture, sessionID string, undelivered bool) {
	t.Helper()
	var pending bool
	err := f.pool.QueryRow(f.ctx, "SELECT delivered_at IS NULL FROM session_revocations WHERE session_id = $1", sessionID).Scan(&pending)
	if err != nil {
		t.Fatal(err)
	}
	if pending != undelivered {
		t.Fatalf("revocation undelivered = %v, want %v", pending, undelivered)
	}
}

func assertNoRevocation(t *testing.T, f postgresSessionFixture, sessionID string) {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(f.ctx, "SELECT count(*) FROM session_revocations WHERE session_id = $1", sessionID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("a failed logout recorded a revocation")
	}
}
