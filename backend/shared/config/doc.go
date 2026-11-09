// Package config loads and validates service settings from environment
// variables with one set of names and rules for every backend service.
//
// A service builds a Reader, reads each setting through it, and checks Err once.
// Every problem is collected, so a misconfigured deployment reports all of its
// mistakes in a single startup failure instead of one per restart:
//
//	r := config.NewReader(nil) // nil reads the process environment
//	common := r.Common("auth-service", 8080)
//	databaseURL := r.URL("DATABASE_URL", "postgres", "postgresql")
//	jwtSecret := r.Secret("JWT_SECRET", 32)
//	if err := r.Err(); err != nil {
//		return err
//	}
//
// Error messages name the variable and the violated rule but never the value,
// so a bad password-bearing URL or a short secret cannot leak through logs.
// Secrets are returned as Secret, which redacts itself in fmt, JSON and slog.
//
// # Shared variable names
//
// Every service uses these names with these meanings:
//
//	APP_ENV      development (default), production or test
//	LOG_FORMAT   text or json; defaults by APP_ENV (production: json)
//	LOG_LEVEL    debug, info, warn or error; defaults by APP_ENV
//	PORT         HTTP listen port, 1-65535
//	DATABASE_URL postgres:// or postgresql:// URL of the service's own database
//
// Service-specific settings (JWT_SECRET, ACCESS_TOKEN_TTL, ...) use the same
// Reader methods and UPPER_SNAKE_CASE names. The package does not load .env
// files; the deployment supplies the environment.
package config
