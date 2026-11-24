package consumer

import (
	"context"
	"log/slog"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/melmut-42/ft_transcendence/backend/shared/rabbitmq"
)

const (
	consumerTag     = "stats-service"
	defaultPrefetch = 16
	// retryDelay holds a failed delivery before it is requeued, so an outage
	// does not turn into a redelivery loop.
	retryDelay        = 5 * time.Second
	minReconnectDelay = time.Second
	maxReconnectDelay = 30 * time.Second
)

// Runner consumes one queue with manual acknowledgements and reconnects
// after a lost connection or channel. It never declares exchanges, queues or
// bindings: those belong to the broker topology.
type Runner struct {
	broker     rabbitmq.Config
	queue      string
	handler    *Handler
	logger     *slog.Logger
	prefetch   int
	retryDelay time.Duration
}

// NewRunner returns a consumer for queue on broker.
func NewRunner(broker rabbitmq.Config, queue string, handler *Handler, logger *slog.Logger) *Runner {
	return &Runner{broker: broker, queue: queue, handler: handler, logger: logger,
		prefetch: defaultPrefetch, retryDelay: retryDelay}
}

// WithRetryDelay returns r with another delay before a failed delivery is
// requeued. Tests use it to stay fast.
func (r *Runner) WithRetryDelay(delay time.Duration) *Runner {
	copied := *r
	copied.retryDelay = delay
	return &copied
}

// Run consumes until ctx ends. Each delivery is acknowledged only after its
// event is stored or recognised as a duplicate, so delivery is at least once.
func (r *Runner) Run(ctx context.Context) {
	delay := minReconnectDelay
	for ctx.Err() == nil {
		consumed, err := r.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if consumed {
			delay = minReconnectDelay
		}
		r.logger.WarnContext(ctx, "stats_event_consumer_disconnected", "error", err, "retry_in", delay.String())
		if !sleep(ctx, delay) {
			return
		}
		delay = min(delay*2, maxReconnectDelay)
	}
}

// session runs one connection until it is lost. consumed reports whether the
// queue was opened, which resets the reconnect backoff.
func (r *Runner) session(ctx context.Context) (consumed bool, err error) {
	conn, err := rabbitmq.Open(ctx, r.broker)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	channel, err := conn.Channel()
	if err != nil {
		return false, err
	}
	defer channel.Close()
	if err := channel.Qos(r.prefetch, 0, false); err != nil {
		return false, err
	}
	deliveries, err := channel.ConsumeWithContext(ctx, r.queue, consumerTag, false, false, false, false, nil)
	if err != nil {
		return false, err
	}
	r.logger.InfoContext(ctx, "stats_event_consumer_started", "queue", r.queue)
	for delivery := range deliveries {
		r.settle(ctx, delivery)
	}

	return true, amqp.ErrClosed
}

func (r *Runner) settle(ctx context.Context, delivery amqp.Delivery) {
	outcome, eventType, err := r.handler.Handle(ctx, delivery.Body)
	switch outcome {
	case Ack:
		_ = delivery.Ack(false)
	case Reject:
		r.logger.ErrorContext(ctx, "stats_event_rejected", "event_type", eventType, "error", err)
		_ = delivery.Nack(false, false)
	case Retry:
		r.logger.WarnContext(ctx, "stats_event_retry", "event_type", eventType, "error", err)
		sleep(ctx, r.retryDelay)
		_ = delivery.Nack(false, true)
	}
}

// sleep waits d or until ctx ends, reporting whether the full wait elapsed.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
