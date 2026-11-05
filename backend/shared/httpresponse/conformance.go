package httpresponse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/sensitive"
)

var (
	snakeCase      = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	upperSnakeCase = regexp.MustCompile(`^[A-Z][A-Z0-9]*(_[A-Z0-9]+)*$`)
	timestampShape = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}`)
)

// Violations checks a response against the API conventions and returns one
// message per problem; an empty result means the response conforms. It is meant
// for service tests that capture a real response:
//
//   - 204 has an empty body; other 2xx bodies are exactly {"data": ...};
//   - 4xx/5xx bodies are exactly {"error": {"code","message","details"}} with an
//     UPPER_SNAKE_CASE code, a message, and an object for details;
//   - every JSON object key is snake_case;
//   - every string that looks like a timestamp is RFC 3339 and ends in "Z" (UTC).
func Violations(status int, body []byte) []string {
	var out []string
	trimmed := bytes.TrimSpace(body)

	if status == http.StatusNoContent {
		if len(trimmed) != 0 {
			out = append(out, "204 response must have an empty body")
		}
		return out
	}

	var doc map[string]any
	if err := json.Unmarshal(trimmed, &doc); err != nil {
		return append(out, "body is not a JSON object")
	}

	if status >= 400 {
		out = append(out, checkErrorEnvelope(doc)...)
	} else if len(doc) != 1 || !hasKey(doc, "data") {
		out = append(out, `success body must contain exactly one key, "data"`)
	}
	walk("", doc, &out)
	sort.Strings(out)
	return out
}

func hasKey(m map[string]any, key string) bool { _, ok := m[key]; return ok }

func checkErrorEnvelope(doc map[string]any) []string {
	var out []string
	inner, ok := doc["error"].(map[string]any)
	if len(doc) != 1 || !ok {
		return []string{`error body must contain exactly one key, "error", holding an object`}
	}
	code, _ := inner["code"].(string)
	if !upperSnakeCase.MatchString(code) {
		out = append(out, "error.code must be UPPER_SNAKE_CASE")
	}
	if message, _ := inner["message"].(string); message == "" {
		out = append(out, "error.message must be a non-empty string")
	}
	if _, ok := inner["details"].(map[string]any); !ok {
		out = append(out, "error.details must be an object")
	}
	for key := range inner {
		if key != "code" && key != "message" && key != "details" {
			out = append(out, fmt.Sprintf("error has unexpected key %q", key))
		}
	}
	if details, ok := inner["details"].(map[string]any); ok {
		out = append(out, sensitiveKeys("error.details", details)...)
	}
	return out
}

func walk(path string, value any, out *[]string) {
	switch v := value.(type) {
	case map[string]any:
		for key, member := range v {
			child := key
			if path != "" {
				child = path + "." + key
			}
			if !snakeCase.MatchString(key) {
				*out = append(*out, fmt.Sprintf("key %q is not snake_case", child))
			}
			walk(child, member, out)
		}
	case []any:
		for i, member := range v {
			walk(fmt.Sprintf("%s[%d]", path, i), member, out)
		}
	case string:
		if timestampShape.MatchString(v) {
			if _, err := time.Parse(time.RFC3339Nano, v); err != nil || !strings.HasSuffix(v, "Z") {
				*out = append(*out, fmt.Sprintf("%s: timestamp must be RFC 3339 in UTC ending in Z", path))
			}
		}
	}
}

// sensitiveKeys reports credential-like keys under details at any depth.
func sensitiveKeys(path string, value any) []string {
	var out []string
	switch v := value.(type) {
	case map[string]any:
		for key, member := range v {
			child := path + "." + key
			if sensitive.IsKey(key) {
				out = append(out, fmt.Sprintf("%s: error details must not contain a sensitive key", child))
				continue
			}
			out = append(out, sensitiveKeys(child, member)...)
		}
	case []any:
		for i, member := range v {
			out = append(out, sensitiveKeys(fmt.Sprintf("%s[%d]", path, i), member)...)
		}
	}
	return out
}
