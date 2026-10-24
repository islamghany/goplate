-- name: InsertNote :one
INSERT INTO notes (id, owner_id, title, body, tags, created_at, updated_at)
VALUES (@id, @owner_id, @title, @body, @tags, @created_at, @created_at)
RETURNING *;

-- name: GetNote :one
SELECT * FROM notes WHERE id = @id AND owner_id = @owner_id;

-- name: GetNoteForUpdate :one
SELECT * FROM notes WHERE id = @id AND owner_id = @owner_id FOR UPDATE;

-- name: UpdateNote :one
UPDATE notes
SET title = @title, body = @body, tags = @tags, version = version + 1, updated_at = @updated_at
WHERE id = @id AND owner_id = @owner_id
RETURNING *;

-- name: DeleteNote :execrows
DELETE FROM notes WHERE id = @id AND owner_id = @owner_id;

-- Optional filters use nullable parameters: NULL means "no filter". The
-- search pattern arrives already escaped for ILIKE.
-- name: ListNotes :many
SELECT * FROM notes
WHERE owner_id = @owner_id
  AND (sqlc.narg(tag)::text IS NULL OR sqlc.narg(tag)::text = ANY (tags))
  AND (sqlc.narg(pattern)::text IS NULL OR title ILIKE sqlc.narg(pattern)::text OR body ILIKE sqlc.narg(pattern)::text)
  AND (sqlc.narg(after_created_at)::timestamptz IS NULL
       OR (created_at, id) < (sqlc.narg(after_created_at)::timestamptz, sqlc.narg(after_id)::uuid))
ORDER BY created_at DESC, id DESC
LIMIT @row_limit;
