BEGIN;

DROP TABLE oauth_links;
DELETE FROM users WHERE password_hash IS NULL;
ALTER TABLE users DROP CONSTRAINT users_password_hash_check;
ALTER TABLE users ADD CONSTRAINT users_password_hash_check CHECK (password_hash <> '');
ALTER TABLE users ALTER COLUMN password_hash SET NOT NULL;

COMMIT;
