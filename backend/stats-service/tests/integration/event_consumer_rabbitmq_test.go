package integration_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/melmut-42/ft_transcendence/backend/shared/rabbitmq"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/consumer"
	statspostgres "github.com/melmut-42/ft_transcendence/backend/stats-service/internal/repository/postgres"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/service"
)

// testQueue declares a fresh queue on the broker in STATS_TEST_RABBITMQ_URL
// and returns its name and a channel for publishing. The test is skipped when
// the variable is unset; the queue is deleted at the end.
func testQueue(t *testing.T) (string, string, *amqp.Channel) {
	t.Helper()
	brokerURL := os.Getenv("STATS_TEST_RABBITMQ_URL")
	if brokerURL == "" {
		t.Skip("set STATS_TEST_RABBITMQ_URL to run RabbitMQ integration tests")
	}
	conn, err := amqp.Dial(brokerURL)
	if err != nil {
		t.Fatal("connect to STATS_TEST_RABBITMQ_URL failed")
	}
	t.Cleanup(func() { _ = conn.Close() })
	channel, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	name := "stats-test-" + strings.ToLower(rand.Text())
	if _, err := channel.QueueDeclare(name, true, false, false, false, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = channel.QueueDelete(name, false, false, false) })

	return brokerURL, name, channel
}

func publish(t *testing.T, channel *amqp.Channel, queue string, body []byte) {
	t.Helper()
	err := channel.PublishWithContext(context.Background(), "", queue, false, false,
		amqp.Publishing{ContentType: "application/json", Body: body})
	if err != nil {
		t.Fatal(err)
	}
}

func envelope(eventID, eventType string, payload []byte) []byte {
	return fmt.Appendf(nil, `{"event_id":%q,"event_type":%q,"occurred_at":"2026-09-10T08:50:00Z","payload":%s}`,
		eventID, eventType, payload)
}

// waitFor polls condition until it holds or ten seconds pass.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestEventConsumerRabbitMQ runs the consumer against a real broker and
// database.
//
// Expected output: a finalized match is recorded once even when delivered
// twice; a penalty is applied once per event_id; a cancelled game, a malformed
// envelope and an invalid match are settled without changing counters; the
// queue ends empty with nothing left unacknowledged.
func TestEventConsumerRabbitMQ(t *testing.T) {
	pool := statsPool(t)
	brokerURL, queue, channel := testQueue(t)

	match := payloadOf(t, matchFor(5001, "RED", []int64{1, 2}, []int64{3, 4}))
	publish(t, channel, queue, envelope("e-match-1", "match.finalized", match))
	publish(t, channel, queue, envelope("e-match-2", "match.finalized", match))
	publish(t, channel, queue, envelope("e-penalty-1", "player.penalized", penaltyPayload(3, 5)))
	publish(t, channel, queue, envelope("e-penalty-1", "player.penalized", penaltyPayload(3, 5)))
	publish(t, channel, queue, envelope("e-cancel-1", "game.cancelled", []byte(`{"game_id":5002}`)))
	publish(t, channel, queue, []byte(`not an envelope`))
	publish(t, channel, queue, envelope("e-bad-1", "match.finalized", []byte(`{"game_id":5003}`)))

	handler := consumer.NewHandler(service.NewMatchIngest(statspostgres.NewMatches(pool)),
		service.NewPenaltyIngest(statspostgres.NewPenalties(pool)))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner := consumer.NewRunner(rabbitmq.Config{URL: brokerURL, Name: "stats-test"}, queue, handler, logger).
		WithRetryDelay(10 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runner.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	waitFor(t, "queue drained", func() bool {
		state, err := channel.QueueDeclarePassive(queue, true, false, false, false, nil)
		return err == nil && state.Messages == 0 &&
			count(t, pool, `SELECT count(*) FROM penalties`) == 1 &&
			count(t, pool, `SELECT count(*) FROM matches`) == 1
	})
	cancel()
	<-done

	if got := count(t, pool, `SELECT count(*) FROM matches`); got != 1 {
		t.Fatalf("matches = %d, want 1", got)
	}
	wantCounters(t, pool, 1, 1, 0)
	wantCounters(t, pool, 3, 0, 1)
	if got := count(t, pool, `SELECT penalty_points FROM user_stats WHERE user_id = 3`); got != 5 {
		t.Fatalf("penalty points = %d, want 5", got)
	}
	state, err := channel.QueueDeclarePassive(queue, true, false, false, false, nil)
	if err != nil || state.Messages != 0 {
		t.Fatalf("queue = %+v, %v; want empty", state, err)
	}
}
