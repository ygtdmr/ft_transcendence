DROP INDEX users.deletion_operations_due_idx;

ALTER TABLE users.deletion_operations
    DROP COLUMN next_attempt_at,
    DROP COLUMN user_cleaned_at,
    DROP COLUMN stats_cleaned_at,
    DROP COLUMN game_cleaned_at,
    DROP COLUMN auth_begun_at;
