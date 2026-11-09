-- Leave penalties. Game Session evaluates points; this service only adds them.
ALTER TABLE stats.user_stats
    ADD COLUMN penalty_points INTEGER NOT NULL DEFAULT 0,
    ADD CONSTRAINT user_stats_penalty_valid CHECK (penalty_points >= 0);

CREATE TABLE stats.penalties (
    event_id     TEXT        PRIMARY KEY,
    user_id      BIGINT      NOT NULL,
    room_id      BIGINT      NOT NULL,
    game_id      BIGINT      NOT NULL,
    role         TEXT        NOT NULL,
    reason       TEXT        NOT NULL,
    points       INTEGER     NOT NULL,
    penalized_at TIMESTAMPTZ NOT NULL,
    recorded_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT penalties_user_positive CHECK (user_id > 0),
    CONSTRAINT penalties_points_valid CHECK (points >= 0)
);
