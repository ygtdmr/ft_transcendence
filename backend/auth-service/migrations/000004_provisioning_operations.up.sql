BEGIN;

-- One profile provisioning operation per account. Auth stores the operation
-- identity and the canonical request in the same transaction that creates the
-- account, before calling User Service, and resends exactly this request on
-- every retry, including after a restart.
CREATE TABLE provisioning_operations (
    user_id BIGINT PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    operation_id UUID NOT NULL UNIQUE,
    username TEXT NOT NULL CHECK (username ~ '^[A-Za-z0-9_]{3,20}$'),
    account_created_at TIMESTAMPTZ NOT NULL,
    -- PENDING: not confirmed yet; retry with the same request.
    -- COMPLETED: User Service returned the receipt for this operation.
    -- FAILED: a terminal answer that needs investigation; never retried.
    state TEXT NOT NULL DEFAULT 'PENDING'
        CONSTRAINT provisioning_state_valid CHECK (state IN ('PENDING', 'COMPLETED', 'FAILED')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error TEXT CHECK (last_error IS NULL OR char_length(last_error) <= 200),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    CONSTRAINT provisioning_completed_matches_state CHECK ((state = 'COMPLETED') = (completed_at IS NOT NULL))
);

CREATE INDEX provisioning_operations_pending_idx
    ON provisioning_operations (updated_at) WHERE state = 'PENDING';

COMMIT;
