package integration_test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/melmut-42/ft_transcendence/backend/game-gateway/internal/app"
	"github.com/melmut-42/ft_transcendence/backend/game-gateway/internal/config"
)

const unauthorizedFrame = `{"type":"error","request_id":null,"error":{"code":"UNAUTHORIZED","message":"The session is no longer valid.","details":{}}}`

// --- Admission -----------------------------------------------------------------

func TestHandshakeRefusesAForeignOriginWithABare403BeforeAnythingElse(t *testing.T) {
	h := newHarness(t, true)
	for name, origin := range map[string]string{"foreign": "https://evil.example", "null": "null", "missing": ""} {
		_, resp, err := dial(t, h.public.URL, "1001", "valid-a", origin, nil)
		if err == nil {
			t.Fatalf("%s origin: upgraded", name)
		}
		defer resp.Body.Close()
		body := make([]byte, 64)
		n, _ := resp.Body.Read(body)
		if resp.StatusCode != http.StatusForbidden || n != 0 {
			t.Errorf("%s origin: %d with %d body bytes, want a bare 403", name, resp.StatusCode, n)
		}
	}
	if h.auth.calls.Load() != 0 || h.game.count("PUT", 0, 0) != 0 {
		t.Fatalf("Auth (%d) or Game Session (%d) was asked for a foreign page", h.auth.calls.Load(), h.game.count("PUT", 0, 0))
	}
}

func TestHandshakeAcceptsTheFtSessionCookieOnly(t *testing.T) {
	h := newHarness(t, true)
	for name, extra := range map[string]struct {
		path   string
		header http.Header
	}{
		"no credential":  {},
		"query token":    {path: "?access_token=valid-a&ft_session=valid-a"},
		"bearer header":  {header: http.Header{"Authorization": {"Bearer valid-a"}}},
		"refresh cookie": {header: http.Header{"Cookie": {"ft_refresh=valid-a"}}},
	} {
		header := http.Header{"Origin": {publicOrigin}}
		for k, v := range extra.header {
			header[k] = v
		}
		_, resp, err := websocket.DefaultDialer.Dial(wsURL(h.public.URL, "1001")+extra.path, header)
		if err == nil {
			t.Fatalf("%s: upgraded", name)
		}
		if status, code := handshakeError(t, resp); status != 401 || code != "UNAUTHORIZED" {
			t.Errorf("%s: %d %q, want 401 UNAUTHORIZED", name, status, code)
		}
	}
	if h.auth.calls.Load() != 0 || h.game.count("PUT", 0, 0) != 0 {
		t.Fatal("Auth or Game Session was asked without an ft_session cookie")
	}
}

func TestHandshakeMapsAuthVerdictsAndNeverAsksGameSessionWithoutASession(t *testing.T) {
	h := newHarness(t, true)
	_, resp, _ := dial(t, h.public.URL, "1001", "expired", publicOrigin, nil)
	if status, code := handshakeError(t, resp); status != 401 || code != "UNAUTHORIZED" {
		t.Errorf("invalid session: %d %q", status, code)
	}
	h.auth.mode.Store("outage")
	_, resp, _ = dial(t, h.public.URL, "1001", "valid-a", publicOrigin, nil)
	if status, code := handshakeError(t, resp); status != 503 || code != "SERVICE_UNAVAILABLE" {
		t.Errorf("Auth outage: %d %q, want 503 (never a logout)", status, code)
	}
	h.auth.mode.Store("reject-credential")
	_, resp, _ = dial(t, h.public.URL, "1001", "valid-a", publicOrigin, nil)
	if status, code := handshakeError(t, resp); status != 500 || code != "INTERNAL_ERROR" {
		t.Errorf("gateway credential refused by Auth: %d %q, want 500", status, code)
	}
	if n := h.game.count("PUT", 0, 0); n != 0 {
		t.Fatalf("Game Session was asked %d times for unauthenticated handshakes", n)
	}
}

func TestHandshakeTakesGameSessionsMembershipVerdictBeforeUpgrading(t *testing.T) {
	h := newHarness(t, true)
	cases := []struct {
		name   string
		room   string
		cookie string
		status int
		code   string
	}{
		{"missing room", strconv.Itoa(roomMissing), "valid-a", 404, "ROOM_NOT_FOUND"},
		{"closed room", strconv.Itoa(roomClosed), "valid-a", 409, "INVALID_ROOM_STATE"},
		{"not a member", strconv.Itoa(roomNotMine), "valid-a", 403, "NOT_ROOM_MEMBER"},
		{"member of no room", strconv.Itoa(roomOpen), "valid-d", 403, "NOT_ROOM_MEMBER"},
		{"room id not a number", "abc", "valid-a", 404, "ROOM_NOT_FOUND"},
		{"room id zero", "0", "valid-a", 404, "ROOM_NOT_FOUND"},
	}
	for _, tc := range cases {
		_, resp, err := dial(t, h.public.URL, tc.room, tc.cookie, publicOrigin, nil)
		if err == nil {
			t.Fatalf("%s: upgraded", tc.name)
		}
		if status, code := handshakeError(t, resp); status != tc.status || code != tc.code {
			t.Errorf("%s: %d %q, want %d %s", tc.name, status, code, tc.status, tc.code)
		}
	}
	h.game.set(func(g *gameStub) { g.outage = true })
	_, resp, _ := dial(t, h.public.URL, "1001", "valid-a", publicOrigin, nil)
	if status, code := handshakeError(t, resp); status != 503 || code != "SERVICE_UNAVAILABLE" {
		t.Errorf("Game Session outage: %d %q, want 503, never a membership verdict", status, code)
	}
	// A refused handshake counts no socket, so nothing is reported closed.
	if n := h.game.count("DELETE", 0, 0); n != 0 {
		t.Fatalf("%d disconnects reported for refused handshakes", n)
	}
}

func TestNoSocketIsAcceptedBeforeGameSessionAcknowledgedTheStart(t *testing.T) {
	h := newHarness(t, false)
	h.game.set(func(g *gameStub) { g.resetFailures = 2 })

	_, resp, _ := dial(t, h.public.URL, "1001", "valid-a", publicOrigin, nil)
	if status, code := handshakeError(t, resp); status != 503 || code != "SERVICE_UNAVAILABLE" {
		t.Fatalf("before the start report: %d %q, want 503", status, code)
	}
	if status, _, _ := call(t, http.MethodGet, h.public.URL+"/readyz", "", ""); status != 503 {
		t.Fatalf("readiness before the start report: %d, want 503", status)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.gateway.ReportStarted(ctx); err != nil {
		t.Fatalf("ReportStarted after two failures: %v", err)
	}
	if status, _, _ := call(t, http.MethodGet, h.public.URL+"/readyz", "", ""); status != 200 {
		t.Fatalf("readiness after the start report: %d", status)
	}
	mustDial(t, h.public.URL, roomOpen, "valid-a")
	events := h.game.all()
	if len(events) < 2 || events[0].what != "RESET" || events[1].what != "PUT" {
		t.Fatalf("events %v, want the start report before the first socket", whats(events))
	}
}

// --- Commands --------------------------------------------------------------------

func TestCommandsAreForwardedUnchangedWithTheSessionIdentity(t *testing.T) {
	h := newHarness(t, true)
	sender := mustDial(t, h.public.URL, roomOpen, "valid-a")
	otherTab := mustDial(t, h.public.URL, roomOpen, "valid-a")

	for _, msg := range []string{
		`{"type":"room.role.select","request_id":"req_0007","payload":{"team":"RED","role":"OPERATIVE"}}`,
		`{ "type" : "room.ready.set",   "request_id":"req_0008", "payload":{"ready":true} }`,
		`this is not json {`,
	} {
		send(t, sender, msg)
		reply := readRaw(t, sender)
		ackSeq(t, reply)
		posts := h.game.all()
		last := posts[len(posts)-1]
		if last.what != "POST" || last.body != msg {
			t.Fatalf("Game Session received %q, want %q unchanged", last.body, msg)
		}
		if last.room != roomOpen || last.head.Get("X-User-Id") != "42" || last.head.Get("X-Session-Id") != sessionA {
			t.Fatalf("identity headers room=%d user=%q session=%q", last.room, last.head.Get("X-User-Id"), last.head.Get("X-Session-Id"))
		}
	}
	// The reply went to the sending socket only: the other tab's next frame
	// is the answer to its own message.
	send(t, otherTab, `{"type":"own","request_id":"req_9"}`)
	if got := readRaw(t, otherTab); !strings.Contains(got, `"len":35`) {
		t.Fatalf("the other tab received %s, want only its own reply", got)
	}
}

func TestOneSocketsCommandsAreForwardedOneAtATimeInOrder(t *testing.T) {
	h := newHarness(t, true)
	conn := mustDial(t, h.public.URL, roomOpen, "valid-a")
	send(t, conn, `{"type":"x","request_id":"first","payload":{"slow":true}}`)
	send(t, conn, `{"type":"x","request_id":"second","payload":{}}`)
	first, second := ackSeq(t, readRaw(t, conn)), ackSeq(t, readRaw(t, conn))
	if first >= second {
		t.Fatalf("replies out of order: %d then %d", first, second)
	}
	var posts []gameEvent
	for _, e := range h.game.all() {
		if e.what == "POST" {
			posts = append(posts, e)
		}
	}
	if len(posts) != 2 || !strings.Contains(posts[0].body, "first") || posts[1].start.Before(posts[0].end) {
		t.Fatalf("the second command was sent before the first one was answered")
	}
}

func TestACommandIsRetriedAfterAnOutageWithTheIdenticalBody(t *testing.T) {
	h := newHarness(t, true)
	conn := mustDial(t, h.public.URL, roomOpen, "valid-a")
	msg := `{"type":"x","request_id":"r1","payload":{"flaky":true}}`
	send(t, conn, msg)
	ackSeq(t, readRaw(t, conn))
	n := 0
	for _, e := range h.game.all() {
		if e.what == "POST" {
			if e.body != msg {
				t.Fatalf("a retry changed the body: %q", e.body)
			}
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d attempts, want 2 (one 503, one success)", n)
	}
}

func TestAClosureWritesTheReplyThenClosesEveryRoomSocketOfThatMember(t *testing.T) {
	h := newHarness(t, true)
	sender := mustDial(t, h.public.URL, roomOpen, "valid-a")
	otherTab := mustDial(t, h.public.URL, roomOpen, "valid-b")
	otherMember := mustDial(t, h.public.URL, roomOpen, "valid-c")

	send(t, sender, `{"type":"room.return","request_id":"r","payload":{"kick-me":true}}`)
	if got := readRaw(t, sender); !strings.Contains(got, `"code":"NOT_ROOM_MEMBER"`) {
		t.Fatalf("the error reply was not written before the close: %s", got)
	}
	if code, reason := expectClose(t, sender); code != 4403 || reason != "NOT_ROOM_MEMBER" {
		t.Fatalf("sender close %d %q, want 4403 NOT_ROOM_MEMBER", code, reason)
	}
	if code, _ := expectClose(t, otherTab); code != 4403 {
		t.Fatalf("the member's other room socket closed with %d, want 4403", code)
	}
	send(t, otherMember, `{"type":"still-here"}`)
	ackSeq(t, readRaw(t, otherMember))
	waitFor(t, func() bool { return h.game.count("DELETE", roomOpen, 42) == 1 }, "user 42's last-socket report")
	if n := h.game.count("DELETE", roomOpen, 43); n != 0 {
		t.Fatalf("user 43 was reported disconnected %d times", n)
	}
}

func TestAClosureForAnotherMemberLeavesTheSenderOpen(t *testing.T) {
	h := newHarness(t, true)
	sender := mustDial(t, h.public.URL, roomOpen, "valid-a")
	target := mustDial(t, h.public.URL, roomOpen, "valid-c")
	send(t, sender, `{"type":"x","payload":{"close-other":43}}`)
	if got := readRaw(t, sender); !strings.Contains(got, `"type":"ack"`) {
		t.Fatalf("sender got %s, want the ack", got)
	}
	if code, reason := expectClose(t, target); code != 4403 || reason != "KICKED_FROM_ROOM" {
		t.Fatalf("target close %d %q", code, reason)
	}
	send(t, sender, `{"type":"after"}`)
	ackSeq(t, readRaw(t, sender))
}

func TestEmptyBinaryAndOversizedMessages(t *testing.T) {
	h := newHarness(t, true)
	conn := mustDial(t, h.public.URL, roomOpen, "valid-a")
	send(t, conn, "")
	send(t, conn, `{"type":"after-empty"}`)
	ackSeq(t, readRaw(t, conn))
	if n := h.game.count("POST", 0, 0); n != 1 {
		t.Fatalf("%d commands forwarded, want 1 (the empty message is ignored)", n)
	}

	if err := conn.WriteMessage(websocket.BinaryMessage, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if code, _ := expectClose(t, conn); code != websocket.CloseUnsupportedData {
		t.Fatalf("binary message: close %d, want 1003", code)
	}

	big := mustDial(t, h.public.URL, roomOpen, "valid-c")
	send(t, big, `{"pad":"`+strings.Repeat("x", 65<<10)+`"}`)
	if code, _ := expectClose(t, big); code != websocket.CloseMessageTooBig {
		t.Fatalf("oversized message: close %d, want 1009", code)
	}
	if n := h.game.count("POST", 0, 0); n != 1 {
		t.Fatalf("an oversized or binary message was forwarded (%d commands)", n)
	}
}

// --- Connectivity reports ------------------------------------------------------------

func TestOnlyTheMembersLastRoomSocketIsReportedClosed(t *testing.T) {
	h := newHarness(t, true)
	tab1 := mustDial(t, h.public.URL, roomOpen, "valid-a")
	tab2 := mustDial(t, h.public.URL, roomOpen, "valid-b")
	if n := h.game.count("PUT", roomOpen, 42); n != 2 {
		t.Fatalf("%d socket-opened reports, want one per handshake", n)
	}
	_ = tab1.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(1000, ""), time.Now().Add(time.Second))
	expectClose(t, tab1)
	time.Sleep(200 * time.Millisecond)
	if n := h.game.count("DELETE", roomOpen, 42); n != 0 {
		t.Fatalf("closing one of two tabs reported the member disconnected")
	}
	if got := connectedIDs(t, h, 42); got != "[42]" {
		t.Fatalf("presence %s, want [42]", got)
	}
	_ = tab2.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(1000, ""), time.Now().Add(time.Second))
	expectClose(t, tab2)
	waitFor(t, func() bool { return h.game.count("DELETE", roomOpen, 42) == 1 }, "the last-socket report")
	waitFor(t, func() bool { return h.gateway.Tracker.Members() == 0 }, "the tracker to forget the member")
	if got := connectedIDs(t, h, 42); got != "[]" {
		t.Fatalf("presence %s after the last socket closed", got)
	}
}

func TestAFailedUpgradeAfterAdmissionIsReportedClosed(t *testing.T) {
	h := newHarness(t, true)
	// An upgrade request without Sec-WebSocket-Key passes admission, then the
	// WebSocket library refuses it.
	conn, err := net.Dial("tcp", strings.TrimPrefix(h.public.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET /ws/v2/rooms/1001 HTTP/1.1\r\nHost: x\r\nOrigin: %s\r\nCookie: ft_session=valid-a\r\n"+
		"Connection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\n\r\n", publicOrigin)
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", resp.StatusCode)
	}
	waitFor(t, func() bool {
		return h.game.count("PUT", roomOpen, 42) == 1 && h.game.count("DELETE", roomOpen, 42) == 1
	},
		"a socket-opened report followed by a last-socket report")
}

func TestALateDisconnectReportCannotOvertakeANewerConnection(t *testing.T) {
	h := newHarness(t, true)
	h.game.set(func(g *gameStub) { g.disconnectDelay = 300 * time.Millisecond })
	first := mustDial(t, h.public.URL, roomOpen, "valid-a")
	_ = first.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(1000, ""), time.Now().Add(time.Second))
	expectClose(t, first)
	time.Sleep(50 * time.Millisecond) // the slow last-socket report is in flight
	mustDial(t, h.public.URL, roomOpen, "valid-a")

	var del, put gameEvent
	for _, e := range h.game.all() {
		switch {
		case e.what == "DELETE" && e.user == 42:
			del = e
		case e.what == "PUT" && e.user == 42:
			put = e
		}
	}
	if del.what == "" || put.start.Before(del.end) {
		t.Fatalf("the reconnect's socket-opened report started before the earlier disconnect was answered")
	}
}

// --- Revocation and presence ---------------------------------------------------------

func TestRevocationClosesEveryRoomSocketOfThatSessionOnly(t *testing.T) {
	h := newHarness(t, true)
	tab1 := mustDial(t, h.public.URL, roomOpen, "valid-a")
	tab2 := mustDial(t, h.public.URL, roomOpen, "valid-a")
	otherSession := mustDial(t, h.public.URL, roomOpen, "valid-b")

	url := h.internal.URL + "/internal/v1/session-revocations/" + sessionA
	if status, _, body := call(t, http.MethodPut, url, authToGateway, `{"user_id":42,"cause":"SESSION_REVOKED"}`); status != 204 {
		t.Fatalf("revocation: %d %v", status, body)
	}
	for name, c := range map[string]*websocket.Conn{"tab1": tab1, "tab2": tab2} {
		if got := readRaw(t, c); got != unauthorizedFrame {
			t.Errorf("%s got %s before the close", name, got)
		}
		if code, reason := expectClose(t, c); code != 4401 || reason != "SESSION_REVOKED" {
			t.Errorf("%s close %d %q, want 4401 SESSION_REVOKED", name, code, reason)
		}
	}
	send(t, otherSession, `{"type":"still-open"}`)
	ackSeq(t, readRaw(t, otherSession))
	if status, _, _ := call(t, http.MethodPut, url, authToGateway, `{"user_id":42,"cause":"SESSION_REVOKED"}`); status != 204 {
		t.Fatalf("duplicate revocation: %d", status)
	}
	late := mustDial(t, h.public.URL, roomOpen, "valid-a")
	if got := readRaw(t, late); got != unauthorizedFrame {
		t.Fatalf("a handshake validated before the revocation got %s", got)
	}
	if code, _ := expectClose(t, late); code != 4401 {
		t.Fatalf("late handshake close %d", code)
	}
	if status, _, body := call(t, http.MethodPut, url, userToGateway, `{"user_id":42,"cause":"SESSION_REVOKED"}`); status != 403 || errorCode(body) != "SERVICE_FORBIDDEN" {
		t.Errorf("user-service revoking: %d %v", status, body)
	}
	if status, _, body := call(t, http.MethodPut, h.internal.URL+"/internal/v1/session-revocations/nope", authToGateway, `{"user_id":42,"cause":"SESSION_REVOKED"}`); status != 422 || errorField(body) != "session_id" {
		t.Errorf("bad session id: %d %v", status, body)
	}
	if status, _, body := call(t, http.MethodPut, url, authToGateway, `{"user_id":42,"cause":"LOGOUT"}`); status != 422 || errorField(body) != "cause" {
		t.Errorf("bad cause: %d %v", status, body)
	}
}

func TestConnectionsReadReportsMembersWithARoomSocketOnThisInstance(t *testing.T) {
	h := newHarness(t, true)
	if got := connectedIDs(t, h, 42, 43); got != "[]" {
		t.Fatalf("empty gateway reported %s", got)
	}
	mustDial(t, h.public.URL, roomOpen, "valid-c")
	mustDial(t, h.public.URL, roomOpen, "valid-a")
	if got := connectedIDs(t, h, 99, 43, 42, 43); got != "[42 43]" {
		t.Fatalf("connections read %s, want [42 43]", got)
	}
	for name, tc := range map[string]struct {
		query      string
		credential string
		status     int
	}{
		"no credential":   {"?user_id=1", "", 401},
		"auth-service":    {"?user_id=1", authToGateway, 403},
		"missing user_id": {"", userToGateway, 422},
		"not a number":    {"?user_id=x", userToGateway, 422},
	} {
		if status, _, _ := call(t, http.MethodGet, h.internal.URL+"/internal/v1/connections"+tc.query, tc.credential, ""); status != tc.status {
			t.Errorf("%s: %d, want %d", name, status, tc.status)
		}
	}
}

func TestPublicAndPrivateRoutesLiveOnSeparateListeners(t *testing.T) {
	h := newHarness(t, true)
	if status, _, body := call(t, http.MethodGet, h.public.URL+"/internal/v1/connections?user_id=1", userToGateway, ""); status != 404 || errorCode(body) != "NOT_FOUND" {
		t.Errorf("public listener served /internal: %d", status)
	}
	if status, _, _ := call(t, http.MethodGet, h.internal.URL+"/ws/v2/rooms/1001", "", ""); status != 404 {
		t.Errorf("private listener served the socket route: %d", status)
	}
}

// --- Lifecycle and configuration --------------------------------------------------------

func TestShutdownClosesSocketsReportsThemAndARestartReportsTheStartAgain(t *testing.T) {
	auth, game := newAuthStub(t), newGameStub(t)
	start := func() (*app.Gateway, string, context.CancelFunc, chan error) {
		publicPort, internalPort := freePort(t), freePort(t)
		cfg, err := config.Load(lookup(env(t, auth.server.URL, game.server.URL, map[string]string{
			"PORT": strconv.Itoa(publicPort), "INTERNAL_HOST": "127.0.0.1", "INTERNAL_PORT": strconv.Itoa(internalPort),
			"SHUTDOWN_TIMEOUT": "2s",
		})))
		if err != nil {
			t.Fatal(err)
		}
		g, err := app.New(cfg, nil, app.Dependencies{ResetBackoff: 20 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- g.Run(ctx) }()
		publicURL := fmt.Sprintf("http://127.0.0.1:%d", publicPort)
		waitFor(t, func() bool {
			resp, err := http.Get(publicURL + "/readyz")
			if err != nil {
				return false
			}
			resp.Body.Close()
			return resp.StatusCode == 200
		}, "the gateway to be ready")

		return g, publicURL, cancel, done
	}

	_, publicURL, cancel, done := start()
	conn := mustDial(t, publicURL, roomOpen, "valid-a")
	cancel()
	if code, reason := expectClose(t, conn); code != websocket.CloseGoingAway || reason != "GOING_AWAY" {
		t.Fatalf("close %d %q, want 1001 GOING_AWAY", code, reason)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return")
	}
	if n := game.count("DELETE", roomOpen, 42); n != 1 {
		t.Fatalf("shutdown reported %d disconnects, want 1", n)
	}

	_, _, cancel2, done2 := start()
	cancel2()
	<-done2
	if n := game.count("RESET", 0, 0); n != 2 {
		t.Fatalf("%d start reports, want one per start", n)
	}
}

func TestConfigReportsEveryProblemByNameWithoutValues(t *testing.T) {
	values := env(t, "http://auth.internal:8081", "http://game.internal:8081", map[string]string{
		"PUBLIC_BASE_URL":       "",
		"GAME_INTERNAL_URL":     "ftp://secret-host.example/x",
		"GAME_INTERNAL_TIMEOUT": "",
		"INTERNAL_HOST":         "host:9999",
	})
	_, err := config.Load(lookup(values))
	if err == nil {
		t.Fatal("accepted an invalid configuration")
	}
	for _, name := range []string{"PUBLIC_BASE_URL", "GAME_INTERNAL_URL", "GAME_INTERNAL_TIMEOUT", "INTERNAL_HOST"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %s: %v", name, err)
		}
	}
	for _, leaked := range []string{"secret-host", "host:9999", gatewayToGame} {
		if strings.Contains(err.Error(), leaked) {
			t.Errorf("error leaks %q", leaked)
		}
	}
}

func whats(events []gameEvent) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.what)
	}

	return out
}
