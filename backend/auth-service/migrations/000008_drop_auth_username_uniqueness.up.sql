BEGIN;

-- User Service owns username uniqueness. users.username holds only the name
-- sent at provisioning, so it goes stale after a rename.
DROP INDEX users_username_unique;

COMMIT;
