// Package ratelimit bounds how often one key (a client address, a user, a
// calling service) may act, as an abuse safeguard at the entry layer.
//
// The limiter is a token bucket held in memory: it protects one process, not the
// whole fleet, which is the right trade for brute-force and flood protection of
// a single replica. The contract defines the response (429 RATE_LIMITED) but no
// numeric limits, so Limit and Window have no defaults: the owning service
// chooses and documents them.
//
// Memory is bounded. When MaxKeys is reached the limiter first drops buckets that
// have been idle for a full window (their state is indistinguishable from a
// fresh one); if the table is still full of active keys it refuses new keys
// until space frees up. That fails closed: a flood of distinct keys is throttled
// instead of exhausting memory or silently switching the limit off.
package ratelimit

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/shared/identity"
	"github.com/melmut-42/ft_transcendence/backend/shared/serviceauth"
)

// DefaultMaxKeys is the table size when Config.MaxKeys is zero.
const DefaultMaxKeys = 10_000

// Config describes one limit: at most Limit events per Window per key, with a
// burst of up to Limit.
type Config struct {
	Limit   int
	Window  time.Duration
	MaxKeys int
	// Now is the clock; nil means time.Now. Tests inject a fake one.
	Now func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// Limiter is safe for concurrent use.
type Limiter struct {
	limit     float64
	window    time.Duration
	maxKeys   int
	now       func() time.Time
	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

// New validates cfg and returns a Limiter.
func New(cfg Config) (*Limiter, error) {
	if cfg.Limit <= 0 || cfg.Window <= 0 {
		return nil, errors.New("ratelimit: Limit and Window must be positive")
	}
	if cfg.MaxKeys < 0 {
		return nil, errors.New("ratelimit: MaxKeys must not be negative")
	}
	l := &Limiter{limit: float64(cfg.Limit), window: cfg.Window, maxKeys: cfg.MaxKeys, now: cfg.Now, buckets: map[string]*bucket{}}
	if l.maxKeys == 0 {
		l.maxKeys = DefaultMaxKeys
	}
	if l.now == nil {
		l.now = time.Now
	}
	return l, nil
}

// Allow reports whether key may act now, consuming one token when it may.
func (l *Limiter) Allow(key string) bool {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	b, exists := l.buckets[key]
	if !exists {
		if len(l.buckets) >= l.maxKeys {
			l.sweep(now)
			if len(l.buckets) >= l.maxKeys {
				return false // full of active keys: refuse new ones
			}
		}
		b = &bucket{tokens: l.limit, last: now}
		l.buckets[key] = b
	} else {
		elapsed := now.Sub(b.last)
		if elapsed > 0 {
			b.tokens += elapsed.Seconds() / l.window.Seconds() * l.limit
			if b.tokens > l.limit {
				b.tokens = l.limit
			}
			b.last = now
		}
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// sweep drops buckets idle for at least a window. It runs at most once per tenth
// of a window so a flood of new keys cannot turn every request into a full scan.
func (l *Limiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < l.window/10 {
		return
	}
	l.lastSweep = now
	for key, b := range l.buckets {
		if now.Sub(b.last) >= l.window {
			delete(l.buckets, key)
		}
	}
}

// Len returns the number of tracked keys (for tests and metrics).
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// Middleware rejects requests whose key is over its limit with 429 RATE_LIMITED.
// message is the public text (the contract words it per operation, for example
// "Too many registration attempts; retry after 60 seconds."); empty uses a
// generic one. A key function that returns "" puts that traffic in one shared
// bucket instead of leaving it unlimited. No Retry-After header is sent because
// the contract does not define one.
func Middleware(l *Limiter, key func(*http.Request) string, message string) func(http.Handler) http.Handler {
	if message == "" {
		message = "Too many requests; try again later."
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.Allow(key(r)) {
				httpresponse.WriteError(w, &httpresponse.Error{
					Status:  http.StatusTooManyRequests,
					Code:    "RATE_LIMITED",
					Message: message,
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// KeyByRemoteAddr keys by the connection's peer address, ignoring X-Forwarded-For
// and similar headers, which a client controls. IPv6 peers are grouped by /64 so
// one host cannot dodge the limit by cycling addresses in its own prefix. Behind
// a reverse proxy every client shares the proxy's address; key by identity or by
// a proxy-verified value there instead.
func KeyByRemoteAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	addr = addr.Unmap()
	if addr.Is6() {
		prefix, _ := addr.Prefix(64)
		return prefix.String()
	}
	return addr.String()
}

// KeyByUser keys by the authenticated user (use after identity.Authenticate).
// Unauthenticated requests share the "" bucket.
func KeyByUser(r *http.Request) string {
	if id, ok := identity.FromContext(r.Context()); ok {
		return "user:" + strconv.FormatInt(id.UserID, 10)
	}
	return ""
}

// KeyByCaller keys by the authenticated calling service (use after
// serviceauth.Authenticator.Require).
func KeyByCaller(r *http.Request) string {
	if caller := serviceauth.Caller(r.Context()); caller != "" {
		return "caller:" + caller
	}
	return ""
}
