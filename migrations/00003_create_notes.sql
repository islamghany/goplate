-- +goose Up
CREATE TABLE notes (
    id         uuid        PRIMARY KEY,
    owner_id   uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    title      text        NOT NULL,
    body       text        NOT NULL DEFAULT '',
    tags       text[]      NOT NULL DEFAULT '{}',
    version    integer     NOT NULL DEFAULT 1,      -- optimistic concurrency (ETag / If-Match)
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

-- Serves "my notes, newest first" and its keyset pagination.
CREATE INDEX notes_owner_created_at_id_idx ON notes (owner_id, created_at DESC, id DESC);
-- Serves "?tag=" filters.
CREATE INDEX notes_tags_idx ON notes USING gin (tags);

-- +goose Down
DROP TABLE notes;
