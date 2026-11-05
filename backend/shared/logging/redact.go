package logging

import (
	"context"
	"log/slog"
	"strings"

	"github.com/melmut-42/ft_transcendence/backend/shared/requestid"
)

// Redacted replaces the value of a sensitive attribute.
const Redacted = "[REDACTED]"

// sensitiveFragments are lower-case substrings of attribute keys whose values
// must never reach a log sink: credentials, tokens, session material and
// authorization headers. Matching is on the key, so "refresh_token",
// "Authorization" and "set-cookie" are all covered.
var sensitiveFragments = []string{
	"password", "passwd", "secret", "token", "jwt", "cookie", "authorization",
	"credential", "api_key", "apikey", "private_key", "session_id", "csrf",
}

// IsSensitiveKey reports whether values logged under key must be redacted.
func IsSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	for _, fragment := range sensitiveFragments {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}

// contextHandler wraps a handler to (1) redact sensitive attribute keys at any
// depth and (2) add request_id from the context when the record lacks one.
// It is applied by New, so every service logger has both behaviours and a
// caller cannot forget them.
type contextHandler struct {
	next slog.Handler
}

func (h contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h contextHandler) Handle(ctx context.Context, record slog.Record) error {
	hasRequestID := false
	clean := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "request_id" {
			hasRequestID = true
		}
		clean.AddAttrs(redactAttr(attr))
		return true
	})
	if id := requestid.FromContext(ctx); id != "" && !hasRequestID {
		clean.AddAttrs(slog.String("request_id", id))
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
