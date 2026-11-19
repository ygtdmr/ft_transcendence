package validate

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

func fail(field, format string, args ...any) error {
	return &FieldError{Field: field, Message: fmt.Sprintf(format, args...)}
}

// Required fails when value is empty or only whitespace.
func Required(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fail(field, "%s is required.", field)
	}
	return nil
}

// MinLen fails when value has fewer than n characters (runes, not bytes).
func MinLen(field, value string, n int) error {
	if utf8.RuneCountInString(value) < n {
		return fail(field, "%s must be at least %d characters.", field, n)
	}
	return nil
}

// MaxLen fails when value has more than n characters (runes, not bytes).
func MaxLen(field, value string, n int) error {
	if utf8.RuneCountInString(value) > n {
		return fail(field, "%s must be at most %d characters.", field, n)
	}
	return nil
}

// Match fails unless value matches re. The message names the pattern, which is a
// constant from code (the contract documents these, e.g. ^[A-Za-z0-9]{6}$).
func Match(field, value string, re *regexp.Regexp) error {
	if !re.MatchString(value) {
		return fail(field, "%s must match %s.", field, re.String())
	}
	return nil
}

// OneOf fails unless value is exactly one of allowed (case-sensitive; enums are
// UPPER_SNAKE_CASE).
func OneOf(field, value string, allowed ...string) error {
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	return fail(field, "%s must be one of: %s.", field, strings.Join(allowed, ", "))
}

// IntBetween fails unless min <= value <= max.
func IntBetween(field string, value, min, max int64) error {
	if value < min || value > max {
		return fail(field, "%s must be an integer in %d..%d.", field, min, max)
	}
	return nil
}

// Positive fails unless value > 0.
func Positive(field string, value int64) error {
	if value <= 0 {
		return fail(field, "%s must be a positive integer.", field)
	}
	return nil
}

// NoControlChars fails when value contains a control character (including NUL
// and line breaks). Use it for single-line free text that reaches logs or
// headers, to rule out log and header injection.
func NoControlChars(field, value string) error {
	for _, r := range value {
		if unicode.IsControl(r) {
			return fail(field, "%s must not contain control characters.", field)
		}
	}
	return nil
}

var (
	canonicalUnsigned = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
	canonicalSigned   = regexp.MustCompile(`^(0|-?[1-9][0-9]*)$`)
)

// ParsePositiveInt parses a path or query value such as a user_id. Only the
// canonical decimal form is accepted: no sign, spaces, leading zeros or
// exponent, so "1", "01" and "+1" cannot all name the same resource (which would
// let cache keys and authorization checks disagree). Zero, negative values and
// values beyond int64 fail with "<field> must be a positive integer."
func ParsePositiveInt(field, raw string) (int64, error) {
	if !canonicalUnsigned.MatchString(raw) {
		return 0, fail(field, "%s must be a positive integer.", field)
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return 0, fail(field, "%s must be a positive integer.", field)
	}
	return n, nil
}

// ParseInt parses a canonical decimal integer within [min, max].
func ParseInt(field, raw string, min, max int64) (int64, error) {
	if !canonicalSigned.MatchString(raw) {
		return 0, fail(field, "%s must be an integer in %d..%d.", field, min, max)
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < min || n > max {
		return 0, fail(field, "%s must be an integer in %d..%d.", field, min, max)
	}
	return n, nil
}
