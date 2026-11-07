BEGIN;

-- One deletion operation per account, chosen by User Service's coordinator.
-- The row outlives the account so retries stay idempotent; it holds no email,
-- username, password or Google subject. Account ids come from an identity
-- column and are never reassigned.
CREATE TABLE account_deletions (
    user_id BIGINT PRIMARY KEY CHECK (user_id > 0),
    operation_id UUID NOT NULL UNIQUE,
    -- BEGUN: the account is DELETING and every session is revoked.
    -- COMPLETED: the account and its credentials are removed.
    state TEXT NOT NULL
        CONSTRAINT account_deletion_state_valid CHECK (state IN ('BEGUN', 'COMPLETED')),
    begun_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    CONSTRAINT account_deletion_completed_matches_state
        CHECK ((state = 'COMPLETED') = (completed_at IS NOT NULL))
);

-- Gateways close sockets differently for account deletion.
ALTER TABLE session_revocations
    ADD COLUMN cause TEXT NOT NULL DEFAULT 'SESSION_REVOKED'
    CONSTRAINT session_revocation_cause_valid CHECK (cause IN ('SESSION_REVOKED', 'ACCOUNT_DELETED'));

COMMIT;
