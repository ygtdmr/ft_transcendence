package unit_test

import (
	"context"
	"strings"
	"testing"

	"github.com/melmut-42/ft_transcendence/backend/shared/logging"
	"github.com/melmut-42/ft_transcendence/backend/shared/rabbitmq"
	"github.com/melmut-42/ft_transcendence/backend/shared/requestid"
	"github.com/melmut-42/ft_transcendence/backend/shared/tracecontext"
	amqp "github.com/rabbitmq/amqp091-go"
)

func correlatedContext(t *testing.T) (context.Context, tracecontext.Context) {
	t.Helper()
	root := tracecontext.NewRoot()
	ctx := tracecontext.WithContext(requestid.WithID(context.Background(), "req-7f3a"), root)

	return ctx, root
}

func TestCorrelationHeadersCarryTheRequestIDAndANewSpanOfTheTrace(t *testing.T) {
	ctx, root := correlatedContext(t)
	headers := rabbitmq.CorrelationHeaders(ctx)
	if headers[rabbitmq.RequestIDHeader] != "req-7f3a" {
		t.Errorf("request id header %v", headers[rabbitmq.RequestIDHeader])
	}
	span, ok := tracecontext.Parse(headers[rabbitmq.TraceparentHeader].(string))
	// A traceparent carries the trace id and the sender's span id (here a new
	// span of the request's trace); it never encodes the grandparent.
	if !ok || span.TraceID != root.TraceID || span.SpanID == root.SpanID {
		t.Fatalf("traceparent %v is not a new span of the request's trace %+v", headers[rabbitmq.TraceparentHeader], root)
	}
	if len(headers) != 2 {
		t.Errorf("headers carry more than correlation: %v", headers)
	}

	// Without a request context: no request id, a new root trace.
	bare := rabbitmq.CorrelationHeaders(context.Background())
	if _, has := bare[rabbitmq.RequestIDHeader]; has {
		t.Error("a request id was invented for a message published outside a request")
	}
	if span, ok := tracecontext.Parse(bare[rabbitmq.TraceparentHeader].(string)); !ok || span.ParentID != "" {
		t.Errorf("bare traceparent %v, want a valid root", bare[rabbitmq.TraceparentHeader])
	}
}

func TestInjectReplacesCorrelationAndKeepsEveryOtherHeader(t *testing.T) {
	ctx, root := correlatedContext(t)
	msg := amqp.Publishing{Headers: amqp.Table{
		rabbitmq.RequestIDHeader:   "stale",
		rabbitmq.TraceparentHeader: "00-" + strings.Repeat("a", 32) + "-" + strings.Repeat("b", 16) + "-01",
		"event_type":               "friend.removed",
	}}
	rabbitmq.Inject(ctx, &msg)
	if msg.Headers[rabbitmq.RequestIDHeader] != "req-7f3a" || msg.Headers["event_type"] != "friend.removed" {
		t.Fatalf("headers %v", msg.Headers)
	}
	if span, _ := tracecontext.Parse(msg.Headers[rabbitmq.TraceparentHeader].(string)); span.TraceID != root.TraceID {
		t.Fatalf("traceparent was not replaced: %v", msg.Headers[rabbitmq.TraceparentHeader])
	}

	// A stale request id is dropped when the publishing context has none.
	stale := amqp.Publishing{Headers: amqp.Table{rabbitmq.RequestIDHeader: "stale"}}
	rabbitmq.Inject(context.Background(), &stale)
	if _, has := stale.Headers[rabbitmq.RequestIDHeader]; has {
		t.Error("a stale request id survived")
	}
	// A message without headers gets them.
	var empty amqp.Publishing
	rabbitmq.Inject(ctx, &empty)
	if empty.Headers[rabbitmq.RequestIDHeader] != "req-7f3a" {
		t.Error("Inject did not create the header table")
	}
}

func TestContextFromRestoresAValidMessageCorrelation(t *testing.T) {
	ctx, _ := correlatedContext(t)
	headers := rabbitmq.CorrelationHeaders(ctx)
	published, _ := tracecontext.Parse(headers[rabbitmq.TraceparentHeader].(string))

	got := rabbitmq.ContextFrom(context.Background(), headers)
	if id := requestid.FromContext(got); id != "req-7f3a" {
		t.Errorf("request id %q", id)
	}
	span, ok := tracecontext.FromContext(got)
	if !ok || span.TraceID != published.TraceID || span.ParentID != published.SpanID || span.SpanID == published.SpanID {
		t.Errorf("consumer span %+v is not a child of the published span %+v", span, published)
	}

	// Some clients deliver long strings as bytes.
	asBytes := amqp.Table{rabbitmq.RequestIDHeader: []byte("req-bytes"), rabbitmq.TraceparentHeader: []byte(published.Header())}
	got = rabbitmq.ContextFrom(context.Background(), asBytes)
	if requestid.FromContext(got) != "req-bytes" {
		t.Errorf("byte headers: request id %q", requestid.FromContext(got))
	}
	if span, _ := tracecontext.FromContext(got); span.TraceID != published.TraceID {
		t.Error("byte headers: trace was not continued")
	}
}

func TestContextFromReplacesInvalidOrMissingCorrelation(t *testing.T) {
	cases := map[string]amqp.Table{
		"missing":       nil,
		"empty":         {rabbitmq.RequestIDHeader: "", rabbitmq.TraceparentHeader: ""},
		"wrong types":   {rabbitmq.RequestIDHeader: int64(42), rabbitmq.TraceparentHeader: true},
		"too long":      {rabbitmq.RequestIDHeader: strings.Repeat("r", 200)},
		"control chars": {rabbitmq.RequestIDHeader: "id\nInjected: 1", rabbitmq.TraceparentHeader: "00-zz-yy-01"},
		"zero trace":    {rabbitmq.TraceparentHeader: "00-" + strings.Repeat("0", 32) + "-" + strings.Repeat("1", 16) + "-01"},
		"upper case":    {rabbitmq.TraceparentHeader: "00-" + strings.Repeat("A", 32) + "-" + strings.Repeat("B", 16) + "-01"},
	}
	seen := map[string]bool{}
	for name, headers := range cases {
		got := rabbitmq.ContextFrom(context.Background(), headers)
		id := requestid.FromContext(got)
		if !requestid.Valid(id) || seen[id] {
			t.Errorf("%s: request id %q, want a new valid one", name, id)
		}
		seen[id] = true
		for _, v := range headers {
			if s, ok := v.(string); ok && s != "" && id == s {
				t.Errorf("%s: an invalid request id was kept", name)
			}
		}
		span, ok := tracecontext.FromContext(got)
		if !ok || span.ParentID != "" {
			t.Errorf("%s: span %+v, want a new root trace", name, span)
		}
	}
}

func TestLogsOfAHandledMessageCarryItsRequestID(t *testing.T) {
	ctx, _ := correlatedContext(t)
	logger, buf := newJSONLogger(t, logging.Config{Service: "consumer-test"})
	logger.InfoContext(rabbitmq.ContextFrom(context.Background(), rabbitmq.CorrelationHeaders(ctx)), "event_handled")
	if record := lastRecord(t, buf); record["request_id"] != "req-7f3a" {
		t.Fatalf("log record %v lacks the message's request_id", record)
	}
}
