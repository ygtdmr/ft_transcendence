// Package sensitive decides which names denote secret material, so logs and
// error responses apply one rule. It is a leaf package with no dependencies.
package sensitive

import "strings"

// Redacted replaces a sensitive value in logs and responses.
const Redacted = "[REDACTED]"

// fragments are lower-case substrings of names whose values must never reach a
// log sink or an API error: credentials, tokens, session material and
// authorization headers. Matching is on the name, so "refresh_token",
// "Authorization" and "set-cookie" are all covered. It is deliberately broad:
// a false positive costs a redacted field, a false negative leaks a secret.
var fragments = []string{
	"password", "passwd", "secret", "token", "jwt", "cookie", "authorization",
	"credential", "api_key", "apikey", "private_key", "session_id", "csrf",
}

// IsKey reports whether values stored under name must be redacted.
func IsKey(name string) bool {
	lower := strings.ToLower(name)
	for _, fragment := range fragments {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}
