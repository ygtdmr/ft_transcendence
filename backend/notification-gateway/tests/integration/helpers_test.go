package integration_test

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/melmut-42/ft_transcendence/backend/notification-gateway/internal/app"
	"github.com/melmut-42/ft_transcendence/backend/notification-gateway/internal/config"
)

const (
	publicOrigin = "https://play.example.test"

	sessionA = "3f9b2c71-5d4e-4a86-9c1b-7e2a6d0f8b53" // user 42, first session
	sessionB = "a1b2c3d4-0000-4000-8000-000000000002" // user 42, second session
	sessionC = "a1b2c3d4-0000-4000-8000-000000000003" // user 43
)

var fixedNow = time.Date(2026, 9, 10, 8, 40, 0, 0, time.UTC)

// credentials are 32+ byte values written to secret files, as in production.
var (
	gatewayToAuth = strings.Repeat("g", 40)
	userToGateway = strings.Repeat("u", 40)
	authToGateway = strings.Repeat("a", 40)
)

// authStub is a stand-in Auth Service serving GET /internal/v1/sessions/current.
type authStub struct {
	server *httptest.Server
	calls  atomic.Int64

	mu          sync.Mutex
	lastCookies []string
	lastBearer  string
	mode        string // "", "outage", "reject-credential"
}

func newAuthStub(t *testing.T) *authStub {
	t.Helper()
	a := &authStub{}
	a.server = httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(a.server.Close)

	return a
}

func (a *authStub) setMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = mode
}

func (a *authStub) seen() (cookies []string, bearer string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.lastCookies, a.lastBearer
}

func (a *authStub) serve(w http.ResponseWriter, r *http.Request) {
	a.calls.Add(1)
	a.mu.Lock()
	a.lastCookies = r.Header.Values("Cookie")
	a.lastBearer = r.Header.Get("Authorization")
	mode := a.mode
	a.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path != "/internal/v1/sessions/current" || r.Method != http.MethodGet:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"NOT_FOUND","message":"x","details":{}}}`)
	case mode == "reject-credential" || r.Header.Get("Authorization") != "Bearer "+gatewayToAuth:
		w.Header().Set("WWW-Authenticate", "Bearer")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"code":"SERVICE_UNAUTHENTICATED","message":"x","details":{}}}`)
	case mode == "outage":
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"code":"SERVICE_UNAVAILABLE","message":"x","details":{}}}`)
	default:
		cookie, err := r.Cookie("ft_session")
		user, session := int64(0), ""
		if err == nil {
			switch cookie.Value {
			case "valid-a":
				user, session = 42, sessionA
			case "valid-b":
				user, session = 42, sessionB
			case "valid-c":
				user, session = 43, sessionC
			}
		}
		if user == 0 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"code":"UNAUTHORIZED","message":"x","details":{}}}`)

			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"user_id": user, "session_id": session, "session_expires_at": "2026-09-10T09:40:00Z",
		}})
	}
}

// env builds a valid environment with credential files in a temp dir.
func env(t *testing.T, authURL string, overrides map[string]string) map[string]string {
	t.Helper()
	dir := t.TempDir()
	file := func(name, value string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(value+"\n"), 0o400); err != nil {
			t.Fatal(err)
		}

		return path
	}
	values := map[string]string{
		"APP_ENV":                               "test",
		"PUBLIC_BASE_URL":                       publicOrigin,
		"AUTH_INTERNAL_URL":                     authURL,
		"AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE": file("gateway-to-auth", gatewayToAuth),
		"AUTH_INTERNAL_TIMEOUT":                 "2s",
		"USER_SERVICE_CREDENTIAL_FILE":          file("user-to-gateway", userToGateway),
		"AUTH_SERVICE_CREDENTIAL_FILE":          file("auth-to-gateway", authToGateway),
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

// harness runs one gateway instance behind httptest servers.
type harness struct {
	gateway  *app.Gateway
	public   *httptest.Server
	internal *httptest.Server
	auth     *authStub
}

func newHarness(t *testing.T, overrides map[string]string) *harness {
	t.Helper()
	auth := newAuthStub(t)
	cfg, err := config.Load(lookup(env(t, auth.server.URL, overrides)))
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	g, err := app.New(cfg, nil, app.Dependencies{Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	h := &harness{gateway: g, public: httptest.NewServer(g.Public.Handler),
		internal: httptest.NewServer(g.Internal.Handler), auth: auth}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := g.CloseSockets(ctx); err != nil {
			t.Errorf("socket goroutines did not finish: %v", err)
		}
		h.public.Close()
		h.internal.Close()
	})

	return h
}

func wsURL(base string) string {
	return "ws" + strings.TrimPrefix(base, "http") + "/ws/v2/notifications"
}

// dial opens a notification socket. cookie "" sends none; origin "" sends none.
func dial(t *testing.T, base, cookie, origin string, extra http.Header) (*websocket.Conn, *http.Response, error) {
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
	conn, resp, err := dialer.Dial(wsURL(base), header)
	if conn != nil {
		t.Cleanup(func() { _ = conn.Close() })
	}

	return conn, resp, err
}

func mustDial(t *testing.T, base, cookie string) *websocket.Conn {
	t.Helper()
	conn, resp, err := dial(t, base, cookie, publicOrigin, nil)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("dial %s: %v (status %d)", cookie, err, status)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status %d, want 101", resp.StatusCode)
	}

	return conn
}

type frame struct {
	Type    string          `json:"type"`
	SentAt  string          `json:"sent_at"`
	Payload json.RawMessage `json:"payload"`
}

func readFrame(t *testing.T, conn *websocket.Conn) frame {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var f frame
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("frame %q is not JSON: %v", data, err)
	}

	return f
}

// readRaw returns the next text message exactly as sent.
func readRaw(t *testing.T, conn *websocket.Conn) string {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	return string(data)
}

// expectClose reads until the server closes and returns the close code and text.
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

// stillOpen proves a socket is alive: a client frame is answered with
// INVALID_EVENT.
func stillOpen(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := readRaw(t, conn); got != `{"type":"error","payload":{"code":"INVALID_EVENT"}}` {
		t.Fatalf("got %s, want INVALID_EVENT", got)
	}
}

// call sends a private request with the given caller credential ("" for none).
func call(t *testing.T, method, url, credential, body string) (int, http.Header, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
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
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("%s %s answered non-JSON %q", method, url, raw)
		}
	}

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

func connectedIDs(t *testing.T, h *harness, ids ...int64) []int64 {
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
	raw, ok := data["connected_user_ids"].([]any)
	if !ok {
		t.Fatalf("connected_user_ids missing or not an array: %v", body)
	}
	out := make([]int64, 0, len(raw))
	for _, v := range raw {
		out = append(out, int64(v.(float64)))
	}

	return out
}

// freePort reserves a TCP port number on 127.0.0.1.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	return ln.Addr().(*net.TCPAddr).Port
}
