-- Delivery fields for the transactional outbox.
-- event_id is the stable identity consumers deduplicate by; a redelivered
-- event always carries the same event_id. next_attempt_at delays a failed
-- event, and with it every later event of the same aggregate, so events of
-- one aggregate are published in id order.
ALTER TABLE users.outbox
    ADD COLUMN event_id UUID NOT NULL DEFAULT gen_random_uuid(),
    ADD COLUMN next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD CONSTRAINT outbox_event_id_key UNIQUE (event_id);

-- Finds the oldest unpublished event of each aggregate.
CREATE INDEX outbox_unpublished_aggregate_idx
    ON users.outbox (aggregate_type, aggregate_id, id) WHERE published_at IS NULL;
