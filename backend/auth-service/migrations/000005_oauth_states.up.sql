BEGIN;

-- Single-use Google sign-in states. Only a SHA-256 hash of the state is
-- stored; the state itself travels in the sealed ft_oauth cookie and the
-- provider redirect. The callback consumes a row once, before expires_at.
CREATE TABLE oauth_states (
    state_hash TEXT PRIMARY KEY CHECK (state_hash ~ '^[0-9a-f]{64}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL CHECK (expires_at > created_at),
    consumed_at TIMESTAMPTZ
);

CREATE INDEX oauth_states_expires_at_idx ON oauth_states (expires_at);

COMMIT;
