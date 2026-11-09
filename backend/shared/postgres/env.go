package postgres

import (
	"fmt"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/config"
)

// ConfigFromEnv reads the service's own PostgreSQL settings through r.
//
//	DATABASE_URL           required postgres:// or postgresql:// URL (this service's own database)
//	DB_CONNECT_TIMEOUT     dial + ping timeout, default 5s
//	DB_MAX_CONNS           pool maximum, default driver value
//	DB_MIN_CONNS           pool minimum, default 0
//	DB_MAX_CONN_LIFETIME   default driver value
//	DB_MAX_CONN_IDLE_TIME  default driver value
//
// Only services that need PostgreSQL call this. Problems are recorded on r; the
// caller checks r.Err() once after reading all settings.
func ConfigFromEnv(r *config.Reader) Config {
	return Config{
		DSN:             r.URL("DATABASE_URL", "postgres", "postgresql"),
		ConnectTimeout:  r.Duration("DB_CONNECT_TIMEOUT", defaultConnectTimeout, time.Second, time.Minute),
		MaxConns:        int32(r.Int("DB_MAX_CONNS", 0, 1, 1000)),
		MinConns:        int32(r.Int("DB_MIN_CONNS", 0, 0, 1000)),
		MaxConnLifetime: r.Duration("DB_MAX_CONN_LIFETIME", 0, time.Minute, 24*time.Hour),
		MaxConnIdleTime: r.Duration("DB_MAX_CONN_IDLE_TIME", 0, time.Second, 24*time.Hour),
	}
}

// String describes cfg without its DSN so logging a Config cannot leak credentials.
func (cfg Config) String() string {
	return fmt.Sprintf("postgres.Config{ConnectTimeout:%s MaxConns:%d MinConns:%d MaxConnLifetime:%s MaxConnIdleTime:%s}",
		cfg.ConnectTimeout, cfg.MaxConns, cfg.MinConns, cfg.MaxConnLifetime, cfg.MaxConnIdleTime)
}

// GoString keeps %#v from printing the DSN.
func (cfg Config) GoString() string { return cfg.String() }
