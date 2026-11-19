// Package tracecontext carries a W3C Trace Context (the traceparent header)
// through a request so logs and downstream calls can be joined into one trace.
//
// It implements only what the backend needs, with no tracing SDK: parse and
// validate an incoming traceparent, start a server-side span under it (or a new
// root trace), and format the header for outgoing private calls. tracestate is
// deliberately not propagated, and a client-supplied traceparent is correlation
// data only: it is validated strictly and never grants any access.
package tracecontext

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
)

// Header is the W3C Trace Context header name.
const Header = "Traceparent"

// Context identifies one span inside a trace.
type Context struct {
	TraceID  string // 32 lower-case hex digits, not all zero
	SpanID   string // 16 lower-case hex digits, not all zero; this process's span
	ParentID string // the caller's span id, empty for a root trace
	Flags    byte   // trace-flags; bit 0 is "sampled"
}

type contextKey struct{}

// Parse validates a traceparent value. Version 00 must have exactly four fields;
// a later version may carry extra fields (per the spec), which are ignored.
// Version ff, upper-case hex, wrong lengths and all-zero ids are rejected.
func Parse(value string) (Context, bool) {
	parts := strings.Split(value, "-")
	if len(parts) < 4 || len(parts[0]) != 2 || !isHex(parts[0]) || parts[0] == "ff" {
		return Context{}, false
	}
	if parts[0] == "00" && len(parts) != 4 {
		return Context{}, false
	}
	traceID, spanID, flags := parts[1], parts[2], parts[3]
	if len(traceID) != 32 || !isHex(traceID) || allZero(traceID) ||
		len(spanID) != 16 || !isHex(spanID) || allZero(spanID) ||
		len(flags) != 2 || !isHex(flags) {
		return Context{}, false
	}
	decoded, _ := hex.DecodeString(flags)
	return Context{TraceID: traceID, SpanID: spanID, Flags: decoded[0]}, true
}

// NewRoot starts a new trace. It is not marked sampled: there is no tracing
// backend, so claiming it would be false.
func NewRoot() Context {
	return Context{TraceID: randomHex(16), SpanID: randomHex(8)}
}

// Child returns a new span in the same trace whose parent is c.
func (c Context) Child() Context {
	return Context{TraceID: c.TraceID, SpanID: randomHex(8), ParentID: c.SpanID, Flags: c.Flags}
}

// Sampled reports the sampled trace-flag.
func (c Context) Sampled() bool { return c.Flags&1 == 1 }

// Header formats c as a version-00 traceparent value naming c's own span, for
// sending to a downstream service.
func (c Context) Header() string {
	return "00-" + c.TraceID + "-" + c.SpanID + "-" + hex.EncodeToString([]byte{c.Flags})
}

// Select derives this process's span for an incoming request: a child of a
// single valid traceparent, otherwise a new root. A missing, malformed or
// repeated header starts a new trace rather than failing the request.
func Select(h http.Header) Context {
	if values := h.Values(Header); len(values) == 1 {
		if incoming, ok := Parse(values[0]); ok {
			return incoming.Child()
		}
	}
	return NewRoot()
}

// WithContext returns ctx carrying c.
func WithContext(ctx context.Context, c Context) context.Context {
	return context.WithValue(ctx, contextKey{}, c)
}

// FromContext returns the trace context in ctx.
func FromContext(ctx context.Context) (Context, bool) {
	c, ok := ctx.Value(contextKey{}).(Context)
	return c, ok
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func allZero(s string) bool { return strings.Trim(s, "0") == "" }

func randomHex(n int) string {
	for {
		b := make([]byte, n)
		_, _ = rand.Read(b)
		if s := hex.EncodeToString(b); !allZero(s) {
			return s
		}
	}
}
