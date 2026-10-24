-- name: InsertUser :one
INSERT INTO users (id, email, name, password_hash, role, created_at, updated_at)
VALUES (@id, @email, @name, @password_hash, @role, @created_at, @created_at)
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = @id;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = @email;

-- name: UpdateUserName :one
UPDATE users SET name = @name, updated_at = @updated_at
WHERE id = @id
RETURNING *;

-- name: ListUsers :many
SELECT * FROM users
WHERE sqlc.narg(after_created_at)::timestamptz IS NULL
   OR (created_at, id) < (sqlc.narg(after_created_at)::timestamptz, sqlc.narg(after_id)::uuid)
ORDER BY created_at DESC, id DESC
LIMIT @row_limit;
