package integration_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/postgres"
	"github.com/melmut-42/ft_transcendence/backend/shared/rabbitmq"
	"github.com/melmut-42/ft_transcendence/backend/shared/redisclient"
)

// These tests need real dependencies and skip when their variables are unset:
//
//	RABBITMQ_TEST_URL, REDIS_TEST_URL (AOF on), REDIS_TEST_URL_NO_AOF,
//	POSTGRES_TEST_DATABASE_URL
//
// The *_TEST_CONTAINER variables name a disposable Docker container that the
// outage tests may stop and start. Never point them at a shared stack.

func requireEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Skipf("%s is not set", name)
	}
	return v
}

// docker runs `docker <action> <container>` for outage drills.
func docker(t *testing.T, action, container string) {
	t.Helper()
	if out, err := exec.Command("docker", action, container).CombinedOutput(); err != nil {
		t.Fatalf("docker %s %s: %v: %s", action, container, err, out)
	}
}

// eventually polls fn until it returns nil or the deadline passes.
func eventually(t *testing.T, within time.Duration, fn func() error) error {
	t.Helper()
	deadline := time.Now().Add(within)
	var err error
	for {
		if err = fn(); err == nil || time.Now().After(deadline) {
			return err
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// withPassword swaps the credentials of a test URL for a wrong password.
func wrongPassword(url string) string {
	at := strings.LastIndex(url, "@")
	colon := strings.LastIndex(url[:at], ":")
	return url[:colon+1] + "not-the-password" + url[at:]
}

func TestRabbitMQAgainstRealBroker(t *testing.T) {
	url := requireEnv(t, "RABBITMQ_TEST_URL")
	ctx := context.Background()

	conn, err := rabbitmq.Open(ctx, rabbitmq.Config{URL: url, Name: "shared-itest", ConnectTimeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := conn.Check(ctx); err != nil {
		t.Fatalf("Check on a healthy broker: %v", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("Channel: %v", err)
	}
	_ = ch.Close()

	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("second Close must be a no-op, got %v", err)
	}
	if err := conn.Check(ctx); !errors.Is(err, rabbitmq.ErrUnavailable) {
		t.Fatalf("Check after Close = %v, want ErrUnavailable", err)
	}
}

func TestRabbitMQWrongCredentialsDoNotLeak(t *testing.T) {
	url := requireEnv(t, "RABBITMQ_TEST_URL")
	bad := wrongPassword(url)
	_, err := rabbitmq.Open(context.Background(), rabbitmq.Config{URL: bad, ConnectTimeout: 10 * time.Second})
	if !errors.Is(err, rabbitmq.ErrConnect) {
		t.Fatalf("Open with a wrong password = %v, want ErrConnect", err)
	}
	if strings.Contains(err.Error(), "not-the-password") {
		t.Fatalf("error leaked the password: %v", err)
	}
}

func TestRabbitMQOutageIsDetectedAndRecovered(t *testing.T) {
	url := requireEnv(t, "RABBITMQ_TEST_URL")
	container := requireEnv(t, "RABBITMQ_TEST_CONTAINER")
	cfg := rabbitmq.Config{URL: url, ConnectTimeout: 10 * time.Second}

	conn, err := rabbitmq.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	docker(t, "stop", container)
	t.Cleanup(func() { _ = exec.Command("docker", "start", container).Run() })

	if err := eventually(t, 20*time.Second, func() error {
		if conn.Check(context.Background()) == nil {
			return errors.New("still available")
		}
		return nil
	}); err != nil {
		t.Fatalf("outage was not reported as ErrUnavailable: %v", err)
	}
	if err := conn.Check(context.Background()); !errors.Is(err, rabbitmq.ErrUnavailable) {
		t.Fatalf("Check during outage = %v, want ErrUnavailable", err)
	}

	docker(t, "start", container)
	if err := eventually(t, 90*time.Second, func() error {
		c, err := rabbitmq.Open(context.Background(), cfg)
		if err != nil {
			return err
		}
		defer c.Close()
		return c.Check(context.Background())
	}); err != nil {
		t.Fatalf("a fresh connection could not be opened after the broker restarted: %v", err)
	}
}

func TestRedisAgainstRealServer(t *testing.T) {
	url := requireEnv(t, "REDIS_TEST_URL")
	ctx := context.Background()

	client, err := redisclient.Open(ctx, redisclient.Config{URL: url, RequireAOF: true, PoolSize: 4})
	if err != nil {
		t.Fatalf("Open with RequireAOF against an AOF server: %v", err)
	}
	if err := redisclient.Check(ctx, client); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if err := client.Set(ctx, "shared-itest", "ok", time.Minute).Err(); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := redisclient.Check(ctx, client); !errors.Is(err, redisclient.ErrUnavailable) {
		t.Fatalf("Check after Close = %v, want ErrUnavailable", err)
	}
}

func TestRedisRequireAOFRejectsServerWithoutAOF(t *testing.T) {
	url := requireEnv(t, "REDIS_TEST_URL_NO_AOF")
	if client, err := redisclient.Open(context.Background(), redisclient.Config{URL: url}); err != nil {
		t.Fatalf("Open without RequireAOF must succeed: %v", err)
	} else {
		_ = client.Close()
	}
	_, err := redisclient.Open(context.Background(), redisclient.Config{URL: url, RequireAOF: true})
	if !errors.Is(err, redisclient.ErrAOFRequired) {
		t.Fatalf("Open = %v, want ErrAOFRequired", err)
	}
}

func TestRedisWrongCredentialsDoNotLeak(t *testing.T) {
	url := requireEnv(t, "REDIS_TEST_URL")
	_, err := redisclient.Open(context.Background(), redisclient.Config{URL: wrongPassword(url)})
	if !errors.Is(err, redisclient.ErrConnect) {
		t.Fatalf("Open with a wrong password = %v, want ErrConnect", err)
	}
	if strings.Contains(err.Error(), "not-the-password") {
		t.Fatalf("error leaked the password: %v", err)
	}
}

func TestRedisOutageIsDetectedAndRecovered(t *testing.T) {
	url := requireEnv(t, "REDIS_TEST_URL")
	container := requireEnv(t, "REDIS_TEST_CONTAINER")
	ctx := context.Background()

	client, err := redisclient.Open(ctx, redisclient.Config{URL: url, ConnectTimeout: 2 * time.Second, OperationTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	docker(t, "stop", container)
	t.Cleanup(func() { _ = exec.Command("docker", "start", container).Run() })
	if err := redisclient.Check(ctx, client); !errors.Is(err, redisclient.ErrUnavailable) {
		t.Fatalf("Check during outage = %v, want ErrUnavailable", err)
	}

	docker(t, "start", container)
	if err := eventually(t, 30*time.Second, func() error { return redisclient.Check(ctx, client) }); err != nil {
		t.Fatalf("the pooled client did not recover after the restart: %v", err)
	}
}

func TestPostgresCheckAndPoolLimitsAgainstRealDatabase(t *testing.T) {
	dsn := requireEnv(t, "POSTGRES_TEST_DATABASE_URL")
	ctx := context.Background()

	pool, err := postgres.Open(ctx, postgres.Config{DSN: dsn, MaxConns: 2})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pool.Close()
	if err := postgres.Check(ctx, pool, 0); err != nil {
		t.Fatalf("Check: %v", err)
	}

	// The pool limit holds: with both connections checked out, a third caller
	// waits and then gives up at its own deadline instead of opening a third.
	c1, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Release()
	c2, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Release()
	short, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if _, err := pool.Acquire(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("third Acquire with MaxConns=2 = %v, want a deadline error", err)
	}
}

func TestPostgresOutageIsDetectedAndRecovered(t *testing.T) {
	dsn := requireEnv(t, "POSTGRES_TEST_DATABASE_URL")
	container := requireEnv(t, "POSTGRES_TEST_CONTAINER")
	ctx := context.Background()

	pool, err := postgres.Open(ctx, postgres.Config{DSN: dsn, MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	docker(t, "stop", container)
	t.Cleanup(func() { _ = exec.Command("docker", "start", container).Run() })
	if err := postgres.Check(ctx, pool, time.Second); !errors.Is(err, postgres.ErrUnavailable) {
		t.Fatalf("Check during outage = %v, want ErrUnavailable", err)
	}

	docker(t, "start", container)
	if err := eventually(t, 60*time.Second, func() error { return postgres.Check(ctx, pool, time.Second) }); err != nil {
		t.Fatalf("the pool did not recover after the restart: %v", err)
	}
}
