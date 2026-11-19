package unit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/logging"
	"github.com/melmut-42/ft_transcendence/backend/shared/requestcontext"
	"github.com/melmut-42/ft_transcendence/backend/shared/requestid"
	"github.com/melmut-42/ft_transcendence/backend/shared/tracecontext"
)

func TestEveryRequestGetsAUniqueID(t *testing.T) {
	seen := map[string]bool{}
	handler := requestcontext.Middleware(0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := requestcontext.FromContext(r.Context()).RequestID
		if seen[id] || !requestid.Valid(id) {
			t.Errorf("request id %q duplicated or invalid", id)
		}
		seen[id] = true
	}))
	for i := 0; i < 1000; i++ {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	}
	if len(seen) != 1000 {
		t.Errorf("%d unique IDs for 1000 requests", len(seen))
	}
}

func TestClientRequestIDAndTraceAreContinued(t *testing.T) {
	var info requestcontext.Info
	handler := requestcontext.Middleware(0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info = requestcontext.FromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(requestid.Header, "client-chosen-id")
	req.Header.Set(tracecontext.Header, "00-"+goodTrace+"-"+goodSpan+"-01")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if info.RequestID != "client-chosen-id" || rec.Header().Get(requestid.Header) != "client-chosen-id" {
		t.Errorf("a valid client request id must be preserved, got %+v / %q", info, rec.Header().Get(requestid.Header))
	}
	if info.TraceID != goodTrace || info.SpanID == goodSpan || info.SpanID == "" {
		t.Errorf("the trace must continue in a new span: %+v", info)
	}
}

func TestMalformedClientHeadersAreReplacedNotTrusted(t *testing.T) {
	var info requestcontext.Info
	handler := requestcontext.Middleware(0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info = requestcontext.FromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(requestid.Header, "evil id; drop table")
	req.Header.Set(tracecontext.Header, "00-"+strings.Repeat("0", 32)+"-"+goodSpan+"-01")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != 200 || !requestid.Valid(info.RequestID) || info.RequestID == "evil id; drop table" ||
		info.TraceID == strings.Repeat("0", 32) || len(info.TraceID) != 32 {
		t.Errorf("bad client values must be replaced without failing the request: %d %+v", rec.Code, info)
	}
}

func TestMiddlewareTimeoutCancelsTheRequestContext(t *testing.T) {
	var deadlineSet bool
	var waited time.Duration
	var err error
	handler := requestcontext.Middleware(100 * time.Millisecond)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, deadlineSet = r.Context().Deadline()
		start := time.Now()
		select {
		case <-r.Context().Done():
			err = r.Context().Err()
		case <-time.After(3 * time.Second):
		}
		waited = time.Since(start)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if !deadlineSet || err != context.DeadlineExceeded || waited > time.Second {
		t.Errorf("deadline=%v err=%v waited=%s; the context must end at the configured timeout", deadlineSet, err, waited)
	}
}

func TestZeroTimeoutMeansNoDeadlineButStillCancellable(t *testing.T) {
	handler := requestcontext.Middleware(0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Context().Deadline(); ok {
			t.Error("a zero timeout must not set a deadline")
		}
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}

func TestClientCannotChooseItsOwnTimeout(t *testing.T) {
	handler := requestcontext.Middleware(200 * time.Millisecond)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deadline, _ := r.Context().Deadline()
		if time.Until(deadline) > time.Second {
			t.Errorf("deadline %s away; a client header must not extend it", time.Until(deadline))
		}
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, h := range []string{"X-Request-Timeout", "Request-Timeout", "Grpc-Timeout", "X-Timeout"} {
		req.Header.Set(h, "3600")
	}
	handler.ServeHTTP(httptest.NewRecorder(), req)
}

// recordingTransport captures the request it was asked to send.
type recordingTransport struct{ got *http.Request }

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.got = req
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
}

func TestTransportPropagatesIDsAndNothingElse(t *testing.T) {
	rt := &recordingTransport{}
	client := &http.Client{Transport: requestcontext.Transport(rt)}

	trace := tracecontext.Context{TraceID: goodTrace, SpanID: goodSpan}
	ctx := tracecontext.WithContext(requestid.WithID(context.Background(), "req-9"), trace)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://upstream.internal/x", nil)
	req.Header.Set("Authorization", "Bearer outbound-only")
	req.Header.Set(requestid.Header, "spoofed")
	req.Header.Set(tracecontext.Header, "00-"+strings.Repeat("a", 32)+"-"+strings.Repeat("b", 16)+"-01")
	if resp, err := client.Do(req); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
	}

	if rt.got.Header.Get(requestid.Header) != "req-9" {
		t.Errorf("request id = %q, want the context's, not a spoofed one", rt.got.Header.Get(requestid.Header))
	}
	sent, ok := tracecontext.Parse(rt.got.Header.Get(tracecontext.Header))
	if !ok || sent.TraceID != goodTrace || sent.SpanID == goodSpan {
		t.Errorf("traceparent = %q, want the same trace in a fresh child span", rt.got.Header.Get(tracecontext.Header))
	}
	if rt.got.Header.Get("Authorization") != "Bearer outbound-only" {
		t.Error("headers the caller set itself must be left alone")
	}
	if req.Header.Get(requestid.Header) != "spoofed" {
		t.Error("the caller's original request must not be mutated")
	}
}

func TestTransportWithoutContextStartsAFreshTraceAndOmitsRequestID(t *testing.T) {
	rt := &recordingTransport{}
	req, _ := http.NewRequest(http.MethodGet, "http://upstream.internal/x", nil)
	req.Header.Set(requestid.Header, "stale")
	resp, err := (&http.Client{Transport: requestcontext.Transport(rt)}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if _, ok := tracecontext.Parse(rt.got.Header.Get(tracecontext.Header)); !ok {
		t.Error("a root traceparent must be sent")
	}
	if rt.got.Header.Get(requestid.Header) != "" {
		t.Errorf("no request id in context, so none may be sent, got %q", rt.got.Header.Get(requestid.Header))
	}
}

func TestLoggerAddsTraceFieldsFromContext(t *testing.T) {
	var buf bytes.Buffer
	logger, err := logging.New(logging.Config{Format: "json", Service: "svc"}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	trace := tracecontext.Context{TraceID: goodTrace, SpanID: goodSpan}
	ctx := tracecontext.WithContext(requestid.WithID(context.Background(), "r1"), trace)

	logger.InfoContext(ctx, "step")
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["request_id"] != "r1" || rec["trace_id"] != goodTrace || rec["span_id"] != goodSpan {
		t.Errorf("record = %v", rec)
	}

	buf.Reset()
	logger.InfoContext(ctx, "explicit", "trace_id", "mine")
	if strings.Count(buf.String(), `"trace_id"`) != 1 {
		t.Errorf("trace_id must not be duplicated: %s", buf.String())
	}
	buf.Reset()
	logger.Info("startup")
	if strings.Contains(buf.String(), "trace_id") {
		t.Error("no trace fields outside a request")
	}
}
