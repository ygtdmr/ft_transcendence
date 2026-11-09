-- Finalized matches and the counters derived from them. Game Session is the
-- only source of these results; user ids are stable ids with no foreign key.

CREATE TABLE stats.matches (
    game_id     BIGINT      PRIMARY KEY,
    room_id     BIGINT      NOT NULL,
    winner      TEXT        NOT NULL,
    loser       TEXT        NOT NULL,
    end_reason  TEXT        NOT NULL,
    score_red   INTEGER     NOT NULL,
    score_blue  INTEGER     NOT NULL,
    started_at  TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT matches_game_positive CHECK (game_id > 0),
    CONSTRAINT matches_room_positive CHECK (room_id > 0),
    CONSTRAINT matches_winner_valid CHECK (winner IN ('RED', 'BLUE')),
    CONSTRAINT matches_loser_valid CHECK (loser IN ('RED', 'BLUE')),
    CONSTRAINT matches_winner_differs CHECK (winner <> loser),
    CONSTRAINT matches_end_reason_valid CHECK (end_reason IN ('ALL_TEAM_CARDS_REVEALED', 'ASSASSIN_REVEALED', 'PLAYER_FORFEIT')),
    CONSTRAINT matches_score_valid CHECK (score_red >= 0 AND score_blue >= 0),
    CONSTRAINT matches_times_ordered CHECK (started_at <= finished_at)
);

-- username is the name recorded with the match. A later rename does not change it.
CREATE TABLE stats.match_participants (
    game_id  BIGINT NOT NULL REFERENCES stats.matches (game_id) ON DELETE CASCADE,
    user_id  BIGINT NOT NULL,
    username TEXT   NOT NULL,
    team     TEXT   NOT NULL,
    role     TEXT   NOT NULL,

    PRIMARY KEY (game_id, user_id),
    CONSTRAINT match_participants_user_positive CHECK (user_id > 0),
    CONSTRAINT match_participants_team_valid CHECK (team IN ('RED', 'BLUE')),
    CONSTRAINT match_participants_role_valid CHECK (role IN ('SPYMASTER', 'OPERATIVE'))
);

CREATE INDEX match_participants_user_idx ON stats.match_participants (user_id);

CREATE TABLE stats.user_stats (
    user_id    BIGINT      PRIMARY KEY,
    wins       INTEGER     NOT NULL DEFAULT 0,
    losses     INTEGER     NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT user_stats_user_positive CHECK (user_id > 0),
    CONSTRAINT user_stats_counts_valid CHECK (wins >= 0 AND losses >= 0)
);
