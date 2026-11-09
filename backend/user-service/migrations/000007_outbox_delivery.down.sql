DROP INDEX users.outbox_unpublished_aggregate_idx;
ALTER TABLE users.outbox
    DROP CONSTRAINT outbox_event_id_key,
    DROP COLUMN next_attempt_at,
    DROP COLUMN event_id;
