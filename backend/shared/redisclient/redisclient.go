// Package redisclient opens a service's Redis client with explicit timeouts,
// credential-safe errors, an optional durability check and a readiness check.
//
// Redis here holds ephemeral support data only. It never owns durable game or
// account state; PostgreSQL does. A service that needs its Redis data to survive
// a restart sets RequireAOF, and Open then refuses a server running without the
// append-only file instead of silently losing data. Enabling AOF on the server
// itself is deployment configuration (issue #190).
package redisclient

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/melmut-42/ft_transcendence/backend/shared/config"
)

const (
	defaultConnectTimeout   = 5 * time.Second
	defaultOperationTimeout = 3 * time.Second
	defaultCheckTimeout     = 2 * time.Second
)

var (
	// ErrInvalidConfig reports a configuration problem. Its message never contains the URL.
	ErrInvalidConfig = errors.New("redis: invalid configuration")
	// ErrConnect reports that the server could not be reached or rejected the credentials.
	ErrConnect = errors.New("redis: connection failed; check configuration and server availability")
	// ErrUnavailable reports that the server did not answer a health check.
	ErrUnavailable = errors.New("redis: dependency unavailable")
	// ErrAOFRequired reports that RequireAOF is set but the server's append-only file is off.
	ErrAOFRequired = errors.New("redis: this service requires AOF persistence but the server has it disabled")
)

// Config describes one service's Redis connection.
type Config struct {
	// URL is a redis:// or rediss:// URL that may carry credentials and a database number.
	URL string `json:"-"`
	// ConnectTimeout bounds dialing and the initial ping. Default 5s.
	ConnectTimeout time.Duration
	// OperationTimeout bounds each read and write. Default 3s.
	OperationTimeout time.Duration
	// PoolSize is the maximum socket count. Zero keeps the driver default.
	PoolSize int
	// MinIdleConns keeps this many sockets warm. Zero means none.
	MinIdleConns int
	// RequireAOF makes Open fail unless the server runs with AOF enabled.
	RequireAOF bool
}

// ConfigFromEnv reads the service's Redis settings through r.
//
//	REDIS_URL               required redis:// or rediss:// URL
//	REDIS_CONNECT_TIMEOUT   default 5s
//	REDIS_OPERATION_TIMEOUT default 3s
//	REDIS_POOL_SIZE         default driver value
//	REDIS_REQUIRE_AOF       default false
//
// Only services that use Redis call this. Use OptionalURL on r first when Redis
// is an optional dependency of the service.
func ConfigFromEnv(r *config.Reader) Config {
	return Config{
		URL:              r.URL("REDIS_URL", "redis", "rediss"),
		ConnectTimeout:   r.Duration("REDIS_CONNECT_TIMEOUT", defaultConnectTimeout, time.Second, time.Minute),
		OperationTimeout: r.Duration("REDIS_OPERATION_TIMEOUT", defaultOperationTimeout, 100*time.Millisecond, time.Minute),
		PoolSize:         r.Int("REDIS_POOL_SIZE", 0, 1, 1000),
		RequireAOF:       r.Bool("REDIS_REQUIRE_AOF", false),
	}
}

// Validate checks cfg without connecting.
func (cfg Config) Validate() error {
	parsed, err := url.Parse(cfg.URL)
	if cfg.URL == "" || err != nil || parsed.Host == "" || (parsed.Scheme != "redis" && parsed.Scheme != "rediss") {
		return fmt.Errorf("%w: URL must be a redis:// or rediss:// URL with a host", ErrInvalidConfig)
	}
	if cfg.ConnectTimeout < 0 || cfg.OperationTimeout < 0 || cfg.PoolSize < 0 || cfg.MinIdleConns < 0 {
		return fmt.Errorf("%w: timeouts and pool sizes must not be negative", ErrInvalidConfig)
	}
	if cfg.PoolSize > 0 && cfg.MinIdleConns > cfg.PoolSize {
		return fmt.Errorf("%w: minimum idle connections exceed the pool size", ErrInvalidConfig)
	}
	return nil
}

// Open connects, pings within cfg.ConnectTimeout (and ctx), and, when
// cfg.RequireAOF is set, verifies the server persists to an append-only file.
// The caller owns the client and must Close it during shutdown.
func Open(ctx context.Context, cfg Config) (*redis.Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	opts, err := redis.ParseURL(cfg.URL)
	if err != nil {
		// The parse error can echo the URL, so it is not wrapped.
		return nil, fmt.Errorf("%w: URL could not be parsed", ErrInvalidConfig)
	}
	connectTimeout := orDefault(cfg.ConnectTimeout, defaultConnectTimeout)
	opTimeout := orDefault(cfg.OperationTimeout, defaultOperationTimeout)
	opts.DialTimeout = connectTimeout
	opts.ReadTimeout = opTimeout
	opts.WriteTimeout = opTimeout
	opts.MaxRetries = 0 // fail fast; callers decide whether to retry
	// Without this go-redis ignores context deadlines on socket reads, so neither
	// the connect timeout nor a readiness check's deadline would be enforced.
	opts.ContextTimeoutEnabled = true
	if cfg.PoolSize > 0 {
		opts.PoolSize = cfg.PoolSize
	}
	opts.MinIdleConns = cfg.MinIdleConns

	client := redis.NewClient(opts)
	pingCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w: %w", ErrConnect, ctx.Err())
		}
		return nil, ErrConnect
	}
	if cfg.RequireAOF {
		enabled, err := aofEnabled(pingCtx, client)
		if err != nil {
			_ = client.Close()
			return nil, ErrConnect
		}
		if !enabled {
			_ = client.Close()
			return nil, ErrAOFRequired
		}
	}
	return client, nil
}

func aofEnabled(ctx context.Context, client *redis.Client) (bool, error) {
	info, err := client.Info(ctx, "persistence").Result()
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(info, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "aof_enabled:") {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "aof_enabled:")) == "1", nil
		}
	}
	return false, errors.New("aof_enabled missing from INFO")
}

func orDefault(d, fallback time.Duration) time.Duration {
	if d == 0 {
		return fallback
	}
	return d
}

// Check pings the server within ctx (default 2s) and returns nil or an error
// wrapping ErrUnavailable. Use it from readiness endpoints.
func Check(ctx context.Context, client *redis.Client) error {
	if client == nil {
		return fmt.Errorf("%w: client is not open", ErrUnavailable)
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultCheckTimeout)
		defer cancel()
	}
	if err := client.Ping(ctx).Err(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%w: %w", ErrUnavailable, ctx.Err())
		}
		return ErrUnavailable
	}
	return nil
}

// String describes cfg without its URL so logging a Config cannot leak credentials.
func (cfg Config) String() string {
	return fmt.Sprintf("redisclient.Config{ConnectTimeout:%s OperationTimeout:%s PoolSize:%d MinIdleConns:%d RequireAOF:%t}",
		cfg.ConnectTimeout, cfg.OperationTimeout, cfg.PoolSize, cfg.MinIdleConns, cfg.RequireAOF)
}

// GoString keeps %#v from printing the URL.
func (cfg Config) GoString() string { return cfg.String() }
