package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/melmut-42/ft_transcendence/backend/game-gateway/internal/app"
	"github.com/melmut-42/ft_transcendence/backend/game-gateway/internal/config"
)

const (
	publicOrigin = "https://play.example.test"

	sessionA = "3f9b2c71-5d4e-4a86-9c1b-7e2a6d0f8b53" // user 42, first session
	sessionB = "a1b2c3d4-0000-4000-8000-000000000002" // user 42, second session
	sessionC = "a1b2c3d4-0000-4000-8000-000000000003" // user 43
	sessionD = "a1b2c3d4-0000-4000-8000-000000000004" // user 99, member of no room

	roomOpen    = 1001   // members 42 and 43
	roomMissing = 404001 // ROOM_NOT_FOUND
	roomClosed  = 409001 // INVALID_ROOM_STATE (CLOSED)
	roomNotMine = 1002   // member 43 only
)

var (
	gatewayToAuth = strings.Repeat("g", 40)
	gatewayToGame = strings.Repeat("s", 40)
	userToGateway = strings.Repeat("u", 40)
	authToGateway = strings.Repeat("a", 40)
)

// --- Auth stub ---------------------------------------------------------------

type authStub struct {
	server *httptest.Server
	calls  atomic.Int64
	mode   atomic.Value // "", "outage", "reject-credential"
}

func newAuthStub(t *testing.T) *authStub {
	t.Helper()
	a := &authStub{}
	a.mode.Store("")
	a.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		mode := a.mode.Load().(string)
		switch {
		case mode == "reject-credential" || r.Header.Get("Authorization") != "Bearer "+gatewayToAuth:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"code":"SERVICE_UNAUTHENTICATED","message":"x","details":{}}}`)
		case mode == "outage":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"code":"SERVICE_UNAVAILABLE","message":"x","details":{}}}`)
		default:
			users := map[string][2]any{"valid-a": {42, sessionA}, "valid-b": {42, sessionB}, "valid-c": {43, sessionC},
				"valid-d": {99, sessionD}}
			cookie, err := r.Cookie("ft_session")
			if err != nil {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"error":{"code":"UNAUTHORIZED","message":"x","details":{}}}`)
				return
			}
			u, ok := users[cookie.Value]
			if !ok {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"error":{"code":"UNAUTHORIZED","message":"x","details":{}}}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"user_id": u[0], "session_id": strings.ToUpper(u[1].(string)), "session_expires_at": "2026-09-10T09:40:00Z"}})
		}
	}))
	t.Cleanup(a.server.Close)

	return a
}

// --- Game Session stub (room.socket contract) ---------------------------------

type gameEvent struct {
	what  string // "PUT", "DELETE", "RESET", "POST"
	room  int64
	user  int64
	start time.Time
	end   time.Time
	body  string
	head  http.Header
}

type gameStub struct {
	server *httptest.Server

	mu              sync.Mutex
	events          []gameEvent
	outage          bool
	resetFailures   int
	disconnectDelay time.Duration
	flakySeen       map[string]bool
	seq             int
}

func newGameStub(t *testing.T) *gameStub {
	t.Helper()
	g := &gameStub{flakySeen: map[string]bool{}}
	g.server = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.server.Close)

	return g
}

func (g *gameStub) set(f func(g *gameStub)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f(g)
}

func (g *gameStub) record(e gameEvent) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.events = append(g.events, e)
}

func (g *gameStub) all() []gameEvent {
	g.mu.Lock()
	defer g.mu.Unlock()

	return append([]gameEvent(nil), g.events...)
}

// count returns how many recorded events match what, room and user (0 = any).
func (g *gameStub) count(what string, room, user int64) int {
	n := 0
	for _, e := range g.all() {
		if e.what == what && (room == 0 || e.room == room) && (user == 0 || e.user == user) {
			n++
		}
	}

	return n
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func apiError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, `{"error":{"code":"`+code+`","message":"x","details":{}}}`)
}

func (g *gameStub) serve(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if r.Header.Get("Authorization") != "Bearer "+gatewayToGame {
		apiError(w, http.StatusUnauthorized, "SERVICE_UNAUTHENTICATED")
		return
	}
	g.mu.Lock()
	outage, resetFailures, delay := g.outage, g.resetFailures, g.disconnectDelay
	g.mu.Unlock()
	if outage {
		apiError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE")
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/internal/v1/"), "/")
	switch {
	case r.Method == http.MethodDelete && r.URL.Path == "/internal/v1/room-connections":
		if resetFailures > 0 {
			g.set(func(g *gameStub) { g.resetFailures-- })
			apiError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE")
			return
		}
		g.record(gameEvent{what: "RESET", start: start, end: time.Now()})
		w.WriteHeader(http.StatusNoContent)
	case len(parts) == 4 && parts[0] == "rooms" && parts[2] == "connections":
		room, _ := strconv.ParseInt(parts[1], 10, 64)
		user, _ := strconv.ParseInt(parts[3], 10, 64)
		if r.Method == http.MethodDelete && delay > 0 {
			time.Sleep(delay)
		}
		switch {
		case room == roomMissing:
			apiError(w, http.StatusNotFound, "ROOM_NOT_FOUND")
			return
		case room == roomClosed:
			apiError(w, http.StatusConflict, "INVALID_ROOM_STATE")
			return
		case !member(room, user):
			apiError(w, http.StatusForbidden, "NOT_ROOM_MEMBER")
			return
		}
		what := "PUT"
		if r.Method == http.MethodDelete {
			what = "DELETE"
		}
		g.record(gameEvent{what: what, room: room, user: user, start: start, end: time.Now()})
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "rooms" && parts[2] == "commands":
		room, _ := strconv.ParseInt(parts[1], 10, 64)
		user, _ := strconv.ParseInt(r.Header.Get("X-User-Id"), 10, 64)
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		g.mu.Lock()
		flaky := strings.Contains(body, "flaky") && !g.flakySeen[body]
		if flaky {
			g.flakySeen[body] = true
		}
		g.seq++
		seq := g.seq
		g.mu.Unlock()
		if strings.Contains(body, "slow") {
			time.Sleep(300 * time.Millisecond)
		}
		g.record(gameEvent{what: "POST", room: room, user: user, start: start, end: time.Now(), body: body, head: r.Header.Clone()})
		switch {
		case flaky:
			apiError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE")
		case strings.Contains(body, "kick-me"):
			writeJSON(w, 200, fmt.Sprintf(`{"data":{"reply":{"type":"error","request_id":"r","error":{"code":"NOT_ROOM_MEMBER","message":"x","details":{}}},"closures":[{"user_id":%d,"code":4403,"reason":"NOT_ROOM_MEMBER"}]}}`, user))
		case strings.Contains(body, "close-other"):
			writeJSON(w, 200, `{"data":{"reply":{"type":"ack","ok":true},"closures":[{"user_id":43,"code":4403,"reason":"KICKED_FROM_ROOM"}]}}`)
		default:
			writeJSON(w, 200, fmt.Sprintf(`{"data":{"reply":{"type":"ack","seq":%d,"len":%d},"closures":[]}}`, seq, len(raw)))
		}
	default:
		apiError(w, http.StatusNotFound, "NOT_FOUND")
	}
}

func member(room, user int64) bool {
	switch room {
	case roomOpen:
		return user == 42 || user == 43
	case roomNotMine:
		return user == 43
	}

	return false
}

// --- Gateway harness -------------------------------------------------------------

func writeSecret(t *testing.T, dir, name, value string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(value+"\n"), 0o400); err != nil {
		t.Fatal(err)
	}

	return path
}

func env(t *testing.T, authURL, gameURL string, overrides map[string]string) map[string]string {
	t.Helper()
	dir := t.TempDir()
	values := map[string]string{
		"APP_ENV":                               "test",
		"PUBLIC_BASE_URL":                       publicOrigin,
		"AUTH_INTERNAL_URL":                     authURL,
		"AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE": writeSecret(t, dir, "gw-auth", gatewayToAuth),
		"AUTH_INTERNAL_TIMEOUT":                 "2s",
		"GAME_INTERNAL_URL":                     gameURL,
		"GAME_SERVICE_OUTBOUND_CREDENTIAL_FILE": writeSecret(t, dir, "gw-game", gatewayToGame),
		"GAME_INTERNAL_TIMEOUT":                 "2s",
		"USER_SERVICE_CREDENTIAL_FILE":          writeSecret(t, dir, "user-gw", userToGateway),
		"AUTH_SERVICE_CREDENTIAL_FILE":          writeSecret(t, dir, "auth-gw", authToGateway),
	}
	for k, v := range overrides {
		if v == "" {
			delete(values, k)
			continue
		}
		values[k] = v
	}

	return values
}

func lookup(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := values[name]
		return v, ok
	}
}

type harness struct {
	gateway  *app.Gateway
	public   *httptest.Server
	internal *httptest.Server
	auth     *authStub
	game     *gameStub
}

// newHarness builds a gateway behind httptest servers. With started, Game
// Session has already acknowledged the start report.
func newHarness(t *testing.T, started bool) *harness {
	t.Helper()
	auth, game := newAuthStub(t), newGameStub(t)
	cfg, err := config.Load(lookup(env(t, auth.server.URL, game.server.URL, nil)))
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	g, err := app.New(cfg, nil, app.Dependencies{ResetBackoff: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	h := &harness{gateway: g, public: httptest.NewServer(g.Public.Handler), internal: httptest.NewServer(g.Internal.Handler),
		auth: auth, game: game}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := g.CloseSockets(ctx); err != nil {
			t.Errorf("socket goroutines did not finish: %v", err)
		}
		h.public.Close()
		h.internal.Close()
	})
	if started {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := g.ReportStarted(ctx); err != nil {
			t.Fatalf("ReportStarted: %v", err)
		}
	}

	return h
}

func wsURL(base string, room string) string {
	return "ws" + strings.TrimPrefix(base, "http") + "/ws/v2/rooms/" + room
}

func dial(t *testing.T, base, room, cookie, origin string, extra http.Header) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	header := http.Header{}
	for k, v := range extra {
		header[k] = v
	}
	if origin != "" {
		header.Set("Origin", origin)
	}
	if cookie != "" {
		header.Add("Cookie", "ft_session="+cookie)
	}
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, resp, err := dialer.Dial(wsURL(base, room), header)
	if conn != nil {
		t.Cleanup(func() { _ = conn.Close() })
	}

	return conn, resp, err
}

func mustDial(t *testing.T, base string, room int64, cookie string) *websocket.Conn {
	t.Helper()
	conn, resp, err := dial(t, base, strconv.FormatInt(room, 10), cookie, publicOrigin, nil)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("dial room %d as %s: %v (status %d)", room, cookie, err, status)
	}

	return conn
}

func handshakeError(t *testing.T, resp *http.Response) (int, string) {
	t.Helper()
	if resp == nil {
		t.Fatal("no handshake response")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)

	return resp.StatusCode, errorCode(body)
}

func send(t *testing.T, conn *websocket.Conn, msg string) {
	t.Helper()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readRaw(t *testing.T, conn *websocket.Conn) string {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	return string(data)
}

func expectClose(t *testing.T, conn *websocket.Conn) (int, string) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, _, err := conn.ReadMessage()
		if err == nil {
			continue
		}
		var ce *websocket.CloseError
		if errors.As(err, &ce) {
			return ce.Code, ce.Text
		}
		t.Fatalf("connection ended without a close frame: %v", err)
	}
}

// ackSeq returns the seq of an ack reply from the stub.
func ackSeq(t *testing.T, reply string) int {
	t.Helper()
	var r struct {
		Type string `json:"type"`
		Seq  int    `json:"seq"`
	}
	if err := json.Unmarshal([]byte(reply), &r); err != nil || r.Type != "ack" {
		t.Fatalf("not an ack: %s", reply)
	}

	return r.Seq
}

func call(t *testing.T, method, url, credential, body string) (int, http.Header, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, url, reader)
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded)

	return resp.StatusCode, resp.Header, decoded
}

func errorCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	code, _ := e["code"].(string)

	return code
}

func errorField(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	details, _ := e["details"].(map[string]any)
	field, _ := details["field"].(string)

	return field
}

func connectedIDs(t *testing.T, h *harness, ids ...int64) string {
	t.Helper()
	q := make([]string, 0, len(ids))
	for _, id := range ids {
		q = append(q, "user_id="+strconv.FormatInt(id, 10))
	}
	status, _, body := call(t, http.MethodGet, h.internal.URL+"/internal/v1/connections?"+strings.Join(q, "&"), userToGateway, "")
	if status != http.StatusOK {
		t.Fatalf("connections read: %d %v", status, body)
	}
	data, _ := body["data"].(map[string]any)

	return fmt.Sprint(data["connected_user_ids"])
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	return ln.Addr().(*net.TCPAddr).Port
}
