// Package postgres holds the Stats Service's PostgreSQL access: its own schema,
// migrations and queries. No other service's tables are read or written here.
package postgres

import (
	"errors"
	"net/url"
)

// Schema is the PostgreSQL schema owned by the Stats Service.
const Schema = "stats"

// ErrInvalidDSN reports a connection string that is not a PostgreSQL URL.
var ErrInvalidDSN = errors.New("invalid PostgreSQL URL")

// DSNWithSchema returns dsn with search_path set to the Stats Service schema.
//
// Parameters:
//
//   - dsn: PostgreSQL URL, for example postgres://stats:pass@host:5432/db?sslmode=disable.
//
// Every connection made with the result resolves unqualified table names inside
// Schema only, so a query can never reach another service's tables by accident.
// Other query parameters are preserved and an existing search_path is replaced.
//
// Errors:
//
//   - ErrInvalidDSN: dsn is not a postgres:// or postgresql:// URL. The error never contains dsn.
func DSNWithSchema(dsn string) (string, error) {
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Host == "" {
		return "", ErrInvalidDSN
	}

	query := parsed.Query()
	query.Set("search_path", Schema)
	parsed.RawQuery = query.Encode()

	return parsed.String(), nil
}
