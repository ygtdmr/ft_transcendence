// Package rabbitmq opens and supervises a service's RabbitMQ connection with
// explicit timeouts, credential-safe errors and a readiness check.
//
// It only provides connectivity. Exchanges, queues, publisher confirms and
// retry policy are the transport layer of issue #189.
package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/melmut-42/ft_transcendence/backend/shared/config"
)

const (
	defaultConnectTimeout = 5 * time.Second
	defaultHeartbeat      = 10 * time.Second
	defaultCheckTimeout   = 2 * time.Second
)

var (
	// ErrInvalidConfig reports a configuration problem. Its message never contains the URL.
	ErrInvalidConfig = errors.New("rabbitmq: invalid configuration")
	// ErrConnect reports that the broker could not be reached or refused the handshake.
	// The cause is dropped because it can contain host or credential details.
	ErrConnect = errors.New("rabbitmq: connection failed; check configuration and broker availability")
	// ErrUnavailable reports that an open connection is no longer usable.
	ErrUnavailable = errors.New("rabbitmq: dependency unavailable")
)

// Config describes one service's broker connection.
type Config struct {
	// URL is an amqp:// or amqps:// URL that may carry the service's credentials.
	URL string `json:"-"`
	// Name labels the connection in the broker's management UI (usually the service name).
	Name string
	// ConnectTimeout bounds the TCP dial and the AMQP handshake. Default 5s.
	ConnectTimeout time.Duration
	// Heartbeat is the AMQP heartbeat interval. Default 10s.
	Heartbeat time.Duration
}

// ConfigFromEnv reads the service's broker settings through r.
//
//	RABBITMQ_URL             required amqp:// or amqps:// URL
//	RABBITMQ_CONNECT_TIMEOUT dial + handshake timeout, default 5s
//	RABBITMQ_HEARTBEAT       default 10s
//
// Only services that publish or consume call this.
func ConfigFromEnv(r *config.Reader, service string) Config {
	return Config{
		URL:            r.URL("RABBITMQ_URL", "amqp", "amqps"),
		Name:           service,
		ConnectTimeout: r.Duration("RABBITMQ_CONNECT_TIMEOUT", defaultConnectTimeout, time.Second, time.Minute),
		Heartbeat:      r.Duration("RABBITMQ_HEARTBEAT", defaultHeartbeat, time.Second, time.Minute),
	}
}

// Validate checks cfg without connecting.
func (cfg Config) Validate() error {
	parsed, err := url.Parse(cfg.URL)
	if cfg.URL == "" || err != nil || parsed.Host == "" || (parsed.Scheme != "amqp" && parsed.Scheme != "amqps") {
		return fmt.Errorf("%w: URL must be an amqp:// or amqps:// URL with a host", ErrInvalidConfig)
	}
	if cfg.ConnectTimeout < 0 || cfg.Heartbeat < 0 {
		return fmt.Errorf("%w: timeouts must not be negative", ErrInvalidConfig)
	}
	return nil
}

// Connection is an open broker connection owned by one service.
type Connection struct {
	conn      *amqp.Connection
	closeOnce sync.Once
	closeErr  error
}

// Open connects to the broker within cfg.ConnectTimeout (and ctx) and returns
// the connection. Failures return ErrInvalidConfig or ErrConnect, never the URL.
func Open(ctx context.Context, cfg Config) (*Connection, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	timeout := cfg.ConnectTimeout
	if timeout == 0 {
		timeout = defaultConnectTimeout
	}
	heartbeat := cfg.Heartbeat
	if heartbeat == 0 {
		heartbeat = defaultHeartbeat
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConnect, err)
	}

	props := amqp.NewConnectionProperties()
	if cfg.Name != "" {
		props.SetClientConnectionName(cfg.Name)
	}

	type result struct {
		conn *amqp.Connection
		err  error
	}
	done := make(chan result, 1)
	go func() {
		// DefaultDial applies the timeout to the TCP dial and to the TLS/AMQP
		// handshake, so a broker that accepts but never answers cannot hang Open.
		conn, err := amqp.DialConfig(cfg.URL, amqp.Config{
			Heartbeat:  heartbeat,
			Dial:       amqp.DefaultDial(timeout),
			Properties: props,
		})
		done <- result{conn, err}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			return nil, ErrConnect
		}
		return &Connection{conn: res.conn}, nil
	case <-ctx.Done():
		// Reap a connection that completes after we gave up.
		go func() {
			if res := <-done; res.conn != nil {
				_ = res.conn.Close()
			}
		}()
		return nil, fmt.Errorf("%w: %w", ErrConnect, ctx.Err())
	}
}

// Channel opens a new AMQP channel. Channels are not goroutine-safe; use one per goroutine.
func (c *Connection) Channel() (*amqp.Channel, error) {
	ch, err := c.conn.Channel()
	if err != nil {
		return nil, ErrUnavailable
	}
	return ch, nil
}

// NotifyClose registers a receiver for connection-level closure so callers can reconnect.
func (c *Connection) NotifyClose(receiver chan *amqp.Error) chan *amqp.Error {
	return c.conn.NotifyClose(receiver)
}

// Check verifies that the connection is open and that the broker still answers
// a channel open within ctx (default 2s). It returns nil or ErrUnavailable
// (wrapping ctx's error when ctx ended first). Use it from readiness endpoints.
func (c *Connection) Check(ctx context.Context) error {
	if c == nil || c.conn == nil || c.conn.IsClosed() {
		return fmt.Errorf("%w: connection is closed", ErrUnavailable)
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultCheckTimeout)
		defer cancel()
	}

	done := make(chan error, 1)
	go func() {
		ch, err := c.conn.Channel()
		if err == nil {
			err = ch.Close()
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			return ErrUnavailable
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", ErrUnavailable, ctx.Err())
	}
}

// Close shuts the connection down. It is safe to call more than once and from
// shutdown hooks; every call returns the first call's result.
func (c *Connection) Close() error {
	c.closeOnce.Do(func() {
		if c.conn != nil && !c.conn.IsClosed() {
			if err := c.conn.Close(); err != nil && !errors.Is(err, amqp.ErrClosed) {
				c.closeErr = ErrUnavailable
			}
		}
	})
	return c.closeErr
}

// String describes cfg without its URL so logging a Config cannot leak credentials.
func (cfg Config) String() string {
	return fmt.Sprintf("rabbitmq.Config{Name:%q ConnectTimeout:%s Heartbeat:%s}", cfg.Name, cfg.ConnectTimeout, cfg.Heartbeat)
}

// GoString keeps %#v from printing the URL.
func (cfg Config) GoString() string { return cfg.String() }
