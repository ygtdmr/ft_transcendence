package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/rabbitmq"
	"github.com/melmut-42/ft_transcendence/backend/shared/requestid"
	"github.com/melmut-42/ft_transcendence/backend/shared/tracecontext"
	amqp "github.com/rabbitmq/amqp091-go"
)

// TestCorrelationSurvivesARealBroker publishes a message with a request's
// correlation and restores it on the consuming side, through RabbitMQ.
func TestCorrelationSurvivesARealBroker(t *testing.T) {
	url := requireEnv(t, "RABBITMQ_TEST_URL")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	conn, err := rabbitmq.Open(ctx, rabbitmq.Config{URL: url, Name: "shared-itest-correlation", ConnectTimeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	queue, err := ch.QueueDeclare("", false, true, true, false, nil) // server-named, exclusive, auto-delete
	if err != nil {
		t.Fatal(err)
	}

	root := tracecontext.NewRoot()
	requestCtx := tracecontext.WithContext(requestid.WithID(ctx, "req-broker-1"), root)
	msg := amqp.Publishing{ContentType: "application/json", Body: []byte(`{"type":"friend.removed"}`),
		Headers: amqp.Table{"event_type": "friend.removed"}}
	rabbitmq.Inject(requestCtx, &msg)
	published, _ := tracecontext.Parse(msg.Headers[rabbitmq.TraceparentHeader].(string))
	if err := ch.PublishWithContext(ctx, "", queue.Name, false, false, msg); err != nil {
		t.Fatalf("publish: %v", err)
	}

	deliveries, err := ch.ConsumeWithContext(ctx, queue.Name, "", false, true, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-deliveries:
		handling := rabbitmq.ContextFrom(context.Background(), d.Headers)
		if id := requestid.FromContext(handling); id != "req-broker-1" {
			t.Errorf("request id after the broker: %q", id)
		}
		span, ok := tracecontext.FromContext(handling)
		if !ok || span.TraceID != root.TraceID || span.ParentID != published.SpanID {
			t.Errorf("consumer span %+v is not a child of the published span %+v", span, published)
		}
		if d.Headers["event_type"] != "friend.removed" {
			t.Errorf("other headers were lost: %v", d.Headers)
		}
		if err := d.Ack(false); err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("no delivery")
	}
}
