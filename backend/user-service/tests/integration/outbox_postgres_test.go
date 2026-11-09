package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	userpostgres "github.com/melmut-42/ft_transcendence/backend/user-service/internal/repository/postgres"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

// fakeBroker records confirmed messages. hook runs before each confirm and
// may fail the publish.
type fakeBroker struct {
	mu        sync.Mutex
	confirmed []service.OutboxMessage
	hook      func(ctx context.Context, message service.OutboxMessage) error
}

func (b *fakeBroker) Publish(ctx context.Context, message service.OutboxMessage) error {
	if b.hook != nil {
		if err := b.hook(ctx, message); err != nil {
			return err
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.confirmed = append(b.confirmed, message)
	return nil
}

func (b *fakeBroker) eventIDs() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	ids := make([]string, len(b.confirmed))
	for i, message := range b.confirmed {
		ids[i] = message.EventID
	}
	return ids
}

type codedError string

func (e codedError) Error() string        { return "broker said no: amqp://user:secret@host" }
func (e codedError) DeliveryCode() string { return string(e) }

func profileEvent(userID int64, username string) service.OutboxEvent {
	return service.OutboxEvent{
		AggregateType: "profile",
		AggregateID:   fmt.Sprint(userID),
		EventType:     service.EventUserProfileUpdated,
		Payload:       map[string]any{"user_id": userID, "username": username},
	}
}

// appendCommitted records events in one committed transaction.
func appendCommitted(t *testing.T, pool *pgxpool.Pool, events ...service.OutboxEvent) []string {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ids := make([]string, 0, len(events))
	for _, event := range events {
		id, err := userpostgres.AppendEvent(ctx, tx, event)
		if err != nil {
			t.Fatalf("AppendEvent(%s) = %v", event.EventType, err)
		}
		ids = append(ids, id)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return ids
}

func noBackoff(int) time.Duration { return 0 }

// TestOutboxAppendPostgres checks that events share their change's transaction.
//
// Expected output:
//
//   - A rolled-back transaction leaves neither the change nor its event.
//   - A committed event gets a unique event_id and is due immediately.
//   - Unknown types, empty aggregates and non-object payloads are refused.
func TestOutboxAppendPostgres(t *testing.T) {
	pool := schemaPool(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO profiles (user_id, username) VALUES (42, 'player_one')"); err != nil {
		t.Fatal(err)
	}
	if _, err := userpostgres.AppendEvent(ctx, tx, profileEvent(42, "player_one")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if got := count(t, pool, "SELECT count(*) FROM outbox"); got != 0 {
		t.Fatalf("outbox rows after rollback = %d, want 0", got)
	}
	if got := count(t, pool, "SELECT count(*) FROM profiles"); got != 0 {
		t.Fatalf("profiles after rollback = %d, want 0", got)
	}

	ids := appendCommitted(t, pool, profileEvent(42, "a_name"), profileEvent(42, "b_name"))
	if ids[0] == ids[1] || len(ids[0]) != 36 {
		t.Fatalf("event ids = %v, want two distinct UUIDs", ids)
	}
	if got := count(t, pool, "SELECT count(*) FROM outbox WHERE published_at IS NULL AND next_attempt_at <= now()"); got != 2 {
		t.Fatalf("due events = %d, want 2", got)
	}

	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for name, event := range map[string]service.OutboxEvent{
		"unknown type":   {AggregateType: "profile", AggregateID: "42", EventType: "user.renamed", Payload: map[string]any{}},
		"empty type":     {AggregateType: " ", AggregateID: "42", EventType: service.EventUserProfileUpdated, Payload: map[string]any{}},
		"empty id":       {AggregateType: "profile", AggregateID: "", EventType: service.EventUserProfileUpdated, Payload: map[string]any{}},
		"array payload":  {AggregateType: "profile", AggregateID: "42", EventType: service.EventUserProfileUpdated, Payload: []int{1}},
		"nil payload":    {AggregateType: "profile", AggregateID: "42", EventType: service.EventUserProfileUpdated},
		"string payload": {AggregateType: "profile", AggregateID: "42", EventType: service.EventUserProfileUpdated, Payload: "x"},
	} {
		if _, err := userpostgres.AppendEvent(ctx, tx, event); !errors.Is(err, service.ErrInvalidEvent) {
			t.Fatalf("%s: AppendEvent = %v, want ErrInvalidEvent", name, err)
		}
	}
}

// TestOutboxRelayPublishesPostgres checks publishing, the envelope and confirm gating.
//
// Expected output:
//
//   - Nothing is marked published while the broker has not confirmed.
//   - A confirmed event is marked published once and never sent again.
//   - The envelope carries event_id, event_type, occurred_at and the payload.
func TestOutboxRelayPublishesPostgres(t *testing.T) {
	pool := schemaPool(t)
	ctx := context.Background()
	ids := appendCommitted(t, pool, profileEvent(42, "player_one"))

	entered, release := make(chan struct{}), make(chan struct{})
	broker := &fakeBroker{hook: func(context.Context, service.OutboxMessage) error {
		close(entered)
		<-release
		return nil
	}}
	relay := service.NewOutboxRelay(userpostgres.NewOutbox(pool), broker)
	done := make(chan error, 1)
	go func() {
		_, err := relay.PublishPending(ctx)
		done <- err
	}()
	<-entered
	if got := count(t, pool, "SELECT count(*) FROM outbox WHERE published_at IS NOT NULL"); got != 0 {
		t.Fatalf("published before confirm = %d, want 0", got)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := count(t, pool, "SELECT count(*) FROM outbox WHERE published_at IS NOT NULL AND attempts = 0"); got != 1 {
		t.Fatalf("published after confirm = %d, want 1", got)
	}

	broker.hook = nil
	if n, err := relay.PublishPending(ctx); err != nil || n != 0 {
		t.Fatalf("second batch = %d, %v; want nothing to publish", n, err)
	}
	if got := broker.eventIDs(); len(got) != 1 || got[0] != ids[0] {
		t.Fatalf("confirmed = %v, want only %s", got, ids[0])
	}

	body, err := broker.confirmed[0].Body()
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope) != 4 {
		t.Fatalf("envelope = %s", body)
	}
	var payload map[string]any
	_ = json.Unmarshal(envelope["payload"], &payload)
	if string(envelope["event_id"]) != `"`+ids[0]+`"` || string(envelope["event_type"]) != `"user.profile.updated"` ||
		payload["username"] != "player_one" {
		t.Fatalf("envelope = %s", body)
	}
	if _, err := time.Parse(time.RFC3339Nano, string(envelope["occurred_at"][1:len(envelope["occurred_at"])-1])); err != nil {
		t.Fatalf("occurred_at = %s", envelope["occurred_at"])
	}
}

// TestOutboxRelayFailuresPostgres checks retries and per-aggregate ordering.
//
// Expected output:
//
//   - A failed publish stays unpublished with attempts, a secret-free code and
//     a later next_attempt_at; it is not retried before then.
//   - Later events of the failed aggregate wait; other aggregates still publish.
//   - Once due again, the aggregate's events publish in id order.
func TestOutboxRelayFailuresPostgres(t *testing.T) {
	pool := schemaPool(t)
	ctx := context.Background()
	failed := appendCommitted(t, pool, profileEvent(42, "first"), profileEvent(42, "second"))
	other := appendCommitted(t, pool, profileEvent(43, "other"))

	broker := &fakeBroker{hook: func(_ context.Context, message service.OutboxMessage) error {
		if message.EventID == failed[0] {
			return codedError("BROKER_NACK")
		}
		return nil
	}}
	relay := service.NewOutboxRelay(userpostgres.NewOutbox(pool), broker)
	if n, err := relay.PublishPending(ctx); err != nil || n != 1 {
		t.Fatalf("first batch = %d, %v; want 1 published", n, err)
	}
	if got := broker.eventIDs(); len(got) != 1 || got[0] != other[0] {
		t.Fatalf("confirmed = %v, want only the other aggregate", got)
	}
	var attempts int
	var lastError string
	var delayed bool
	err := pool.QueryRow(ctx, `SELECT attempts, last_error, next_attempt_at > now() FROM outbox WHERE event_id = $1`,
		failed[0]).Scan(&attempts, &lastError, &delayed)
	if err != nil || attempts != 1 || lastError != "BROKER_NACK" || !delayed {
		t.Fatalf("failed row = %d %q %v (%v)", attempts, lastError, delayed, err)
	}
	if n, err := relay.PublishPending(ctx); err != nil || n != 0 {
		t.Fatalf("batch during backoff = %d, %v; want nothing", n, err)
	}

	mustExec(t, pool, "UPDATE outbox SET next_attempt_at = now() WHERE event_id = $1", failed[0])
	broker.hook = nil
	for range 3 {
		if _, err := relay.PublishPending(ctx); err != nil {
			t.Fatal(err)
		}
	}
	got := broker.eventIDs()
	if len(got) != 3 || got[1] != failed[0] || got[2] != failed[1] {
		t.Fatalf("confirmed order = %v, want %s then %s", got, failed[0], failed[1])
	}

	unknown := appendCommitted(t, pool, profileEvent(44, "plain"))
	broker.hook = func(context.Context, service.OutboxMessage) error { return errors.New("dial amqp://user:secret@host") }
	if _, err := relay.PublishPending(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT last_error FROM outbox WHERE event_id = $1`, unknown[0]).Scan(&lastError); err != nil ||
		lastError != "PUBLISH_FAILED" {
		t.Fatalf("uncoded failure stored %q, want PUBLISH_FAILED", lastError)
	}
}

// TestOutboxRelayCrashAfterConfirmPostgres checks the crash window after a confirm.
//
// Expected output: when the batch cannot record its result after the broker
// confirmed, the event stays unpublished and the next batch sends it again
// with the same event_id; nothing is lost.
func TestOutboxRelayCrashAfterConfirmPostgres(t *testing.T) {
	pool := schemaPool(t)
	ids := appendCommitted(t, pool, profileEvent(42, "player_one"))

	crashCtx, crash := context.WithCancel(context.Background())
	broker := &fakeBroker{}
	broker.hook = func(context.Context, service.OutboxMessage) error {
		// The broker confirms, then the relay dies before marking the row.
		crash()
		return nil
	}
	relay := service.NewOutboxRelay(userpostgres.NewOutbox(pool), broker)
	if _, err := relay.PublishPending(crashCtx); err == nil {
		t.Fatal("a batch that cannot record its result must fail")
	}
	if got := count(t, pool, "SELECT count(*) FROM outbox WHERE published_at IS NULL"); got != 1 {
		t.Fatalf("unpublished after crash = %d, want 1", got)
	}

	broker.hook = nil
	if n, err := relay.PublishPending(context.Background()); err != nil || n != 1 {
		t.Fatalf("batch after restart = %d, %v; want 1", n, err)
	}
	got := broker.eventIDs()
	if len(got) != 2 || got[0] != ids[0] || got[1] != ids[0] {
		t.Fatalf("deliveries = %v, want the same event_id twice", got)
	}
	if count(t, pool, "SELECT count(*) FROM outbox WHERE published_at IS NOT NULL") != 1 {
		t.Fatal("the event must end published exactly once")
	}
}

// TestOutboxRelayConcurrentPostgres checks concurrent relays on one outbox.
//
// Expected output: four relays draining 60 events over 6 aggregates publish
// every event exactly once, and each aggregate's events in id order.
func TestOutboxRelayConcurrentPostgres(t *testing.T) {
	pool := schemaPool(t)
	var events []service.OutboxEvent
	for n := range 60 {
		events = append(events, profileEvent(int64(100+n%6), fmt.Sprintf("name_%d", n)))
	}
	appendCommitted(t, pool, events...)

	broker := &fakeBroker{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		relay := service.NewOutboxRelay(userpostgres.NewOutbox(pool), broker,
			service.WithRelayBatchSize(3), service.WithRelayBackoff(noBackoff))
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				if _, err := relay.PublishPending(ctx); err != nil {
					errs <- err
					return
				}
				var pending int
				if err := pool.QueryRow(ctx, "SELECT count(*) FROM outbox WHERE published_at IS NULL").Scan(&pending); err != nil {
					errs <- err
					return
				}
				if pending == 0 {
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	broker.mu.Lock()
	defer broker.mu.Unlock()
	if len(broker.confirmed) != 60 {
		t.Fatalf("confirmed = %d, want 60", len(broker.confirmed))
	}
	seen := map[string]bool{}
	last := map[string]int64{}
	for _, message := range broker.confirmed {
		if seen[message.EventID] {
			t.Fatalf("event %s published twice", message.EventID)
		}
		seen[message.EventID] = true
		if message.ID <= last[message.AggregateID] {
			t.Fatalf("aggregate %s published %d after %d", message.AggregateID, message.ID, last[message.AggregateID])
		}
		last[message.AggregateID] = message.ID
	}
}
