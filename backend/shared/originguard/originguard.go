// Package originguard enforces the deployment's single-origin model on requests
// that carry the ambient ft_session cookie.
//
// The contract serves the SPA, REST and WebSockets from ONE public origin and
// authenticates with a SameSite=Lax cookie. That cookie is attached by the
// browser whatever page triggered the request, so two protections complement it:
//
//   - CSRF: a cookie-authenticated request that changes state (POST, PUT, PATCH,
//     DELETE) must come from the deployment's own origin. A browser always states
//     where a request came from (Origin, or Sec-Fetch-Site), so a foreign page
//     cannot forge one. Clients that are not browsers send neither header and
//     are not affected, because only browsers attach cookies by themselves.
//   - WebSocket upgrades: the handshake's Origin must match the deployment's own
//     origin, checked before the cookie is. SameSite=Lax alone does not cover
//     upgrades in every browser (cross-site WebSocket hijacking).
//
// CORS is deliberately not offered. With a single public origin no cross-origin
// read is ever intended, and the safe configuration is to send no
// Access-Control-Allow-* headers at all, so browsers block foreign pages from
// reading responses. In particular never answer a credentialed request with
// "Access-Control-Allow-Origin: *" or by echoing the request's Origin.
package originguard

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
)

// Guard knows the one origin the deployment serves.
type Guard struct {
	scheme string
	host   string // lower-case host with the port only when it is not the scheme default
}

// New parses allowedOrigin, the single public origin, such as
// "https://play.example.com" or "http://localhost:5173". It must be an http(s)
// origin with a host and nothing else: no path, query, fragment or credentials.
func New(allowedOrigin string) (*Guard, error) {
	scheme, host, ok := parseOrigin(allowedOrigin)
	if !ok {
		return nil, errors.New("originguard: the allowed origin must look like https://host[:port] with no path, query or credentials")
	}
	return &Guard{scheme: scheme, host: host}, nil
}

// parseOrigin splits an origin into scheme and normalized host. The serialized
// form of an origin has no trailing slash and no path, so anything more is not
// an origin. Default ports are dropped so https://a and https://a:443 are equal.
func parseOrigin(value string) (scheme, host string, ok bool) {
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || u.Opaque != "" ||
		strings.HasSuffix(value, "/") {
		return "", "", false
	}
	host = strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	port := u.Port()
	if port != "" && !(u.Scheme == "http" && port == "80") && !(u.Scheme == "https" && port == "443") {
		host += ":" + port
	}
	return u.Scheme, host, true
}

func (g *Guard) matches(origin string) bool {
	scheme, host, ok := parseOrigin(origin)
	return ok && scheme == g.scheme && host == g.host
}

// CSRF rejects a state-changing request that a browser says came from another
// origin, with 403 FORBIDDEN_ORIGIN. Safe methods (GET, HEAD, OPTIONS) pass: they
// must not change state, and WebSocket upgrades are covered by WebSocket.
//
// For POST, PUT, PATCH and DELETE:
//
//   - exactly one Origin header: it must equal the deployment's origin ("null"
//     and anything else is refused);
//   - more than one Origin header: refused (ambiguous);
//   - no Origin but a Sec-Fetch-Site other than same-origin or none: refused;
//   - neither header: a non-browser client, allowed.
func (g *Guard) CSRF() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}
			origins := r.Header.Values("Origin")
			switch {
			case len(origins) > 1:
				forbiddenOrigin(w)
			case len(origins) == 1:
				if !g.matches(origins[0]) {
					forbiddenOrigin(w)
					return
				}
				next.ServeHTTP(w, r)
			default:
				if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
					forbiddenOrigin(w)
					return
				}
				next.ServeHTTP(w, r)
			}
		})
	}
}

// WebSocket rejects a WebSocket upgrade whose Origin is not exactly the
// deployment's origin, with a bare 403 (the contract: "handshake rejected, no
// code") before anything else, so no cookie is examined for a foreign page.
// A missing, repeated or "null" Origin is a mismatch: browsers always send one.
// Requests that are not upgrades pass through untouched.
func (g *Guard) WebSocket() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isWebSocketUpgrade(r) {
				next.ServeHTTP(w, r)
				return
			}
			origins := r.Header.Values("Origin")
			if len(origins) != 1 || !g.matches(origins[0]) {
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func isWebSocketUpgrade(r *http.Request) bool {
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") {
		return false
	}
	for _, v := range r.Header.Values("Connection") {
		for _, token := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
				return true
			}
		}
	}
	return false
}

func forbiddenOrigin(w http.ResponseWriter) {
	httpresponse.WriteError(w, &httpresponse.Error{
		Status:  http.StatusForbidden,
		Code:    "FORBIDDEN_ORIGIN",
		Message: "The request origin is not allowed.",
	})
}
