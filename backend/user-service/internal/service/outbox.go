package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Integration event types published by User Service. Each one feeds the
// matching chat-socket event described in the public contract.
const (
	EventUserProfileUpdated    = "user.profile.updated"
	EventFriendRequestReceived = "friend.request.received"
	EventFriendRequestResolved = "friend.request.resolved"
)

var knownEvents = map[string]bool{
	EventUserProfileUpdated:    true,
	EventFriendRequestReceived: true,
	EventFriendRequestResolved: true,
}

// ErrInvalidEvent reports an outbox event that cannot be recorded.
var ErrInvalidEvent = errors.New("invalid outbox event")

// OutboxEvent is an integration event written in the same transaction as the
// change it describes.
type OutboxEvent struct {
	// AggregateType and AggregateID name the record the event is about, such
	// as "profile" and a user_id. Events of one aggregate publish in order.
	AggregateType string
	AggregateID   string
	EventType     string
	// Payload must marshal to a JSON object.
	Payload any
}

// Validate checks the event against the catalog and column limits.
func (e OutboxEvent) Validate() error {
	switch {
	case !knownEvents[e.EventType]:
		return fmt.Errorf("%w: unknown event type %q", ErrInvalidEvent, e.EventType)
	case strings.TrimSpace(e.AggregateType) == "" || len(e.AggregateType) > 64:
		return fmt.Errorf("%w: aggregate type", ErrInvalidEvent)
	case strings.TrimSpace(e.AggregateID) == "" || len(e.AggregateID) > 128:
		return fmt.Errorf("%w: aggregate id", ErrInvalidEvent)
	}

	return nil
}

// PayloadJSON returns the payload as a JSON object.
func (e OutboxEvent) PayloadJSON() ([]byte, error) {
	raw, err := json.Marshal(e.Payload)
	if err != nil {
		return nil, fmt.Errorf("%w: payload: %w", ErrInvalidEvent, err)
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, fmt.Errorf("%w: payload must be a JSON object", ErrInvalidEvent)
	}

	return raw, nil
}

// OutboxMessage is one claimed event ready to publish.
type OutboxMessage struct {
	ID            int64
	EventID       string
	EventType     string
	AggregateType string
	AggregateID   string
	OccurredAt    time.Time
	Payload       json.RawMessage
	Attempts      int
}

// Body returns the published envelope. Consumers deduplicate by event_id.
func (m OutboxMessage) Body() ([]byte, error) {
	return json.Marshal(struct {
		EventID    string          `json:"event_id"`
		EventType  string          `json:"event_type"`
		OccurredAt string          `json:"occurred_at"`
		Payload    json.RawMessage `json:"payload"`
	}{m.EventID, m.EventType, m.OccurredAt.UTC().Format(time.RFC3339Nano), m.Payload})
}

// EventPublisher sends one message to the broker. It returns nil only after
// the broker confirms the message. Errors must not contain credentials or
// connection strings; an error implementing DeliveryCode() string is stored by
// that code.
type EventPublisher interface {
	Publish(ctx context.Context, message OutboxMessage) error
}

// DeliveryResult reports what happened to one claimed message.
type DeliveryResult struct {
	ID int64
	// Published is true only after a broker confirm.
	Published bool
	// Skipped leaves the message untouched, for example because an earlier
	// message of the same aggregate failed in this batch.
	Skipped bool
	// Code describes a failed attempt; it never contains secrets.
	Code string
	// RetryAt is when a failed message becomes due again.
	RetryAt time.Time
}

// OutboxStore claims due messages and records their results atomically.
type OutboxStore interface {
	// DeliverBatch locks up to limit due messages, at most one per aggregate
	// and each the oldest unpublished message of its aggregate, passes them
	// in id order to deliver, then records every result and commits. Locked
	// rows are skipped by concurrent callers. If recording fails, nothing is
	// marked and confirmed messages are delivered again later.
	DeliverBatch(ctx context.Context, limit int, deliver func([]OutboxMessage) []DeliveryResult) (int, error)
}

// OutboxRelay publishes recorded events at least once, in order per aggregate.
type OutboxRelay struct {
	store          OutboxStore
	publisher      EventPublisher
	batchSize      int
	publishTimeout time.Duration
	backoff        func(attempts int) time.Duration
	now            func() time.Time
}

// RelayOption configures an OutboxRelay.
type RelayOption func(*OutboxRelay)

// WithRelayBackoff replaces the retry delay for a message after its nth failed attempt.
func WithRelayBackoff(backoff func(attempts int) time.Duration) RelayOption {
	return func(r *OutboxRelay) { r.backoff = backoff }
}

// WithRelayBatchSize sets how many messages one batch claims (1 to 500).
func WithRelayBatchSize(size int) RelayOption {
	return func(r *OutboxRelay) {
		if size >= 1 && size <= 500 {
			r.batchSize = size
		}
	}
}

// NewOutboxRelay returns a relay with a batch of 50, a 5s publish timeout and
// exponential backoff from 1s up to 5 minutes.
func NewOutboxRelay(store OutboxStore, publisher EventPublisher, options ...RelayOption) *OutboxRelay {
	relay := &OutboxRelay{
		store:          store,
		publisher:      publisher,
		batchSize:      50,
		publishTimeout: 5 * time.Second,
		backoff:        ExponentialBackoff,
		now:            time.Now,
	}
	for _, option := range options {
		option(relay)
	}

	return relay
}

// ExponentialBackoff returns 1s after the first failure, doubling up to 5 minutes.
func ExponentialBackoff(attempts int) time.Duration {
	const ceiling = 5 * time.Minute
	if attempts < 1 {
		attempts = 1
	}
	if attempts > 9 {
		return ceiling
	}
	delay := time.Second << (attempts - 1)

	return min(delay, ceiling)
}

// PublishPending publishes one batch and returns how many messages the broker
// confirmed and the store marked published.
//
// Messages publish one at a time in id order. After a failure, later messages
// of the same aggregate in the batch are skipped. A failure is recorded with
// its code and a retry time; it is not returned as an error.
//
// Errors:
//
//   - Store errors: nothing in the batch was marked; confirmed messages are
//     published again by a later batch with the same event_id.
func (r *OutboxRelay) PublishPending(ctx context.Context) (int, error) {
	published := 0
	_, err := r.store.DeliverBatch(ctx, r.batchSize, func(batch []OutboxMessage) []DeliveryResult {
		published = 0
		failed := map[string]bool{}
		results := make([]DeliveryResult, 0, len(batch))
		for _, message := range batch {
			key := message.AggregateType + "\x00" + message.AggregateID
			if failed[key] || ctx.Err() != nil {
				results = append(results, DeliveryResult{ID: message.ID, Skipped: true})
				continue
			}
			if err := r.publish(ctx, message); err != nil {
				failed[key] = true
				results = append(results, DeliveryResult{ID: message.ID, Code: deliveryCode(err),
					RetryAt: r.now().Add(r.backoff(message.Attempts + 1))})
				continue
			}
			published++
			results = append(results, DeliveryResult{ID: message.ID, Published: true})
		}
		return results
	})
	if err != nil {
		return 0, err
	}

	return published, nil
}

func (r *OutboxRelay) publish(ctx context.Context, message OutboxMessage) error {
	publishCtx, cancel := context.WithTimeout(ctx, r.publishTimeout)
	defer cancel()

	return r.publisher.Publish(publishCtx, message)
}

// Run publishes until ctx ends. It drains full batches back to back and
// otherwise waits interval between batches. Batch errors go to onError.
func (r *OutboxRelay) Run(ctx context.Context, interval time.Duration, onError func(error)) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		published, err := r.PublishPending(ctx)
		if err != nil && ctx.Err() == nil && onError != nil {
			onError(err)
		}
		next := interval
		if err == nil && published == r.batchSize {
			next = 0
		}
		timer.Reset(next)
	}
}

// deliveryCode stores a short, secret-free description of a failed attempt.
func deliveryCode(err error) string {
	var coded interface{ DeliveryCode() string }
	if errors.As(err, &coded) {
		if code := coded.DeliveryCode(); code != "" && len(code) <= 64 {
			return code
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "PUBLISH_TIMEOUT"
	}

	return "PUBLISH_FAILED"
}
