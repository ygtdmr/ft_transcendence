package config

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// LookupFunc reports the value of an environment variable and whether it is set.
// os.LookupEnv satisfies it; tests pass a map-backed function.
type LookupFunc func(name string) (string, bool)

// Problem is one invalid or missing setting. Message never contains the value.
type Problem struct {
	Name    string
	Message string
}

// ValidationError lists every problem found while reading settings.
type ValidationError struct {
	Problems []Problem
}

// Error joins all problems into one line suitable for a startup log.
func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		parts[i] = p.Name + " " + p.Message
	}
	return "invalid configuration: " + strings.Join(parts, "; ")
}

// Reader reads typed settings and accumulates problems. Each method returns the
// fallback (or a zero value) on failure so reading can continue; call Err once
// after reading everything. A Reader is not safe for concurrent use.
type Reader struct {
	lookup   LookupFunc
	problems []Problem
}

// NewReader returns a Reader over lookup, or over the process environment when
// lookup is nil.
func NewReader(lookup LookupFunc) *Reader {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	return &Reader{lookup: lookup}
}

// Err returns a *ValidationError describing every problem found so far, or nil.
func (r *Reader) Err() error {
	if len(r.problems) == 0 {
		return nil
	}
	return &ValidationError{Problems: append([]Problem(nil), r.problems...)}
}

// value returns the trimmed value and whether it is non-empty.
func (r *Reader) value(name string) (string, bool) {
	raw, _ := r.lookup(name)
	raw = strings.TrimSpace(raw)
	return raw, raw != ""
}

func (r *Reader) fail(name, message string) {
	r.problems = append(r.problems, Problem{Name: name, Message: message})
}

// Problem records a problem found by validation code outside this package (for
// example a cross-variable rule). The message must name the variable and the
// rule, never a value.
func (r *Reader) Problem(name, message string) { r.fail(name, message) }

// String returns the setting, or fallback when it is unset or blank.
func (r *Reader) String(name, fallback string) string {
	if v, ok := r.value(name); ok {
		return v
	}
	return fallback
}

// Required returns the setting and records a problem when it is unset or blank.
func (r *Reader) Required(name string) string {
	v, ok := r.value(name)
	if !ok {
		r.fail(name, "is required")
	}
	return v
}

// Enum returns the setting when it is one of allowed, or fallback when unset.
// An unsupported value records a problem and returns fallback.
func (r *Reader) Enum(name, fallback string, allowed ...string) string {
	v, ok := r.value(name)
	if !ok {
		return fallback
	}
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	r.fail(name, "must be one of: "+strings.Join(allowed, ", "))
	return fallback
}

// Port returns a TCP port between 1 and 65535, or fallback when unset.
func (r *Reader) Port(name string, fallback int) int {
	v, ok := r.value(name)
	if !ok {
		return fallback
	}
	port, err := strconv.Atoi(v)
	if err != nil || port < 1 || port > 65535 {
		r.fail(name, "must be an integer between 1 and 65535")
		return fallback
	}
	return port
}

// Int returns an integer within [min, max], or fallback when unset.
func (r *Reader) Int(name string, fallback, min, max int) int {
	v, ok := r.value(name)
	if !ok {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < min || n > max {
		r.fail(name, fmt.Sprintf("must be an integer between %d and %d", min, max))
		return fallback
	}
	return n
}

// Duration returns a Go duration within [min, max], or fallback when unset.
// A max of zero means no upper bound.
func (r *Reader) Duration(name string, fallback, min, max time.Duration) time.Duration {
	v, ok := r.value(name)
	if !ok {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		r.fail(name, "must be a valid Go duration such as 30s or 15m")
		return fallback
	}
	if d < min || (max > 0 && d > max) {
		bound := "at least " + min.String()
		if max > 0 {
			bound = fmt.Sprintf("between %s and %s", min, max)
		}
		r.fail(name, "must be "+bound)
		return fallback
	}
	return d
}

// Bool returns a boolean as understood by strconv.ParseBool, or fallback when unset.
func (r *Reader) Bool(name string, fallback bool) bool {
	v, ok := r.value(name)
	if !ok {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		r.fail(name, "must be true or false")
		return fallback
	}
	return b
}

// URL returns a required absolute URL whose scheme is one of schemes and which
// has a host. Credentials inside the URL are kept but never reported.
func (r *Reader) URL(name string, schemes ...string) string {
	return r.url(name, true, schemes)
}

// OptionalURL is like URL but returns "" when the setting is unset.
func (r *Reader) OptionalURL(name string, schemes ...string) string {
	return r.url(name, false, schemes)
}

func (r *Reader) url(name string, required bool, schemes []string) string {
	v, ok := r.value(name)
	if !ok {
		if required {
			r.fail(name, "is required")
		}
		return ""
	}
	// The parse error is dropped on purpose: net/url echoes the input,
	// which may contain a password.
	parsed, err := url.Parse(v)
	if err != nil || parsed.Host == "" || !contains(schemes, parsed.Scheme) {
		r.fail(name, "must be a URL with a host and scheme "+strings.Join(schemes, " or "))
		return ""
	}
	return v
}

// Address returns a host:port listen or dial address, or fallback when unset.
// The host may be empty (":8080"); the port must be 1-65535.
func (r *Reader) Address(name, fallback string) string {
	v, ok := r.value(name)
	if !ok {
		return fallback
	}
	_, portText, err := net.SplitHostPort(v)
	if port, convErr := strconv.Atoi(portText); err != nil || convErr != nil || port < 1 || port > 65535 {
		r.fail(name, "must be a host:port address with a port between 1 and 65535")
		return fallback
	}
	return v
}

// Secret returns a required secret of at least minBytes bytes. A problem names
// the variable and the minimum length, never the value or its actual length.
func (r *Reader) Secret(name string, minBytes int) Secret {
	v, ok := r.value(name)
	if !ok {
		r.fail(name, "is required")
		return Secret{}
	}
	if len(v) < minBytes {
		r.fail(name, fmt.Sprintf("must contain at least %d bytes", minBytes))
		return Secret{}
	}
	return NewSecret(v)
}

// maxSecretFile caps how much of a secret file is read, so a wrong path (a log,
// a device) cannot make startup read gigabytes.
const maxSecretFile = 4 << 10

// SecretFile reads a secret from the file whose path is in the variable name,
// the way mounted deployment secrets arrive. Surrounding whitespace (the
// trailing newline `openssl rand -hex 32 > file` writes) is trimmed. A problem
// names the variable and the minimum length, never the path or the contents,
// because a path is deployment detail. An unset variable is a problem.
func (r *Reader) SecretFile(name string, minBytes int) Secret {
	path, ok := r.value(name)
	if !ok {
		r.fail(name, "is required")
		return Secret{}
	}
	return r.readSecretFile(name, path, minBytes)
}

// OptionalSecretFile is like SecretFile but returns an unset Secret, with no
// problem, when the variable is unset. An unreadable or too-short file is still
// a problem: a half-configured secret must not silently disable a feature.
func (r *Reader) OptionalSecretFile(name string, minBytes int) Secret {
	path, ok := r.value(name)
	if !ok {
		return Secret{}
	}
	return r.readSecretFile(name, path, minBytes)
}

func (r *Reader) readSecretFile(name, path string, minBytes int) Secret {
	file, err := os.Open(path)
	if err != nil {
		r.fail(name, "must name a readable file")
		return Secret{}
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxSecretFile+1))
	if err != nil || len(raw) > maxSecretFile {
		r.fail(name, "must name a readable file of at most 4096 bytes")
		return Secret{}
	}
	value := strings.TrimSpace(string(raw))
	if len(value) < minBytes {
		r.fail(name, fmt.Sprintf("file must contain at least %d bytes", minBytes))
		return Secret{}
	}
	return NewSecret(value)
}

func contains(list []string, item string) bool {
	for _, s := range list {
		if s == item {
			return true
		}
	}
	return false
}
