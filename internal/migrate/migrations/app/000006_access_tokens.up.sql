-- Credentials for clients that are not browsers.
--
-- The session cookie is SameSite=Strict, carries an XSRF token, and expires
-- after ten minutes of HTTP silence. All three are right for a browser and
-- wrong for a long-lived program: an MCP client holds no cookie jar and has no
-- XSRF cookie to echo. A token is what such a client can hold.
--
-- Only a hash is stored, so a leaked database does not yield usable
-- credentials and the token itself is visible exactly once, when it is
-- created. Tokens are high-entropy random values rather than passwords, so a
-- fast hash is the right choice: bcrypt and argon2 exist to slow down guessing
-- a human-chosen secret, and there is nothing here to guess.
BEGIN;

CREATE TABLE access_tokens (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid NOT NULL REFERENCES "SessionUsers" ("Id") ON DELETE CASCADE,
    name         text NOT NULL,
    -- The lookup key: sha256 of the token as presented.
    token_sha256 bytea NOT NULL UNIQUE,
    -- Enough of the token to recognise it in a list, never enough to use.
    prefix       text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz,
    -- Revoking marks rather than deletes, so a token that was used remains
    -- accountable for what it did.
    revoked_at   timestamptz,
    last_used_at timestamptz
);

CREATE INDEX access_tokens_user_idx ON access_tokens (user_id);

COMMIT;
