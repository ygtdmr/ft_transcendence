package unit_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/config"
	"github.com/melmut-42/ft_transcendence/backend/shared/ratelimit"
)

func proxies(t *testing.T, entries ...string) *ratelimit.TrustedProxies {
	t.Helper()
	p, err := ratelimit.ParseTrustedProxies(entries)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func fromPeer(peer string, forwarded ...string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = peer
	for _, f := range forwarded {
		r.Header.Add("X-Forwarded-For", f)
	}
	return r
}

func TestTrustedProxyEntriesAreParsedStrictly(t *testing.T) {
	if _, err := ratelimit.ParseTrustedProxies([]string{"10.0.0.0/8", "172.18.0.5", " fd00::/8 ", "", "::1"}); err != nil {
		t.Errorf("valid entries rejected: %v", err)
	}
	for _, bad := range []string{"10.0.0.0/33", "not-an-ip", "10.0.0.1/abc", "300.1.1.1", "10.0.0.1-10.0.0.9"} {
		if _, err := ratelimit.ParseTrustedProxies([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		} else if strings.Contains(err.Error(), bad) {
			t.Errorf("the error must not echo the entry: %v", err)
		}
	}
}

func TestForwardedHeadersAreIgnoredUnlessThePeerIsATrustedProxy(t *testing.T) {
	trusted := proxies(t, "10.0.0.0/8")
	cases := []struct {
		name string
		t    *ratelimit.TrustedProxies
		req  *http.Request
		want string
	}{
		{"no proxies configured", proxies(t), fromPeer("203.0.113.9:1", "1.2.3.4"), "203.0.113.9"},
		{"nil value", nil, fromPeer("203.0.113.9:1", "1.2.3.4"), "203.0.113.9"},
		{"untrusted peer lies", trusted, fromPeer("203.0.113.9:1", "1.2.3.4"), "203.0.113.9"},
		{"trusted peer, no header", trusted, fromPeer("10.0.0.1:1"), "10.0.0.1"},
		{"trusted peer reports the client", trusted, fromPeer("10.0.0.1:1", "203.0.113.5"), "203.0.113.5"},
		{"client padded the header, only the trusted part is believed", trusted, fromPeer("10.0.0.1:1", "1.1.1.1, 203.0.113.5"), "203.0.113.5"},
		{"two trusted hops", trusted, fromPeer("10.0.0.1:1", "1.1.1.1, 203.0.113.5, 10.0.0.2"), "203.0.113.5"},
		{"header split across lines", trusted, fromPeer("10.0.0.1:1", "1.1.1.1", "203.0.113.5, 10.0.0.2"), "203.0.113.5"},
		{"every hop trusted", trusted, fromPeer("10.0.0.1:1", "10.0.0.7, 10.0.0.2"), "10.0.0.1"},
		{"garbage in the header", trusted, fromPeer("10.0.0.1:1", "not-an-ip"), "10.0.0.1"},
		{"garbage to the left of a real client", trusted, fromPeer("10.0.0.1:1", "garbage, 203.0.113.5"), "203.0.113.5"},
		{"garbage right next to the proxy", trusted, fromPeer("10.0.0.1:1", "203.0.113.5, garbage"), "10.0.0.1"},
		{"IPv4-mapped IPv6 peer", trusted, fromPeer("[::ffff:10.0.0.1]:1", "203.0.113.5"), "203.0.113.5"},
		{"peer without a port", trusted, fromPeer("10.0.0.1", "203.0.113.5"), "203.0.113.5"},
	}
	for _, tc := range cases {
		if got := tc.t.ClientIP(tc.req); got != tc.want {
			t.Errorf("%s: ClientIP = %q, want %q", tc.name, got, tc.want)
		}
	}

	// A header longer than any real proxy chain is not examined at all.
	long := strings.TrimSuffix(strings.Repeat("198.51.100.1, ", 40), ", ")
	if got := trusted.ClientIP(fromPeer("10.0.0.1:1", long)); got != "10.0.0.1" {
		t.Errorf("an oversized header must fall back to the peer, got %q", got)
	}
}

func TestKeyByClientIPCannotBeChosenByAnAttacker(t *testing.T) {
	trusted := proxies(t, "10.0.0.1")
	key := ratelimit.KeyByClientIP(trusted)

	// An attacker connecting directly rotates X-Forwarded-For: one bucket regardless.
	if key(fromPeer("203.0.113.9:1", "1.1.1.1")) != key(fromPeer("203.0.113.9:2", "2.2.2.2")) {
		t.Error("rotating X-Forwarded-For from an untrusted peer must not change the key")
	}
	// Two real clients behind the trusted proxy get two buckets.
	a, b := key(fromPeer("10.0.0.1:1", "203.0.113.5")), key(fromPeer("10.0.0.1:2", "203.0.113.6"))
	if a == b || a != "203.0.113.5" {
		t.Errorf("clients behind the proxy must be told apart: %q %q", a, b)
	}
	// Padding the header on the client side does not change who they are.
	if key(fromPeer("10.0.0.1:1", "9.9.9.9, 203.0.113.5")) != a {
		t.Error("client-supplied padding must not change the key")
	}
	// IPv6 clients are grouped by /64 so one host cannot dodge the limit.
	if key(fromPeer("10.0.0.1:1", "2001:db8:1:2:aaaa::1")) != key(fromPeer("10.0.0.1:1", "2001:db8:1:2:bbbb::9")) {
		t.Error("one /64 must share one bucket")
	}
}

func TestRateLimitingBehindATrustedProxyThrottlesThePerClientNotTheProxy(t *testing.T) {
	clock := newClock()
	l, _ := ratelimit.New(ratelimit.Config{Limit: 2, Window: time.Hour, Now: clock.now})
	h, _ := rateServer(l, ratelimit.KeyByClientIP(proxies(t, "10.0.0.1")), "")
	send := func(peer, xff string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, fromPeer(peer, xff))
		return rec.Code
	}
	if send("10.0.0.1:1", "203.0.113.5") != 204 || send("10.0.0.1:1", "203.0.113.5") != 204 || send("10.0.0.1:1", "203.0.113.5") != 429 {
		t.Error("one client must be limited after its budget")
	}
	if send("10.0.0.1:1", "203.0.113.6") != 204 {
		t.Error("another client behind the same proxy must have its own budget")
	}
	// Spoofing from an untrusted peer: a new X-Forwarded-For each time must not reset the budget.
	allowed := 0
	for i := 0; i < 10; i++ {
		if send("198.51.100.20:5", "7.7.7."+string(rune('0'+i))) == 204 {
			allowed++
		}
	}
	if allowed != 2 {
		t.Errorf("a client rotating forged X-Forwarded-For values got %d requests through, want exactly its 2", allowed)
	}
}

func TestConfigReaderListValidatesEveryItemWithoutEchoingIt(t *testing.T) {
	isNum := func(s string) bool { return s != "" && strings.Trim(s, "0123456789") == "" }
	rd := func(v string, set bool) *config.Reader {
		return config.NewReader(func(string) (string, bool) { return v, set })
	}
	r := rd(" 1, 22 ,,333 ", true)
	if got := r.List("L", isNum, "must be numbers"); len(got) != 3 || got[1] != "22" || r.Err() != nil {
		t.Errorf("got %v err=%v", got, r.Err())
	}
	if got := rd("", false).List("L", isNum, "x"); got != nil {
		t.Errorf("an unset list is nil, got %v", got)
	}
	r = rd("1,secret-token-abc,3", true)
	if got := r.List("L", isNum, "must be numbers"); got != nil {
		t.Errorf("an invalid list must yield nothing, got %v", got)
	}
	if err := r.Err(); err == nil || !strings.Contains(err.Error(), "L must be numbers") || strings.Contains(err.Error(), "secret-token") {
		t.Errorf("err = %v", err)
	}
}
