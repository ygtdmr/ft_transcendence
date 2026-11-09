package config

import (
	"log/slog"
)

const redacted = "[REDACTED]"

// Secret holds a sensitive setting such as a signing key or a password.
//
// Printing it with fmt (any verb), encoding it as JSON or text, or passing it to
// slog yields "[REDACTED]". The real value is only reachable through Reveal.
// The value sits behind a pointer so that even reflection-based printing of a
// struct with an unexported Secret field shows an address, not the secret.
type Secret struct {
	value *string
}

// NewSecret wraps value. An empty value produces an unset Secret.
func NewSecret(value string) Secret {
	if value == "" {
		return Secret{}
	}
	return Secret{value: &value}
}

// Reveal returns the underlying value for use in signing, connecting or hashing.
// Callers must not log or return it.
func (s Secret) Reveal() string {
	if s.value == nil {
		return ""
	}
	return *s.value
}

// IsSet reports whether the secret holds a non-empty value.
func (s Secret) IsSet() bool { return s.value != nil }

// String implements fmt.Stringer.
func (s Secret) String() string { return redacted }

// GoString implements fmt.GoStringer so %#v does not leak the value.
func (s Secret) GoString() string { return redacted }

// MarshalText implements encoding.TextMarshaler.
func (s Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// MarshalJSON implements json.Marshaler.
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }

// LogValue implements slog.LogValuer.
func (s Secret) LogValue() slog.Value { return slog.StringValue(redacted) }
