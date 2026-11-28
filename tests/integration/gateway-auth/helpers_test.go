package gatewayauth_test

// Helpers for the gateway/Auth proof. Everything goes through the real ingress
// with real cookies against the running stack (`make up`); nothing is mocked.

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

var (
	base     = env("BASE", "https://localhost:8443")
	wsBase   = "wss" + strings.TrimPrefix(base, "https")
	repoRoot = func() string { p, _ := filepath.Abs("../../.."); return p }()
	insecure = &tls.Config{InsecureSkipVerify: true} // the local certificate is self-signed
	client   = &http.Client{
		Timeout:       15 * time.Second,
		Transport:     &http.Transport{TLSClientConfig: insecure},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	passwordFor = "CorrectHorse9!Battery"
)

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}

	return fallback
}

// session is one logged-in browser session: the two cookies the backend issues.
type session struct {
	userID  int64
	name    string
	session string
	refresh string
}

func (s session) cookie() string { return "ft_session=" + s.session }

// api sends one REST request through the ingress and returns status, body and cookies.
func api(method, path, cookie string, body any, header map[string]string) (int, string, map[string]string) {
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, base+path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err.Error(), nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	cookies := map[string]string{}
	for _, c := range resp.Cookies() {
		cookies[c.Name] = c.Value
	}

	return resp.StatusCode, string(raw), cookies
}

var userIDPattern = regexp.MustCompile(`"user_id":(\d+)`)

func newUser(t *testing.T, tag string) session {
	t.Helper()
	name := fmt.Sprintf("gw_%s_%d", tag, rand.Intn(900000)+100000)
	status, body, cookies := api("POST", "/api/v1/auth/register", "", map[string]string{
		"username": name, "email": name + "@example.com", "password": passwordFor,
	}, nil)
	m := userIDPattern.FindStringSubmatch(body)
	if status != 201 || m == nil {
		t.Fatalf("register %s: %d %s", name, status, body)
	}
	id, _ := strconv.ParseInt(m[1], 10, 64)

	return session{userID: id, name: name, session: cookies["ft_session"], refresh: cookies["ft_refresh"]}
}

// login opens another, independent session of an existing account (a second browser).
func login(t *testing.T, s session) session {
	t.Helper()
	status, body, cookies := api("POST", "/api/v1/auth/login", "", map[string]string{
		"email": s.name + "@example.com", "password": passwordFor,
	}, nil)
	if status != 200 {
		t.Fatalf("login %s: %d %s", s.name, status, body)
	}

	return session{userID: s.userID, name: s.name, session: cookies["ft_session"], refresh: cookies["ft_refresh"]}
}

// gameREST calls Game Session's REST v2 the way a browser does: through the
// ingress and the Web BFF, with the user's session cookie. The BFF checks the
// session and sets the caller; the test never sends an identity header itself.
func gameREST(t *testing.T, who session, path, body string) (int, string) {
	t.Helper()
	status, out, _ := api("POST", path, who.cookie(), json.RawMessage(body), nil)

	return status, out
}

func compose(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("docker", append([]string{"compose"}, args...)...)
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker compose %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// --- WebSocket -----------------------------------------------------------------

type sock struct {
	conn   *websocket.Conn
	frames chan string
	pongs  chan struct{}
	done   chan struct{}
	err    error
}

// dial opens a socket through the ingress. On a refused handshake it returns the
// HTTP status and the error code of the body instead of a socket.
func dial(path, cookie, origin string, extra http.Header) (*sock, int, string) {
	header := http.Header{}
	if cookie != "" {
		header.Set("Cookie", cookie)
	}
	if origin != "" {
		header.Set("Origin", origin)
	}
	for k, v := range extra {
		header[k] = v
	}
	dialer := websocket.Dialer{TLSClientConfig: insecure, HandshakeTimeout: 10 * time.Second}
	conn, resp, err := dialer.Dial(wsBase+path, header)
	if err != nil {
		status, code := 0, ""
		if resp != nil {
			status = resp.StatusCode
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if m := regexp.MustCompile(`"code":"([A-Z_]+)"`).FindStringSubmatch(string(raw)); m != nil {
				code = m[1]
			}
		}

		return nil, status, code
	}
	s := &sock{conn: conn, frames: make(chan string, 64), pongs: make(chan struct{}, 8), done: make(chan struct{})}
	conn.SetPongHandler(func(string) error {
		select {
		case s.pongs <- struct{}{}:
		default:
		}

		return nil
	})
	go func() {
		defer close(s.done)
		for {
			kind, data, err := conn.ReadMessage()
			if err != nil {
				s.err = err

				return
			}
			if kind == websocket.TextMessage {
				s.frames <- string(data)
			}
		}
	}()

	return s, 101, ""
}

func (s *sock) frame(t *testing.T, what string) string {
	t.Helper()
	select {
	case f := <-s.frames:
		return f
	case <-s.done:
		// A frame and the close that follows it arrive together: the frame wins.
		select {
		case f := <-s.frames:
			return f
		default:
		}
		t.Fatalf("%s: socket closed (%v) while waiting for a frame", what, s.err)
	case <-time.After(6 * time.Second):
		t.Fatalf("%s: no frame within 6s", what)
	}

	return ""
}

// closed waits for the server to close the socket and returns its code and reason.
func (s *sock) closed(t *testing.T, what string) (int, string) {
	t.Helper()
	select {
	case <-s.done:
		var ce *websocket.CloseError
		if asClose(s.err, &ce) {
			return ce.Code, ce.Text
		}
		t.Fatalf("%s: connection ended without a close frame: %v", what, s.err)
	case <-time.After(8 * time.Second):
		t.Fatalf("%s: still open 8s after the revocation", what)
	}

	return 0, ""
}

func asClose(err error, target **websocket.CloseError) bool {
	ce, ok := err.(*websocket.CloseError)
	if ok {
		*target = ce
	}

	return ok
}

// stillOpen proves the socket is alive: the server answers a ping.
func (s *sock) stillOpen(t *testing.T, what string) {
	t.Helper()
	if err := s.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(3*time.Second)); err != nil {
		t.Fatalf("%s: ping failed: %v", what, err)
	}
	select {
	case <-s.pongs:
	case <-s.done:
		t.Fatalf("%s: closed by the server: %v", what, s.err)
	case <-time.After(4 * time.Second):
		t.Fatalf("%s: no pong", what)
	}
}

// command sends a room command and returns the reply Game Session produced.
func (s *sock) command(t *testing.T, id, typ, payload string) string {
	t.Helper()
	msg := fmt.Sprintf(`{"type":%q,"request_id":%q,"payload":%s}`, typ, id, payload)
	if err := s.conn.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
		t.Fatalf("send %s: %v", typ, err)
	}

	return s.frame(t, "reply to "+typ)
}

func (s *sock) close() { _ = s.conn.Close() }

// --- Forged tokens ---------------------------------------------------------------

// forge re-signs a real session token with the stack's real signing key after
// changing its claims. With no change it must still be accepted, which proves the
// key and the format are right, so a rejection of a changed one means something.
func forge(t *testing.T, token string, mutate func(map[string]any), key string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %d parts", len(parts))
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	mutate(claims)
	payload, _ := json.Marshal(claims)
	signing := parts[0] + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(signing))

	return signing + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func signingKey(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot, "secrets", "jwt_secret"))
	if err != nil {
		t.Fatalf("read the stack's JWT secret (run `make up` first): %v", err)
	}

	return strings.TrimSpace(string(raw))
}
