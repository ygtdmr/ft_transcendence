// Package requestcontext initialises and propagates the request-scoped context:
// correlation ID, trace context and a server-side deadline, and carries them to
// private downstream calls.
//
// One inbound step, Begin, is shared by the net/http Middleware here and by
// ginhttp.Trace, so both frameworks behave identically.
//
// Cancellation: the context of an incoming request is cancelled when the client
// disconnects, when the deadline set here passes, or when the server shuts down.
// Pass that context to every downstream operation (database calls, Transport
// clients, broker publishes); those stop when it ends. Work that must outlive the
// request should detach with context.WithoutCancel(ctx), which keeps the IDs.
package requestcontext

import (
	"context"
	"net/http"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/requestid"
	"github.com/melmut-42/ft_transcendence/backend/shared/tracecontext"
)

// Info is the request-scoped identity visible to handlers and services.
type Info struct {
	RequestID string
	TraceID   string
	SpanID    string
}

// FromContext returns the IDs carried by ctx; fields are empty when absent.
func FromContext(ctx context.Context) Info {
	info := Info{RequestID: requestid.FromContext(ctx)}
	if tc, ok := tracecontext.FromContext(ctx); ok {
		info.TraceID, info.SpanID = tc.TraceID, tc.SpanID
	}
	return info
}

// Begin prepares the context for an incoming request. It keeps one valid
// X-Request-ID or generates one, starts this process's span under a valid
// traceparent (or a new root), and applies timeout when it is positive. The
// returned cancel must be called when the request ends.
//
// The timeout is configured by the service, never taken from the client, so a
// caller cannot extend its own deadline.
func Begin(r *http.Request, timeout time.Duration) (ctx context.Context, cancel context.CancelFunc, id string, trace tracecontext.Context) {
	id = requestid.Select(r.Header)
	trace = tracecontext.Select(r.Header)
	ctx = tracecontext.WithContext(requestid.WithID(r.Context(), id), trace)
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	return ctx, cancel, id, trace
}

// Middleware wraps next with Begin and echoes the request ID in the response.
// A zero timeout means no server-side deadline. Do not apply a short timeout to
// long-lived routes such as WebSocket upgrades.
func Middleware(timeout time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel, id, _ := Begin(r, timeout)
			defer cancel()
			w.Header().Set(requestid.Header, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Transport returns a RoundTripper for private service-to-service calls. For
// each request it sets X-Request-ID and a traceparent (a new child span of the
// request's trace, or a new root if ctx carries none) and otherwise sends the
// request unchanged. It never copies Authorization, Cookie or any other inbound
// header, and it overwrites any X-Request-ID / Traceparent already on the
// outgoing request. The request's own context (and so the caller's deadline and
// cancellation) is honoured by the underlying transport.
func Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return roundTripper{base: base}
}

type roundTripper struct{ base http.RoundTripper }

func (t roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	out := req.Clone(req.Context()) // never mutate the caller's request
	ctx := req.Context()
	if id := requestid.FromContext(ctx); id != "" {
		out.Header.Set(requestid.Header, id)
	} else {
		out.Header.Del(requestid.Header)
	}
	span := tracecontext.NewRoot()
	if current, ok := tracecontext.FromContext(ctx); ok {
		span = current.Child()
	}
	out.Header.Set(tracecontext.Header, span.Header())
	return t.base.RoundTrip(out)
}
