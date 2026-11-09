package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

var _ service.OutboxStore = (*OutboxRepository)(nil)

// AppendEvent records event in tx, the transaction of the change it
// describes, so both commit or roll back together.
//
// It returns the stable event_id consumers deduplicate by.
//
// Errors:
//
//   - service.ErrInvalidEvent: unknown type, bad aggregate or non-object payload.
//   - service.ErrUnavailable or other database errors.
func AppendEvent(ctx context.Context, tx pgx.Tx, event service.OutboxEvent) (string, error) {
	if err := event.Validate(); err != nil {
		return "", err
	}
	payload, err := event.PayloadJSON()
	if err != nil {
		return "", err
	}
	var eventID string
	err = tx.QueryRow(ctx, `
		INSERT INTO outbox (aggregate_type, aggregate_id, event_type, payload)
		VALUES ($1, $2, $3, $4::jsonb) RETURNING event_id::text`,
		event.AggregateType, event.AggregateID, event.EventType, string(payload)).Scan(&eventID)
	if err != nil {
		return "", classify(err)
	}

	return eventID, nil
}

// OutboxRepository claims and marks outbox rows for the relay.
type OutboxRepository struct {
	pool *pgxpool.Pool
}

// NewOutbox returns an outbox store; pool ownership stays with the caller.
func NewOutbox(pool *pgxpool.Pool) *OutboxRepository {
	return &OutboxRepository{pool: pool}
}

// claimDue selects due rows that are the oldest unpublished row of their
// aggregate. FOR UPDATE SKIP LOCKED keeps concurrent relays off rows another
// relay holds, and the head-of-aggregate rule keeps a later row of a locked or
// delayed aggregate from overtaking it.
const claimDue = `
	SELECT o.id, o.event_id::text, o.event_type, o.aggregate_type, o.aggregate_id,
	       o.created_at, o.payload, o.attempts
	FROM outbox o
	WHERE o.published_at IS NULL
	  AND o.next_attempt_at <= now()
	  AND NOT EXISTS (
	      SELECT 1 FROM outbox p
	      WHERE p.published_at IS NULL
	        AND p.aggregate_type = o.aggregate_type
	        AND p.aggregate_id = o.aggregate_id
	        AND p.id < o.id)
	ORDER BY o.id
	LIMIT $1
	FOR UPDATE OF o SKIP LOCKED`

// DeliverBatch implements service.OutboxStore. The rows stay locked while
// deliver publishes them, and results are written in the same transaction.
// A crash or failed commit after a broker confirm leaves the row unpublished,
// so it is delivered again with the same event_id: duplicates are possible,
// loss is not.
func (r *OutboxRepository) DeliverBatch(ctx context.Context, limit int,
	deliver func([]service.OutboxMessage) []service.DeliveryResult) (int, error) {
	if limit < 1 || limit > 500 {
		return 0, errors.New("outbox batch limit must be between 1 and 500")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, classify(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	rows, err := tx.Query(ctx, claimDue, limit)
	if err != nil {
		return 0, classify(err)
	}
	batch, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (service.OutboxMessage, error) {
		var message service.OutboxMessage
		err := row.Scan(&message.ID, &message.EventID, &message.EventType, &message.AggregateType,
			&message.AggregateID, &message.OccurredAt, &message.Payload, &message.Attempts)
		message.OccurredAt = message.OccurredAt.UTC()
		return message, err
	})
	if err != nil {
		return 0, classify(err)
	}
	if len(batch) == 0 {
		return 0, nil
	}

	results := deliver(batch)
	claimed := make(map[int64]bool, len(batch))
	for _, message := range batch {
		claimed[message.ID] = true
	}
	for _, result := range results {
		if !claimed[result.ID] {
			return 0, fmt.Errorf("outbox result for unclaimed row %d", result.ID)
		}
		if err := recordResult(ctx, tx, result); err != nil {
			return 0, classify(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, classify(err)
	}

	return len(batch), nil
}

func recordResult(ctx context.Context, tx pgx.Tx, result service.DeliveryResult) error {
	switch {
	case result.Skipped:
		return nil
	case result.Published:
		_, err := tx.Exec(ctx, `
			UPDATE outbox SET published_at = now(), last_error = NULL WHERE id = $1`, result.ID)
		return err
	default:
		_, err := tx.Exec(ctx, `
			UPDATE outbox SET attempts = attempts + 1, last_error = left($2, 2000), next_attempt_at = $3
			WHERE id = $1`, result.ID, result.Code, result.RetryAt)
		return err
	}
}
