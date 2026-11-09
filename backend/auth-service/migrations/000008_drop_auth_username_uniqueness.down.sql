BEGIN;

-- Not unique: rows written since the up migration may share a username.
CREATE INDEX users_username_unique ON users (lower(username));

COMMIT;
