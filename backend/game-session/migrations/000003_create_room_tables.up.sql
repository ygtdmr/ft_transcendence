BEGIN;

CREATE TABLE rooms (
    room_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    room_code VARCHAR(6) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'WAITING',
    max_players INTEGER NOT NULL DEFAULT 20,
    host_user_id BIGINT,
    turn_timer_seconds INTEGER,
    countdown_ends_at TIMESTAMPTZ,
    post_game_ends_at TIMESTAMPTZ,
    empty_since TIMESTAMPTZ,
    close_at TIMESTAMPTZ,
    language TEXT NOT NULL DEFAULT 'en',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    room_version BIGINT NOT NULL DEFAULT 0,
    event_sequence BIGINT NOT NULL DEFAULT 0,

    CONSTRAINT chk_rooms_room_code CHECK (room_code ~ '^[A-Z0-9]{6}$'),
    CONSTRAINT chk_rooms_status CHECK (
        status IN ('WAITING', 'COUNTDOWN', 'IN_GAME', 'POST_GAME', 'CLOSED')
    ),
    CONSTRAINT chk_rooms_max_players CHECK (max_players BETWEEN 4 AND 20),
    CONSTRAINT chk_rooms_turn_timer CHECK (turn_timer_seconds IN (60, 90, 120)),
    CONSTRAINT chk_rooms_language CHECK (language IN ('en', 'tr', 'fr'))
);

CREATE UNIQUE INDEX idx_rooms_active_room_code
    ON rooms (room_code)
    WHERE status <> 'CLOSED';

-- partial index: the deadline worker polls these every tick, keep it off a full scan
CREATE INDEX idx_rooms_countdown_due
    ON rooms (countdown_ends_at)
    WHERE countdown_ends_at IS NOT NULL;

CREATE INDEX idx_rooms_post_game_due
    ON rooms (post_game_ends_at)
    WHERE post_game_ends_at IS NOT NULL;

CREATE INDEX idx_rooms_close_due
    ON rooms (close_at)
    WHERE close_at IS NOT NULL;

CREATE TABLE room_members (
    room_id BIGINT NOT NULL REFERENCES rooms (room_id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL PRIMARY KEY,
    username TEXT NOT NULL,
    state VARCHAR(16) NOT NULL DEFAULT 'IN_LOBBY',
    team VARCHAR(4),
    role VARCHAR(16),
    ready BOOLEAN NOT NULL DEFAULT false,
    joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    grace_ends_at TIMESTAMPTZ,

    CONSTRAINT chk_room_members_state CHECK (
        state IN ('IN_LOBBY', 'IN_GAME', 'POST_GAME')
    ),
    CONSTRAINT chk_room_members_team CHECK (team IN ('RED', 'BLUE')),
    CONSTRAINT chk_room_members_selection CHECK (
        (role IS NULL AND team IS NULL AND ready = false)
        OR (role IS NOT NULL AND role IN ('SPYMASTER', 'OPERATIVE') AND team IS NOT NULL)
    )
);

CREATE INDEX idx_room_members_join_order
    ON room_members (room_id, joined_at, user_id);

CREATE UNIQUE INDEX idx_room_members_spymaster
    ON room_members (room_id, team)
    WHERE role = 'SPYMASTER';

-- partial index: the deadline worker polls these every tick, keep it off a full scan
CREATE INDEX idx_room_members_grace_due
    ON room_members (grace_ends_at)
    WHERE grace_ends_at IS NOT NULL;

COMMIT;
