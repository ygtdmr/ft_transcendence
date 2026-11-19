-- Durable progress of the account deletion coordinator. Each participant step
-- records when it was confirmed, so a retry or a restart resumes after the
-- last confirmed step. next_attempt_at delays a failed operation. state
-- 'failed' marks an operation stopped by a permanent participant answer; it is
-- not retried automatically and needs investigation.
ALTER TABLE users.deletion_operations
    ADD COLUMN auth_begun_at    TIMESTAMPTZ,
    ADD COLUMN game_cleaned_at  TIMESTAMPTZ,
    ADD COLUMN stats_cleaned_at TIMESTAMPTZ,
    ADD COLUMN user_cleaned_at  TIMESTAMPTZ,
    ADD COLUMN next_attempt_at  TIMESTAMPTZ NOT NULL DEFAULT now();

-- The worker looks only at unfinished operations that are due.
CREATE INDEX deletion_operations_due_idx
    ON users.deletion_operations (next_attempt_at) WHERE state IN ('pending', 'in_progress');
