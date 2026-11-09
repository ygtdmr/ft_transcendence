-- Deleted accounts. A retained participant or penalty keeps no user id, and
-- deleted_users stops late events from recreating one.
ALTER TABLE stats.match_participants
    DROP CONSTRAINT match_participants_pkey,
    ALTER COLUMN user_id DROP NOT NULL,
    ADD CONSTRAINT match_participants_game_user_unique UNIQUE (game_id, user_id);

ALTER TABLE stats.penalties ALTER COLUMN user_id DROP NOT NULL;

CREATE TABLE stats.deleted_users (
    user_id      BIGINT      PRIMARY KEY,
    operation_id UUID        NOT NULL,
    completed_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT deleted_users_operation_unique UNIQUE (operation_id),
    CONSTRAINT deleted_users_user_positive CHECK (user_id > 0)
);
