// Package proxy forwards a browser request to the service that owns it.
//
// The forwarded request keeps the browser's method, path, query, body and
// cookies, with three exceptions that keep identity in the BFF's hands:
//
//   - headers that could carry a client-chosen identity or credential
//     (Authorization, Proxy-Authorization, X-User-Id, X-Session-Id) are removed;
//   - for Game Session, which trusts X-User-Id from its caller, the BFF sets
//     X-User-Id to the user of the validated session and removes every cookie,
//     because Game Session never needs the browser's credentials;
//   - X-Request-ID and Traceparent are set from this request's context, and
//     X-Forwarded-For gains the address the BFF received the request from.
//
// The service's response, including Set-Cookie and redirects, is returned
// unchanged. A service that cannot be reached is answered 503
// SERVICE_UNAVAILABLE, never as a missing route or a logout.
package proxy

import (
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/shared/identity"
	"github.com/melmut-42/ft_transcendence/backend/shared/requestcontext"
	"github.com/melmut-42/ft_transcendence/backend/shared/requestid"
)

// UserIDHeader carries the validated user to Game Session.
const UserIDHeader = "X-User-Id"

// strippedHeaders never reach a service from the browser.
var strippedHeaders = []string{"Authorization", "Proxy-Authorization", UserIDHeader, "X-Session-Id"}

// Options configures one service's proxy.
type Options struct {
	// Name is the service name used in logs.
	Name string
	// Target is the service's public listener origin.
	Target *url.URL
	// ForwardIdentity sets X-User-Id from the validated session and removes the
	// browser's cookies. It requires identity.Authenticate to have run.
	ForwardIdentity bool
	// Transport sends the request; nil uses a transport with ResponseTimeout.
	Transport http.RoundTripper
	// ResponseTimeout bounds the wait for the service's response headers.
	ResponseTimeout time.Duration
	// Logger receives upstream failures, by kind only. May be nil.
	Logger *slog.Logger
}

// New returns the handler forwarding requests to opts.Target.
func New(opts Options) http.Handler {
	transport := opts.Transport
	if transport == nil {
		base := http.DefaultTransport.(*http.Transport).Clone()
		base.ResponseHeaderTimeout = opts.ResponseTimeout
		transport = base
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(opts.Target)
			forwardedFor(pr)
			for _, name := range strippedHeaders {
				pr.Out.Header.Del(name)
			}
			if opts.ForwardIdentity {
				pr.Out.Header.Del("Cookie")
				if id, ok := identity.FromContext(pr.In.Context()); ok {
					pr.Out.Header.Set(UserIDHeader, strconv.FormatInt(id.UserID, 10))
				}
			}
		},
		// Sets X-Request-ID and a child Traceparent from the request context.
		Transport: requestcontext.Transport(transport),
		ModifyResponse: func(resp *http.Response) error {
			// The BFF already echoes the same ID; avoid a repeated header.
			resp.Header.Del(requestid.Header)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, _ error) {
			// The error may contain the service address; log the kind only.
			logger.WarnContext(r.Context(), "upstream_unavailable", "upstream", opts.Name)
			httpresponse.WriteError(w, &httpresponse.Error{
				Status:  http.StatusServiceUnavailable,
				Code:    "SERVICE_UNAVAILABLE",
				Message: "The service is temporarily unavailable.",
			})
		},
	}
}

// forwardedFor keeps the inbound X-Forwarded-For chain and appends the address
// the BFF received the request from. Services resolve the client through their
// trusted proxies, reading the chain from the right, so entries a browser
// added on the left are never believed.
func forwardedFor(pr *httputil.ProxyRequest) {
	chain := pr.In.Header.Values("X-Forwarded-For")
	peer, _, err := net.SplitHostPort(pr.In.RemoteAddr)
	if err != nil {
		peer = pr.In.RemoteAddr
	}
	if peer != "" {
		chain = append(chain, peer)
	}
	if len(chain) > 0 {
		pr.Out.Header.Set("X-Forwarded-For", strings.Join(chain, ", "))
	}
	for _, name := range []string{"X-Forwarded-Proto", "X-Forwarded-Host"} {
		if values := pr.In.Header.Values(name); len(values) > 0 {
			pr.Out.Header[name] = values
		}
	}
}
