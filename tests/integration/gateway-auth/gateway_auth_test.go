package gatewayauth_test

// Proof, on the running stack, of what #194 asks: the Game Gateway and the
// Notification Gateway admit only a cookie-authenticated, same-origin, valid
// session (and, for rooms, a member), and a revoked session loses every socket
// and every REST call while unrelated sessions carry on.
//
// Run with `make integration-test` (it restarts Auth first so the in-memory rate
// limits start empty). The tests call a real ingress, real Auth, real gateways,
// the real Web BFF and a real Game Session; nothing is a stand-in. Game Session
// REST is reached the way a browser reaches it, through the ingress and the BFF.

import (
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	notifications = "/ws/v2/notifications"
	roomPrefix    = "/ws/v2/rooms/"
)

func TestMain(m *testing.M) {
	if status, _, _ := api("GET", "/ingress/healthz", "", nil, nil); status != 200 {
		fmt.Fprintln(os.Stderr, "the stack is not running at "+base+": run `make up` first")
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func TestGatewayAndAuthAdmissionAndRevocation(t *testing.T) {
	host := newUser(t, "host")
	member := newUser(t, "mem")
	stranger := newUser(t, "out")

	status, body := gameREST(t, host, "/api/v2/rooms", `{"max_players":4}`)
	m := regexp.MustCompile(`"room_id":(\d+)`).FindStringSubmatch(body)
	if status != 201 || m == nil {
		t.Fatalf("create room: %d %s", status, body)
	}
	room := roomPrefix + m[1]
	if status, body = gameREST(t, member, "/api/v2/rooms/"+m[1]+"/members", `{}`); status != 201 {
		t.Fatalf("join room: %d %s", status, body)
	}

	// --- Admission ----------------------------------------------------------------
	t.Run("admission: only the ft_session cookie counts", func(t *testing.T) {
		for _, path := range []string{notifications, room} {
			for name, h := range map[string]http.Header{
				"Authorization bearer":     {"Authorization": {"Bearer " + host.session}},
				"custom token header":      {"X-Session-Token": {host.session}},
				"custom auth header":       {"X-Auth-Token": {host.session}},
				"ft_refresh as ft_session": nil,
			} {
				cookie := ""
				if name == "ft_refresh as ft_session" {
					cookie = "ft_session=" + host.refresh
				}
				if _, got, _ := dial(path, cookie, base, h); got != 401 {
					t.Errorf("%s via %s: status %d, want 401", path, name, got)
				}
			}
			for _, q := range []string{"ft_session", "access_token", "token"} {
				if _, got, _ := dial(path+"?"+q+"="+host.session, "", base, nil); got != 401 {
					t.Errorf("%s with ?%s=: status %d, want 401", path, q, got)
				}
			}
			if _, got, _ := dial(path, "ft_session=not-a-token", base, nil); got != 401 {
				t.Errorf("%s with a garbage cookie: status %d, want 401", path, got)
			}
			if _, got, _ := dial(path, "", base, nil); got != 401 {
				t.Errorf("%s with no cookie: status %d, want 401", path, got)
			}
		}
		s, got, _ := dial(notifications, host.cookie(), base, nil)
		if s == nil {
			t.Fatalf("notification socket with the cookie: status %d", got)
		}
		defer s.close()
		if first := s.frame(t, "notifications.ready"); !strings.Contains(first, `"notifications.ready"`) {
			t.Errorf("first frame %s, want notifications.ready", first)
		}
	})

	t.Run("admission: the Origin must be this site's own", func(t *testing.T) {
		for name, origin := range map[string]string{
			"a foreign site":        "https://evil.example",
			"the same host, a port": "https://localhost:9999",
			"http instead of https": strings.Replace(base, "https", "http", 1),
			"null":                  "null",
		} {
			_, gotN, codeN := dial(notifications, host.cookie(), origin, nil)
			if gotN != 403 || codeN != "ORIGIN_NOT_ALLOWED" {
				t.Errorf("notification socket from %s: %d %q, want 403 ORIGIN_NOT_ALLOWED", name, gotN, codeN)
			}
			_, gotR, codeR := dial(room, host.cookie(), origin, nil)
			if gotR != 403 || codeR != "" {
				t.Errorf("room socket from %s: %d %q, want a bare 403", name, gotR, codeR)
			}
		}
		// The origin is judged before the cookie is looked at: a foreign site with no
		// cookie still gets 403, not 401.
		if _, got, _ := dial(room, "", "https://evil.example", nil); got != 403 {
			t.Errorf("foreign origin without a cookie: %d, want 403 (origin is checked first)", got)
		}
	})

	t.Run("admission: expired, forged and unknown sessions are refused", func(t *testing.T) {
		key := signingKey(t)
		same := forge(t, host.session, func(map[string]any) {}, key)
		if s, got, _ := dial(notifications, "ft_session="+same, base, nil); s == nil {
			t.Fatalf("re-signing the real token with the real key must still be accepted (%d): the forging is wrong", got)
		} else {
			s.close()
		}
		now := time.Now().Unix()
		for name, token := range map[string]string{
			"expired":                 forge(t, host.session, func(c map[string]any) { c["exp"], c["iat"] = now-60, now-1860 }, key),
			"signed with another key": forge(t, host.session, func(map[string]any) {}, "not-the-real-signing-key-not-the-real"),
			"a session that never existed": forge(t, host.session, func(c map[string]any) {
				c["sid"] = "00000000-0000-4000-8000-000000000000"
			}, key),
			"another user's id": forge(t, host.session, func(c map[string]any) { c["sub"] = strconv.FormatInt(stranger.userID, 10) }, "wrong"),
		} {
			for _, path := range []string{notifications, room} {
				if _, got, _ := dial(path, "ft_session="+token, base, nil); got != 401 {
					t.Errorf("%s with a token that is %s: %d, want 401", path, name, got)
				}
			}
		}
	})

	t.Run("admission: a room socket needs a member and an existing room", func(t *testing.T) {
		s, got, _ := dial(room, host.cookie(), base, nil)
		if s == nil {
			t.Fatalf("the host is a member: status %d", got)
		}
		s.close()
		if _, got, code := dial(room, stranger.cookie(), base, nil); got != 403 || code != "NOT_ROOM_MEMBER" {
			t.Errorf("non-member: %d %q, want 403 NOT_ROOM_MEMBER", got, code)
		}
		if _, got, code := dial(roomPrefix+"987654", host.cookie(), base, nil); got != 404 || code != "ROOM_NOT_FOUND" {
			t.Errorf("unknown room: %d %q, want 404 ROOM_NOT_FOUND", got, code)
		}
	})

	// --- Revocation -----------------------------------------------------------------
	host2 := login(t, host) // the same account in a second browser

	open := func(path string, who session) *sock {
		s, got, code := dial(path, who.cookie(), base, nil)
		if s == nil {
			t.Fatalf("open %s as %s: %d %s", path, who.name, got, code)
		}

		return s
	}
	hostTab1, hostTab2 := open(room, host), open(room, host)
	hostNote := open(notifications, host)
	host2Note := open(notifications, host2)
	memberTab := open(room, member)
	strangerNote := open(notifications, stranger)
	for _, s := range []*sock{hostNote, host2Note, strangerNote} {
		s.frame(t, "notifications.ready")
		defer s.close()
	}
	defer hostTab1.close()
	defer hostTab2.close()
	defer memberTab.close()

	t.Run("revocation: sockets work before it", func(t *testing.T) {
		if reply := hostTab1.command(t, "c1", "room.team.select", `{"team":"RED"}`); !strings.Contains(reply, `"request_id":"c1"`) {
			t.Errorf("Game Session did not answer through the gateway: %s", reply)
		}
		for name, s := range map[string]*sock{"host tab 1": hostTab1, "host tab 2": hostTab2, "host notifications": hostNote,
			"second session notifications": host2Note, "member tab": memberTab, "stranger notifications": strangerNote} {
			s.stillOpen(t, name)
		}
	})

	t.Run("revocation: logging out closes every socket of that session and no other", func(t *testing.T) {
		if status, body, _ := api("DELETE", "/api/v1/auth/session", host.cookie(), nil, map[string]string{"Origin": base}); status != 204 {
			t.Fatalf("logout: %d %s", status, body)
		}
		for name, s := range map[string]*sock{"host tab 1": hostTab1, "host tab 2": hostTab2, "host notifications": hostNote} {
			if first := s.frame(t, name); !strings.Contains(first, `"UNAUTHORIZED"`) {
				t.Errorf("%s: first frame after revocation %s, want an UNAUTHORIZED error", name, first)
			}
			if code, reason := s.closed(t, name); code != 4401 || reason != "SESSION_REVOKED" {
				t.Errorf("%s: closed with %d %q, want 4401 SESSION_REVOKED", name, code, reason)
			}
		}
		host2Note.stillOpen(t, "the same account's other session")
		strangerNote.stillOpen(t, "an unrelated user")
		memberTab.stillOpen(t, "a room member's socket")
		if reply := memberTab.command(t, "m1", "room.team.select", `{"team":"BLUE"}`); !strings.Contains(reply, `"request_id":"m1"`) {
			t.Errorf("the unrelated member's socket stopped answering: %s", reply)
		}
	})

	t.Run("revocation: the revoked session gets nothing new", func(t *testing.T) {
		for _, path := range []string{notifications, room} {
			if _, got, _ := dial(path, host.cookie(), base, nil); got != 401 {
				t.Errorf("handshake on %s with the revoked session: %d, want 401", path, got)
			}
		}
		for _, path := range []string{"/api/v1/auth/session", "/api/v1/users/me", "/api/v1/users/me/matches"} {
			if got, _, _ := api("GET", path, host.cookie(), nil, nil); got != 401 {
				t.Errorf("GET %s with the revoked session: %d, want 401", path, got)
			}
		}
		if got, body, _ := api("POST", "/api/v1/auth/refresh", "ft_refresh="+host.refresh, nil, map[string]string{"Origin": base}); got != 401 || !strings.Contains(body, "SESSION_EXPIRED") {
			t.Errorf("refresh with the revoked session's refresh token: %d %s, want 401 SESSION_EXPIRED", got, body)
		}
		if got, _, _ := api("GET", "/api/v1/auth/session", host2.cookie(), nil, nil); got != 200 {
			t.Errorf("the second session of the same account: %d, want 200", got)
		}
	})

	t.Run("revocation: refresh-token reuse closes the family's sockets", func(t *testing.T) {
		victim := newUser(t, "reuse")
		tab := open(notifications, victim)
		tab.frame(t, "notifications.ready")
		defer tab.close()
		// A rotation, then the old refresh token is presented again: Auth treats that
		// as theft and revokes the whole family.
		status, body, rotated := api("POST", "/api/v1/auth/refresh", "ft_refresh="+victim.refresh, nil, map[string]string{"Origin": base})
		if status != 200 || rotated["ft_refresh"] == "" {
			t.Fatalf("first refresh: %d %s", status, body)
		}
		tab.stillOpen(t, "the socket after an ordinary rotation")
		if status, _, _ = api("POST", "/api/v1/auth/refresh", "ft_refresh="+victim.refresh, nil, map[string]string{"Origin": base}); status != 401 {
			t.Fatalf("replayed refresh token: %d, want 401", status)
		}
		tab.frame(t, "unauthorized after reuse")
		if code, reason := tab.closed(t, "family socket"); code != 4401 || reason != "SESSION_REVOKED" {
			t.Errorf("closed with %d %q, want 4401 SESSION_REVOKED", code, reason)
		}
	})

	// --- Restart ----------------------------------------------------------------------
	t.Run("restart: the Game Gateway comes back and members reconnect", func(t *testing.T) {
		compose(t, "restart", "game-gateway")
		code, _ := memberTab.closed(t, "member tab during the gateway restart")
		if code != 1001 && code != 1012 {
			t.Errorf("closed with %d, want 1001 (going away)", code)
		}
		deadline := time.Now().Add(60 * time.Second)
		var refused []int
		for {
			s, got, _ := dial(room, member.cookie(), base, nil)
			if s != nil {
				s.stillOpen(t, "reconnected member")
				s.close()

				break
			}
			refused = append(refused, got)
			if time.Now().After(deadline) {
				t.Fatalf("the member could not reconnect within 60s; statuses %v", refused)
			}
			time.Sleep(time.Second)
		}
		for _, got := range refused {
			if got != 503 && got != 502 && got != 504 {
				t.Errorf("while the gateway was starting a member got %d: only an availability failure (503) may be answered, never a membership verdict", got)
			}
		}
		t.Logf("statuses seen while the gateway started: %v", refused)
	})
}
