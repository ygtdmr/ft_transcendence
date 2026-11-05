package httpresponse

import (
	"net/http"

	"github.com/melmut-42/ft_transcendence/backend/shared/sensitive"
)

// setAPIHeaders adds the headers every API response carries. API bodies can hold
// personal or session-scoped data, so shared caches must not store them, and
// browsers must not second-guess the declared JSON type.
func setAPIHeaders(h http.Header) {
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
}

// sanitizeDetails returns a copy of details that is never nil and from which
// credential-like keys (password, token, cookie, authorization, ...) are omitted
// at any depth, so a diagnostic cannot become a leak and does not even hint that
// such a field exists. The caller's map is not modified.
func sanitizeDetails(details map[string]any) map[string]any {
	out := make(map[string]any, len(details))
	for key, value := range details {
		if sensitive.IsKey(key) {
			continue
		}
		out[key] = sanitizeValue(value)
	}
	return out
}

func sanitizeValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return sanitizeDetails(v)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = sanitizeValue(item)
		}
		return out
	default:
		return value
	}
}

// Slice returns s, or an empty non-nil slice when s is nil, so a list response
// encodes as [] and never as null.
func Slice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// Respond maps a service result to its response DTO and writes it as {"data": dto}.
// Keeping the mapping explicit stops service or database structs from being
// serialised straight onto the wire, where a new field would leak by default.
func Respond[T, D any](writer http.ResponseWriter, status int, result T, toDTO func(T) D) {
	WriteData(writer, status, toDTO(result))
}
