package unit_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/melmut-42/ft_transcendence/backend/shared/config"
	"github.com/melmut-42/ft_transcendence/backend/shared/postgres"
	"github.com/melmut-42/ft_transcendence/backend/shared/rabbitmq"
	"github.com/melmut-42/ft_transcendence/backend/shared/redisclient"
)

const depPassword = "p4ss-must-never-appear"

// closedAddr returns a loopback address nothing is listening on.
func closedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// silentAddr returns an address that accepts TCP connections and never answers.
func silentAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var conns []net.Conn
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			conns = append(conns, c)
		}
	}()
	t.Cleanup(func() {
		_ = l.Close()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return l.Addr().String()
}

func assertNoSecret(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), depPassword) {
		t.Errorf("%s leaked the password: %v", what, err)
	}
}

func TestReaderIntBounds(t *testing.T) {
	r := config.NewReader(env(map[string]string{"OK": "5", "LOW": "0", "HIGH": "99", "BAD": "x"}))
	if got := r.Int("OK", 1, 1, 10); got != 5 {
		t.Errorf("Int = %d", got)
	}
	r.Int("LOW", 1, 1, 10)
	r.Int("HIGH", 1, 1, 10)
	r.Int("BAD", 1, 1, 10)
	if got := len(problems(t, r.Err())); got != 3 {
		t.Errorf("collected %d problems, want 3", got)
	}
}

func TestConfigsRedactURLsWhenPrinted(t *testing.T) {
	url := "amqp://svc:" + depPassword + "@broker:5672/"
	configs := map[string]fmt.Stringer{
		"rabbitmq": rabbitmq.Config{URL: url, Name: "svc"},
		"redis":    redisclient.Config{URL: "redis://:" + depPassword + "@cache:6379/0"},
		"postgres": postgres.Config{DSN: "postgres://svc:" + depPassword + "@db/app"},
	}
	for name, c := range configs {
		for _, verb := range []string{"%v", "%+v", "%s", "%#v"} {
			if out := fmt.Sprintf(verb, c); strings.Contains(out, depPassword) {
				t.Errorf("%s config printed with %s leaked the password: %s", name, verb, out)
			}
		}
	}
}

func TestDependencyConfigFromEnv(t *testing.T) {
	r := config.NewReader(env(map[string]string{
		"RABBITMQ_URL":      "amqp://u:p@broker:5672/",
		"REDIS_URL":         "redis://cache:6379/1",
		"REDIS_REQUIRE_AOF": "true",
		"REDIS_POOL_SIZE":   "20",
		"DATABASE_URL":      "postgres://u:p@db:5432/app",
		"DB_MAX_CONNS":      "15",
	}))
	mq := rabbitmq.ConfigFromEnv(r, "chat-service")
	rc := redisclient.ConfigFromEnv(r)
	pg := postgres.ConfigFromEnv(r)
	if err := r.Err(); err != nil {
		t.Fatal(err)
	}
	if mq.Name != "chat-service" || mq.ConnectTimeout != 5*time.Second || mq.Heartbeat != 10*time.Second {
		t.Errorf("rabbitmq defaults: %v", mq)
	}
	if !rc.RequireAOF || rc.PoolSize != 20 || rc.OperationTimeout != 3*time.Second {
		t.Errorf("redis config: %v", rc)
	}
	if pg.MaxConns != 15 || pg.ConnectTimeout != 5*time.Second {
		t.Errorf("postgres config: %v", pg)
	}
}

func TestServicesOnlyNeedTheDependenciesTheyRead(t *testing.T) {
	// A service that reads only PostgreSQL is not forced to configure the others.
	r := config.NewReader(env(map[string]string{"DATABASE_URL": "postgres://u:p@db/app"}))
	postgres.ConfigFromEnv(r)
	if err := r.Err(); err != nil {
		t.Fatalf("postgres-only service failed: %v", err)
	}
	// Reading a dependency that is not configured is a clear startup error.
	rabbitmq.ConfigFromEnv(r, "svc")
	redisclient.ConfigFromEnv(r)
	if err := r.Err(); err == nil || !strings.Contains(err.Error(), "RABBITMQ_URL is required") ||
		!strings.Contains(err.Error(), "REDIS_URL is required") {
		t.Fatalf("missing dependency settings must be named: %v", err)
	}
}

func TestRabbitMQRejectsInvalidConfigWithoutEchoingTheURL(t *testing.T) {
	for _, cfg := range []rabbitmq.Config{
		{},
		{URL: "http://svc:" + depPassword + "@broker/"},
		{URL: "amqp://svc:" + depPassword + "@broker/", ConnectTimeout: -time.Second},
		{URL: "://" + depPassword},
	} {
		_, err := rabbitmq.Open(context.Background(), cfg)
		if !errors.Is(err, rabbitmq.ErrInvalidConfig) {
			t.Errorf("Open(%v) = %v, want ErrInvalidConfig", cfg, err)
		}
		assertNoSecret(t, "rabbitmq invalid config", err)
	}
}

func TestRabbitMQConnectionFailuresAreClearAndCredentialSafe(t *testing.T) {
	cfg := rabbitmq.Config{URL: "amqp://svc:" + depPassword + "@" + closedAddr(t) + "/", ConnectTimeout: time.Second}
	_, err := rabbitmq.Open(context.Background(), cfg)
	if !errors.Is(err, rabbitmq.ErrConnect) {
		t.Fatalf("Open = %v, want ErrConnect", err)
	}
	assertNoSecret(t, "rabbitmq connect failure", err)
}

func TestRabbitMQHandshakeTimeoutIsEnforced(t *testing.T) {
	cfg := rabbitmq.Config{URL: "amqp://svc:" + depPassword + "@" + silentAddr(t) + "/", ConnectTimeout: time.Second}
	start := time.Now()
	_, err := rabbitmq.Open(context.Background(), cfg)
	if !errors.Is(err, rabbitmq.ErrConnect) {
		t.Fatalf("Open = %v, want ErrConnect", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Open hung for %s against a silent broker; timeout is 1s", elapsed)
	}
	assertNoSecret(t, "rabbitmq handshake timeout", err)
}

func TestRabbitMQHonoursContextCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	cfg := rabbitmq.Config{URL: "amqp://svc:x@" + silentAddr(t) + "/", ConnectTimeout: 10 * time.Second}
	start := time.Now()
	_, err := rabbitmq.Open(ctx, cfg)
	if !errors.Is(err, rabbitmq.ErrConnect) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Open = %v, want ErrConnect wrapping DeadlineExceeded", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("Open ignored the context deadline")
	}
}

func TestRabbitMQCheckOnClosedConnectionIsUnavailable(t *testing.T) {
	var nilConn *rabbitmq.Connection
	if err := nilConn.Check(context.Background()); !errors.Is(err, rabbitmq.ErrUnavailable) {
		t.Errorf("nil connection Check = %v", err)
	}
	if err := (&rabbitmq.Connection{}).Close(); err != nil {
		t.Errorf("Close of an empty connection = %v", err)
	}
}

func TestRedisRejectsInvalidConfigWithoutEchoingTheURL(t *testing.T) {
	for _, cfg := range []redisclient.Config{
		{},
		{URL: "http://:" + depPassword + "@cache/"},
		{URL: "redis://:" + depPassword + "@cache/", PoolSize: 2, MinIdleConns: 5},
		{URL: "redis://:" + depPassword + "@cache/", ConnectTimeout: -1},
	} {
		_, err := redisclient.Open(context.Background(), cfg)
		if !errors.Is(err, redisclient.ErrInvalidConfig) {
			t.Errorf("Open(%v) = %v, want ErrInvalidConfig", cfg, err)
		}
		assertNoSecret(t, "redis invalid config", err)
	}
}

func TestRedisConnectionFailuresAreClearAndCredentialSafe(t *testing.T) {
	cfg := redisclient.Config{URL: "redis://:" + depPassword + "@" + closedAddr(t) + "/0", ConnectTimeout: time.Second}
	_, err := redisclient.Open(context.Background(), cfg)
	if !errors.Is(err, redisclient.ErrConnect) {
		t.Fatalf("Open = %v, want ErrConnect", err)
	}
	assertNoSecret(t, "redis connect failure", err)
}

func TestRedisPingTimeoutIsEnforced(t *testing.T) {
	cfg := redisclient.Config{URL: "redis://:" + depPassword + "@" + silentAddr(t) + "/0", ConnectTimeout: time.Second}
	start := time.Now()
	_, err := redisclient.Open(context.Background(), cfg)
	if !errors.Is(err, redisclient.ErrConnect) {
		t.Fatalf("Open = %v, want ErrConnect", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Open hung for %s against a silent server; timeout is 1s", elapsed)
	}
	assertNoSecret(t, "redis ping timeout", err)
}

func TestRedisCheckWithoutClientIsUnavailable(t *testing.T) {
	if err := redisclient.Check(context.Background(), nil); !errors.Is(err, redisclient.ErrUnavailable) {
		t.Errorf("Check(nil) = %v", err)
	}
}

func TestPostgresCheckDistinguishesOpenFromAvailable(t *testing.T) {
	if err := postgres.Check(context.Background(), nil, 0); !errors.Is(err, postgres.ErrUnavailable) {
		t.Errorf("Check(nil) = %v", err)
	}

	// pgxpool connects lazily, so a pool can exist while the database is down:
	// "open" is not the same as "available".
	dsn := "postgres://svc:" + depPassword + "@" + closedAddr(t) + "/app?sslmode=disable"
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	err = postgres.Check(context.Background(), pool, time.Second)
	if !errors.Is(err, postgres.ErrUnavailable) {
		t.Fatalf("Check = %v, want ErrUnavailable", err)
	}
	assertNoSecret(t, "postgres check", err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := postgres.Check(ctx, pool, time.Second); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled context must stay visible to the caller, got %v", err)
	}
}
