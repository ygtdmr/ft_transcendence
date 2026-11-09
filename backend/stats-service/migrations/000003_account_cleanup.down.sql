-- Anonymized rows have no user id and cannot satisfy the earlier constraints.
DROP TABLE stats.deleted_users;

DELETE FROM stats.penalties WHERE user_id IS NULL;
ALTER TABLE stats.penalties ALTER COLUMN user_id SET NOT NULL;

DELETE FROM stats.match_participants WHERE user_id IS NULL;
ALTER TABLE stats.match_participants
    DROP CONSTRAINT match_participants_game_user_unique,
    ALTER COLUMN user_id SET NOT NULL,
    ADD PRIMARY KEY (game_id, user_id);
