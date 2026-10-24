-- +goose Up
-- One row per issued refresh token. Rotation creates a new row in the same
-- family and marks the old one used; presenting a used token again revokes
-- the whole family (reuse detection, ADR-0007).
CREATE TABLE auth_sessions (
    id         uuid        PRIMARY KEY,
    family_id  uuid        NOT NULL,
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash bytea       NOT NULL,                -- sha256(refresh token); the token itself is never stored
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    revoked_at timestamptz,
    user_agent text        NOT NULL DEFAULT '',
    ip         text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    CONSTRAINT auth_sessions_token_hash_key UNIQUE (token_hash)
);

CREATE INDEX auth_sessions_family_id_idx ON auth_sessions (family_id);
CREATE INDEX auth_sessions_user_id_idx ON auth_sessions (user_id);
CREATE INDEX auth_sessions_expires_at_idx ON auth_sessions (expires_at);

-- +goose Down
DROP TABLE auth_sessions;
