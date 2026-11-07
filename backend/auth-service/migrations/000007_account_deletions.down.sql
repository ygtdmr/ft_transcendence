BEGIN;

ALTER TABLE session_revocations DROP COLUMN cause;
DROP TABLE account_deletions;

COMMIT;
