package validate

import (
	"net/http"
	"net/url"
	"slices"
)

// Query reads query-string parameters with the same strictness as bodies. The
// first problem is remembered and every later call returns its fallback, so a
// handler reads all parameters and checks Err once.
type Query struct {
	values url.Values
	err    error
}

// NewQuery parses r's query string. A malformed query string is a 422 on the
// field "query".
func NewQuery(r *http.Request) *Query {
	values, err := url.ParseQuery(r.URL.RawQuery)
	q := &Query{values: values}
	if err != nil {
		q.err = fail("query", "query must be a valid query string.")
		q.values = url.Values{}
	}
	return q
}

// Err returns the first problem found, or nil.
func (q *Query) Err() error { return q.err }

// Only rejects any parameter whose name is not in allowed. The offending name is
// not echoed back.
func (q *Query) Only(allowed ...string) *Query {
	if q.err != nil {
		return q
	}
	for name := range q.values {
		if !slices.Contains(allowed, name) {
			q.err = fail("query", "query contains an unsupported parameter.")
			break
		}
	}
	return q
}

// single returns the parameter's only value. A repeated parameter is a problem:
// which of ?limit=1&limit=500 wins differs between parsers.
func (q *Query) single(name string) (string, bool) {
	if q.err != nil {
		return "", false
	}
	vs := q.values[name]
	switch len(vs) {
	case 0:
		return "", false
	case 1:
		return vs[0], true
	default:
		q.err = fail(name, "%s must be provided only once.", name)
		return "", false
	}
}

// Int returns the integer parameter within [min, max], or def when absent.
func (q *Query) Int(name string, def, min, max int) int {
	raw, ok := q.single(name)
	if !ok {
		return def
	}
	n, err := ParseInt(name, raw, int64(min), int64(max))
	if err != nil {
		q.err = err
		return def
	}
	return int(n)
}

// String returns the parameter (at most maxLen characters, no control
// characters), or def when absent.
func (q *Query) String(name, def string, maxLen int) string {
	raw, ok := q.single(name)
	if !ok {
		return def
	}
	if err := First(MaxLen(name, raw, maxLen), NoControlChars(name, raw)); err != nil {
		q.err = err
		return def
	}
	return raw
}

// Enum returns the parameter if it is one of allowed, or def when absent.
func (q *Query) Enum(name, def string, allowed ...string) string {
	raw, ok := q.single(name)
	if !ok {
		return def
	}
	if err := OneOf(name, raw, allowed...); err != nil {
		q.err = err
		return def
	}
	return raw
}

// Bool returns "true" or "false" (exactly), or def when absent.
func (q *Query) Bool(name string, def bool) bool {
	raw, ok := q.single(name)
	if !ok {
		return def
	}
	switch raw {
	case "true":
		return true
	case "false":
		return false
	}
	q.err = fail(name, "%s must be true or false.", name)
	return def
}
