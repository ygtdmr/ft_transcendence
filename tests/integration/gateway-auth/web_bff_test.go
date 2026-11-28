package gatewayauth_test

// Proof, on the running stack, that the Web BFF (#88) admits REST requests the way
// the gateways admit sockets, and that a dependency being down is never turned
// into access: only a verified session reaches a service, the caller is the
// session's user whatever the client sends, and when Auth cannot answer the
// request is a 503, not a pass-through and not a logout.
//
// The outage tests stop a real container and start it again in a cleanup, so a
// failing run still leaves the stack up. They run after the gateway tests because
// Go runs a package's tests in file order.

import (
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var errorCodePattern = regexp.MustCompile(`"code":"([A-Z_]+)"`)

func errorCodeOf(body string) string {
	if m := errorCodePattern.FindStringSubmatch(body); m != nil {
		return m[1]
	}

	return ""
}

var hostPattern = regexp.MustCompile(`"host_user_id":(\d+)`)

// bffUser returns the one account the BFF tests share. Registration is limited to
// five per ten minutes per client and the gateway tests already use four, so these
// tests log in again instead of registering more accounts.
var (
	bffOnce    sync.Once
	bffSession session
)

func bffUser(t *testing.T) session {
	t.Helper()
	bffOnce.Do(func() { bffSession = newUser(t, "bff") })

	return bffSession
}

// stopService stops one container and starts it again when the test ends, even
// if the test fails, so one failure cannot leave the stack broken for the next.
func stopService(t *testing.T, name string) {
	t.Helper()
	compose(t, "stop", name)
	t.Cleanup(func() {
		cmd := exec.Command("docker", "compose", "start", name)
		cmd.Dir = repoRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("docker compose start %s: %v\n%s", name, err, out)
		}
	})
}

// eventually polls check until it is true or the deadline passes.
func eventually(t *testing.T, what string, within time.Duration, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("%s did not happen within %s", what, within)
}

func TestWebBFFAdmission(t *testing.T) {
	alice := bffUser(t)
	// A different id is all the identity checks need; the account does not have to exist.
	otherID := alice.userID + 1000
	protected := []struct{ method, path string }{
		{"GET", "/api/v1/auth/session"},
		{"GET", "/api/v1/users/me"},
		{"GET", "/api/v1/users/me/matches"},
		{"POST", "/api/v2/rooms"},
	}
	roomBody := map[string]int{"max_players": 4}

	leave := func(t *testing.T, who session, body string) {
		t.Helper()
		m := regexp.MustCompile(`"room_id":(\d+)`).FindStringSubmatch(body)
		if m == nil {
			return
		}
		api("DELETE", "/api/v2/rooms/"+m[1]+"/members/me", who.cookie(), nil, map[string]string{"Origin": base})
	}

	t.Run("REST: no, expired, forged and unknown sessions are refused on every protected route", func(t *testing.T) {
		key := signingKey(t)
		same := forge(t, alice.session, func(map[string]any) {}, key)
		if got, body, _ := api("GET", "/api/v1/users/me", "ft_session="+same, nil, nil); got != 200 {
			t.Fatalf("re-signing the real token with the real key must still be accepted (%d %s): the forging is wrong", got, body)
		}
		now := time.Now().Unix()
		for name, cookie := range map[string]string{
			"missing":                      "",
			"expired":                      "ft_session=" + forge(t, alice.session, func(c map[string]any) { c["exp"], c["iat"] = now-60, now-1860 }, key),
			"signed with another key":      "ft_session=" + forge(t, alice.session, func(map[string]any) {}, "not-the-real-signing-key-not-the-real"),
			"a session that never existed": "ft_session=" + forge(t, alice.session, func(c map[string]any) { c["sid"] = "00000000-0000-4000-8000-000000000000" }, key),
			"another user's id":            "ft_session=" + forge(t, alice.session, func(c map[string]any) { c["sub"] = strconv.FormatInt(otherID, 10) }, "wrong"),
			"not a token at all":           "ft_session=not.a.token",
		} {
			for _, route := range protected {
				var body any
				if route.method == "POST" {
					body = roomBody
				}
				got, out, _ := api(route.method, route.path, cookie, body, nil)
				if got != 401 {
					t.Errorf("%s %s with a session that is %s: %d %s, want 401", route.method, route.path, name, got, out)
				}
			}
		}
	})

	t.Run("REST: the caller is the session's user, whatever the client sends", func(t *testing.T) {
		status, body, _ := api("POST", "/api/v2/rooms", alice.cookie(), roomBody, map[string]string{
			"X-User-Id":     strconv.FormatInt(otherID, 10),
			"X-Session-Id":  "00000000-0000-4000-8000-000000000000",
			"Authorization": "Bearer not-a-real-credential",
		})
		defer leave(t, alice, body)
		m := hostPattern.FindStringSubmatch(body)
		if status != 201 || m == nil {
			t.Fatalf("create room: %d %s", status, body)
		}
		if m[1] != strconv.FormatInt(alice.userID, 10) {
			t.Errorf("room host is %s, want the session's user %d (the client asked for %d)", m[1], alice.userID, otherID)
		}
		if status, _, _ := api("POST", "/api/v2/rooms", "", roomBody, map[string]string{"X-User-Id": strconv.FormatInt(otherID, 10)}); status != 401 {
			t.Errorf("a forged X-User-Id with no session: %d, want 401", status)
		}
	})

	t.Run("REST: a cross-site write is refused before the session is looked at", func(t *testing.T) {
		for name, cookie := range map[string]string{"with a valid session": alice.cookie(), "with none": ""} {
			status, body, _ := api("POST", "/api/v2/rooms", cookie, roomBody, map[string]string{"Origin": "https://evil.example"})
			if status != 403 || errorCodeOf(body) != "FORBIDDEN_ORIGIN" {
				t.Errorf("foreign Origin %s: %d %s, want 403 FORBIDDEN_ORIGIN", name, status, body)
			}
		}
	})

	t.Run("REST: private and unknown paths are not routed, in the contract's shape", func(t *testing.T) {
		if status, _, _ := api("GET", "/internal/v1/memberships/1", "", nil, nil); status != 404 {
			t.Errorf("/internal/v1/memberships/1: %d, want 404", status)
		}
		for _, path := range []string{"/api/v1/internal/v1/memberships/1", "/api/v2/internal/x", "/api/v1/no/such/route"} {
			status, body, _ := api("GET", path, alice.cookie(), nil, nil)
			if status != 404 || errorCodeOf(body) != "NOT_FOUND" {
				t.Errorf("GET %s: %d %s, want 404 NOT_FOUND", path, status, body)
			}
		}
	})
}

func TestWebBFFOutages(t *testing.T) {
	alice := bffUser(t)
	ok := func() bool {
		status, _, _ := api("GET", "/api/v1/users/me", alice.cookie(), nil, nil)

		return status == 200
	}
	if !ok() {
		t.Fatal("the session does not work before the outage")
	}

	t.Run("Auth down: a verified session is a 503, never a pass-through and never a logout", func(t *testing.T) {
		stopService(t, "auth-service")
		key := signingKey(t)
		forged := "ft_session=" + forge(t, alice.session, func(c map[string]any) { c["sid"] = "00000000-0000-4000-8000-000000000000" }, key)
		eventually(t, "the BFF to notice Auth is down", 30*time.Second, func() bool {
			status, _, _ := api("GET", "/api/v1/users/me", alice.cookie(), nil, nil)

			return status == 503
		})

		for _, route := range []struct{ method, path string }{{"GET", "/api/v1/users/me"}, {"GET", "/api/v1/users/me/matches"}, {"POST", "/api/v2/rooms"}} {
			var body any
			if route.method == "POST" {
				body = map[string]int{"max_players": 4}
			}
			status, out, _ := api(route.method, route.path, alice.cookie(), body, nil)
			if status != 503 || errorCodeOf(out) != "SERVICE_UNAVAILABLE" {
				t.Errorf("%s %s with a valid session while Auth is down: %d %s, want 503 SERVICE_UNAVAILABLE", route.method, route.path, status, out)
			}
			if status, out, _ := api(route.method, route.path, forged, body, nil); status/100 == 2 {
				t.Errorf("%s %s with a forged session while Auth is down: %d %s, a bypass", route.method, route.path, status, out)
			}
		}
		if status, body, _ := api("POST", "/api/v1/auth/login", "", map[string]string{"email": alice.name + "@example.com", "password": passwordFor}, nil); status != 503 {
			t.Errorf("login while Auth is down: %d %s, want 503", status, body)
		}
		if s, status, _ := dial(notifications, alice.cookie(), base, nil); s != nil || status != 503 {
			if s != nil {
				s.close()
			}
			t.Errorf("notification socket while Auth is down: %d, want a 503 handshake refusal", status)
		}

		compose(t, "start", "auth-service")
		eventually(t, "the same session to work again", 60*time.Second, ok)
		status, body, _ := api("GET", "/api/v1/auth/session", alice.cookie(), nil, nil)
		if status != 200 || !strings.Contains(body, `"active_room_id":null`) {
			t.Errorf("after Auth returned: %d %s, want 200 and no room: the refused create must have had no effect", status, body)
		}
	})

	t.Run("User Service down: its routes are a 503 and the session works again afterwards", func(t *testing.T) {
		stopService(t, "user-service")
		eventually(t, "the BFF to notice User Service is down", 30*time.Second, func() bool {
			status, _, _ := api("GET", "/api/v1/users/me", alice.cookie(), nil, nil)

			return status == 503
		})
		if status, body, _ := api("GET", "/api/v1/users/me", alice.cookie(), nil, nil); status != 503 || errorCodeOf(body) != "SERVICE_UNAVAILABLE" {
			t.Errorf("GET /api/v1/users/me while User Service is down: %d %s, want 503 SERVICE_UNAVAILABLE", status, body)
		}
		compose(t, "start", "user-service")
		eventually(t, "the same session to work again", 60*time.Second, ok)
	})
}
