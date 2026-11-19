package logging

import (
	"context"
	"log/slog"

	"github.com/melmut-42/ft_transcendence/backend/shared/requestid"
	"github.com/melmut-42/ft_transcendence/backend/shared/sensitive"
	"github.com/melmut-42/ft_transcendence/backend/shared/tracecontext"
)

// Redacted replaces the value of a sensitive attribute.
const Redacted = sensitive.Redacted

// IsSensitiveKey reports whether values logged under key must be redacted. The
// rule is shared with API error responses (see package sensitive).
func IsSensitiveKey(key string) bool { return sensitive.IsKey(key) }

// contextHandler wraps a handler to (1) redact sensitive attribute keys at any
// depth and (2) add request_id, trace_id and span_id from the context when the
// record lacks them.
// It is applied by New, so every service logger has both behaviours and a
// caller cannot forget them.
type contextHandler struct {
	next slog.Handler
}

func (h contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h contextHandler) Handle(ctx context.Context, record slog.Record) error {
	present := map[string]bool{}
	clean := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		present[attr.Key] = true
		clean.AddAttrs(redactAttr(attr))
		return true
	})
	if id := requestid.FromContext(ctx); id != "" && !present["request_id"] {
		clean.AddAttrs(slog.String("request_id", id))
	}
	if trace, ok := tracecontext.FromContext(ctx); ok {
		if !present["trace_id"] {
			clean.AddAttrs(slog.String("trace_id", trace.TraceID))
		}
		if !present["span_id"] {
			clean.AddAttrs(slog.String("span_id", trace.SpanID))
		}
	}
	return h.next.Handle(ctx, clean)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, len(attrs))
	for i, attr := range attrs {
		redacted[i] = redactAttr(attr)
	}
	return contextHandler{next: h.next.WithAttrs(redacted)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{next: h.next.WithGroup(name)}
}

func redactAttr(attr slog.Attr) slog.Attr {
	if IsSensitiveKey(attr.Key) {
		return slog.String(attr.Key, Redacted)
	}
	value := attr.Value.Resolve()
	if value.Kind() == slog.KindGroup {
		group := value.Group()
		cleaned := make([]slog.Attr, len(group))
		for i, member := range group {
			cleaned[i] = redactAttr(member)
		}
		return slog.Attr{Key: attr.Key, Value: slog.GroupValue(cleaned...)}
	}
	return attr
}
