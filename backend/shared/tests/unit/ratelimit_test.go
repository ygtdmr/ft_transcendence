package unit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/identity"
	"github.com/melmut-42/ft_transcendence/backend/shared/ratelimit"
	"github.com/melmut-42/ft_transcendence/backend/shared/serviceauth"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)} }

func TestRateLimiterConfigurationIsValidated(t *testing.T) {
	for name, cfg := range map[string]ratelimit.Config{
		"no limit":        {Window: time.Minute},
		"negative limit":  {Limit: -1, Window: time.Minute},
		"no window":       {Limit: 5},
		"negative window": {Limit: 5, Window: -time.Second},
		"negative keys":   {Limit: 5, Window: time.Minute, MaxKeys: -1},
	} {
		if _, err := ratelimit.New(cfg); err == nil {
			t.Errorf("%s accepted; there are no defaults for the limit itself", name)
		}
	}
}

func TestRateLimiterAllowsABurstThenRefuses(t *testing.T) {
	clock := newClock()
	l, _ := ratelimit.New(ratelimit.Config{Limit: 3, Window: time.Minute, Now: clock.now})
	for i := 0; i < 3; i++ {
		if !l.Allow("a") {
			t.Fatalf("request %d of the burst was refused", i+1)
		}
	}
	if l.Allow("a") {
		t.Error("the fourth request must be refused")
	}
	if !l.Allow("b") {
		t.Error("keys are independent: another key still has its own budget")
	}
}

func TestRateLimiterRefillsGraduallyAndNeverAboveTheLimit(t *testing.T) {
	clock := newClock()
	l, _ := ratelimit.New(ratelimit.Config{Limit: 3, Window: time.Minute, Now: clock.now})
	for i := 0; i < 3; i++ {
		l.Allow("a")
	}
	clock.advance(10 * time.Second) // 3 per minute = one token per 20s: half a token
	if l.Allow("a") {
		t.Error("half a token must not allow a request")
	}
	clock.advance(10 * time.Second) // now one full token
	if !l.Allow("a") || l.Allow("a") {
		t.Error("exactly one request should be allowed after 20 seconds")
	}
	clock.advance(24 * time.Hour) // refill is capped at the limit, not accumulated
	allowed := 0
	for i := 0; i < 10; i++ {
		if l.Allow("a") {
			allowed++
		}
	}
	if allowed != 3 {
		t.Errorf("after a long idle period %d requests passed, want exactly the limit (3)", allowed)
	}
}

func TestRateLimiterIsExactUnderConcurrency(t *testing.T) {
	clock := newClock()
	l, _ := ratelimit.New(ratelimit.Config{Limit: 50, Window: time.Hour, Now: clock.now})
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 500; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Allow("shared") {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if allowed.Load() != 50 {
		t.Errorf("%d of 500 concurrent requests passed, want exactly 50", allowed.Load())
	}
}

func TestRateLimiterMemoryIsBoundedAndFailsClosedForNewKeys(t *testing.T) {
	clock := newClock()
	l, _ := ratelimit.New(ratelimit.Config{Limit: 2, Window: time.Minute, MaxKeys: 100, Now: clock.now})
	for i := 0; i < 100; i++ {
		if !l.Allow("k" + string(rune('A'+i%26)) + strings.Repeat("x", i)) {
			t.Fatalf("key %d refused before the table was full", i)
		}
	}
	if l.Len() != 100 {
		t.Fatalf("tracked keys = %d", l.Len())
	}
	if l.Allow("one-more-key") {
		t.Error("a new key must be refused while the table is full of active keys")
	}
	if l.Len() > 100 {
		t.Errorf("memory grew past MaxKeys: %d", l.Len())
	}
	clock.advance(2 * time.Minute) // everything is idle: swept on the next new key
	if !l.Allow("one-more-key") {
		t.Error("idle keys must be dropped so new keys fit again")
	}
	if l.Len() > 100 {
		t.Errorf("memory grew past MaxKeys after the sweep: %d", l.Len())
	}
}

func rateServer(l *ratelimit.Limiter, key func(*http.Request) string, message string) (http.Handler, *atomic.Int32) {
	var reached atomic.Int32
	h := ratelimit.Middleware(l, key, message)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		w.WriteHeader(204)
	}))
	return h, &reached
}

func TestRateLimitMiddlewareAnswers429RateLimited(t *testing.T) {
	clock := newClock()
	l, _ := ratelimit.New(ratelimit.Config{Limit: 2, Window: time.Minute, Now: clock.now})
	h, reached := rateServer(l, ratelimit.KeyByRemoteAddr, "Too many registration attempts; retry after 60 seconds.")
	do := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/v1/auth/register", nil)
		req.RemoteAddr = "203.0.113.9:51000"
		h.ServeHTTP(rec, req)
		return rec
	}
	if do().Code != 204 || do().Code != 204 {
		t.Fatal("the first two requests must pass")
	}
	rec := do()
	if rec.Code != 429 || reached.Load() != 2 {
		t.Fatalf("third request: %d, handler ran %d times", rec.Code, reached.Load())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"code":"RATE_LIMITED"`) || !strings.Contains(body, "retry after 60 seconds") || !strings.Contains(body, `"details":{}`) {
		t.Errorf("body = %s", body)
	}
	if rec.Header().Get("Retry-After") != "" {
		t.Error("the contract defines no Retry-After header")
	}
	if strings.Contains(body, "203.0.113.9") {
		t.Error("the response must not echo the client address")
	}
}

func TestRateLimitMiddlewareDefaultMessageAndSharedBucketForUnknownKeys(t *testing.T) {
	clock := newClock()
	l, _ := ratelimit.New(ratelimit.Config{Limit: 1, Window: time.Minute, Now: clock.now})
	h, _ := rateServer(l, func(*http.Request) string { return "" }, "")
	first := httptest.NewRecorder()
	h.ServeHTTP(first, httptest.NewRequest("GET", "/", nil))
	second := httptest.NewRecorder()
	h.ServeHTTP(second, httptest.NewRequest("GET", "/", nil))
	if first.Code != 204 || second.Code != 429 {
		t.Errorf("traffic with no key must share one bucket, not be unlimited: %d then %d", first.Code, second.Code)
	}
	if !strings.Contains(second.Body.String(), "Too many requests; try again later.") {
		t.Errorf("default message missing: %s", second.Body.String())
	}
}

func TestKeyByRemoteAddr(t *testing.T) {
	cases := map[string]string{
		"203.0.113.9:51000":           "203.0.113.9",
		"203.0.113.9":                 "203.0.113.9",
		"[::ffff:203.0.113.9]:80":     "203.0.113.9", // IPv4-mapped IPv6 is the same host
		"[2001:db8:1:2:aaaa::1]:443":  "2001:db8:1:2::/64",
		"[2001:db8:1:2:bbbb::99]:443": "2001:db8:1:2::/64", // same /64 shares one bucket
		"not-an-address":              "not-an-address",
	}
	for remote, want := range cases {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = remote
		req.Header.Set("X-Forwarded-For", "198.51.100.77")
		req.Header.Set("X-Real-IP", "198.51.100.78")
		if got := ratelimit.KeyByRemoteAddr(req); got != want {
			t.Errorf("KeyByRemoteAddr(%q) = %q, want %q (forwarded headers must be ignored)", remote, got, want)
		}
	}
}

func TestKeyByUserAndCaller(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	if ratelimit.KeyByUser(req) != "" || ratelimit.KeyByCaller(req) != "" {
		t.Error("without identity both keys are empty")
	}
	req = req.WithContext(identity.WithIdentity(context.Background(), identity.Identity{UserID: 42, SessionID: "s"}))
	if got := ratelimit.KeyByUser(req); got != "user:42" {
		t.Errorf("KeyByUser = %q", got)
	}
	var caller string
	a := serviceauth.New(map[string][]string{"game-gateway": {credGateway}}, testMatrix)
	a.Require("session.validate")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller = ratelimit.KeyByCaller(r)
	})).ServeHTTP(httptest.NewRecorder(), func() *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Authorization", "Bearer "+credGateway)
		return r
	}())
	if caller != "caller:game-gateway" {
		t.Errorf("KeyByCaller = %q", caller)
	}
}
