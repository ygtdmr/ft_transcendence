package rabbitmq

import (
	"context"

	"github.com/melmut-42/ft_transcendence/backend/shared/requestid"
	"github.com/melmut-42/ft_transcendence/backend/shared/tracecontext"
	amqp "github.com/rabbitmq/amqp091-go"
)

// Message header names carrying correlation. They are the HTTP names in
// lower case, so one request id and one trace follow a change from the
// browser request through the outbox to every consumer's logs.
const (
	RequestIDHeader   = "x-request-id"
	TraceparentHeader = "traceparent"
)

// CorrelationHeaders returns the correlation of ctx as message headers: the
// request id when ctx has one, and a new span of ctx's trace (a new root trace
// when ctx has none).
//
// A producer that writes to a transactional outbox calls it in the request,
// stores the result with the outbox row, and publishes those headers later,
// so the relay's own context never replaces the original request's. Nothing
// else from ctx is copied: no identity, credential or cookie.
func CorrelationHeaders(ctx context.Context) amqp.Table {
	headers := amqp.Table{}
	if id := requestid.FromContext(ctx); id != "" {
		headers[RequestIDHeader] = id
	}
	span := tracecontext.NewRoot()
	if current, ok := tracecontext.FromContext(ctx); ok {
		span = current.Child()
	}
	headers[TraceparentHeader] = span.Header()

	return headers
}

// Inject sets ctx's correlation on msg (see CorrelationHeaders). It replaces
// any value already under the two correlation names, drops a request id that
// ctx does not have, and keeps every other header.
func Inject(ctx context.Context, msg *amqp.Publishing) {
	if msg.Headers == nil {
		msg.Headers = amqp.Table{}
	}
	delete(msg.Headers, RequestIDHeader)
	for name, value := range CorrelationHeaders(ctx) {
		msg.Headers[name] = value
	}
}

// ContextFrom returns ctx carrying the correlation of a received message, for
// handling and logging it (logger.InfoContext(ctx, ...) then records the
// message's request_id):
//
//   - the message's request id when it is valid, otherwise a new one, so each
//     message's records still belong together;
//   - a span that is a child of the message's trace when its traceparent is
//     valid, otherwise a new root trace.
//
// Invalid values are dropped, never copied into the context or a log. Pass the
// delivery's headers: ContextFrom(ctx, delivery.Headers).
func ContextFrom(ctx context.Context, headers amqp.Table) context.Context {
	id, ok := headerString(headers, RequestIDHeader)
	if !ok || !requestid.Valid(id) {
		id = requestid.New()
	}
	ctx = requestid.WithID(ctx, id)

	span := tracecontext.NewRoot()
	if raw, ok := headerString(headers, TraceparentHeader); ok {
		if parent, valid := tracecontext.Parse(raw); valid {
			span = parent.Child()
		}
	}

	return tracecontext.WithContext(ctx, span)
}

// headerString reads a text header; AMQP clients send long strings as string
// or as bytes. Any other type counts as absent.
func headerString(headers amqp.Table, name string) (string, bool) {
	switch v := headers[name].(type) {
	case string:
		return v, true
	case []byte:
		return string(v), true
	default:
		return "", false
	}
}
