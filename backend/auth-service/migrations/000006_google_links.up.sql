BEGIN;

-- Accounts created through Google sign-in have no password.
ALTER TABLE users ALTER COLUMN password_hash DROP NOT NULL;
ALTER TABLE users DROP CONSTRAINT users_password_hash_check;
ALTER TABLE users ADD CONSTRAINT users_password_hash_check CHECK (password_hash IS NULL OR password_hash <> '');

-- One Google identity per account and one account per Google identity.
-- subject is Google's stable "sub" claim; the email may change at Google.
CREATE TABLE oauth_links (
    provider TEXT NOT NULL CHECK (provider = 'google'),
    subject TEXT NOT NULL CHECK (subject <> '' AND char_length(subject) <= 255),
    user_id BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, subject),
    UNIQUE (provider, user_id)
);

COMMIT;
