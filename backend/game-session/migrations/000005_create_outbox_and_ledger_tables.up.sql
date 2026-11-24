BEGIN;

CREATE TABLE outbox_events (
    event_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    room_id BIGINT NOT NULL,
    event_type TEXT NOT NULL,
    outbox_content JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,
    room_version BIGINT NOT NULL,
    recipient_user_id BIGINT,
    event_sequence BIGINT
);

CREATE INDEX idx_outbox_events_pending
    ON outbox_events (room_id, room_version)
    WHERE published_at IS NULL;

CREATE INDEX idx_outbox_events_recipient_pending
    ON outbox_events (room_id, event_sequence)
    WHERE published_at IS NULL AND recipient_user_id IS NOT NULL;

CREATE TABLE command_ledger (
    room_id BIGINT NOT NULL,
    session_id UUID NOT NULL,
    request_id VARCHAR(64) NOT NULL,
    command_text TEXT NOT NULL,
    response JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (room_id, session_id, request_id),

    CONSTRAINT chk_command_ledger_request_id CHECK (request_id ~ '^[!-~]{1,64}$')
);

CREATE TABLE account_cleanups (
    operation_id UUID PRIMARY KEY,
    user_id BIGINT NOT NULL UNIQUE,
    completed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE session_departures (
    session_id UUID PRIMARY KEY,
    user_id BIGINT NOT NULL,
    completed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMIT;